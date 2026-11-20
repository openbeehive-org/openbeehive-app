package auth

import (
	"context"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

func seedLocalApiary(t *testing.T, f *apiFixture, id string) {
	t.Helper()
	now := time.Now().UTC()
	db := f.store.DB()
	if _, err := db.Exec(db.Rebind(
		`INSERT INTO apiary (id, organization_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`),
		id, LocalUser.OrgID, "Home", now, now); err != nil {
		t.Fatal(err)
	}
	var seq int64
	_ = db.Get(&seq, `SELECT COALESCE(MAX(seq), 0) + 1 FROM change_log`)
	if _, err := db.Exec(db.Rebind(
		`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"cl-"+id, seq, id, "apiary", id, 1, `{"organization_id":"local","name":"Home"}`, "hlc", LocalUser.UserID, LocalUser.OrgID); err != nil {
		t.Fatal(err)
	}
}

// Turning login on must not hide what was recorded without it: the records
// under the fixed single-user workspace move to the instance admin.
func TestAdoptLocalDataMovesSingleUserRecordsToTheAdmin(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
	adopt := func() {
		t.Helper()
		if err := AdoptLocalData(ctx, f.store, f.store.Users(), f.store.Members(), prov, f.cfg); err != nil {
			t.Fatalf("adopt: %v", err)
		}
	}
	admin, err := f.store.Users().GetByEmail(ctx, f.cfg.Auth.AdminEmail)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing to adopt: no workspace is created for the admin.
	adopt()
	if ms, _ := f.store.Members().ListByUser(ctx, admin.ID); len(ms) != 0 {
		t.Fatalf("no data, yet a workspace was created: %v", ms)
	}

	seedLocalApiary(t, f, "apiary-1")
	adopt()
	ms, _ := f.store.Members().ListByUser(ctx, admin.ID)
	if len(ms) != 1 || ms[0].Role != "owner" {
		t.Fatalf("the admin must own a workspace now: %v", ms)
	}
	target := ms[0].OrgID
	var org, logOrg, author string
	db := f.store.DB()
	_ = db.Get(&org, `SELECT organization_id FROM apiary WHERE id = 'apiary-1'`)
	_ = db.Get(&logOrg, `SELECT org_id FROM change_log WHERE id = 'cl-apiary-1'`)
	_ = db.Get(&author, `SELECT author_id FROM change_log WHERE id = 'cl-apiary-1'`)
	if org != target || logOrg != target || author != admin.ID {
		t.Fatalf("moved to org=%q log=%q author=%q, want workspace %q and author %q", org, logOrg, author, target, admin.ID)
	}

	// The admin signs in and finds the workspace active.
	res := f.call(t, "POST", "/auth/signin", "", map[string]string{"email": f.cfg.Auth.AdminEmail, "password": f.cfg.Auth.AdminPassword})
	if res.status != 200 || res.str("active_org") != target {
		t.Fatalf("admin sign-in: %d %s", res.status, res.raw)
	}

	// Idempotent, and data that appears under "local" later lands in the
	// same workspace.
	adopt()
	seedLocalApiary(t, f, "apiary-2")
	adopt()
	if ms, _ := f.store.Members().ListByUser(ctx, admin.ID); len(ms) != 1 {
		t.Fatalf("a second workspace was created: %v", ms)
	}
	var n int
	_ = db.Get(&n, db.Rebind(`SELECT COUNT(*) FROM apiary WHERE organization_id = ?`), target)
	if n != 2 {
		t.Fatalf("%d apiaries in the admin's workspace, want 2", n)
	}
}

// Without password auth there is no instance admin: the records stay where
// they are until one is configured.
func TestAdoptLocalDataWaitsForAnAdmin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.WebAuthn.Enabled = true
	f := newAPIFixture(t, cfg)
	seedLocalApiary(t, f, "apiary-1")
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
	if err := AdoptLocalData(context.Background(), f.store, f.store.Users(), f.store.Members(), prov, cfg); err != nil {
		t.Fatalf("adopt without admin: %v", err)
	}
	if has, _ := f.store.OrgHasData(context.Background(), LocalUser.OrgID); !has {
		t.Fatal("the records were moved although nobody can own them")
	}
}

// Units or a language chosen in single-user mode are enough to adopt: they
// become the admin's settings, even without any record.
func TestAdoptLocalDataCarriesSettingsToTheAdmin(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	db := f.store.DB()
	if _, err := db.Exec(db.Rebind(`INSERT INTO user_settings (id, weight_unit, field_hlc) VALUES (?, 'lb', ?)`),
		LocalUser.UserID, `{"weight_unit":"001767225600000:00000:dev"}`); err != nil {
		t.Fatal(err)
	}
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
	if err := AdoptLocalData(ctx, f.store, f.store.Users(), f.store.Members(), prov, f.cfg); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	admin, err := f.store.Users().GetByEmail(ctx, f.cfg.Auth.AdminEmail)
	if err != nil {
		t.Fatal(err)
	}
	var weight string
	if err := db.Get(&weight, db.Rebind(`SELECT weight_unit FROM user_settings WHERE id = ?`), admin.ID); err != nil || weight != "lb" {
		t.Fatalf("admin's weight unit = %q, %v", weight, err)
	}
	if has, _ := f.store.UserHasSettings(ctx, LocalUser.UserID); has {
		t.Fatal("settings left under the single-user account")
	}
}
