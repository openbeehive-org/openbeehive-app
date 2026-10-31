package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// newAPIKeyFixture: a store with one user in one tenant, the key endpoints
// mounted, and a session for that user.
func newAPIKeyFixture(t *testing.T) (*httptest.Server, *APIKeys, string, Identity) {
	t.Helper()
	srvBase, store, sessions := newAuthFixture(t, &config.Config{})
	srvBase.Close()
	ctx := context.Background()
	u := &storage.User{ID: "u1", Email: "u1@example.org", Name: "U", Role: "user", CreatedAt: time.Now()}
	if err := store.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := store.Orgs().Create(ctx, &storage.Organization{ID: "org-1", Name: "Org", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Members().Add(ctx, &storage.Membership{OrgID: "org-1", UserID: "u1", Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	keys := NewAPIKeys(store.DB(), sessions, store.Users(), store.Members())
	mux := http.NewServeMux()
	keys.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	id := Identity{UserID: "u1", OrgID: "org-1", Email: u.Email, Role: "user"}
	tok, err := sessions.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	return srv, keys, tok, id
}

func doJSON(t *testing.T, method, url, session string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPIKeyLifecycle(t *testing.T) {
	srv, keys, session, id := newAPIKeyFixture(t)
	ctx := context.Background()

	// Create: plaintext returned once, prefixed and listed by prefix only.
	code, res := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]string{"name": "hive scale"})
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, res)
	}
	token, _ := res["token"].(string)
	if !strings.HasPrefix(token, apiKeyPrefix) || len(token) < 40 {
		t.Fatalf("bad token %q", token)
	}
	code, list := doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys", session, nil)
	ks, _ := list["keys"].([]any)
	if code != http.StatusOK || len(ks) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}
	k0 := ks[0].(map[string]any)
	if k0["name"] != "hive scale" || k0["prefix"] != token[:12] || k0["token"] != nil {
		t.Fatalf("list row must show name and prefix only: %v", k0)
	}

	// Verify: the key resolves to its owner in the bound tenant.
	got, err := keys.Verify(ctx, token)
	if err != nil || got.UserID != id.UserID || got.OrgID != id.OrgID || got.Role != "user" {
		t.Fatalf("verify: %v %+v", err, got)
	}
	// Session tokens still pass through the same verifier.
	if got, err := keys.Verify(ctx, session); err != nil || got.UserID != id.UserID {
		t.Fatalf("session passthrough: %v %+v", err, got)
	}
	// Garbage and a foreign-looking key fail.
	for _, bad := range []string{apiKeyPrefix + "nope", "not-a-token", ""} {
		if _, err := keys.Verify(ctx, bad); err == nil {
			t.Fatalf("%q must not verify", bad)
		}
	}
	// last_used_at is stamped on use.
	_, list = doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys", session, nil)
	if lu := list["keys"].([]any)[0].(map[string]any)["last_used_at"]; lu == nil {
		t.Fatalf("last_used_at not stamped: %v", list)
	}

	// Management needs a session: a key cannot list or mint keys.
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys", token, nil); code != http.StatusUnauthorized {
		t.Fatalf("key must not manage keys, got %d", code)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous create must fail, got %d", code)
	}

	// Revoke: gone from the list, verification fails immediately.
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys/delete", session, map[string]string{"id": k0["id"].(string)})
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, err := keys.Verify(ctx, token); err == nil {
		t.Fatal("revoked key must not verify")
	}
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys/delete", session, map[string]string{"id": k0["id"].(string)})
	if code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
}

func TestAPIKeyRefusedForDemoAndDeadMembership(t *testing.T) {
	srv, keys, session, id := newAPIKeyFixture(t)
	ctx := context.Background()

	demoTok, _ := keys.sessions.Issue(Identity{UserID: "u1", OrgID: "org-1", Role: "demo"})
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", demoTok, map[string]string{"name": "x"}); code != http.StatusForbidden {
		t.Fatalf("demo must not mint keys, got %d", code)
	}

	// A key bound to a tenant the user no longer belongs to is dead.
	_, res := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]string{"name": "x"})
	token := res["token"].(string)
	if _, err := keys.Verify(ctx, token); err != nil {
		t.Fatalf("fresh key: %v", err)
	}
	if _, err := keys.db.ExecContext(ctx, keys.db.Rebind(`DELETE FROM member WHERE organization_id = ? AND benutzer_id = ?`), id.OrgID, id.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Verify(ctx, token); err == nil {
		t.Fatal("key must die with the membership")
	}
}

