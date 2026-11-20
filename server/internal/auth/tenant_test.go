package auth

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// A link or form on another site must not be able to delete the workspace of
// a signed-in owner: the browser attaches the session cookie to top-level GET
// navigations, so the endpoint must not act on a GET, nor on a request that
// does not name the workspace.
func TestDeleteWorkspaceCannotBeTriggeredFromAnotherSite(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	cookie := []string{"Cookie", SessionCookie + "=" + owner.token}
	exists := func() bool {
		_, err := f.store.Orgs().Get(context.Background(), owner.orgID)
		return err == nil
	}

	// A cross-site GET (a link, an image tag) with the owner's cookie.
	res := f.call(t, http.MethodGet, "/tenants/delete", "", nil, append(cookie, "Sec-Fetch-Site", "cross-site")...)
	wantError(t, res, http.StatusMethodNotAllowed, "method_not_allowed")
	// A cross-site form post: no JSON content type, foreign origin.
	res = f.call(t, http.MethodPost, "/tenants/delete", "", "org_id="+owner.orgID,
		append(cookie, "Content-Type", "application/x-www-form-urlencoded", "Origin", "https://evil.example")...)
	wantError(t, res, http.StatusForbidden, "cross_site_request")
	// Even with a JSON body, a foreign origin is refused.
	res = f.call(t, http.MethodPost, "/tenants/delete", "", map[string]string{"org_id": owner.orgID},
		append(cookie, "Origin", "https://evil.example")...)
	wantError(t, res, http.StatusForbidden, "cross_site_request")
	// Same-origin, but without naming the workspace: no default.
	res = f.call(t, http.MethodPost, "/tenants/delete", "", nil, append(cookie, "Content-Type", "application/json")...)
	wantError(t, res, http.StatusBadRequest, "invalid_body")
	res = f.call(t, http.MethodPost, "/tenants/delete", "", map[string]string{}, cookie...)
	wantError(t, res, http.StatusBadRequest, "org_id_required")
	if !exists() {
		t.Fatal("the workspace was deleted by a request that must be refused")
	}

	// The app's own request still works, with the cookie or the bearer token.
	res = f.call(t, http.MethodPost, "/tenants/delete", "", map[string]string{"org_id": owner.orgID},
		append(cookie, "Origin", f.srv.URL, "Sec-Fetch-Site", "same-origin")...)
	if res.status != http.StatusOK || exists() {
		t.Fatalf("legitimate delete: %d %s (still exists: %v)", res.status, res.raw, exists())
	}
}

func TestStateChangingRoutesRequirePOST(t *testing.T) {
	cfg := loginConfig()
	cfg.Demo.Enabled = true
	cfg.Demo.Email = "demo@test.dev"
	f := newAPIFixture(t, cfg)
	owner := f.signup(t, "owner@test.dev")
	for _, path := range []string{
		"/auth/logout", "/auth/logout-all", "/auth/demo-login", "/auth/switch", "/auth/signin", "/auth/signup",
		"/tenants/create", "/tenants/invite", "/tenants/invite/revoke", "/tenants/delete", "/auth/accept-invite",
		"/auth/api-keys/delete",
	} {
		res := f.call(t, http.MethodGet, path+"?org_id="+owner.orgID, owner.token, nil)
		wantError(t, res, http.StatusMethodNotAllowed, "method_not_allowed")
	}
	// The session survived all of it.
	if res := f.call(t, http.MethodGet, "/auth/me", owner.token, nil); res.status != http.StatusOK {
		t.Fatalf("session after the GET attempts: %d %s", res.status, res.raw)
	}
}

