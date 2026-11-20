package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// exec runs a statement on the fixture's database.
func (f *apiFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(q), args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// count returns the number of rows a query counts.
func (f *apiFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.store.DB().Get(&n, f.store.DB().Rebind(q), args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// writeRecords stores what an account leaves in a workspace: a shared
// history entry it wrote, a personal task, and the change log of both.
func (f *apiFixture) writeRecords(t *testing.T, orgID, userID, prefix string) {
	t.Helper()
	now := time.Now().UTC()
	personal := "user:" + userID
	logChange := func(id, scope, entity, entityID string) {
		f.seq++
		f.exec(t, `INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, f.seq, scope, entity, entityID, 1, `{}`, "h", userID, orgID)
	}
	f.exec(t, `INSERT INTO event (id, organization_id, scope_id, type, date, title, author_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		prefix+"-ev", orgID, "ap-1", 1, now, "Visit", userID)
	logChange(prefix+"-cl-ev", "ap-1", "event", prefix+"-ev")
	f.exec(t, `INSERT INTO task (id, organization_id, title, created_at) VALUES (?, ?, ?, ?)`,
		prefix+"-task", orgID, "Order jars", now)
	logChange(prefix+"-cl-task", personal, "task", prefix+"-task")
	f.exec(t, `INSERT INTO event (id, organization_id, scope_id, type, date, title, author_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		prefix+"-ev-own", orgID, personal, 2, now, "Task done", userID)
}

func deleteAccount(f *apiFixture, t *testing.T, token string, body map[string]string) response {
	t.Helper()
	return f.call(t, http.MethodPost, "/auth/account/delete", token, body)
}

func TestDeleteAccount(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	club := f.signup(t, "club-owner@test.dev")
	leaver := f.signup(t, "leaver@test.dev")
	f.join(t, club.orgID, leaver, "member")
	inClub := f.switchTo(t, leaver, club.orgID)
	clubKey, ownKey := f.apiKey(t, inClub), f.apiKey(t, leaver.token)
	f.writeRecords(t, club.orgID, leaver.userID, "club")
	f.writeRecords(t, leaver.orgID, leaver.userID, "own")
	f.exec(t, `INSERT INTO user_passkey (id, user_id, name, cred, created_at) VALUES (?, ?, ?, ?, ?)`, "pk-1", leaver.userID, "Laptop", `{}`, time.Now().UTC())
	// An invite somebody else addressed to the account, and one the account sent.
	f.exec(t, `INSERT INTO invite (id, organization_id, email, role, token, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "inv-to", club.orgID, "Leaver@test.dev", "member", "tok-to", time.Now().UTC())
	f.exec(t, `INSERT INTO invite (id, organization_id, email, role, token, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "inv-from", leaver.orgID, "friend@test.dev", "member", "tok-from", time.Now().UTC())

	// What the account is told beforehand.
	info := f.call(t, http.MethodGet, "/auth/account", leaver.token, nil)
	plan, _ := info.body["deletion"].(map[string]any)
	if info.status != http.StatusOK || info.str("password") != "set" || plan["confirm"] != "password" || plan["protected"] != "" {
		t.Fatalf("account info: %d %s", info.status, info.raw)
	}
	for key, wantID := range map[string]string{"deletes": leaver.orgID, "leaves": club.orgID} {
		list, _ := plan[key].([]any)
		if len(list) != 1 || list[0].(map[string]any)["id"] != wantID {
			t.Fatalf("plan %s: %v", key, plan[key])
		}
	}
	if blocked, _ := plan["blocked"].([]any); len(blocked) != 0 {
		t.Fatalf("nothing blocks this account: %v", plan["blocked"])
	}

	// The password is asked for again.
	wantError(t, deleteAccount(f, t, "", map[string]string{"password": "password123"}), http.StatusUnauthorized, "not_signed_in")
	wantError(t, deleteAccount(f, t, leaver.token, map[string]string{"password": "wrong"}), http.StatusForbidden, "wrong_password")
	wantError(t, deleteAccount(f, t, leaver.token, map[string]string{}), http.StatusForbidden, "wrong_password")
	if _, err := f.store.Users().GetByID(ctx, leaver.userID); err != nil {
		t.Fatalf("a refused deletion must leave the account: %v", err)
	}

	res := deleteAccount(f, t, leaver.token, map[string]string{"password": "password123"})
	if res.status != http.StatusOK {
		t.Fatalf("delete: %d %s", res.status, res.raw)
	}

	// The account and everything that identifies its owner is gone.
	if _, err := f.store.Users().GetByID(ctx, leaver.userID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the account still exists: %v", err)
	}
	for what, q := range map[string][2]string{
		"memberships":       {`SELECT COUNT(*) FROM member WHERE benutzer_id = ?`, leaver.userID},
		"passkeys":          {`SELECT COUNT(*) FROM user_passkey WHERE user_id = ?`, leaver.userID},
		"API keys":          {`SELECT COUNT(*) FROM api_key WHERE user_id = ?`, leaver.userID},
		"personal history":  {`SELECT COUNT(*) FROM change_log WHERE scope_id = ?`, "user:" + leaver.userID},
		"personal timeline": {`SELECT COUNT(*) FROM event WHERE scope_id = ?`, "user:" + leaver.userID},
	} {
		if n := f.count(t, q[0], q[1]); n != 0 {
			t.Errorf("%s left behind: %d", what, n)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE id IN ('club-task', 'own-task')`); n != 0 {
		t.Errorf("personal tasks left behind: %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM invite WHERE id = 'inv-to'`); n != 0 {
		t.Error("an invite to the deleted account's address is left behind")
	}
	for name, tok := range map[string]string{"own workspace": leaver.token, "club": inClub} {
		if me := f.call(t, http.MethodGet, "/auth/me", tok, nil); me.status != http.StatusUnauthorized {
			t.Errorf("the session for the %s survived: %d", name, me.status)
		}
	}
	for name, key := range map[string]string{"own workspace": ownKey, "club": clubKey} {
		if _, err := f.keys.Verify(ctx, key); err == nil {
			t.Errorf("the API key for the %s survived", name)
		}
	}
	wantError(t, f.signIn(t, "leaver@test.dev", "password123"), http.StatusUnauthorized, "invalid_credentials")

	// The workspace it was alone in went with it, with all its rows.
	if _, err := f.store.Orgs().Get(ctx, leaver.orgID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the sole-member workspace still exists: %v", err)
	}
	for _, table := range []string{"event", "task", "invite", "api_key", "member"} {
		if n := f.count(t, `SELECT COUNT(*) FROM `+table+` WHERE organization_id = ?`, leaver.orgID); n != 0 {
			t.Errorf("%s rows of the deleted workspace left behind: %d", table, n)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM change_log WHERE org_id = ?`, leaver.orgID); n != 0 {
		t.Errorf("change log of the deleted workspace left behind: %d", n)
	}

	// What it wrote for others stays, under an id that names no account.
	if n := f.count(t, `SELECT COUNT(*) FROM event WHERE id = 'club-ev' AND author_id = ?`, leaver.userID); n != 1 {
		t.Error("the shared history entry must stay")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM change_log WHERE id = 'club-cl-ev'`); n != 1 {
		t.Error("the shared change-log entry must stay")
	}
	if _, err := f.store.Orgs().Get(ctx, club.orgID); err != nil {
		t.Fatalf("the shared workspace must stay: %v", err)
	}
	if got := memberList(t, f.call(t, http.MethodGet, "/tenants/members", club.token, nil)); len(got) != 1 {
		t.Fatalf("the club's members after the deletion: %v", got)
	}

	// The address is free again.
	if again := f.signup(t, "leaver@test.dev"); again.userID == leaver.userID {
		t.Fatal("a new sign-up must get a new account id")
	}
}

func TestDeleteAccountIsBlockedForASoleOwnerWithMembers(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	f.join(t, owner.orgID, member, "member")

	info := f.call(t, http.MethodGet, "/auth/account", owner.token, nil)
	plan, _ := info.body["deletion"].(map[string]any)
	blocked, _ := plan["blocked"].([]any)
	if len(blocked) != 1 || blocked[0].(map[string]any)["id"] != owner.orgID || blocked[0].(map[string]any)["members"] != float64(2) {
		t.Fatalf("the plan must name the blocking workspace: %s", info.raw)
	}

	res := deleteAccount(f, t, owner.token, map[string]string{"password": "password123"})
	wantError(t, res, http.StatusConflict, "sole_owner")
	if list, _ := res.body["workspaces"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != owner.orgID {
		t.Fatalf("the refusal must name the workspace: %s", res.raw)
	}
	ctx := context.Background()
	if _, err := f.store.Users().GetByID(ctx, owner.userID); err != nil {
		t.Fatalf("the account must still exist: %v", err)
	}
	if _, err := f.store.Orgs().Get(ctx, owner.orgID); err != nil {
		t.Fatalf("the workspace must still exist: %v", err)
	}

	// Hand the workspace over, and the account can go; the workspace stays.
	if res := f.call(t, http.MethodPost, "/tenants/members/role", owner.token, map[string]string{"org_id": owner.orgID, "user_id": member.userID, "role": "owner"}); res.status != http.StatusOK {
		t.Fatalf("promote: %d %s", res.status, res.raw)
	}
	if res := deleteAccount(f, t, owner.token, map[string]string{"password": "password123"}); res.status != http.StatusOK {
		t.Fatalf("delete after handing over: %d %s", res.status, res.raw)
	}
	if _, err := f.store.Orgs().Get(ctx, owner.orgID); err != nil {
		t.Fatalf("a workspace with other members must survive its former owner: %v", err)
	}
	if got := memberList(t, f.call(t, http.MethodGet, "/tenants/members?org_id="+owner.orgID, f.switchTo(t, member, owner.orgID), nil)); len(got) != 1 {
		t.Fatalf("members after the deletion: %v", got)
	}
}

func TestDeleteAccountRefusesProtectedAccounts(t *testing.T) {
	cfg := loginConfig()
	cfg.Demo.Enabled, cfg.Demo.Email = true, "demo@test.dev"
	f := newAPIFixture(t, cfg)
	ctx := context.Background()

	admin := f.adminLogin(t)
	info := f.call(t, http.MethodGet, "/auth/account", admin.token, nil)
	if plan, _ := info.body["deletion"].(map[string]any); plan["protected"] != "instance_admin" || info.str("password") != "managed" {
		t.Fatalf("admin account info: %s", info.raw)
	}
	wantError(t, deleteAccount(f, t, admin.token, map[string]string{"password": cfg.Auth.AdminPassword}), http.StatusForbidden, "account_protected")
	if _, err := f.store.Users().GetByID(ctx, admin.userID); err != nil {
		t.Fatalf("the instance admin must still exist: %v", err)
	}

	seedMember(t, f.store, "demo", "demo@test.dev", "demo-org", "owner")
	demoTok, _ := f.sessions.Issue(Identity{UserID: "demo", OrgID: "demo-org", Email: "demo@test.dev", Role: "demo"})
	wantError(t, deleteAccount(f, t, demoTok, map[string]string{"password": "demo"}), http.StatusForbidden, "demo_read_only")
	if _, err := f.store.Users().GetByID(ctx, "demo"); err != nil {
		t.Fatalf("the demo account must still exist: %v", err)
	}
}

// An account without a password confirms with a fresh passkey assertion.
func TestDeleteAccountWithPasskey(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	seedMember(t, f.store, "sso", "sso@test.dev", "sso-org", "owner")
	f.exec(t, `INSERT INTO user_passkey (id, user_id, name, cred, created_at) VALUES (?, ?, ?, ?, ?)`, "pk-1", "sso", "Phone", `{}`, time.Now().UTC())
	// Even a session fresh from a sign-in does not replace the passkey.
	tok, _ := f.sessions.IssueLogin(Identity{UserID: "sso", OrgID: "sso-org", Email: "sso@test.dev", Role: "user"})

	info := f.call(t, http.MethodGet, "/auth/account", tok, nil)
	if plan, _ := info.body["deletion"].(map[string]any); plan["confirm"] != "passkey" || info.str("password") != "none" {
		t.Fatalf("account info with a passkey: %s", info.raw)
	}
	wantError(t, deleteAccount(f, t, tok, map[string]string{}), http.StatusForbidden, "reauth_required")
	wantError(t, deleteAccount(f, t, tok, map[string]string{"reauth": "made-up"}), http.StatusForbidden, "reauth_required")
	// A proof issued to another account is worth nothing, and is spent by the attempt.
	foreign := f.reauth.issue("someone-else")
	wantError(t, deleteAccount(f, t, tok, map[string]string{"reauth": foreign}), http.StatusForbidden, "reauth_required")
	if _, err := f.store.Users().GetByID(ctx, "sso"); err != nil {
		t.Fatalf("the account must still exist: %v", err)
	}

	if res := deleteAccount(f, t, tok, map[string]string{"reauth": f.reauth.issue("sso")}); res.status != http.StatusOK {
		t.Fatalf("delete with a passkey proof: %d %s", res.status, res.raw)
	}
	if _, err := f.store.Users().GetByID(ctx, "sso"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the account still exists: %v", err)
	}
}

// An account with neither password nor passkey (it signs in with a provider)
// confirms by having just signed in: the session must come straight out of
// a sign-in that is at most five minutes old.
func TestDeleteAccountWithRecentSignIn(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	seedMember(t, f.store, "sso", "sso@test.dev", "sso-org", "owner")
	seedMember(t, f.store, "sso", "sso@test.dev", "second-org", "owner")
	id := Identity{UserID: "sso", OrgID: "sso-org", Email: "sso@test.dev", Role: "user"}
	stillThere := func(when string) {
		t.Helper()
		if _, err := f.store.Users().GetByID(ctx, "sso"); err != nil {
			t.Fatalf("%s: the account must still exist: %v", when, err)
		}
	}

	fresh, err := f.sessions.IssueLogin(id)
	if err != nil {
		t.Fatal(err)
	}
	info := f.call(t, http.MethodGet, "/auth/account", fresh, nil)
	if plan, _ := info.body["deletion"].(map[string]any); plan["confirm"] != "login" || info.str("password") != "none" {
		t.Fatalf("account info: %s", info.raw)
	}

	// Too old: signed in six minutes ago.
	old, _ := f.sessions.issueAt(id, time.Now().Add(-RecentLoginWindow-time.Minute), true)
	wantError(t, deleteAccount(f, t, old, map[string]string{}), http.StatusForbidden, "login_required")
	stillThere("old sign-in")

	// Not from a sign-in at all: a plain session, and the sessions that
	// switching the workspace, recovering and "log out everywhere" hand out
	// in exchange for a fresh one.
	plain, _ := f.sessions.Issue(id)
	wantError(t, deleteAccount(f, t, plain, map[string]string{}), http.StatusForbidden, "login_required")
	for path, body := range map[string]any{
		"/auth/switch":     map[string]string{"org_id": "second-org"},
		"/auth/logout-all": nil,
	} {
		login, _ := f.sessions.IssueLogin(id)
		res := f.call(t, http.MethodPost, path, login, body)
		if res.status != http.StatusOK || res.str("token") == "" {
			t.Fatalf("%s: %d %s", path, res.status, res.raw)
		}
		wantError(t, deleteAccount(f, t, res.str("token"), map[string]string{}), http.StatusForbidden, "login_required")
		stillThere(path)
	}
	// Recovery: the session's workspace disappears, the replacement is not a sign-in.
	lost, _ := f.sessions.IssueLogin(Identity{UserID: "sso", OrgID: "second-org", Email: "sso@test.dev", Role: "user"})
	if err := f.store.Orgs().Delete(ctx, "second-org"); err != nil {
		t.Fatal(err)
	}
	f.sessions.Invalidate()
	res := f.call(t, http.MethodPost, "/auth/session/recover", lost, nil)
	if res.status != http.StatusOK || res.body["switched"] != true {
		t.Fatalf("recover: %d %s", res.status, res.raw)
	}
	wantError(t, deleteAccount(f, t, res.str("token"), map[string]string{}), http.StatusForbidden, "login_required")
	// A password in the body changes nothing: the account has none.
	wantError(t, deleteAccount(f, t, res.str("token"), map[string]string{"password": "guess"}), http.StatusForbidden, "login_required")
	stillThere("recovered session")

	// Accepted: the session of a sign-in that has just happened.
	fresh, _ = f.sessions.IssueLogin(id)
	if res := deleteAccount(f, t, fresh, map[string]string{}); res.status != http.StatusOK {
		t.Fatalf("delete right after signing in: %d %s", res.status, res.raw)
	}
	if _, err := f.store.Users().GetByID(ctx, "sso"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the account still exists: %v", err)
	}
}

// Only the token a sign-in hands out carries the time of that sign-in.
func TestSignInMarksItsSession(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	a := f.signup(t, "a@test.dev")
	for name, tok := range map[string]string{"sign-up": a.token, "sign-in": f.signIn(t, "a@test.dev", "password123").str("token")} {
		id, err := f.sessions.Verify(ctx, tok)
		if err != nil || !RecentLogin(id, time.Now()) {
			t.Errorf("%s: want a session marked as a fresh sign-in, got %+v %v", name, id, err)
		}
		if RecentLogin(id, time.Now().Add(RecentLoginWindow+time.Second)) {
			t.Errorf("%s: the mark must lapse after five minutes", name)
		}
	}
	switched := f.switchTo(t, a, a.orgID)
	if id, err := f.sessions.Verify(ctx, switched); err != nil || !id.LoginAt.IsZero() {
		t.Fatalf("a reissued session must not carry the sign-in time: %+v %v", id, err)
	}
	// A session from a 0.4.x server has no such claim either.
	if RecentLogin(Identity{UserID: "u"}, time.Now()) {
		t.Fatal("a session without a sign-in time is not a recent sign-in")
	}
}

// joinDuringPlan makes another account join a workspace right after the
// deletion plan has looked at its members: the moment a race would hit.
type joinDuringPlan struct {
	storage.MemberRepo
	join func()
}

func (m *joinDuringPlan) ListByOrg(ctx context.Context, orgID string) ([]storage.Member, error) {
	members, err := m.MemberRepo.ListByOrg(ctx, orgID)
	if join := m.join; join != nil {
		m.join = nil
		join()
	}
	return members, err
}

// Someone accepts an invite between the moment the plan finds the account
// alone in its workspace and the delete. The workspace, with whatever the
// newcomer wrote, must not go down with the account.
func TestDeleteAccountDoesNotTakeAWorkspaceSomeoneJustJoined(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	owner := f.signup(t, "owner@test.dev")
	late := f.signup(t, "late@test.dev")

	racing := &joinDuringPlan{MemberRepo: f.store.Members()}
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
	tenants := NewTenantAPI(f.sessions, f.store.Users(), f.store.Orgs(), racing, f.store.Invites(), prov, f.cfg, nil)
	rt := NewRouter(f.cfg)
	NewAccountAPI(tenants, f.store, NewThrottle(false), f.reauth).Routes(rt)
	srv := httptest.NewServer(rt)
	t.Cleanup(srv.Close)
	f.srv = srv

	racing.join = func() {
		f.join(t, owner.orgID, late, "member")
		f.writeRecords(t, owner.orgID, late.userID, "late")
	}
	res := deleteAccount(f, t, owner.token, map[string]string{"password": "password123"})
	wantError(t, res, http.StatusConflict, "sole_owner")
	if list, _ := res.body["workspaces"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != owner.orgID {
		t.Fatalf("the refusal must name the workspace that now has another member: %s", res.raw)
	}
	if _, err := f.store.Users().GetByID(ctx, owner.userID); err != nil {
		t.Fatalf("the account must still exist: %v", err)
	}
	if _, err := f.store.Orgs().Get(ctx, owner.orgID); err != nil {
		t.Fatalf("the workspace must still exist: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM event WHERE id = 'late-ev'`); n != 1 {
		t.Fatal("the newcomer's record was deleted")
	}
	if members, _ := f.store.Members().ListByOrg(ctx, owner.orgID); len(members) != 2 {
		t.Fatalf("members after the refused deletion: %+v", members)
	}
}

// A workspace that is gone cannot be joined: the invite is answered as invalid.
func TestAcceptInviteForADeletedWorkspace(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	guest := f.signup(t, "guest@test.dev")
	inv := f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "guest@test.dev"})
	if inv.status != http.StatusOK {
		t.Fatalf("invite: %d %s", inv.status, inv.raw)
	}
	// The workspace disappears, but the invite row is still there (as it is
	// for a moment while a delete is running).
	f.exec(t, `DELETE FROM organization WHERE id = ?`, owner.orgID)
	wantError(t, f.call(t, http.MethodPost, "/auth/accept-invite", guest.token, map[string]string{"token": inv.str("token")}), http.StatusBadRequest, "invite_invalid")
	if n := f.count(t, `SELECT COUNT(*) FROM member WHERE organization_id = ? AND benutzer_id = ?`, owner.orgID, guest.userID); n != 0 {
		t.Fatal("a membership in a deleted workspace was created")
	}
}

func TestReauthProofs(t *testing.T) {
	s := NewReauthStore()
	now := time.Now()
	s.now = func() time.Time { return now }

	proof := s.issue("u1")
	if s.consume("u2", proof) {
		t.Fatal("a proof must only work for the account it was issued to")
	}
	if s.consume("u1", proof) {
		t.Fatal("a proof is spent by the first attempt, whoever made it")
	}
	proof = s.issue("u1")
	if !s.consume("u1", proof) || s.consume("u1", proof) {
		t.Fatal("a proof works exactly once")
	}
	proof = s.issue("u1")
	now = now.Add(reauthTTL)
	if s.consume("u1", proof) {
		t.Fatal("a proof expires")
	}
	stale := s.issue("u1")
	now = now.Add(reauthTTL)
	s.issue("u1")
	if _, kept := s.proofs[stale]; kept {
		t.Fatal("expired proofs must be dropped")
	}
}

// A member removed from the workspace they were working in is moved to
// another workspace of theirs instead of being signed out.
func TestRecoverSessionAfterLosingTheWorkspace(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	f.join(t, owner.orgID, member, "member")
	inClub := f.switchTo(t, member, owner.orgID)
	recover := func(token string) response { return f.call(t, http.MethodPost, "/auth/session/recover", token, nil) }

	// A session that is fine stays as it is.
	if res := recover(inClub); res.status != http.StatusOK || res.body["switched"] != false || res.str("token") != "" || res.str("active_org") != owner.orgID {
		t.Fatalf("recover on a good session: %d %s", res.status, res.raw)
	}
	wantError(t, recover(""), http.StatusUnauthorized, "not_signed_in")
	wantError(t, recover("garbage"), http.StatusUnauthorized, "not_signed_in")

	if res := f.call(t, http.MethodPost, "/tenants/members/remove", owner.token, map[string]string{"org_id": owner.orgID, "user_id": member.userID}); res.status != http.StatusOK {
		t.Fatalf("remove: %d %s", res.status, res.raw)
	}
	wantError(t, f.call(t, http.MethodGet, "/auth/me", inClub, nil), http.StatusUnauthorized, "not_signed_in")

	res := recover(inClub)
	if res.status != http.StatusOK || res.body["switched"] != true || res.str("active_org") != member.orgID {
		t.Fatalf("recover after the removal: %d %s", res.status, res.raw)
	}
	me := f.call(t, http.MethodGet, "/auth/me", res.str("token"), nil)
	if me.status != http.StatusOK || me.str("active_org") != member.orgID {
		t.Fatalf("the recovered session: %d %s", me.status, me.raw)
	}

	// No workspace left at all: a personal one is created.
	ctx := context.Background()
	if err := f.store.Orgs().Delete(ctx, member.orgID); err != nil {
		t.Fatal(err)
	}
	f.sessions.Invalidate()
	res = recover(res.str("token"))
	if res.status != http.StatusOK || res.body["switched"] != true || res.str("active_org") == "" || res.str("active_org") == member.orgID {
		t.Fatalf("recover without any workspace: %d %s", res.status, res.raw)
	}
	if m, err := f.store.Members().Get(ctx, res.str("active_org"), member.userID); err != nil || m.Role != "owner" {
		t.Fatalf("the account must own its new personal workspace: %v %+v", err, m)
	}

	// Revoked sessions and deleted accounts are not recovered.
	if out := f.call(t, http.MethodPost, "/auth/logout-all", res.str("token"), nil); out.status != http.StatusOK {
		t.Fatalf("logout-all: %d", out.status)
	}
	wantError(t, recover(res.str("token")), http.StatusUnauthorized, "not_signed_in")
	if res := deleteAccount(f, t, owner.token, map[string]string{"password": "password123"}); res.status != http.StatusOK {
		t.Fatalf("delete: %d %s", res.status, res.raw)
	}
	wantError(t, recover(owner.token), http.StatusUnauthorized, "not_signed_in")
}