func TestAPIKeyScopeAndExpiry(t *testing.T) {
	srv, keys, session, _ := newAPIKeyFixture(t)
	ctx := context.Background()

	// Read-scoped key: identity is read-only and the guard blocks writes.
	code, res := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]any{"name": "dashboard", "scope": "read"})
	if code != http.StatusOK || res["scope"] != "read" {
		t.Fatalf("create read key: %d %v", code, res)
	}
	id, err := keys.Verify(ctx, res["token"].(string))
	if err != nil || !id.ReadOnly {
		t.Fatalf("read key identity: %v %+v", err, id)
	}
	rctx := WithIdentity(ctx, id)
	if err := callGuard(rctx, "/openbeehive.v1.InspectionService/ListInspections"); err != nil {
		t.Fatalf("read key must read: %v", err)
	}
	err = callGuard(rctx, "/openbeehive.v1.InspectionService/CreateInspection")
	if !errors.Is(err, ErrKeyReadOnly) {
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied {
			t.Fatalf("read key must not write: %v", err)
		}
	}
	// Write keys are not read-only.
	_, res = doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]any{"name": "w"})
	if id, _ := keys.Verify(ctx, res["token"].(string)); id.ReadOnly || res["scope"] != "write" {
		t.Fatalf("default scope must be write: %+v %v", id, res)
	}
	// Bad scope and bad lifetime are rejected.
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]any{"scope": "admin"}); code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d", code)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]any{"expires_in_days": -1}); code != http.StatusBadRequest {
		t.Fatalf("bad lifetime: %d", code)
	}

	// Expiry: a 30-day key verifies now and reports expires_at; an expired
	// row is refused.
	_, res = doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", session, map[string]any{"name": "temp", "expires_in_days": 30})
	if res["expires_at"] == nil {
		t.Fatalf("expires_at missing: %v", res)
	}
	tok := res["token"].(string)
	if _, err := keys.Verify(ctx, tok); err != nil {
		t.Fatalf("fresh 30-day key: %v", err)
	}
	if _, err := keys.db.ExecContext(ctx, keys.db.Rebind(`UPDATE api_key SET expires_at = ? WHERE id = ?`),
		time.Now().Add(-time.Minute), res["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Verify(ctx, tok); !errors.Is(err, errExpiredAPIKey) {
		t.Fatalf("expired key must fail with errExpiredAPIKey, got %v", err)
	}
	_, list := doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys", session, nil)
	if n := len(list["keys"].([]any)); n != 3 {
		t.Fatalf("list must show all three keys incl. the expired one: %d", n)
	}
}

func TestTenantOwnerManagesMemberKeys(t *testing.T) {
	srv, keys, ownerSession, _ := newAPIKeyFixture(t)
	ctx := context.Background()
	// A second, plain member of org-1 with a key of their own.
	if err := keys.users.Create(ctx, &storage.User{ID: "u2", Email: "u2@example.org", Role: "user", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := keys.members.Add(ctx, &storage.Membership{OrgID: "org-1", UserID: "u2", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	memberSession, _ := keys.sessions.Issue(Identity{UserID: "u2", OrgID: "org-1", Role: "user"})
	_, res := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", memberSession, map[string]string{"name": "member scale"})
	memberKeyID, memberToken := res["id"].(string), res["token"].(string)
	_, res = doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys", ownerSession, map[string]string{"name": "owner key"})
	ownerKeyID := res["id"].(string)

	// Plain listing stays personal for both.
	_, list := doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys", memberSession, nil)
	if n := len(list["keys"].([]any)); n != 1 {
		t.Fatalf("member personal list: %d", n)
	}
	// Tenant-wide listing: owner sees both with emails; member is refused.
	code, list := doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys?tenant=1", ownerSession, nil)
	ks, _ := list["keys"].([]any)
	if code != http.StatusOK || len(ks) != 2 {
		t.Fatalf("owner tenant list: %d %v", code, list)
	}
	seen := map[string]bool{}
	for _, raw := range ks {
		k := raw.(map[string]any)
		seen[k["user_email"].(string)] = k["mine"].(bool)
	}
	if seen["u2@example.org"] || !seen["u1@example.org"] {
		t.Fatalf("mine flags wrong: %v", seen)
	}
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/auth/api-keys?tenant=1", memberSession, nil); code != http.StatusForbidden {
		t.Fatalf("member must not list tenant keys, got %d", code)
	}

	// Member cannot revoke the owner's key; owner can revoke the member's.
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys/delete", memberSession, map[string]string{"id": ownerKeyID}); code != http.StatusNotFound {
		t.Fatalf("member revoking owner key: %d", code)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys/delete", ownerSession, map[string]string{"id": memberKeyID}); code != http.StatusNoContent {
		t.Fatalf("owner revoking member key: %d", code)
	}
	if _, err := keys.Verify(ctx, memberToken); err == nil {
		t.Fatal("revoked member key must not verify")
	}
	// Owner of another tenant cannot touch org-1 keys through the override.
	if err := keys.members.Add(ctx, &storage.Membership{OrgID: "org-2", UserID: "u2", Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	foreignOwner, _ := keys.sessions.Issue(Identity{UserID: "u2", OrgID: "org-2", Role: "user"})
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/auth/api-keys/delete", foreignOwner, map[string]string{"id": ownerKeyID}); code != http.StatusNotFound {
		t.Fatalf("foreign owner revoking org-1 key: %d", code)
	}
}