func TestDeleteWorkspaceRemovesKeysAndEndsMemberSessions(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	if err := f.store.Members().Add(ctx, &storage.Membership{OrgID: owner.orgID, UserID: member.userID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	res := f.call(t, http.MethodPost, "/auth/switch", member.token, map[string]string{"org_id": owner.orgID})
	if res.status != http.StatusOK {
		t.Fatalf("switch: %d %s", res.status, res.raw)
	}
	memberInShared := res.str("token")
	res = f.call(t, http.MethodPost, "/auth/api-keys", memberInShared, map[string]string{"name": "scale"})
	if res.status != http.StatusOK {
		t.Fatalf("create key: %d %s", res.status, res.raw)
	}
	key := res.str("token")
	if _, err := f.keys.Verify(ctx, key); err != nil {
		t.Fatalf("fresh key: %v", err)
	}

	// Only owners delete.
	res = f.call(t, http.MethodPost, "/tenants/delete", memberInShared, map[string]string{"org_id": owner.orgID})
	wantError(t, res, http.StatusForbidden, "owner_only")

	res = f.call(t, http.MethodPost, "/tenants/delete", owner.token, map[string]string{"org_id": owner.orgID})
	if res.status != http.StatusOK || res.str("active_org") == "" || res.str("active_org") == owner.orgID {
		t.Fatalf("delete: %d %s", res.status, res.raw)
	}
	var keys int
	if err := f.store.DB().Get(&keys, f.store.DB().Rebind(`SELECT COUNT(*) FROM api_key WHERE organization_id = ?`), owner.orgID); err != nil || keys != 0 {
		t.Fatalf("api keys of the deleted workspace: %d (err %v)", keys, err)
	}
	if _, err := f.keys.Verify(ctx, key); err == nil {
		t.Fatal("a key of the deleted workspace still verifies")
	}
	// The member's session for the deleted workspace is dead at once; their
	// personal one, and the owner's new one, work.
	if res := f.call(t, http.MethodGet, "/auth/me", memberInShared, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("member session for the deleted workspace: %d", res.status)
	}
	if res := f.call(t, http.MethodGet, "/auth/me", member.token, nil); res.status != http.StatusOK {
		t.Fatalf("member's personal session: %d", res.status)
	}
	if me := f.call(t, http.MethodGet, "/auth/me", res.str("token"), nil); me.status != http.StatusOK || me.str("active_org") != res.str("active_org") {
		t.Fatalf("owner's reissued session: %d %s", me.status, me.raw)
	}
}

func TestAcceptInviteIsBoundToItsEmailAndExpires(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	owner := f.signup(t, "owner@test.dev")
	invited := f.signup(t, "Invited@Test.dev") // stored lower-case
	stranger := f.signup(t, "stranger@test.dev")

	res := f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "INVITED@test.dev", "org_id": owner.orgID})
	if res.status != http.StatusOK || res.str("token") == "" || res.str("expires_at") == "" {
		t.Fatalf("invite: %d %s", res.status, res.raw)
	}
	token := res.str("token")
	inv, err := f.store.Invites().GetByToken(ctx, token)
	if err != nil || inv.ExpiresAt == nil || time.Until(*inv.ExpiresAt).Round(time.Hour) != storage.InviteTTL {
		t.Fatalf("stored invite must expire in seven days: %v %+v", err, inv)
	}

	// Someone else holding the link cannot join.
	res = f.call(t, http.MethodPost, "/auth/accept-invite", stranger.token, map[string]string{"token": token})
	wantError(t, res, http.StatusForbidden, "invite_email_mismatch")
	if _, err := f.store.Members().Get(ctx, owner.orgID, stranger.userID); err == nil {
		t.Fatal("a stranger joined with someone else's invite")
	}
	res = f.call(t, http.MethodPost, "/auth/accept-invite", invited.token, map[string]string{"token": "nope"})
	wantError(t, res, http.StatusBadRequest, "invite_invalid")

	// Expired: refused, also for the right account.
	past := time.Now().Add(-time.Minute)
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(`UPDATE invite SET expires_at = ? WHERE id = ?`), past, inv.ID); err != nil {
		t.Fatal(err)
	}
	res = f.call(t, http.MethodPost, "/auth/accept-invite", invited.token, map[string]string{"token": token})
	wantError(t, res, http.StatusBadRequest, "invite_expired")
	list := f.call(t, http.MethodGet, "/tenants/invites?org_id="+owner.orgID, owner.token, nil)
	if row := list.body["invites"].([]any)[0].(map[string]any); row["expired"] != true || row["expires_at"] == nil {
		t.Fatalf("the owner's list must flag the expired invite: %v", row)
	}

	// Valid again: the invited account joins (email compared case-insensitively).
	future := time.Now().Add(time.Hour)
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(`UPDATE invite SET expires_at = ? WHERE id = ?`), future, inv.ID); err != nil {
		t.Fatal(err)
	}
	res = f.call(t, http.MethodPost, "/auth/accept-invite", invited.token, map[string]string{"token": token})
	if res.status != http.StatusOK || res.str("active_org") != owner.orgID {
		t.Fatalf("accept: %d %s", res.status, res.raw)
	}
	if res := f.call(t, http.MethodGet, "/auth/me", res.str("token"), nil); res.status != http.StatusOK || res.str("active_org") != owner.orgID {
		t.Fatalf("session in the joined workspace: %d %s", res.status, res.raw)
	}
	// Used once.
	res = f.call(t, http.MethodPost, "/auth/accept-invite", invited.token, map[string]string{"token": token})
	wantError(t, res, http.StatusBadRequest, "invite_invalid")
}

