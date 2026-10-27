package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var errOIDCDenied = errors.New("OIDC authorization denied")
var errOIDCUnavailable = errors.New("OIDC authorization temporarily unavailable")

func (l *oidcLogin) now() time.Time {
	if l.clock != nil {
		return l.clock()
	}
	return time.Now()
}

func (l *oidcLogin) refreshAAD(hash, subject, identity string) []byte {
	return []byte(strings.Join([]string{l.issuer, l.clientID, hash, subject, identity}, "\x00"))
}

func (l *oidcLogin) encryptRefresh(hash, subject, identity, refresh string) []byte {
	if len(l.keys) == 0 {
		return nil
	}
	return l.keys[0].Seal(nil, nil, []byte(refresh), l.refreshAAD(hash, subject, identity))
}

func (l *oidcLogin) decryptRefresh(hash, subject, identity string, data []byte) (string, error) {
	for _, key := range l.keys {
		if raw, err := key.Open(nil, nil, data, l.refreshAAD(hash, subject, identity)); err == nil && len(raw) > 0 {
			return string(raw), nil
		}
	}
	return "", errOIDCDenied
}

func oidcVerificationError(err error) error {
	// go-oidc wraps remote-key transport errors in signature errors. Never log
	// that error text: it may contain a provider response or URL.
	for _, marker := range []string{"fetch", "request", "context deadline", "connection"} {
		if strings.Contains(strings.ToLower(err.Error()), marker) {
			return errOIDCUnavailable
		}
	}
	return errOIDCDenied
}

func (l *oidcLogin) verifyProviderToken(ctx context.Context, p *oidc.Provider, raw string) (*oidc.IDToken, error) {
	id, err := p.Verifier(&oidc.Config{ClientID: l.clientID, Now: l.now}).Verify(oidcContext(ctx), raw)
	if err != nil {
		return nil, oidcVerificationError(err)
	}
	var claims map[string]json.RawMessage
	if id.Subject == "" || id.Claims(&claims) != nil {
		return nil, errOIDCDenied
	}
	var party string
	if value, present := claims["azp"]; present || len(id.Audience) > 1 {
		if json.Unmarshal(value, &party) != nil || party != l.clientID {
			return nil, errOIDCDenied
		}
	}
	return id, nil
}

func (l *oidcLogin) currentIdentity(claims map[string]json.RawMessage) string {
	var identity, email, team, slack string
	var verified bool
	json.Unmarshal(claims[l.identityClaim], &identity)
	json.Unmarshal(claims["email"], &email)
	json.Unmarshal(claims["email_verified"], &verified)
	json.Unmarshal(claims["slack_team_id"], &team)
	json.Unmarshal(claims["slack_user_id"], &slack)
	if identity == "" || !verified || !strings.Contains(email, "@") ||
		(l.team != "" && (team != l.team || slack == "")) ||
		!oidcGrantPresent(claims["app_entitlements"], l.entitlement) || !oidcGrantPresent(claims["app_roles"], l.role) {
		return ""
	}
	return identity
}

// Both the signed access token and current UserInfo must identify the same
// approved account. ID-token validation alone cannot refresh authorization.
func (l *oidcLogin) validateCurrentTokens(ctx context.Context, p *oidc.Provider, tok *oauth2.Token, subject, identity, nonce string) (time.Time, error) {
	access, err := l.verifyProviderToken(ctx, p, tok.AccessToken)
	if err != nil {
		return time.Time{}, err
	}
	var accessClaims map[string]json.RawMessage
	if access.Subject != subject || access.Claims(&accessClaims) != nil || l.currentIdentity(accessClaims) != identity {
		return time.Time{}, errOIDCDenied
	}
	if raw, ok := tok.Extra("id_token").(string); ok {
		id, err := l.verifyProviderToken(ctx, p, raw)
		if err != nil {
			return time.Time{}, err
		}
		if id.Subject != subject || (id.Nonce != "" && id.Nonce != nonce) {
			return time.Time{}, errOIDCDenied
		}
	}
	info, err := p.UserInfo(oidcContext(ctx), oauth2.StaticTokenSource(tok))
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			return time.Time{}, errOIDCDenied
		}
		return time.Time{}, errOIDCUnavailable
	}
	var current map[string]json.RawMessage
	if info.Subject != subject || info.Claims(&current) != nil || l.currentIdentity(current) != identity {
		return time.Time{}, errOIDCDenied
	}
	return access.Expiry, nil
}

type oidcStoredSession struct {
	cipher                                   []byte
	subject, identity, nonce, userID, expiry string
	authUntil, verifiedAt                    int64
}

