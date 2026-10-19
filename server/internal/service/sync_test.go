package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// newSyncFixture opens a fresh store with two tenants, one apiary each, and
// one hive owned by tenant A. Defaults to SQLite in a temp dir; set
// BEEHIVE_TEST_DATABASE_DRIVER + BEEHIVE_TEST_DATABASE_DSN to run the whole
// suite against a live Postgres or MySQL instead (the schema is wiped!).
func newSyncFixture(t *testing.T) *SyncService {
	t.Helper()
	return NewSyncService(newFixtureStore(t).DB(), wsync.NewHLC("test"))
}

// newFixtureStore opens and seeds the store behind newSyncFixture.
func newFixtureStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	cfg := &config.Config{Profile: "selfhost"}
	cfg.DB.Driver = config.DriverSQLite
	cfg.DB.DSN = "file:" + filepath.Join(t.TempDir(), "sync.db")
	if d := os.Getenv("BEEHIVE_TEST_DATABASE_DRIVER"); d != "" {
		cfg.DB.Driver = config.DBDriver(d)
		cfg.DB.DSN = os.Getenv("BEEHIVE_TEST_DATABASE_DSN")
	}
	store, err := sqlstore.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	resetSchema(t, store, cfg.DB.Driver)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db := store.DB()
	seed := []string{
		`INSERT INTO apiary (id, organization_id, name, created_at, updated_at)
		 VALUES ('apiary-a', 'org-a', 'A', '2026-01-01', '2026-01-01')`,
		`INSERT INTO apiary (id, organization_id, name, created_at, updated_at)
		 VALUES ('apiary-b', 'org-b', 'B', '2026-01-01', '2026-01-01')`,
		`INSERT INTO hive (id, organization_id, apiary_id, name, created_at, updated_at)
		 VALUES ('hive-a', 'org-a', 'apiary-a', 'H1', '2026-01-01', '2026-01-01')`,
	}
	for _, q := range seed {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return store
}

// resetSchema empties a live test database so every test starts blank.
// (SQLite fixtures live in a fresh temp dir and need no reset.)
func resetSchema(t *testing.T, store *sqlstore.Store, driver config.DBDriver) {
	t.Helper()
	db := store.DB()
	switch driver {
	case config.DriverPostgres:
		db.MustExec(`DROP SCHEMA public CASCADE`)
		db.MustExec(`CREATE SCHEMA public`)
	case config.DriverMySQL:
		var tables []string
		if err := db.Select(&tables, `SHOW TABLES`); err != nil {
			t.Fatalf("show tables: %v", err)
		}
		for _, table := range tables {
			db.MustExec("DROP TABLE IF EXISTS `" + table + "`")
		}
	}
}

func identityCtx(user, org string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{UserID: user, OrgID: org})
}

func push(svc *SyncService, ctx context.Context, changes ...*wv1.Change) error {
	_, err := svc.Push(ctx, connect.NewRequest(&wv1.PushRequest{Changes: changes}))
	return err
}

// One clock for all test changes: a fresh HLC per change would restart at
// counter 0 and hand two changes in the same millisecond identical stamps —
// the second write then loses the LWW comparison and is dropped as stale.
var testHLC = wsync.NewHLC("dev")

func change(entity, id, scope, payload string) *wv1.Change {
	return &wv1.Change{
		Entity: entity, EntityId: id, ScopeId: scope,
		Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: payload,
		Hlc: testHLC.Now(), AuthorId: "dev",
	}
}

