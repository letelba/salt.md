package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Optional generic OIDC login. Configuration and explicit identity-to-account
// links are operator-owned; claims never create users or change native roles.
// No provider tokens are retained. Sessions have a hard ten-minute ceiling so
// disabling an identity or removing its grant cannot leave a long-lived login.
const oidcSessionLifetime = 10 * time.Minute
const oidcTransactionLifetime = 5 * time.Minute
const oidcTransactionCookie = "salt_oidc"

type oidcLogin struct {
	issuer, clientID, secret, redirectURL, name string
	identityClaim, entitlement, role            string
	accounts                                    map[string]string
	mu                                          sync.Mutex
	provider                                    *oidc.Provider
	pending                                     map[string]oidcLoginTransaction
}

type oidcLoginTransaction struct {
	state, nonce, verifier, next string
	expires                      time.Time
}

// HTTPS is required outside loopback development. Endpoints and callbacks come
// only from configuration/discovery, never the incoming Host or proxy headers.
func validOIDCURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

func oidcLoginFromEnv() (*oidcLogin, error) {
	issuer := strings.TrimRight(os.Getenv("SALT_OIDC_ISSUER"), "/")
	if issuer == "" {
		return nil, nil
	}
	l := &oidcLogin{
		issuer: issuer, clientID: os.Getenv("SALT_OIDC_CLIENT_ID"), secret: os.Getenv("SALT_OIDC_CLIENT_SECRET"),
		redirectURL: os.Getenv("SALT_OIDC_REDIRECT_URL"), name: os.Getenv("SALT_OIDC_NAME"),
		identityClaim: os.Getenv("SALT_OIDC_IDENTITY_CLAIM"),
		entitlement:   os.Getenv("SALT_OIDC_REQUIRED_ENTITLEMENT"), role: os.Getenv("SALT_OIDC_REQUIRED_ROLE"),
		pending: make(map[string]oidcLoginTransaction),
	}
	if !validOIDCURL(l.issuer) || !validOIDCURL(l.redirectURL) || l.clientID == "" || l.secret == "" {
		return nil, fmt.Errorf("OIDC requires a valid issuer, redirect URL, client ID and client secret")
	}
	u, _ := url.Parse(l.redirectURL)
	if u.Path != "/api/oidc/callback" {
		return nil, fmt.Errorf("OIDC redirect URL must end in /api/oidc/callback")
	}
	if json.Unmarshal([]byte(os.Getenv("SALT_OIDC_ACCOUNT_LINKS")), &l.accounts) != nil || len(l.accounts) == 0 {
		return nil, fmt.Errorf("OIDC requires explicit identity-to-account links")
	}
	for identity, account := range l.accounts {
		if strings.TrimSpace(identity) == "" || strings.TrimSpace(account) == "" {
			return nil, fmt.Errorf("OIDC account links must not contain empty identifiers")
		}
	}
	if l.name == "" {
		l.name = "OpenID Connect"
	}
	if l.identityClaim == "" {
		l.identityClaim = "sub"
	}
	return l, nil
}

func oidcContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, &http.Client{Timeout: 15 * time.Second})
}

func (l *oidcLogin) discover(ctx context.Context) (*oidc.Provider, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.provider != nil {
		return l.provider, nil
	}
	p, err := oidc.NewProvider(oidcContext(ctx), l.issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery unavailable")
	}
	var metadata struct {
		JWKSURL string `json:"jwks_uri"`
		Logout  string `json:"end_session_endpoint"`
	}
	if p.Claims(&metadata) != nil || !validOIDCURL(p.Endpoint().AuthURL) || !validOIDCURL(p.Endpoint().TokenURL) || !validOIDCURL(metadata.JWKSURL) || (metadata.Logout != "" && !validOIDCURL(metadata.Logout)) {
		return nil, fmt.Errorf("OIDC discovery returned invalid endpoints")
	}
	l.provider = p
	return p, nil
}

