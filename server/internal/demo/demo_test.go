package demo

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/service"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql/sqltest"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// newSeeder opens a migrated store on the backend under test (see sqltest)
// and returns a seeder on it.
func newSeeder(t *testing.T) (*Seeder, *sqlstore.Store) {
	t.Helper()
	cfg := &config.Config{Profile: "selfhost"}
	cfg.DB.Driver, cfg.DB.DSN = sqltest.New(t)
	cfg.Demo.Email = "demo@example.org"
	cfg.Demo.Password = "demo"
	store, err := sqlstore.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(store.DB(), wsync.NewHLC("demo"), cfg), store
}

func demoCtx() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{UserID: UserID, OrgID: OrgID})
}

// The demo workspace must look complete from every side: the CRUD API, the
// statistics and the change feed the app pulls.
func TestSeedFillsAPIStatsAndChangeFeed(t *testing.T) {
	s, store := newSeeder(t)
	ctx := demoCtx()
	if err := s.ensureAccount(ctx); err != nil {
		t.Fatalf("account: %v", err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := service.NewWriter(store.DB(), wsync.NewHLC("test"))

	apiaries, err := service.NewApiaryService(store.Apiaries(), w).ListApiaries(ctx, &wv1.ListApiariesRequest{})
	if err != nil || len(apiaries.Apiaries) != 4 {
		t.Fatalf("apiaries: %v, %d rows", err, len(apiaries.GetApiaries()))
	}
	var hiveCount int32
	for _, a := range apiaries.Apiaries {
		hiveCount += a.HiveCount
	}
	hives, err := service.NewHiveService(store.Hives(), w).ListHives(ctx, &wv1.ListHivesRequest{})
	if err != nil || hives.Page.Total != 15 || hiveCount != 15 {
		t.Fatalf("hives: %v, total %d, per apiary %d", err, hives.GetPage().GetTotal(), hiveCount)
	}

	stats := service.NewStatsService(w)
	d, err := stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Apiaries != 4 || d.Hives != 15 || d.ActiveQueens != 15 || len(d.RecentInspections) != 10 || len(d.HoneyHistory) == 0 {
		t.Fatalf("dashboard: %d apiaries, %d hives, %d queens, %d recent, %d honey months",
			d.Apiaries, d.Hives, d.ActiveQueens, len(d.RecentInspections), len(d.HoneyHistory))
	}
	if len(d.DueInspections) != 0 {
		t.Fatalf("every demo hive was visited recently, got %d due", len(d.DueInspections))
	}
	hs, err := stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
	if err != nil || len(hs.PerApiary) != 4 || len(hs.PerQueen) != 15 {
		t.Fatalf("honey stats: %v, %d apiaries, %d queens", err, len(hs.GetPerApiary()), len(hs.GetPerQueen()))
	}

	// Records carry their frozen context and a timeline event each.
	harvests, err := service.NewHarvestService(w).ListHarvests(ctx, &wv1.ListHarvestsRequest{HiveId: "demo-hive-1"})
	if err != nil || len(harvests.Harvests) != 1 {
		t.Fatalf("harvests: %v %+v", err, harvests)
	}
	if h := harvests.Harvests[0]; h.ApiaryId != "demo-apiary-1" || h.QueenId != "demo-queen-1" {
		t.Fatalf("harvest context: %+v", h)
	}
	events, err := service.NewEventService(w).ListEvents(ctx, &wv1.ListEventsRequest{HiveId: "demo-hive-1"})
	// created + queen introduced + 7 inspections + harvest + 2 treatments
	if err != nil || len(events.Events) != 12 {
		t.Fatalf("events of one hive: %v, %d", err, len(events.GetEvents()))
	}

	// The app pulls the same data; timestamps use the form devices write.
	pull, err := service.NewSyncService(store.DB(), wsync.NewHLC("test")).Pull(ctx, &wv1.PullRequest{Limit: 500})
	if err != nil || len(pull.Changes) == 0 {
		t.Fatalf("pull: %v, %d changes", err, len(pull.GetChanges()))
	}
	jsDate := regexp.MustCompile(`"date":"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z"`)
	for _, c := range pull.Changes {
		if c.AuthorId != UserID {
			t.Fatalf("change not written as the demo user: %+v", c)
		}
		if c.Entity == "inspection" && !jsDate.MatchString(c.PayloadJson) {
			t.Fatalf("timestamp format: %s", c.PayloadJson)
		}
	}
}

// Seeding again (the hourly reset) must rewrite the same rows, not add copies.
func TestSeedIsRepeatable(t *testing.T) {
	s, store := newSeeder(t)
	ctx := demoCtx()
	counts := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, table := range []string{"apiary", "hive", "queen", "placement", "inspection", "harvest", "treatment", "event", "change_log"} {
			var n int
			if err := store.DB().Get(&n, "SELECT COUNT(*) FROM "+table); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			out[table] = n
		}
		return out
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	first := counts()
	// 15 x (created + queen + 7 inspections + harvest + 2 treatments)
	if first["hive"] != 15 || first["placement"] != 15 || first["inspection"] != 105 || first["event"] != 15*12 {
		t.Fatalf("first seed: %v", first)
	}
	// A visitor's leftovers are removed by the reset.
	store.DB().MustExec(`INSERT INTO hive (id, organization_id, apiary_id, name, created_at, updated_at)
		VALUES ('stray', 'demo-tenant', 'demo-apiary-1', 'Stray', '2026-01-01', '2026-01-01')`)
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	second := counts()
	for table, n := range first {
		if second[table] != n {
			t.Fatalf("%s: %d rows after the first seed, %d after the second", table, n, second[table])
		}
	}
	var events []string
	if err := store.DB().Select(&events, `SELECT id FROM event WHERE hive_id = 'demo-hive-1' AND type = 7`); err != nil || len(events) != 1 || events[0] != "demo-harv-1:event" {
		t.Fatalf("event ids must be stable: %v %v", events, err)
	}
}

// TestInstallAndReseed runs what boot and the hourly reset run: installing
// twice and seeding again leaves one demo account, its membership and the
// four apiaries.
func TestInstallAndReseed(t *testing.T) {
	s, store := newSeeder(t)
	ctx, cancel := context.WithCancel(demoCtx())
	t.Cleanup(cancel) // stops the reset loop
	for run, seed := range []func(context.Context) error{s.Install, s.Install, s.Seed} {
		if err := seed(ctx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		var apiaries int
		db := store.DB()
		if err := db.Get(&apiaries, db.Rebind(`SELECT COUNT(*) FROM apiary WHERE organization_id = ?`), OrgID); err != nil {
			t.Fatalf("run %d: count apiaries: %v", run, err)
		}
		if apiaries != 4 {
			t.Fatalf("run %d: %d apiaries, want 4", run, apiaries)
		}
	}
	u, err := store.Users().GetByEmail(ctx, "demo@example.org")
	if err != nil || u.ID != UserID || !u.EmailVerified {
		t.Fatalf("demo user: %+v, %v", u, err)
	}
	if m, err := store.Members().Get(ctx, OrgID, UserID); err != nil || m.Role != "owner" {
		t.Fatalf("demo membership: %+v, %v", m, err)
	}
}

// With the demo turned off, the account an earlier run created must not be
// usable: its password is public.
func TestLockDisablesTheDemoAccount(t *testing.T) {
	s, store := newSeeder(t)
	ctx := context.Background()
	if err := s.ensureAccount(ctx); err != nil {
		t.Fatalf("account: %v", err)
	}
	if _, err := store.DB().Exec(store.DB().Rebind(`INSERT INTO api_key (id, user_id, organization_id, name, key_prefix, key_hash, scope, created_at)
		VALUES ('k1', ?, ?, 'x', 'obh_x', 'h', 'write', CURRENT_TIMESTAMP)`), UserID, OrgID); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	for range 2 { // every start
		if err := Lock(ctx, store.DB()); err != nil {
			t.Fatalf("lock: %v", err)
		}
	}
	u, err := store.Users().GetByID(ctx, UserID)
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordHash != "" || u.SessionsValidAfter < before {
		t.Fatalf("demo account after Lock: password set %v, sessions valid after %d", u.PasswordHash != "", u.SessionsValidAfter)
	}
	var keys int
	if err := store.DB().Get(&keys, store.DB().Rebind(`SELECT COUNT(*) FROM api_key WHERE user_id = ?`), UserID); err != nil || keys != 0 {
		t.Fatalf("api keys left: %d (%v)", keys, err)
	}
	// Turning the demo on again restores the account.
	if err := s.ensureAccount(ctx); err != nil {
		t.Fatalf("account: %v", err)
	}
	if u, err := store.Users().GetByID(ctx, UserID); err != nil || u.PasswordHash == "" {
		t.Fatalf("demo account after it is turned on again: %+v, %v", u, err)
	}
}
