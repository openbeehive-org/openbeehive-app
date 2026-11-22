package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// newDevice signs in to an existing account again: another device. Two
// sign-ins in the same millisecond would hand out the same token, one session
// for both devices, so it lets the clock move on first.
func (f *apiFixture) newDevice(t *testing.T, a account) account {
	t.Helper()
	time.Sleep(2 * time.Millisecond)
	res := f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": a.email, "password": "password123"})
	if res.status != http.StatusOK {
		t.Fatalf("signin %s: %d %s", a.email, res.status, res.raw)
	}
	return account{token: res.str("token"), userID: a.userID, orgID: res.str("active_org"), email: a.email}
}

// wantSignedOut asserts the answer to POST /auth/logout: 204 and the session
// cookie cleared, whatever the request carried.
func wantSignedOut(t *testing.T, res response) {
	t.Helper()
	if res.status != http.StatusNoContent || !strings.Contains(res.header.Get("Set-Cookie"), SessionCookie+"=;") {
		t.Fatalf("logout: want 204 and the cookie cleared, got %d %v %s", res.status, res.header, res.raw)
	}
}

// revocationRows counts the saved sign-outs.
func revocationRows(t *testing.T, f *apiFixture) int {
	t.Helper()
	var n int
	if err := f.store.DB().Get(&n, `SELECT COUNT(*) FROM session_revocation`); err != nil {
		t.Fatal(err)
	}
	return n
}

// Signing out ends the session that signs out, on the server: a copy of its
// token is refused from then on, by the plain HTTP endpoints and the RPCs.
// The account's other sessions are not touched.
func TestLogoutEndsOnlyThatSession(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	phone, laptop := f.newDevice(t, a), f.newDevice(t, a)
	other := f.signup(t, "b@test.dev")

	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", phone.token, nil))
	wantError(t, f.call(t, http.MethodGet, "/auth/me", phone.token, nil), http.StatusUnauthorized, "not_signed_in")
	if _, err := f.sessions.Verify(ctx, phone.token); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("signed-out session: want errSessionRevoked, got %v", err)
	}
	// What the RPC interceptor does with the request's credentials.
	if _, err := verifyHeaders(ctx, f.keys, headerWith("Authorization", "Bearer "+phone.token)); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("signed-out session on an RPC: want errSessionRevoked, got %v", err)
	}
	// A session that has lost its workspace cannot be moved to another one.
	wantError(t, f.call(t, http.MethodPost, "/auth/session/recover", phone.token, nil), http.StatusUnauthorized, "not_signed_in")

	for name, tok := range map[string]string{"sign-up session": a.token, "laptop": laptop.token, "another account": other.token} {
		if res := f.call(t, http.MethodGet, "/auth/me", tok, nil); res.status != http.StatusOK {
			t.Fatalf("%s after the phone signed out: %d %s", name, res.status, res.raw)
		}
	}
	if n := revocationRows(t, f); n != 1 {
		t.Fatalf("saved sign-outs: %d, want 1", n)
	}
	// Signing out again with the same token changes nothing.
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", phone.token, nil))
	if n := revocationRows(t, f); n != 1 {
		t.Fatalf("saved sign-outs after signing out twice: %d, want 1", n)
	}

	// The browser sends the session as a cookie.
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", "", nil, "Cookie", SessionCookie+"="+laptop.token))
	if _, err := f.sessions.Verify(ctx, laptop.token); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("session signed out by its cookie: want errSessionRevoked, got %v", err)
	}
	// A bearer token is the session that signs out, not the cookie next to it.
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", phone.token, nil, "Cookie", SessionCookie+"="+a.token))
	if res := f.call(t, http.MethodGet, "/auth/me", a.token, nil); res.status != http.StatusOK {
		t.Fatalf("the session in the cookie beside a bearer token: %d %s", res.status, res.raw)
	}
}