func (l *oidcLogin) oauthConfig(p *oidc.Provider) oauth2.Config {
	return oauth2.Config{ClientID: l.clientID, ClientSecret: l.secret, Endpoint: p.Endpoint(), RedirectURL: l.redirectURL, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
}

func (s *Server) oidcName() string {
	if s.oidcLogin == nil {
		return ""
	}
	return s.oidcLogin.name
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	l := s.oidcLogin
	if l == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.loginRate.allow(s.clientIP(r)) {
		httpError(w, http.StatusTooManyRequests, "too many sign-in attempts")
		return
	}
	p, err := l.discover(r.Context())
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, "Sign-in provider is unavailable. Please try again.")
		return
	}
	tx := oidcLoginTransaction{state: randB64(32), nonce: randB64(32), verifier: oauth2.GenerateVerifier(), next: safeNext(r.URL.Query().Get("next")), expires: time.Now().Add(oidcTransactionLifetime)}
	cookie := randB64(32)
	l.mu.Lock()
	for key, pending := range l.pending {
		if time.Now().After(pending.expires) {
			delete(l.pending, key)
		}
	}
	if len(l.pending) >= 1000 {
		l.mu.Unlock()
		httpError(w, http.StatusServiceUnavailable, "Please try signing in again later.")
		return
	}
	l.pending[tokenHash(cookie)] = tx
	l.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oidcTransactionCookie, Value: cookie, Path: "/api/oidc/", MaxAge: int(oidcTransactionLifetime.Seconds()), HttpOnly: true, Secure: strings.HasPrefix(l.redirectURL, "https://"), SameSite: http.SameSiteLaxMode})
	cfg := l.oauthConfig(p)
	http.Redirect(w, r, cfg.AuthCodeURL(tx.state, oidc.Nonce(tx.nonce), oauth2.S256ChallengeOption(tx.verifier)), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	l := s.oidcLogin
	if l == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, &http.Cookie{Name: oidcTransactionCookie, Path: "/api/oidc/", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(l.redirectURL, "https://"), SameSite: http.SameSiteLaxMode})
	deny := func() {
		s.logAuthFailure(r, "oidc")
		s.audit("human", "", "", "oidc_login_denied", "", "", "OIDC sign-in denied")
		loginErrorRedirect(w, r, "oauth_failed", "Sign-in failed. Access must be granted by your administrator.")
	}
	cookie, err := r.Cookie(oidcTransactionCookie)
	if err != nil {
		deny()
		return
	}
	l.mu.Lock()
	tx, ok := l.pending[tokenHash(cookie.Value)]
	delete(l.pending, tokenHash(cookie.Value)) // one-time, including failed exchanges
	l.mu.Unlock()
	if !ok || time.Now().After(tx.expires) || subtle.ConstantTimeCompare([]byte(tx.state), []byte(r.URL.Query().Get("state"))) != 1 || r.URL.Query().Get("code") == "" || r.URL.Query().Get("error") != "" {
		deny()
		return
	}
	p, err := l.discover(r.Context())
	if err != nil {
		deny()
		return
	}
	cfg := l.oauthConfig(p)
	tok, err := cfg.Exchange(oidcContext(r.Context()), r.URL.Query().Get("code"), oauth2.VerifierOption(tx.verifier))
	if err != nil {
		deny()
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		deny()
		return
	}
	id, err := p.Verifier(&oidc.Config{ClientID: l.clientID}).Verify(oidcContext(r.Context()), raw)
	if err != nil || id.Subject == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(tx.nonce)) != 1 {
		deny()
		return
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(tok.AccessToken) != nil {
		deny()
		return
	}
	var claims map[string]json.RawMessage
	if id.Claims(&claims) != nil {
		deny()
		return
	}
	var authorizedParty string
	if rawParty, present := claims["azp"]; present || len(id.Audience) > 1 {
		if json.Unmarshal(rawParty, &authorizedParty) != nil || authorizedParty != l.clientID {
			deny()
			return
		}
	}
	var identity string
	if json.Unmarshal(claims[l.identityClaim], &identity) != nil || identity == "" || !oidcGrantPresent(claims["app_entitlements"], l.entitlement) || !oidcGrantPresent(claims["app_roles"], l.role) {
		deny()
		return
	}
	// An operator has approved this exact immutable identity, independently of
	// mutable email claims. Never fall back to matching an email or create users.
	uid := l.accounts[identity]
	u := s.userByID(uid)
	if uid == "" || u == nil || u.Disabled {
		deny()
		return
	}
	expires := time.Now().Add(oidcSessionLifetime)
	if id.Expiry.Before(expires) {
		expires = id.Expiry
	}
	if !tok.Expiry.IsZero() && tok.Expiry.Before(expires) {
		expires = tok.Expiry
	}
	if !expires.After(time.Now()) {
		deny()
		return
	}
	session := randB64(32)
	if _, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, auth_provider) VALUES (?, ?, ?, ?, 'oidc')`, tokenHash(session), uid, now(), expires.UTC().Format(time.RFC3339Nano)); err != nil {
		deny()
		return
	}
	s.audit("human", uid, u.Name, "oidc_login", "", "", "OIDC sign-in")
	setSessionCookie(w, r, session, max(1, int(time.Until(expires).Seconds())))
	dest := safeNext(tx.next)
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func oidcGrantPresent(raw json.RawMessage, required string) bool {
	if required == "" {
		return true
	}
	var grants []string
	if json.Unmarshal(raw, &grants) != nil {
		return false
	}
	for _, grant := range grants {
		if grant == required {
			return true
		}
	}
	return false
}

// The local session is removed before the browser visits the provider. A
// configured client ID and exact post-logout URI avoid retaining ID tokens.
func (s *Server) oidcLogoutURL(r *http.Request) string {
	l := s.oidcLogin
	if l == nil {
		return ""
	}
	p, err := l.discover(r.Context())
	if err != nil {
		return ""
	}
	var metadata struct {
		Logout string `json:"end_session_endpoint"`
	}
	if p.Claims(&metadata) != nil || metadata.Logout == "" {
		return ""
	}
	callback, _ := url.Parse(l.redirectURL)
	q := url.Values{"client_id": {l.clientID}, "post_logout_redirect_uri": {callback.Scheme + "://" + callback.Host + "/"}}
	return metadata.Logout + "?" + q.Encode()
}
