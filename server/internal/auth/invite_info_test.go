package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The sign-in page names the workspace and the (masked) address an invite is
// for, before anyone signs in.
func TestInviteInfoNamesWorkspaceAndMaskedEmail(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	owner := f.signup(t, "owner@test.dev")
	res := f.call(t, http.MethodPost, "/tenants/invite", owner.token, map[string]string{"email": "friend@bees.example.org", "org_id": owner.orgID})
	if res.status != http.StatusOK {
		t.Fatalf("invite: %d %s", res.status, res.raw)
	}
	token := res.str("token")
	org, err := f.store.Orgs().Get(context.Background(), owner.orgID)
	if err != nil {
		t.Fatal(err)
	}

	info := f.call(t, http.MethodGet, "/auth/invite?token="+token, "", nil)
	if info.status != http.StatusOK || info.str("workspace") != org.Name || info.str("email") != "f…@b….org" {
		t.Fatalf("invite info: %d %s", info.status, info.raw)
	}

	wantError(t, f.call(t, http.MethodGet, "/auth/invite?token=nope", "", nil), http.StatusBadRequest, "invite_invalid")
	inv, _ := f.store.Invites().GetByToken(context.Background(), token)
	past := time.Now().Add(-time.Minute)
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(`UPDATE invite SET expires_at = ? WHERE id = ?`), past, inv.ID); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.call(t, http.MethodGet, "/auth/invite?token="+token, "", nil), http.StatusBadRequest, "invite_expired")
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"beekeeper@example.org": "b…@e….org",
		"a@localhost":           "a…@l…",
		"ölf@bienen.de":         "ö…@b….de",
		"broken":                "…",
		"x@":                    "…",
	} {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
