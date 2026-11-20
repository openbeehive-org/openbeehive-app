package auth

import (
	"context"
	"net/http"
	"testing"
)

// apiKey creates an API key in the session's workspace and returns it.
func (f *apiFixture) apiKey(t *testing.T, token string) string {
	t.Helper()
	res := f.call(t, http.MethodPost, "/auth/api-keys", token, map[string]string{"name": "scale"})
	if res.status != http.StatusOK || res.str("token") == "" {
		t.Fatalf("create api key: %d %s", res.status, res.raw)
	}
	return res.str("token")
}

// switchTo returns a session of the account for another of its workspaces.
func (f *apiFixture) switchTo(t *testing.T, a account, orgID string) string {
	t.Helper()
	res := f.call(t, http.MethodPost, "/auth/switch", a.token, map[string]string{"org_id": orgID})
	if res.status != http.StatusOK {
		t.Fatalf("switch: %d %s", res.status, res.raw)
	}
	return res.str("token")
}

func memberList(t *testing.T, res response) map[string]map[string]any {
	t.Helper()
	if res.status != http.StatusOK {
		t.Fatalf("list members: %d %s", res.status, res.raw)
	}
	out := map[string]map[string]any{}
	list, _ := res.body["members"].([]any)
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		id, _ := m["user_id"].(string)
		out[id] = m
	}
	return out
}

func TestMemberList(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	outsider := f.signup(t, "outsider@test.dev")
	f.join(t, owner.orgID, member, "member")

	// The owner sees everyone's email address and role.
	res := f.call(t, http.MethodGet, "/tenants/members", owner.token, nil)
	got := memberList(t, res)
	if len(got) != 2 || res.body["can_manage"] != true {
		t.Fatalf("owner's list: %s", res.raw)
	}
	if m := got[member.userID]; m["email"] != "member@test.dev" || m["role"] != "member" || m["me"] != false {
		t.Fatalf("the owner sees the member's email and role: %v", m)
	}
	if m := got[owner.userID]; m["role"] != "owner" || m["me"] != true {
		t.Fatalf("the owner's own entry: %v", m)
	}

	// A member sees who else is there, but nobody else's address or role.
	res = f.call(t, http.MethodGet, "/tenants/members?org_id="+owner.orgID, member.token, nil)
	got = memberList(t, res)
	if len(got) != 2 || res.body["can_manage"] != false {
		t.Fatalf("member's list: %s", res.raw)
	}
	if m := got[owner.userID]; m["email"] != nil || m["role"] != nil {
		t.Fatalf("a member must not see another member's email or role: %v", m)
	}
	if m := got[member.userID]; m["email"] != "member@test.dev" || m["me"] != true {
		t.Fatalf("a member sees their own entry in full: %v", m)
	}

	wantError(t, f.call(t, http.MethodGet, "/tenants/members?org_id="+owner.orgID, outsider.token, nil), http.StatusForbidden, "not_a_member")
	wantError(t, f.call(t, http.MethodGet, "/tenants/members", "", nil), http.StatusUnauthorized, "not_signed_in")
}

func TestRemoveMember(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	other := f.signup(t, "other@test.dev")
	f.join(t, owner.orgID, member, "member")
	f.join(t, owner.orgID, other, "member")
	inClub := f.switchTo(t, member, owner.orgID)
	clubKey, ownKey := f.apiKey(t, inClub), f.apiKey(t, member.token)
	remove := func(token, userID string) response {
		return f.call(t, http.MethodPost, "/tenants/members/remove", token, map[string]string{"org_id": owner.orgID, "user_id": userID})
	}

	wantError(t, remove("", member.userID), http.StatusUnauthorized, "not_signed_in")
	// Only an owner removes members.
	wantError(t, remove(f.switchTo(t, other, owner.orgID), member.userID), http.StatusForbidden, "owner_only")
	wantError(t, remove(owner.token, owner.userID), http.StatusBadRequest, "cannot_remove_self")
	wantError(t, remove(owner.token, "nobody"), http.StatusNotFound, "member_not_found")
	if me := f.call(t, http.MethodGet, "/auth/me", inClub, nil); me.status != http.StatusOK {
		t.Fatalf("the member is still in: %d", me.status)
	}

	if res := remove(owner.token, member.userID); res.status != http.StatusOK {
		t.Fatalf("remove: %d %s", res.status, res.raw)
	}
	// Access ends with the request that removed them: session and API key.
	ctx := context.Background()
	wantError(t, f.call(t, http.MethodGet, "/auth/me", inClub, nil), http.StatusUnauthorized, "not_signed_in")
	if _, err := f.keys.Verify(ctx, clubKey); err == nil {
		t.Fatal("the removed member's API key for the workspace still works")
	}
	var keys int
	if err := f.store.DB().Get(&keys, f.store.DB().Rebind(`SELECT COUNT(*) FROM api_key WHERE organization_id = ?`), owner.orgID); err != nil || keys != 0 {
		t.Fatalf("the removed member's keys for the workspace must be deleted: %d %v", keys, err)
	}
	// What they have elsewhere is untouched.
	if _, err := f.keys.Verify(ctx, ownKey); err != nil {
		t.Fatalf("the key for their own workspace must keep working: %v", err)
	}
	if me := f.call(t, http.MethodGet, "/auth/me", member.token, nil); me.status != http.StatusOK {
		t.Fatalf("their session for their own workspace: %d", me.status)
	}
	if got := memberList(t, f.call(t, http.MethodGet, "/tenants/members", owner.token, nil)); len(got) != 2 || got[member.userID] != nil {
		t.Fatalf("member list after the removal: %v", got)
	}
}

