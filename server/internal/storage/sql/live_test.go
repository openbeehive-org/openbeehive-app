package sqlstore

// Live dialect tests: run the full migration chain and a smoke workload
// against a real backend. SQLite always runs; Postgres and MySQL are skipped
// unless a DSN is provided, e.g. with podman:
//
//	podman run -d --rm --name beehive-pg -e POSTGRES_USER=beehive \
//	  -e POSTGRES_PASSWORD=beehive -e POSTGRES_DB=beehive -p 5433:5432 postgres:17
//	BEEHIVE_TEST_PG_DSN="postgres://beehive:beehive@localhost:5433/beehive?sslmode=disable" \
//	  go test ./internal/storage/sql/
//
//	podman run -d --rm --name beehive-mysql -e MYSQL_ROOT_PASSWORD=beehive \
//	  -e MYSQL_DATABASE=beehive -p 3307:3306 mysql:8
//	BEEHIVE_TEST_MYSQL_DSN="root:beehive@tcp(127.0.0.1:3307)/beehive" \
//	  go test ./internal/storage/sql/

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// syncedTables must all exist after the chain has run.
var syncedTables = []string{
	"organization", "users", "member", "apiary", "hive", "queen", "inspection",
	"task", "treatment", "harvest", "placement", "event", "change_log",
	"seq_counter", "apiary_share", "user_passkey", "invite",
}

func openSQLite(t *testing.T) *Store {
	t.Helper()
	cfg := &config.Config{}
	cfg.DB.Driver = config.DriverSQLite
	cfg.DB.DSN = "file:" + filepath.Join(t.TempDir(), "live.db")
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func openLive(t *testing.T, driver config.DBDriver, envVar string) *Store {
	t.Helper()
	dsn := os.Getenv(envVar)
	if dsn == "" {
		t.Skipf("set %s to run the %s live test", envVar, driver)
	}
	cfg := &config.Config{}
	cfg.DB.Driver = driver
	cfg.DB.DSN = dsn
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("open %s: %v", driver, err)
	}
	t.Cleanup(func() { s.Close() })
	resetSchema(t, s)
	return s
}

// resetSchema drops everything so each test starts from a blank database.
func resetSchema(t *testing.T, s *Store) {
	t.Helper()
	switch s.driver {
	case config.DriverPostgres:
		s.db.MustExec(`DROP SCHEMA public CASCADE`)
		s.db.MustExec(`CREATE SCHEMA public`)
	case config.DriverMySQL:
		var tables []string
		if err := s.db.Select(&tables, `SHOW TABLES`); err != nil {
			t.Fatalf("show tables: %v", err)
		}
		for _, table := range tables {
			s.db.MustExec("DROP TABLE IF EXISTS `" + table + "`")
		}
	}
}

func TestMigrateLiveSQLite(t *testing.T) {
	testMigratedStore(t, openSQLite(t))
	testPartialRecovery(t, openSQLite(t))
}

func TestMigrateLivePostgres(t *testing.T) {
	s := openLive(t, config.DriverPostgres, "BEEHIVE_TEST_PG_DSN")
	testMigratedStore(t, s)
	resetSchema(t, s)
	testPartialRecovery(t, s)
}

func TestMigrateLiveMySQL(t *testing.T) {
	s := openLive(t, config.DriverMySQL, "BEEHIVE_TEST_MYSQL_DSN")
	testMigratedStore(t, s)
	resetSchema(t, s)
	testPartialRecovery(t, s)
}

// testMigratedStore: full chain, idempotent re-run, then a smoke workload that
// exercises the spots that differ between dialects (booleans, defaults,
// timestamps, the seq counter).
func testMigratedStore(t *testing.T, s *Store) {
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate (2nd run): %v", err)
	}
	assertFullyMigrated(t, s)

	// Boolean round-trip through the user repo (write bool, filter, scan bool).
	now := time.Now().UTC().Truncate(time.Second)
	u := &storage.User{ID: "live-u1", Email: "live@example.org", Name: "Live",
		Role: "user", VerifyToken: "tok", CreatedAt: now}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("user create: %v", err)
	}
	if err := s.Users().MarkVerified(ctx, u.ID); err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	got, err := s.Users().GetByEmail(ctx, "LIVE@example.org")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !got.EmailVerified {
		t.Fatalf("email_verified: got false, want true")
	}
	// Timestamp round-trip (MySQL needs parseTime, which Open forces on).
	if got.CreatedAt.UTC().Sub(now).Abs() > time.Second {
		t.Fatalf("created_at round-trip: got %v, want %v", got.CreatedAt, now)
	}

	// BOOLEAN DEFAULT TRUE (queen.active) and scanning it back as bool.
	if _, err := s.db.Exec(s.db.Rebind(
		`INSERT INTO queen (id, organization_id, hive_id, year, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`), "live-q1", "org", "hive", 2026, now, now); err != nil {
		t.Fatalf("queen insert: %v", err)
	}
	var active bool
	if err := s.db.Get(&active, s.db.Rebind(`SELECT active FROM queen WHERE id = ?`), "live-q1"); err != nil {
		t.Fatalf("queen active scan: %v", err)
	}
	if !active {
		t.Fatalf("queen.active default: got false, want true")
	}

	// The change feed counter seeded by 0002.
	if _, err := s.db.Exec(`UPDATE seq_counter SET val = val + 1 WHERE name = 'change'`); err != nil {
		t.Fatalf("seq bump: %v", err)
	}
	var seq int64
	if err := s.db.Get(&seq, `SELECT val FROM seq_counter WHERE name = 'change'`); err != nil {
		t.Fatalf("seq read: %v", err)
	}
	if seq != 1 {
		t.Fatalf("seq: got %d, want 1", seq)
	}
}

// testPartialRecovery replays the situation a pre-fix Postgres instance is
// left in: 0001 failed halfway (at the queen table), the statements before it
// are applied but no version was recorded. Migrate must pick up and finish.
func testPartialRecovery(t *testing.T, s *Store) {
	ctx := context.Background()
	raw, _ := migrationsFS.ReadFile("migrations/0001_init.sql")
	for _, stmt := range splitStatements(string(raw)) {
		if strings.Contains(stmt, "queen") {
			break
		}
		if _, err := s.db.ExecContext(ctx, s.translate(stmt)); err != nil {
			t.Fatalf("replaying partial 0001: %v", err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate over partial 0001: %v", err)
	}
	assertFullyMigrated(t, s)
}

func assertFullyMigrated(t *testing.T, s *Store) {
	t.Helper()
	entries, _ := migrationsFS.ReadDir("migrations")
	var applied int
	if err := s.db.Get(&applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if applied != len(entries) {
		t.Fatalf("applied migrations: got %d, want %d", applied, len(entries))
	}
	for _, table := range syncedTables {
		var n int
		if err := s.db.Get(&n, `SELECT COUNT(*) FROM `+table); err != nil {
			t.Fatalf("table %s missing after migration: %v", table, err)
		}
	}
}