// Signing out is saved: a restarted server still refuses the token.
func TestLogoutSurvivesARestart(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	stays := f.newDevice(t, a)
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", a.token, nil))

	restarted, err := NewSessionManager(testSecret, time.Hour, f.store.Users(), f.store.Members())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Verify(ctx, a.token); err != nil {
		t.Fatalf("before the list is loaded the manager knows nothing of it: %v", err)
	}
	if err := restarted.UseRevocations(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Verify(ctx, a.token); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("signed-out session after a restart: want errSessionRevoked, got %v", err)
	}
	if _, err := restarted.Verify(ctx, stays.token); err != nil {
		t.Fatalf("the account's other session after a restart: %v", err)
	}
}

// Signing out never fails for the user. Without a token, or with one that
// is not a valid session, the cookie is cleared and nothing is saved.
func TestLogoutWithoutAValidSessionStillSignsOut(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	id, err := f.sessions.Verify(ctx, a.token)
	if err != nil {
		t.Fatal(err)
	}
	forger, err := NewSessionManager(strings.Repeat("z", config.MinSessionSecretBytes), time.Hour, f.store.Users(), f.store.Members())
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := forger.Issue(id)
	expired, _ := f.sessions.issueAt(id, time.Now().Add(-2*time.Hour), false)

	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", "", nil))
	for name, tok := range map[string]string{"garbage": "not-a-token", "forged": forged, "expired": expired} {
		wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", tok, nil))
		wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", "", nil, "Cookie", SessionCookie+"="+tok))
		if n := revocationRows(t, f); n != 0 {
			t.Fatalf("%s token: %d sign-outs saved, want none", name, n)
		}
	}
	f.sessions.mu.Lock()
	held := len(f.sessions.signedOut)
	f.sessions.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d sign-outs held in memory, want none", held)
	}
	if res := f.call(t, http.MethodGet, "/auth/me", a.token, nil); res.status != http.StatusOK {
		t.Fatalf("the account's session: %d %s", res.status, res.raw)
	}
}

// An API key sent to the sign-out endpoint is not a session: it keeps
// working (keys are deleted in Settings).
func TestLogoutLeavesAPIKeysAlone(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	res := f.call(t, http.MethodPost, "/auth/api-keys", a.token, map[string]string{"name": "scale"})
	key := res.str("token")
	if res.status != http.StatusOK || !strings.HasPrefix(key, apiKeyPrefix) {
		t.Fatalf("create key: %d %s", res.status, res.raw)
	}
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", key, nil))
	if _, err := f.keys.Verify(ctx, key); err != nil {
		t.Fatalf("API key after a sign-out with it: %v", err)
	}
	if n := revocationRows(t, f); n != 0 {
		t.Fatalf("%d sign-outs saved for an API key, want none", n)
	}
}