func TestLeaveWorkspace(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	f.join(t, owner.orgID, member, "member")
	inClub := f.switchTo(t, member, owner.orgID)
	clubKey := f.apiKey(t, inClub)
	leave := func(token, orgID string) response {
		return f.call(t, http.MethodPost, "/tenants/leave", token, map[string]string{"org_id": orgID})
	}

	wantError(t, leave(inClub, ""), http.StatusBadRequest, "org_id_required")
	// The only owner stays: with other members, and alone.
	wantError(t, leave(owner.token, owner.orgID), http.StatusConflict, "last_owner")
	wantError(t, leave(member.token, member.orgID), http.StatusConflict, "last_owner")
	wantError(t, leave(owner.token, member.orgID), http.StatusNotFound, "member_not_found")

	// Leaving the active workspace moves the session to another one.
	res := leave(inClub, owner.orgID)
	if res.status != http.StatusOK || res.str("active_org") != member.orgID || res.str("token") == "" {
		t.Fatalf("leave: %d %s", res.status, res.raw)
	}
	if me := f.call(t, http.MethodGet, "/auth/me", res.str("token"), nil); me.status != http.StatusOK || me.str("active_org") != member.orgID {
		t.Fatalf("the session after leaving: %d %s", me.status, me.raw)
	}
	wantError(t, f.call(t, http.MethodGet, "/auth/me", inClub, nil), http.StatusUnauthorized, "not_signed_in")
	if _, err := f.keys.Verify(context.Background(), clubKey); err == nil {
		t.Fatal("the API key for the workspace they left still works")
	}

	// With a second owner, the first one may go.
	f.join(t, owner.orgID, member, "owner")
	if res := leave(owner.token, owner.orgID); res.status != http.StatusOK || res.str("active_org") == owner.orgID {
		t.Fatalf("an owner leaves once there is another: %d %s", res.status, res.raw)
	}
}

func TestChangeMemberRole(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	f.join(t, owner.orgID, member, "member")
	inClub := f.switchTo(t, member, owner.orgID)
	setRole := func(token, userID, role string) response {
		return f.call(t, http.MethodPost, "/tenants/members/role", token, map[string]string{"org_id": owner.orgID, "user_id": userID, "role": role})
	}
	role := func(userID string) string {
		m, err := f.store.Members().Get(context.Background(), owner.orgID, userID)
		if err != nil {
			t.Fatal(err)
		}
		return m.Role
	}

	wantError(t, setRole(inClub, member.userID, "owner"), http.StatusForbidden, "owner_only")
	wantError(t, setRole(owner.token, member.userID, "king"), http.StatusBadRequest, "invalid_role")
	wantError(t, setRole(owner.token, "nobody", "owner"), http.StatusNotFound, "member_not_found")
	// The last owner cannot be demoted, not even by themselves.
	wantError(t, setRole(owner.token, owner.userID, "member"), http.StatusConflict, "last_owner")
	if role(owner.userID) != "owner" || role(member.userID) != "member" {
		t.Fatal("refused changes must change nothing")
	}

	if res := setRole(owner.token, member.userID, "owner"); res.status != http.StatusOK || role(member.userID) != "owner" {
		t.Fatalf("promote: %d %s", res.status, res.raw)
	}
	// Two owners: either can be demoted, by either.
	if res := setRole(inClub, owner.userID, "member"); res.status != http.StatusOK || role(owner.userID) != "member" {
		t.Fatalf("demote: %d %s", res.status, res.raw)
	}
	wantError(t, setRole(inClub, member.userID, "member"), http.StatusConflict, "last_owner")
}

func TestDemoCannotManageMembers(t *testing.T) {
	cfg := loginConfig()
	cfg.Demo.Enabled, cfg.Demo.Email = true, "demo@test.dev"
	f := newAPIFixture(t, cfg)
	seedMember(t, f.store, "demo", "demo@test.dev", "demo-org", "owner")
	seedMember(t, f.store, "guest", "guest@test.dev", "demo-org", "member")
	tok, _ := f.sessions.Issue(Identity{UserID: "demo", OrgID: "demo-org", Email: "demo@test.dev", Role: "demo"})
	for path, body := range map[string]map[string]string{
		"/tenants/members/remove": {"org_id": "demo-org", "user_id": "guest"},
		"/tenants/members/role":   {"org_id": "demo-org", "user_id": "guest", "role": "owner"},
		"/tenants/leave":          {"org_id": "demo-org"},
	} {
		wantError(t, f.call(t, http.MethodPost, path, tok, body), http.StatusForbidden, "demo_read_only")
	}
}
