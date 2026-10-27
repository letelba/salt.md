package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func TestOIDCStreamDeadlineAndRollback(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "owner@example.test")
	val := strings.TrimPrefix(cookie, sessionCookie+"=")
	expiry := time.Now().Add(300 * time.Millisecond)
	if _, err := s.db.Exec(`UPDATE sessions SET auth_provider = 'oidc', expires_at = ? WHERE token_hash = ?`, expiry.UTC().Format(time.RFC3339Nano), tokenHash(val)); err != nil {
		t.Fatal(err)
	}
	s.oidcLogin = &oidcLogin{}
	s.oidcLogin.keys = testOIDCKeys(t)
	s.db.Exec(`INSERT INTO oidc_sessions(session_hash,refresh_cipher,subject,identity,nonce,auth_until,verified_at) VALUES (?,?,?,?,?,?,?)`, tokenHash(val), []byte("test"), "subject", "identity", "nonce", expiry.UnixMilli(), time.Now().UnixMilli())
	s.oidcLogin.accounts = map[string]string{"identity": uid}
	page := s.makePage(t, s.firstWorkspaceOf(t, uid), uid, "", "OIDC stream", `{}`)
	srv := httptest.NewServer(s)
	defer srv.Close()
	conn := dialCollab(t, srv, cookie, page)
	defer conn.CloseNow()
	if _, err := readUntilSynced(conn); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("collaboration stream survived session expiry")
			}
			break
		}
	}
	// Disabling the connector denies its unexpired sessions immediately while
	// password recovery continues to use the existing session implementation.
	s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), tokenHash(val))
	s.oidcLogin = nil
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.Header.Set("Cookie", cookie)
	if s.currentUser(r) != nil {
		t.Fatal("disabled connector accepted an OIDC session")
	}
	s.db.Exec(`UPDATE sessions SET auth_provider = '' WHERE token_hash = ?`, tokenHash(val))
	if s.currentUser(r) == nil {
		t.Fatal("OIDC rollback broke password recovery")
	}
}