func wantPermissionDenied(t *testing.T, err error) {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodePermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

func TestPushRejectsForeignScope(t *testing.T) {
	svc := newSyncFixture(t)
	// User of tenant B labels a change with tenant A's apiary scope.
	err := push(svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-x", "apiary-a", `{"name":"intruder"}`))
	wantPermissionDenied(t, err)
}

func TestPushRejectsForeignRow(t *testing.T) {
	svc := newSyncFixture(t)
	// Scope label is user B's own apiary, but the row id belongs to tenant A.
	err := push(svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-a", "apiary-b", `{"name":"hijacked"}`))
	wantPermissionDenied(t, err)

	var name string
	if err := svc.db.Get(&name, `SELECT name FROM hive WHERE id = 'hive-a'`); err != nil || name != "H1" {
		t.Fatalf("victim row was modified: name=%q err=%v", name, err)
	}
}

func TestPushRejectsForeignOrgPayload(t *testing.T) {
	svc := newSyncFixture(t)
	// Insert claiming to belong to tenant A while authenticated as tenant B.
	err := push(svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-y", "apiary-b", `{"organization_id":"org-a","name":"smuggled"}`))
	wantPermissionDenied(t, err)
}

func TestPushStampsCallerTenant(t *testing.T) {
	svc := newSyncFixture(t)
	// A legitimate insert (no organization_id in the payload) lands in the
	// caller's tenant.
	if err := push(svc, identityCtx("user-a", "org-a"),
		change("hive", "hive-new", "apiary-a", `{"apiary_id":"apiary-a","name":"New","created_at":"2026-07-11","updated_at":"2026-07-11"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}
	var org string
	if err := svc.db.Get(&org, `SELECT organization_id FROM hive WHERE id = 'hive-new'`); err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if org != "org-a" {
		t.Fatalf("organization_id = %q, want org-a", org)
	}
}

func TestPushAllowsNewApiaryScope(t *testing.T) {
	svc := newSyncFixture(t)
	// A brand-new apiary opens its own scope (scope id = apiary id).
	if err := push(svc, identityCtx("user-a", "org-a"),
		change("apiary", "apiary-new", "apiary-new", `{"name":"Orchard","created_at":"2026-07-11","updated_at":"2026-07-11"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}
	var org string
	if err := svc.db.Get(&org, `SELECT organization_id FROM apiary WHERE id = 'apiary-new'`); err != nil || org != "org-a" {
		t.Fatalf("new apiary org = %q err=%v, want org-a", org, err)
	}
}

func TestPullFiltersByScope(t *testing.T) {
	svc := newSyncFixture(t)
	if err := push(svc, identityCtx("user-a", "org-a"),
		change("hive", "hive-a", "apiary-a", `{"name":"A renamed"}`)); err != nil {
		t.Fatalf("push a: %v", err)
	}
	if err := push(svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-b", "apiary-b", `{"apiary_id":"apiary-b","name":"B hive","created_at":"2026-07-11","updated_at":"2026-07-11"}`)); err != nil {
		t.Fatalf("push b: %v", err)
	}
	resp, err := svc.Pull(identityCtx("user-b", "org-b"),
		connect.NewRequest(&wv1.PullRequest{}))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	for _, ch := range resp.Msg.Changes {
		if ch.ScopeId != "apiary-b" && ch.ScopeId != "user:user-b" {
			t.Fatalf("pull leaked foreign scope %q", ch.ScopeId)
		}
	}
	if len(resp.Msg.Changes) != 1 {
		t.Fatalf("want exactly the own change, got %d", len(resp.Msg.Changes))
	}
}

// TestPushAppliesSQLiteClientPayload pushes payloads shaped exactly like the
// offline clients produce them (SQLite stores booleans as 0/1, integers arrive
// as JSON numbers, unset timestamps as ""): the server must coerce them into
// whatever the backing dialect expects (Postgres rejects 0/1 for BOOLEAN,
// MySQL cannot parse RFC3339 into DATETIME).
func TestPushAppliesSQLiteClientPayload(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := push(svc, ctx,
		change("inspection", "insp-1", "apiary-a",
			`{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","queen_seen":1,"eggs_seen":0,`+
				`"covered_larva":1,"frames":9,"brood_frames":4,"honey_kg":2.5,"temp_hive":34.8,`+
				`"note":"","created_at":"2026-07-11T10:05:00Z","deleted":0,"photo_keys":{"add":["k1"]}}`),
		change("treatment", "treat-1", "apiary-a",
			`{"hive_id":"hive-a","date":"2026-08-18T09:00:00Z","product":"Formic acid 60%",`+
				`"withdrawal_until":"","deleted":0}`),
		change("task", "task-1", "apiary-a",
			`{"title":"feed","done":0,"priority":2,"due_at":"2026-08-01","created_at":"2026-07-11T10:00:00Z","deleted":0}`),
	); err != nil {
		t.Fatalf("push client payload: %v", err)
	}

	var queenSeen, eggsSeen bool
	var frames int
	if err := svc.db.QueryRow(
		`SELECT queen_seen, eggs_seen, frames FROM inspection WHERE id = 'insp-1'`).
		Scan(&queenSeen, &eggsSeen, &frames); err != nil {
		t.Fatalf("inspection row: %v", err)
	}
	if !queenSeen || eggsSeen || frames != 9 {
		t.Fatalf("coercion: queen_seen=%v eggs_seen=%v frames=%d, want true/false/9", queenSeen, eggsSeen, frames)
	}
	var withdrawal any
	if err := svc.db.QueryRow(
		`SELECT withdrawal_until FROM treatment WHERE id = 'treat-1'`).Scan(&withdrawal); err != nil {
		t.Fatalf("treatment row: %v", err)
	}
	if s, isStr := withdrawal.(string); withdrawal != nil && !(isStr && s == "") {
		t.Fatalf(`withdrawal_until = %v (%T), want NULL or ""`, withdrawal, withdrawal)
	}

	// A delete op flips the tombstone (bool column) on every dialect.
	del := change("task", "task-1", "apiary-a", ``)
	del.Op = wv1.ChangeOp_CHANGE_OP_DELETE
	if err := push(svc, ctx, del); err != nil {
		t.Fatalf("push delete: %v", err)
	}
	var deleted bool
	if err := svc.db.QueryRow(`SELECT deleted FROM task WHERE id = 'task-1'`).Scan(&deleted); err != nil {
		t.Fatalf("task row: %v", err)
	}
	if !deleted {
		t.Fatalf("task not tombstoned after delete op")
	}
}