// Tokens issued by 0.4.x servers carry no issue time; they can be ended by
// signing out all the same, without touching the account's other sessions.
func TestLogoutOfATokenWithoutIssueTime(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	payload, _ := json.Marshal(map[string]any{
		"uid": a.userID, "org": a.orgID, "email": a.email, "role": "user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	body := base64.RawURLEncoding.EncodeToString(payload)
	untimed := body + "." + f.sessions.sign(body)
	if _, err := f.sessions.Verify(ctx, untimed); err != nil {
		t.Fatalf("token without issue time: %v", err)
	}

	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", untimed, nil))
	if _, err := f.sessions.Verify(ctx, untimed); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("token without issue time after signing out: want errSessionRevoked, got %v", err)
	}
	if _, err := f.sessions.Verify(ctx, a.token); err != nil {
		t.Fatalf("the account's other session: %v", err)
	}
}

// A demo visitor signing out only loses the cookie: the demo is read-only and
// public, and recording its sessions would let anyone grow the list without
// bound by fetching and ending demo sessions. "Log out everywhere" stays
// refused (TestLogoutAllRefusedForTheSharedDemoAccount).
func TestDemoSignOutIsNotRecorded(t *testing.T) {
	cfg := loginConfig()
	cfg.Demo = config.DemoConfig{Enabled: true, Email: "demo@test.dev"}
	f := newAPIFixture(t, cfg)
	seedMember(t, f.store, "demo-user", "demo@test.dev", "demo-org", "owner")
	res := f.call(t, http.MethodPost, "/auth/demo-login", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("demo login: %d %s", res.status, res.raw)
	}
	visitor := res.str("token")
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", visitor, nil))
	if n := revocationRows(t, f); n != 0 {
		t.Fatalf("a demo sign-out was recorded: %d rows", n)
	}
	f.sessions.mu.Lock()
	inMemory := len(f.sessions.signedOut)
	f.sessions.mu.Unlock()
	if inMemory != 0 {
		t.Fatalf("a demo sign-out was kept in memory: %d entries", inMemory)
	}
}

// Records of tokens that have expired are dropped when the next session is
// ended, from memory and from the database, and are not loaded at start.
func TestExpiredSignOutsArePruned(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	first, second := f.newDevice(t, a), f.newDevice(t, a)
	db := f.store.DB()
	if _, err := db.Exec(db.Rebind(`INSERT INTO session_revocation (token_hash, expires_at) VALUES (?, ?)`),
		"expired-in-the-table", time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", first.token, nil))
	var left []string
	if err := db.Select(&left, `SELECT token_hash FROM session_revocation`); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0] != hashKey(first.token) {
		t.Fatalf("saved sign-outs after pruning: %v, want only the first session's hash", left)
	}

	// The first token's record runs out; the next sign-out drops it.
	f.sessions.mu.Lock()
	f.sessions.signedOut[hashKey(first.token)] = time.Now().Add(-time.Second)
	f.sessions.mu.Unlock()
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", second.token, nil))
	f.sessions.mu.Lock()
	_, kept := f.sessions.signedOut[hashKey(first.token)]
	held := len(f.sessions.signedOut)
	f.sessions.mu.Unlock()
	if kept || held != 1 {
		t.Fatalf("held in memory after pruning: %d entries, the expired one kept: %v", held, kept)
	}

	// A restart loads only the records whose tokens have not expired.
	if _, err := db.Exec(db.Rebind(`UPDATE session_revocation SET expires_at = ? WHERE token_hash = ?`),
		time.Now().Add(-time.Second).UnixMilli(), hashKey(second.token)); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSessionManager(testSecret, time.Hour, f.store.Users(), f.store.Members())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.UseRevocations(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	if n := len(restarted.signedOut); n != 1 {
		t.Fatalf("loaded at start: %d records, want 1", n)
	}
	if _, ok := restarted.signedOut[hashKey(second.token)]; ok {
		t.Fatal("an expired record was loaded at start")
	}
}

// When the sign-out cannot be saved the user is signed out all the same, and
// this server keeps refusing the token (see SessionManager.signOut).
func TestLogoutWhenTheDatabaseFails(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	if _, err := f.store.DB().Exec(`ALTER TABLE session_revocation RENAME TO session_revocation_gone`); err != nil {
		t.Fatal(err)
	}
	wantSignedOut(t, f.call(t, http.MethodPost, "/auth/logout", a.token, nil))
	if _, err := f.sessions.Verify(ctx, a.token); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("session whose sign-out was not saved: want errSessionRevoked, got %v", err)
	}
}

// A server that cannot read the list of signed-out sessions must not start
// without it: their tokens would work again.
func TestUseRevocationsFailsWithoutTheTable(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	if _, err := f.store.DB().Exec(`ALTER TABLE session_revocation RENAME TO session_revocation_gone`); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSessionManager(testSecret, time.Hour, f.store.Users(), f.store.Members())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.UseRevocations(context.Background(), f.store); err == nil {
		t.Fatal("loading the signed-out sessions from a missing table must fail")
	}
}