func TestOIDCLoginSecurity(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "owner@example.test")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, nonce, challenge string
	var userClaims map[string]any
	var refreshLifetime int
	var userInfoDelay time.Duration
	var alter func(map[string]any)
	var exchanges int
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "userinfo_endpoint": issuer + "/userinfo", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}, "end_session_endpoint": issuer + "/logout"})
		case "/userinfo":
			time.Sleep(userInfoDelay)
			json.NewEncoder(w).Encode(userClaims)
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			exchanges++
			r.ParseForm()
			client, secret, _ := r.BasicAuth()
			if client != "notes" || secret != "test-secret" || sha256Sum(r.Form.Get("code_verifier")) != challenge || r.Form.Get("redirect_uri") != "https://notes.example.test/api/oidc/callback" {
				t.Error("token exchange lost client authentication, PKCE or exact callback")
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			claims := map[string]any{"iss": issuer, "aud": "notes", "sub": "provider-subject", "nonce": nonce, "exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(), "stable_id": "approved-identity", "app_entitlements": []string{"notes"}, "app_roles": []string{"notes.user"}, "email": "changed@example.test", "email_verified": true}
			if alter != nil {
				alter(claims)
			}
			userClaims = claims
			payload, _ := json.Marshal(claims)
			signed, _ := signer.Sign(payload)
			token, _ := signed.CompactSerialize()
			if r.Form.Get("code") == "bad-signature" {
				parts := strings.Split(token, ".")
				parts[2] = b64url(make([]byte, 256))
				token = strings.Join(parts, ".")
			}
			json.NewEncoder(w).Encode(map[string]any{"id_token": token, "access_token": token, "refresh_token": "test-refresh-token", "token_type": "Bearer", "expires_in": 600, "refresh_expires_in": refreshLifetime})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	s.oidcLogin = &oidcLogin{issuer: issuer, clientID: "notes", secret: "test-secret", redirectURL: "https://notes.example.test/api/oidc/callback", name: "Company Login", identityClaim: "stable_id", entitlement: "notes", role: "notes.user", accounts: map[string]string{"approved-identity": uid}, pending: make(map[string]oidcLoginTransaction)}
	s.oidcLogin.keys = testOIDCKeys(t)

	start := func(t *testing.T, next string) (*http.Cookie, url.Values) {
		t.Helper()
		s.loginRate = newRateLimiter(60, 100)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/api/oidc/start?next="+url.QueryEscape(next), nil))
		if w.Code != http.StatusFound {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		u, _ := url.Parse(w.Header().Get("Location"))
		q := u.Query()
		nonce, challenge = q.Get("nonce"), q.Get("code_challenge")
		if nonce == "" || challenge == "" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile email" {
			t.Fatal("incomplete authorization request")
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == oidcTransactionCookie {
				if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/oidc/" {
					t.Fatal("unsafe transaction cookie")
				}
				return c, q
			}
		}
		t.Fatal("missing transaction cookie")
		return nil, nil
	}
	callback := func(cookie *http.Cookie, state, code string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/oidc/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		s.ServeHTTP(w, r)
		return w
	}
	deny := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if !strings.Contains(w.Header().Get("Location"), "oauthError=") {
			t.Fatalf("denial missing: %d %s", w.Code, w.Header().Get("Location"))
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookie {
				t.Fatal("denied login minted a session")
			}
		}
	}

	t.Run("success_preserves_account_and_permissions", func(t *testing.T) {
		cookie, q := start(t, "/p/existing")
		w := callback(cookie, q.Get("state"), "valid")
		if w.Header().Get("Location") != "/p/existing" {
			t.Fatal("valid login did not preserve its destination")
		}
		var session *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookie {
				session = c
			}
		}
		maximum := int(s.oidcSessionLifetime().Seconds())
		if session == nil || session.MaxAge > maximum || session.MaxAge < maximum-10 {
			t.Fatal("missing or unbounded session")
		}
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.AddCookie(session)
		u := s.currentUser(r)
		if u == nil || u.ID != uid || u.Email != "owner@example.test" || !u.IsAdmin {
			t.Fatal("OIDC changed the existing account")
		}
		var expiry string
		s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash = ?`, tokenHash(session.Value)).Scan(&expiry)
		expires, _ := time.Parse(time.RFC3339Nano, expiry)
		if time.Until(expires) > s.oidcSessionLifetime() {
			t.Fatal("database session lifetime exceeds revocation ceiling")
		}
		lw := httptest.NewRecorder()
		lr := httptest.NewRequest("POST", "/api/logout", nil)
		lr.AddCookie(session)
		s.handleLogout(lw, lr)
		var response struct{ LogoutURL string }
		json.Unmarshal(lw.Body.Bytes(), &response)
		logout, _ := url.Parse(response.LogoutURL)
		if logout.Path != "/logout" || logout.Query().Get("client_id") != "notes" || logout.Query().Get("post_logout_redirect_uri") != "https://notes.example.test/" || s.currentUser(r) != nil {
			t.Fatal("logout failed to revoke locally and select provider logout")
		}
		before := exchanges
		deny(t, callback(cookie, q.Get("state"), "valid"))
		if exchanges != before {
			t.Fatal("replayed transaction reached token endpoint")
		}
	})

	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong_issuer", func(c map[string]any) { c["iss"] = "https://attacker.example" }},
		{"wrong_audience", func(c map[string]any) { c["aud"] = "other-app" }},
		{"wrong_authorized_party", func(c map[string]any) { c["azp"] = "other-app" }},
		{"multiple_audiences_without_authorized_party", func(c map[string]any) { c["aud"] = []string{"notes", "other-app"} }},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{"wrong_nonce", func(c map[string]any) { c["nonce"] = "other-transaction" }},
		{"missing_subject", func(c map[string]any) { delete(c, "sub") }},
		{"missing_identity", func(c map[string]any) { delete(c, "stable_id") }},
		{"unlinked_identity_same_email", func(c map[string]any) { c["stable_id"] = "stranger"; c["email"] = "owner@example.test" }},
		{"missing_entitlement", func(c map[string]any) { delete(c, "app_entitlements") }},
		{"foreign_role", func(c map[string]any) { c["app_roles"] = []string{"other.user"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alter = tc.change
			defer func() { alter = nil }()
			cookie, q := start(t, "/")
			deny(t, callback(cookie, q.Get("state"), "valid"))
		})
	}
	t.Run("bad_signature", func(t *testing.T) {
		cookie, q := start(t, "/")
		deny(t, callback(cookie, q.Get("state"), "bad-signature"))
	})
	t.Run("state_and_browser_binding", func(t *testing.T) {
		cookie, q := start(t, "/")
		before := exchanges
		deny(t, callback(nil, q.Get("state"), "valid"))
		deny(t, callback(cookie, "wrong-state", "valid"))
		deny(t, callback(cookie, q.Get("state"), "valid"))
		if exchanges != before {
			t.Fatal("invalid state reached token endpoint")
		}
	})
	t.Run("expired_transaction", func(t *testing.T) {
		cookie, q := start(t, "/")
		l := s.oidcLogin
		l.mu.Lock()
		tx := l.pending[tokenHash(cookie.Value)]
		tx.expires = time.Now().Add(-time.Minute)
		l.pending[tokenHash(cookie.Value)] = tx
		l.mu.Unlock()
		deny(t, callback(cookie, q.Get("state"), "valid"))
	})
	t.Run("disabled_account", func(t *testing.T) {
		s.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, uid)
		defer s.db.Exec(`UPDATE users SET disabled = 0 WHERE id = ?`, uid)
		cookie, q := start(t, "/")
		deny(t, callback(cookie, q.Get("state"), "valid"))
	})
	t.Run("external_next_is_dropped", func(t *testing.T) {
		cookie, q := start(t, "//attacker.example")
		if callback(cookie, q.Get("state"), "valid").Header().Get("Location") != "/" {
			t.Fatal("unsafe redirect accepted")
		}
	})
	t.Run("instance_session_setting_controls_new_logins", func(t *testing.T) {
		defer s.setSetting("session_days", "90")
		for _, days := range []int{1, 7, 90} {
			s.setSetting("session_days", strconv.Itoa(days))
			cookie, q := start(t, "/")
			w := callback(cookie, q.Get("state"), "valid")
			var session *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == sessionCookie {
					session = c
				}
			}
			maximum := days * 24 * 3600
			if session == nil || session.MaxAge > maximum || session.MaxAge < maximum-5 {
				t.Fatalf("session did not follow %d-day instance setting", days)
			}
			var original string
			s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, tokenHash(session.Value)).Scan(&original)
			deadline, err := time.Parse(time.RFC3339Nano, original)
			if err != nil || time.Until(deadline) > time.Duration(maximum)*time.Second || time.Until(deadline) < time.Duration(maximum-5)*time.Second {
				t.Fatalf("database expiry did not follow %d-day setting", days)
			}
			s.setSetting("session_days", "365")
			r := httptest.NewRequest("GET", "/api/me", nil)
			r.AddCookie(session)
			if s.currentUser(r) == nil {
				t.Fatal("setting change broke existing login")
			}
			var after string
			s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, tokenHash(session.Value)).Scan(&after)
			if after != original {
				t.Fatal("setting change extended an issued session")
			}
		}
	})
	t.Run("provider_remaining_lifetime_and_cookie_deadline", func(t *testing.T) {
		refreshLifetime = 120
		userInfoDelay = 2100 * time.Millisecond
		defer func() { refreshLifetime = 0; userInfoDelay = 0 }()
		cookie, q := start(t, "/")
		w := callback(cookie, q.Get("state"), "valid")
		for _, c := range w.Result().Cookies() {
			if c.Name != sessionCookie {
				continue
			}
			var value string
			s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, tokenHash(c.Value)).Scan(&value)
			deadline, err := time.Parse(time.RFC3339Nano, value)
			remaining := time.Until(deadline).Seconds()
			if err != nil || remaining > 120 || float64(c.MaxAge) > remaining+0.1 || c.MaxAge < 116 {
				t.Fatalf("cookie/provider lifetime mismatch: cookie %d, server remaining %.2f", c.MaxAge, remaining)
			}
			return
		}
		t.Fatal("provider-bounded login minted no session")
	})
}

func TestOIDCConfiguration(t *testing.T) {
	t.Setenv("SALT_OIDC_ISSUER", "")
	if l, err := oidcLoginFromEnv(); l != nil || err != nil {
		t.Fatal("OIDC must be disabled by default")
	}
	t.Setenv("SALT_OIDC_ISSUER", "https://identity.example.test/realms/company")
	t.Setenv("SALT_OIDC_CLIENT_ID", "notes")
	t.Setenv("SALT_OIDC_CLIENT_SECRET", "test-only")
	t.Setenv("SALT_OIDC_REDIRECT_URL", "https://notes.example.test/api/oidc/callback")
	t.Setenv("SALT_OIDC_ACCOUNT_LINKS", `{"immutable-id":"native-account-id"}`)
	t.Setenv("SALT_OIDC_SESSION_KEYS", b64url(make([]byte, 32)))
	if _, err := oidcLoginFromEnv(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://remote.example.test", "https://user:password@example.test", "https://example.test/?query=1", "https://example.test/#fragment", "javascript:alert(1)"} {
		if validOIDCURL(raw) {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	t.Setenv("SALT_OIDC_ACCOUNT_LINKS", `{}`)
	if _, err := oidcLoginFromEnv(); err == nil {
		t.Fatal("missing explicit links must fail closed")
	}
}

func testOIDCKeys(t *testing.T) []cipher.AEAD {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	return []cipher.AEAD{aead}
}
