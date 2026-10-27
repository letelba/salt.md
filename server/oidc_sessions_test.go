package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-jose/go-jose/v4"
)

type renewalFixture struct {
	s               *Server
	issuer          *httptest.Server
	uid, handle     string
	now             atomic.Int64
	mu              sync.Mutex
	mode, refresh   string
	calls, revokes  int
	issued, release chan struct{}
}

func newRenewalFixture(t *testing.T) *renewalFixture {
	t.Helper()
	s := testServer(t)
	uid, _ := signedIn(t, s, "renewal@example.test")
	f := &renewalFixture{s: s, uid: uid, handle: "opaque-test-session", refresh: "refresh-0"}
	f.now.Store(time.Now().UTC().Truncate(time.Second).UnixMilli())
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		mode := f.mode
		f.mu.Unlock()
		claims := map[string]any{"iss": f.issuer.URL, "aud": "notes", "sub": "subject", "stable_id": "identity", "email": "renewal@example.test", "email_verified": true, "slack_team_id": "T_APPROVED", "slack_user_id": "U_TEST_USER", "app_entitlements": []string{"notes"}, "app_roles": []string{"notes.user"}, "exp": time.UnixMilli(f.now.Load()).Add(600 * time.Second).Unix()}
		switch mode {
		case "missing_role":
			claims["app_roles"] = []string{"foreign.user"}
		case "missing_entitlement":
			claims["app_entitlements"] = []string{"foreign"}
		case "changed_subject":
			claims["sub"] = "different-subject"
		case "changed_identity":
			claims["stable_id"] = "different-identity"
		case "wrong_team":
			claims["slack_team_id"] = "T_FOREIGN"
		case "unverified_email":
			claims["email_verified"] = false
		case "wrong_issuer":
			claims["iss"] = "https://foreign.example"
		case "wrong_audience":
			claims["aud"] = "foreign"
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": f.issuer.URL, "authorization_endpoint": f.issuer.URL + "/auth", "token_endpoint": f.issuer.URL + "/token", "userinfo_endpoint": f.issuer.URL + "/userinfo", "jwks_uri": f.issuer.URL + "/keys", "revocation_endpoint": f.issuer.URL + "/revoke", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Algorithm: "RS256", Use: "sig"}}})
		case "/userinfo":
			if mode == "userinfo_outage" {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(claims)
		case "/revoke":
			f.mu.Lock()
			f.revokes++
			f.mu.Unlock()
			w.WriteHeader(200)
		case "/token":
			r.ParseForm()
			client, secret, _ := r.BasicAuth()
			if client != "notes" || secret != "test-secret" || r.Form.Get("grant_type") != "refresh_token" {
				t.Error("refresh grant lost client authentication")
				w.WriteHeader(401)
				return
			}
			if mode == "outage" {
				w.WriteHeader(503)
				return
			}
			f.mu.Lock()
			if r.Form.Get("refresh_token") != f.refresh || mode == "revoked" || mode == "disabled" {
				f.mu.Unlock()
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			f.calls++
			if mode != "unchanged_refresh" {
				f.refresh = fmt.Sprintf("refresh-%d", f.calls)
			}
			refresh := f.refresh
			issued, release := f.issued, f.release
			f.mu.Unlock()
			if issued != nil {
				close(issued)
				<-release
			}
			payload, _ := json.Marshal(claims)
			signed, _ := signer.Sign(payload)
			raw, _ := signed.CompactSerialize()
			json.NewEncoder(w).Encode(map[string]any{"access_token": raw, "refresh_token": refresh, "expires_in": 600, "token_type": "Bearer"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.issuer.Close)
	l := &oidcLogin{issuer: f.issuer.URL, clientID: "notes", secret: "test-secret", identityClaim: "stable_id", entitlement: "notes", role: "notes.user", team: "T_APPROVED", accounts: map[string]string{"identity": uid}, keys: testOIDCKeys(t), pending: make(map[string]oidcLoginTransaction), clock: func() time.Time { return time.UnixMilli(f.now.Load()) }}
	s.oidcLogin = l
	expiry := l.now().Add(s.oidcSessionLifetime()).Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,created_at,expires_at,auth_provider) VALUES(?,?,?,?,'oidc')`, tokenHash(f.handle), uid, now(), expiry); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO oidc_sessions(session_hash,refresh_cipher,subject,identity,nonce,auth_until,verified_at) VALUES(?,?,?,?,?,?,?)`, tokenHash(f.handle), l.encryptRefresh(tokenHash(f.handle), "subject", "identity", f.refresh), "subject", "identity", "nonce", l.now().Add(600*time.Second).UnixMilli(), l.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *renewalFixture) modeIs(mode string)           { f.mu.Lock(); f.mode = mode; f.mu.Unlock() }
func (f *renewalFixture) advances(delta time.Duration) { f.now.Add(delta.Milliseconds()) }
func (f *renewalFixture) requests(s *Server) int {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/protected", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.handle})
	s.auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(w, r)
	return w.Code
}
func (f *renewalFixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func TestOIDCRenewalBoundariesAndFixedMaximum(t *testing.T) {
	f := newRenewalFixture(t)
	var original string
	f.s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, tokenHash(f.handle)).Scan(&original)
	f.advances(599 * time.Second)
	if f.requests(f.s) != 200 || f.count() != 0 {
		t.Fatal("599 seconds must use valid authorization")
	}
	f.advances(time.Second)
	if f.requests(f.s) != 200 || f.count() != 1 {
		t.Fatal("600 seconds must renew before serving")
	}
	f.advances(time.Second)
	if f.requests(f.s) != 200 || f.count() != 1 {
		t.Fatal("601 seconds must reuse the new authorization")
	}
	for _, delta := range []time.Duration{30 * time.Minute, 2 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		f.advances(delta)
		if f.requests(f.s) != 200 {
			t.Fatalf("return after %v requires seamless renewal", delta)
		}
	}
	var expiry string
	f.s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, tokenHash(f.handle)).Scan(&expiry)
	if expiry != original {
		t.Fatal("refresh extended the original deadline")
	}
	deadline, _ := time.Parse(time.RFC3339Nano, original)
	f.now.Store(deadline.Add(-time.Second).UnixMilli())
	if f.requests(f.s) != 200 {
		t.Fatal("session ended before its maximum")
	}
	f.now.Store(deadline.UnixMilli())
	if f.requests(f.s) != 401 {
		t.Fatal("session survived its configured maximum deadline")
	}
	f.advances(time.Second)
	if f.requests(f.s) != 401 {
		t.Fatal("expired session returned after its deadline")
	}
}

func TestOIDCRenewalConcurrentWorkersAndRestart(t *testing.T) {
	f := newRenewalFixture(t)
	other, err := New(f.s.dataDir, fstest.MapFS{"index.html": {Data: []byte("test")}})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.oidcLogin = f.s.oidcLogin
	f.advances(600 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			server := f.s
			if i%2 == 0 {
				server = other
			}
			if status := f.requests(server); status != 200 {
				t.Errorf("concurrent request: %d", status)
			}
		}(i)
	}
	wg.Wait()
	if f.count() != 1 {
		t.Fatalf("rotation was not serialized across workers: %d", f.count())
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := New(f.s.dataDir, fstest.MapFS{"index.html": {Data: []byte("test")}})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	l := f.s.oidcLogin
	fresh.oidcLogin = &oidcLogin{issuer: l.issuer, clientID: l.clientID, secret: l.secret, identityClaim: l.identityClaim, entitlement: l.entitlement, role: l.role, team: l.team, accounts: l.accounts, keys: l.keys, clock: l.clock}
	f.advances(2 * time.Hour)
	if f.requests(fresh) != 200 || f.count() != 2 {
		t.Fatal("fresh process could not use encrypted shared session state")
	}
}

func TestOIDCRenewalRejectsRevocationAndChangedClaims(t *testing.T) {
	for _, mode := range []string{"revoked", "disabled", "missing_role", "missing_entitlement", "changed_subject", "changed_identity", "wrong_team", "unverified_email", "wrong_issuer", "wrong_audience", "unchanged_refresh"} {
		t.Run(mode, func(t *testing.T) {
			f := newRenewalFixture(t)
			f.modeIs(mode)
			f.advances(600 * time.Second)
			if f.requests(f.s) != 401 {
				t.Fatal("invalid authorization was accepted")
			}
			if f.requests(f.s) != 401 {
				t.Fatal("invalid session was not removed")
			}
		})
	}
}

func TestOIDCUnavailableDoesNotDiscardRotatedCredential(t *testing.T) {
	f := newRenewalFixture(t)
	f.modeIs("userinfo_outage")
	f.advances(600 * time.Second)
	if f.requests(f.s) != 503 {
		t.Fatal("UserInfo outage must deny stale access with 503")
	}
	row, err := f.s.readOIDCSession(tokenHash(f.handle))
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := f.s.oidcLogin.decryptRefresh(tokenHash(f.handle), row.subject, row.identity, row.cipher)
	if err != nil || refresh != "refresh-1" || row.authUntil > f.now.Load() {
		t.Fatal("outage lost the rotated credential or extended stale authorization")
	}
	f.modeIs("")
	if f.requests(f.s) != 200 || f.count() != 2 {
		t.Fatal("session did not recover through the replacement credential")
	}
	f.advances(600 * time.Second)
	f.modeIs("outage")
	if f.requests(f.s) != 503 {
		t.Fatal("provider outage did not return 503")
	}
	if _, err := f.s.readOIDCSession(tokenHash(f.handle)); err != nil {
		t.Fatal("transient outage deleted the persistent session")
	}
	f.modeIs("")
	if f.requests(f.s) != 200 {
		t.Fatal("session did not recover after provider outage")
	}
}

func TestOIDCLogoutCannotBeOverwrittenByRenewal(t *testing.T) {
	f := newRenewalFixture(t)
	f.issued = make(chan struct{})
	f.release = make(chan struct{})
	f.advances(600 * time.Second)
	status := make(chan int, 1)
	go func() { status <- f.requests(f.s) }()
	<-f.issued
	if err := f.s.invalidateOIDCSession(context.Background(), f.handle); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	if <-status != 401 {
		t.Fatal("racing renewal resurrected logout")
	}
	if f.requests(f.s) != 401 {
		t.Fatal("copied cookie replayed after logout")
	}
}

func TestOIDCSessionEncryptionAndKeyRotation(t *testing.T) {
	f := newRenewalFixture(t)
	row, err := f.s.readOIDCSession(tokenHash(f.handle))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.cipher), "refresh-0") {
		t.Fatal("plaintext refresh credential persisted")
	}
	if _, err := f.s.oidcLogin.decryptRefresh("different-session", row.subject, row.identity, row.cipher); err == nil {
		t.Fatal("encrypted credentials were not session-bound")
	}
	old := f.s.oidcLogin.keys[0]
	f.s.oidcLogin.keys = append(testOIDCKeys(t), old)
	f.advances(600 * time.Second)
	if f.requests(f.s) != 200 {
		t.Fatal("key rotation broke an existing session")
	}
	f.s.oidcLogin.keys = f.s.oidcLogin.keys[:1]
	f.advances(600 * time.Second)
	if f.requests(f.s) != 200 {
		t.Fatal("renewal did not migrate encryption to the current key")
	}
}

func TestOIDCStoreFailureReturns503(t *testing.T) {
	f := newRenewalFixture(t)
	f.s.db.Close()
	if f.requests(f.s) != 503 {
		t.Fatal("store failure must not masquerade as a logged-out session")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.handle})
	f.s.handleLogout(w, r)
	if w.Code != 503 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("failed logout falsely cleared the cookie or reported success")
	}
}