func (s *Server) readOIDCSession(hash string) (oidcStoredSession, error) {
	var row oidcStoredSession
	err := s.db.QueryRow(`SELECT o.refresh_cipher,o.subject,o.identity,o.nonce,o.auth_until,o.verified_at,s.user_id,s.expires_at FROM oidc_sessions o JOIN sessions s ON s.token_hash=o.session_hash WHERE o.session_hash=? AND s.auth_provider='oidc'`, hash).
		Scan(&row.cipher, &row.subject, &row.identity, &row.nonce, &row.authUntil, &row.verifiedAt, &row.userID, &row.expiry)
	return row, err
}

// The conditional SQLite lease is shared across processes. No SQLite write
// transaction is held over HTTP, so unrelated edits can still save. Renewal has
// a 15-second total deadline; its 30-second lease cannot expire mid-exchange.
func (s *Server) authorizeOIDCSession(ctx context.Context, handle, userID string) (time.Time, error) {
	l := s.oidcLogin
	if l == nil {
		return time.Time{}, errOIDCDenied
	}
	hash := tokenHash(handle)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		row, err := s.readOIDCSession(hash)
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, errOIDCDenied
		}
		if err != nil {
			return time.Time{}, errOIDCUnavailable
		}
		expiry, err := time.Parse(time.RFC3339Nano, row.expiry)
		if err != nil || !l.now().Before(expiry) || row.userID != userID || l.accounts[row.identity] != userID {
			return time.Time{}, errOIDCDenied
		}
		if until := time.UnixMilli(row.authUntil); l.now().Before(until) && row.authUntil <= row.verifiedAt+oidcAuthorizationLifetime.Milliseconds() && until.Before(expiry.Add(time.Millisecond)) {
			return until, nil
		}
		owner := randB64(24)
		lease, err := s.db.ExecContext(ctx, `UPDATE oidc_sessions SET lease_owner=?,lease_until=? WHERE session_hash=? AND (lease_owner='' OR lease_until<?)`, owner, time.Now().Add(30*time.Second).UnixMilli(), hash, time.Now().UnixMilli())
		if err != nil {
			return time.Time{}, errOIDCUnavailable
		}
		n, err := lease.RowsAffected()
		if err != nil {
			return time.Time{}, errOIDCUnavailable
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return time.Time{}, errOIDCUnavailable
			case <-time.After(25 * time.Millisecond):
				continue
			}
		}
		// A competing worker may have renewed just before we acquired the lease.
		row, err = s.readOIDCSession(hash)
		if err != nil {
			s.releaseOIDCLease(hash, owner)
			if errors.Is(err, sql.ErrNoRows) {
				return time.Time{}, errOIDCDenied
			}
			return time.Time{}, errOIDCUnavailable
		}
		if until := time.UnixMilli(row.authUntil); l.now().Before(until) && row.authUntil <= row.verifiedAt+oidcAuthorizationLifetime.Milliseconds() && !until.After(expiry) {
			s.releaseOIDCLease(hash, owner)
			continue
		}
		until, err := s.renewOIDCSession(ctx, hash, owner, row)
		s.releaseOIDCLease(hash, owner)
		return until, err
	}
}

func (s *Server) releaseOIDCLease(hash, owner string) {
	s.db.Exec(`UPDATE oidc_sessions SET lease_owner='',lease_until=0 WHERE session_hash=? AND lease_owner=?`, hash, owner)
}