func TestInviteOnlySignupRejectsExpiredInvite(t *testing.T) {
	cfg := loginConfig()
	cfg.Auth.RegistrationOpen = false
	f := newAPIFixture(t, cfg)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	inv := &storage.Invite{ID: "inv1", OrgID: "org1", Email: "friend@test.dev", Role: "member", Token: "tok-friend", CreatedAt: old}
	if err := f.store.Invites().Create(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "friend@test.dev", "password": "password123", "invite": "tok-friend"})
	wantError(t, res, http.StatusForbidden, "invite_expired")
}

// syncBuffer collects log output written from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Invite and verification links are credentials; they must not end up in the
// server log, whether or not mail is configured.
func TestSignInLinksAreNotLogged(t *testing.T) {
	var buf syncBuffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	cfg := loginConfig()
	cfg.Auth.EmailVerification = true
	cfg.Auth.SMTP.Host = "127.0.0.1" // nothing listens: sending fails
	cfg.Auth.SMTP.Port = "1"
	f := newAPIFixture(t, cfg)

	res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "new@test.dev", "password": "password123"})
	if res.status != http.StatusOK || res.str("status") != "verify" {
		t.Fatalf("signup: %d %s", res.status, res.raw)
	}
	u, err := f.store.Users().GetByEmail(context.Background(), "new@test.dev")
	if err != nil || u.VerifyToken == "" {
		t.Fatalf("verification token: %v", err)
	}

	admin := f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": "admin@test.dev", "password": "password123"})
	res = f.call(t, http.MethodPost, "/tenants/invite", admin.str("token"), map[string]string{"email": "friend@test.dev"})
	if res.status != http.StatusOK {
		t.Fatalf("invite: %d %s", res.status, res.raw)
	}
	inviteToken := res.str("token")

	// Both mails are sent in the background; wait for their failures to be logged.
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), "not sent") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	logged := buf.String()
	if strings.Count(logged, "not sent") < 2 {
		t.Fatalf("expected both send failures in the log, got:\n%s", logged)
	}
	for name, secret := range map[string]string{"invite token": inviteToken, "verification token": u.VerifyToken} {
		if strings.Contains(logged, secret) {
			t.Errorf("the %s was written to the log:\n%s", name, logged)
		}
	}
}

