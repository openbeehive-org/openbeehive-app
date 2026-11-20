package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
)

func newWebAuthnFixture(t *testing.T) (*httptest.Server, *SessionManager, *sqlstore.Store) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Auth.WebAuthn = config.WebAuthnConfig{
		Enabled: true, RPID: "example.org",
		RPOrigins: []string{"https://app.example.org"}, DisplayName: "Test",
	}
	srvBase, store, sessions := newAuthFixture(t, cfg)
	srvBase.Close() // fixture built the store; we mount our own mux

	prov := NewProvisioner(store.Users(), store.Orgs(), store.Members())
	wa, err := NewWebAuthn(cfg, store.DB(), store.Users(), prov, sessions, NewThrottle(false))
	if err != nil {
		t.Fatalf("new webauthn: %v", err)
	}
	rt := NewRouter(cfg)
	wa.Routes(rt)
	srv := httptest.NewServer(rt)
	t.Cleanup(srv.Close)
	return srv, sessions, store
}

// Enrollment endpoints must be unreachable without a session: the legacy flow
// let anonymous visitors mint accounts + owner sessions, which this design
// exists to prevent.
func TestWebAuthnEnrollRequiresSession(t *testing.T) {
	srv, _, _ := newWebAuthnFixture(t)
	for _, path := range []string{
		"/auth/webauthn/enroll/begin",
		"/auth/webauthn/enroll/finish",
		"/auth/webauthn/credentials/delete",
	} {
		code, _ := postJSON(t, srv.URL+path, map[string]any{})
		if code != http.StatusUnauthorized {
			t.Errorf("%s without session: code=%d, want 401", path, code)
		}
	}
	resp, err := http.Get(srv.URL + "/auth/webauthn/credentials")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("credentials list without session: code=%d, want 401", resp.StatusCode)
	}
}

// Demo sessions are shared; they must not be able to attach credentials.
func TestWebAuthnEnrollRefusesDemoRole(t *testing.T) {
	srv, sessions, store := newWebAuthnFixture(t)
	seedMember(t, store, "u1", "demo@x", "o1", "owner")
	tok, err := sessions.Issue(Identity{UserID: "u1", OrgID: "o1", Email: "demo@x", Role: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/webauthn/enroll/begin", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("demo enroll: code=%d, want 403", resp.StatusCode)
	}
}

// A signed-in user gets registration options bound to their own account;
// never to a name they merely claim.
func TestWebAuthnEnrollBeginBindsToSessionUser(t *testing.T) {
	srv, sessions, store := newWebAuthnFixture(t)
	u := &storage.User{ID: "user-1", Email: "u@example.org", Name: "U", Role: "user",
		CreatedAt: time.Now().UTC(), EmailVerified: true}
	if err := store.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	seedMember(t, store, u.ID, u.Email, "org", "owner")
	tok, err := sessions.Issue(Identity{UserID: u.ID, OrgID: "org", Email: u.Email, Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/webauthn/enroll/begin?name=laptop", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll begin: code=%d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	// The options' user.id is base64url("user-1"), the session's user, and
	// residentKey is required so login can be usernameless.
	if !strings.Contains(body, `"dXNlci0x"`) {
		t.Errorf("options do not carry the session user id: %s", body)
	}
	if !strings.Contains(body, `"residentKey":"required"`) {
		t.Errorf("options do not require a resident key: %s", body)
	}
}

// Login begin is public (that is the point of passkeys) and must return
// discoverable-login options without leaking any account information.
func TestWebAuthnLoginBeginIsPublicAndEmpty(t *testing.T) {
	srv, _, _ := newWebAuthnFixture(t)
	code, _ := postJSON(t, srv.URL+"/auth/webauthn/login/begin", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("login begin: code=%d, want 200", code)
	}
}

func TestChallengeStoreExpiresAndIsCapped(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newChallengeStore(time.Minute, 3)
	s.now = func() time.Time { return now }
	c := func() *waChallenge { return &waChallenge{} }

	s.put("a", c())
	if _, ok := s.take("a"); !ok {
		t.Fatal("a fresh challenge must be found")
	}
	if _, ok := s.take("a"); ok {
		t.Fatal("a challenge is single-use")
	}

	// Expiry: an abandoned challenge cannot be finished, and is dropped when
	// the next one comes in.
	s.put("old", c())
	now = now.Add(time.Minute)
	if _, ok := s.take("old"); ok {
		t.Fatal("an expired challenge must not be usable")
	}
	s.put("old2", c())
	now = now.Add(time.Minute)
	s.put("new", c())
	if s.len() != 1 {
		t.Fatalf("expired challenges must be evicted, %d left", s.len())
	}

	// Cap: the oldest pending challenge makes room.
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		s.put(id, c())
	}
	if s.len() != 3 {
		t.Fatalf("store holds %d challenges, cap is 3", s.len())
	}
	if _, ok := s.take("new"); ok {
		t.Fatal("the oldest challenge should have been pushed out")
	}
	if _, ok := s.take("c4"); !ok {
		t.Fatal("the newest challenge must survive")
	}
}

// Anyone can call login/begin, so the challenges it creates must not pile
// up without bound.
func TestPasskeyLoginBeginDoesNotAccumulateChallenges(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.WebAuthn = config.WebAuthnConfig{Enabled: true, RPID: "example.org",
		RPOrigins: []string{"https://app.example.org"}, DisplayName: "Test"}
	wa, err := NewWebAuthn(cfg, nil, nil, nil, nil, NewThrottle(false))
	if err != nil {
		t.Fatal(err)
	}
	wa.pending = newChallengeStore(waChallengeTTL, 50)
	rt := NewRouter(cfg)
	wa.Routes(rt)
	for range 500 {
		rec := serve(rt, http.MethodPost, "/auth/webauthn/login/begin", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("login begin: %d %s", rec.Code, rec.Body)
		}
	}
	if n := wa.pending.len(); n != 50 {
		t.Fatalf("%d pending challenges after 500 begins, cap is 50", n)
	}
	// A failed finish counts against the client address like a wrong password.
	for i := range throttlePerIP {
		rec := serve(rt, http.MethodPost, "/auth/webauthn/login/begin", "")
		cookie := rec.Header().Get("Set-Cookie")
		rec = serve(rt, http.MethodPost, "/auth/webauthn/login/finish", `{}`, "Cookie", strings.SplitN(cookie, ";", 2)[0])
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("finish %d with a bogus assertion: %d %s", i, rec.Code, rec.Body)
		}
	}
	rec := serve(rt, http.MethodPost, "/auth/webauthn/login/begin", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("passkey login after %d failures: %d", throttlePerIP, rec.Code)
	}
}