func (s *Server) renewOIDCSession(ctx context.Context, hash, owner string, row oidcStoredSession) (time.Time, error) {
	l := s.oidcLogin
	started := l.now()
	expiry, _ := time.Parse(time.RFC3339Nano, row.expiry)
	if !started.Before(expiry) {
		return time.Time{}, errOIDCDenied
	}
	refresh, err := l.decryptRefresh(hash, row.subject, row.identity, row.cipher)
	if err != nil {
		return time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := l.discover(ctx)
	if err != nil {
		return time.Time{}, errOIDCUnavailable
	}
	cfg := l.oauthConfig(p)
	tok, err := cfg.TokenSource(oidcContext(ctx), &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		var response *oauth2.RetrieveError
		if errors.As(err, &response) && (response.ErrorCode == "invalid_grant" || response.ErrorCode == "invalid_token" || (response.Response != nil && response.Response.StatusCode == 401)) {
			return time.Time{}, errOIDCDenied
		}
		return time.Time{}, errOIDCUnavailable
	}
	if tok.RefreshToken == "" || tok.RefreshToken == refresh {
		return time.Time{}, errOIDCDenied
	}
	ciphertext := l.encryptRefresh(hash, row.subject, row.identity, tok.RefreshToken)
	if len(ciphertext) == 0 {
		return time.Time{}, errOIDCDenied
	}
	// Persist rotation before UserInfo/JWKS work. A temporary outage after a
	// successful exchange must not lose the only usable refresh credential.
	result, err := s.db.ExecContext(ctx, `UPDATE oidc_sessions SET refresh_cipher=? WHERE session_hash=? AND lease_owner=? AND EXISTS(SELECT 1 FROM sessions WHERE token_hash=?)`, ciphertext, hash, owner, hash)
	if err != nil {
		return time.Time{}, errOIDCUnavailable
	}
	if n, _ := result.RowsAffected(); n != 1 {
		l.revokeRefresh(ctx, tok.RefreshToken)
		return time.Time{}, errOIDCDenied
	}
	accessExpiry, err := l.validateCurrentTokens(ctx, p, tok, row.subject, row.identity, row.nonce)
	if err != nil {
		return time.Time{}, err
	}
	until := oidcMinimum(started.Add(oidcAuthorizationLifetime).UnixMilli(), accessExpiry.UnixMilli(), expiry.UnixMilli())
	if until <= l.now().UnixMilli() {
		return time.Time{}, errOIDCUnavailable
	}
	result, err = s.db.ExecContext(ctx, `UPDATE oidc_sessions SET auth_until=?,verified_at=? WHERE session_hash=? AND lease_owner=? AND EXISTS(SELECT 1 FROM sessions WHERE token_hash=?)`, until, started.UnixMilli(), hash, owner, hash)
	if err != nil {
		return time.Time{}, errOIDCUnavailable
	}
	if n, _ := result.RowsAffected(); n != 1 {
		l.revokeRefresh(ctx, tok.RefreshToken)
		return time.Time{}, errOIDCDenied
	}
	s.audit("human", row.userID, "", "oidc_refresh", "", "", "Authorization revalidated")
	return time.UnixMilli(until), nil
}

func (l *oidcLogin) revokeRefresh(ctx context.Context, refresh string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := l.discover(ctx)
	if err != nil {
		return
	}
	var metadata struct {
		Revocation string `json:"revocation_endpoint"`
	}
	if p.Claims(&metadata) != nil || !validOIDCURL(metadata.Revocation) {
		return
	}
	form := url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}}
	req, err := http.NewRequestWithContext(ctx, "POST", metadata.Revocation, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.SetBasicAuth(l.clientID, l.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
		response.Body.Close()
	}
}

func (s *Server) invalidateOIDCSession(ctx context.Context, handle string) error {
	hash := tokenHash(handle)
	row, readErr := s.readOIDCSession(hash)
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hash); err != nil {
		return err
	}
	if readErr == nil && s.oidcLogin != nil {
		if refresh, err := s.oidcLogin.decryptRefresh(hash, row.subject, row.identity, row.cipher); err == nil {
			s.oidcLogin.revokeRefresh(ctx, refresh)
		}
	}
	return nil
}

func (s *Server) resolveUser(r *http.Request) (*user, error) {
	if hasBearer(r) {
		return s.localUser(r), nil
	}
	handle, ok := sessionCookieValue(r)
	if !ok {
		return nil, nil
	}
	var userID, expiry, provider string
	err := s.db.QueryRow(`SELECT user_id,expires_at,auth_provider FROM sessions WHERE token_hash=?`, tokenHash(handle)).Scan(&userID, &expiry, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errOIDCUnavailable
	}
	if provider != "oidc" {
		return s.localUser(r), nil
	}
	u := s.userByID(userID)
	if s.oidcLogin == nil || u == nil || u.Disabled {
		return nil, nil
	}
	until, err := s.authorizeOIDCSession(r.Context(), handle, u.ID)
	if errors.Is(err, errOIDCDenied) {
		if s.invalidateOIDCSession(r.Context(), handle) != nil {
			return nil, errOIDCUnavailable
		}
		s.audit("human", u.ID, "", "oidc_session_denied", "", "", "Authorization revoked or expired")
		return nil, nil
	}
	if err != nil {
		return nil, errOIDCUnavailable
	}
	u.SessionExpires = until
	u.SessionProvider = "oidc"
	return u, nil
}

// Kept for internal callers/tests that need a nullable user. Protected HTTP
// entry points call resolveUser so a temporary provider failure becomes 503.
func (s *Server) currentUser(r *http.Request) *user { u, _ := s.resolveUser(r); return u }

func oidcMinimum(a, b, c int64) int64 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