func TestErrorsFollowTheContractAndHideDatabaseErrors(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")

	wantError(t, f.call(t, http.MethodGet, "/auth/me", "", nil), http.StatusUnauthorized, "not_signed_in")
	wantError(t, f.call(t, http.MethodGet, "/auth/me", "garbage", nil), http.StatusUnauthorized, "not_signed_in")
	wantError(t, f.call(t, http.MethodPost, "/tenants/create", owner.token, map[string]string{"name": " "}), http.StatusBadRequest, "name_required")
	wantError(t, f.call(t, http.MethodPost, "/auth/switch", owner.token, map[string]string{"org_id": "nope"}), http.StatusForbidden, "not_a_member")
	wantError(t, f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "nope"}), http.StatusBadRequest, "invalid_email")
	wantError(t, f.call(t, http.MethodPost, "/tenants/invite/revoke", owner.token, map[string]string{"id": "nope"}), http.StatusNotFound, "invite_not_found")
	wantError(t, f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": "owner@test.dev", "password": "wrong"}), http.StatusUnauthorized, "invalid_credentials")
	wantError(t, f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "owner@test.dev", "password": "password123"}), http.StatusConflict, "email_taken")
	wantError(t, f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "x@test.dev", "password": "short"}), http.StatusBadRequest, "invalid_signup")
	wantError(t, f.call(t, http.MethodPost, "/auth/api-keys/delete", owner.token, map[string]string{"id": "nope"}), http.StatusNotFound, "key_not_found")
	wantError(t, f.call(t, http.MethodGet, "/tenants/unknown", "", nil), http.StatusNotFound, "not_found")

	// A failing database: the driver's message stays in the log.
	if _, err := f.store.DB().Exec(`DROP TABLE organization`); err != nil {
		t.Fatal(err)
	}
	for _, res := range []response{
		f.call(t, http.MethodPost, "/tenants/create", owner.token, map[string]string{"name": "Club"}),
		f.call(t, http.MethodGet, "/auth/me", owner.token, nil),
	} {
		wantError(t, res, http.StatusInternalServerError, "internal")
		if low := strings.ToLower(res.raw); strings.Contains(low, "organization") || strings.Contains(low, "sql") || strings.Contains(low, "table") {
			t.Fatalf("database error leaked to the client: %s", res.raw)
		}
	}
	if _, err := f.store.DB().Exec(`DROP TABLE api_key`); err != nil {
		t.Fatal(err)
	}
	for _, res := range []response{
		f.call(t, http.MethodGet, "/auth/api-keys", owner.token, nil),
		f.call(t, http.MethodPost, "/auth/api-keys", owner.token, map[string]string{"name": "k"}),
		f.call(t, http.MethodPost, "/auth/api-keys/delete", owner.token, map[string]string{"id": "k"}),
	} {
		wantError(t, res, http.StatusInternalServerError, "internal")
		if low := strings.ToLower(res.raw); strings.Contains(low, "api_key") || strings.Contains(low, "sql") {
			t.Fatalf("database error leaked to the client: %s", res.raw)
		}
	}
}

func TestInstanceInfo(t *testing.T) {
	cfg := loginConfig()
	cfg.Maps.Enabled = true
	cfg.Maps.TileURL = "https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
	cfg.Maps.GeocoderURL = "https://nominatim.openstreetmap.org"
	f := newAPIFixture(t, cfg)
	res := f.call(t, http.MethodGet, "/auth/instance", "", nil)
	m, _ := res.body["map"].(map[string]any)
	if res.status != http.StatusOK || res.body["password_auth"] != true || res.body["registration"] != true ||
		m["enabled"] != true || m["tile_url"] != cfg.Maps.TileURL || m["geocoder_url"] != cfg.Maps.GeocoderURL {
		t.Fatalf("instance: %d %s", res.status, res.raw)
	}

	// Maps switched off, no login configured: still served, everything false.
	noLogin := &config.Config{}
	noLogin.Auth.RegistrationOpen = true // the env default; meaningless without login
	rt := NewRouter(noLogin)
	rt.Get("/auth/instance", InstanceHandler(noLogin))
	rec := serve(rt, http.MethodGet, "/auth/instance", "")
	body := rec.Body.String()
	for _, want := range []string{`"password_auth":false`, `"registration":false`, `"oidc_providers":[]`,
		`"map":{"enabled":false,"geocoder_url":"","tile_url":""}`} {
		if !strings.Contains(body, want) {
			t.Errorf("no-login instance info lacks %s: %s", want, body)
		}
	}
}

// Inviting a member is refused; inviting an address again replaces its open
// invite instead of piling up links.
func TestInviteRefusesMembersAndReplacesOpenInvites(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	if err := f.store.Members().Add(ctx, &storage.Membership{OrgID: owner.orgID, UserID: member.userID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	res := f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "Member@test.dev", "org_id": owner.orgID})
	wantError(t, res, http.StatusConflict, "already_member")
	res = f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "owner@test.dev", "org_id": owner.orgID})
	wantError(t, res, http.StatusConflict, "already_member")

	first := f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "friend@test.dev", "org_id": owner.orgID})
	if first.status != http.StatusOK || first.body["renewed"] != false {
		t.Fatalf("first invite: %d %s", first.status, first.raw)
	}
	second := f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "FRIEND@test.dev", "org_id": owner.orgID})
	if second.status != http.StatusOK || second.body["renewed"] != true || second.str("token") == first.str("token") {
		t.Fatalf("second invite: %d %s", second.status, second.raw)
	}
	open, err := f.store.Invites().ListByOrg(ctx, owner.orgID)
	if err != nil || len(open) != 1 || open[0].Token != second.str("token") {
		t.Fatalf("open invites: %+v (%v), want only the second", open, err)
	}
}
