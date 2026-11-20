package sqlstore

// Live dialect tests: run the migration chain and a smoke workload against a
// real backend. They run on the backend selected by
// BEEHIVE_TEST_DATABASE_DRIVER / BEEHIVE_TEST_DATABASE_DSN (SQLite when
// unset, see package sqltest) and additionally on Postgres and the MySQL
// family whenever BEEHIVE_TEST_PG_DSN / BEEHIVE_TEST_MYSQL_DSN is set, e.g.
// with podman:
//
//	podman run -d --rm --name beehive-pg -e POSTGRES_USER=beehive \
//	  -e POSTGRES_PASSWORD=beehive -e POSTGRES_DB=beehive -p 5433:5432 postgres:18-alpine
//	BEEHIVE_TEST_PG_DSN="postgres://beehive:beehive@localhost:5433/beehive?sslmode=disable" \
//	  go test ./internal/storage/sql/
//
//	podman run -d --rm --name beehive-mariadb -e MARIADB_ROOT_PASSWORD=beehive \
//	  -e MARIADB_DATABASE=beehive -p 3307:3306 mariadb:13.0.2
//	BEEHIVE_TEST_MYSQL_DSN="root:beehive@tcp(127.0.0.1:3307)/beehive" \
//	  go test ./internal/storage/sql/

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql/sqltest"
)

// syncedTables must all exist after the chain has run.
var syncedTables = []string{
	"organization", "users", "member", "apiary", "hive", "queen", "inspection",
	"task", "treatment", "harvest", "placement", "event", "change_log",
	"seq_counter", "user_passkey", "invite", "api_key",
}

type backend struct {
	driver config.DBDriver
	dsn    string
}

// backends lists where the live tests run: the selected backend, plus the
// ones named by the older per-dialect variables.
func backends() []backend {
	driver, dsn := sqltest.Selected()
	out := []backend{{driver, dsn}}
	for _, b := range []backend{
		{config.DriverPostgres, os.Getenv("BEEHIVE_TEST_PG_DSN")},
		{config.DriverMySQL, os.Getenv("BEEHIVE_TEST_MYSQL_DSN")},
	} {
		if b.dsn != "" && b.driver != driver {
			out = append(out, b)
		}
	}
	return out
}

// onEachBackend runs fn once per backend, as a subtest named after it.
func onEachBackend(t *testing.T, fn func(t *testing.T, b backend)) {
	for _, b := range backends() {
		t.Run(string(b.driver), func(t *testing.T) { fn(t, b) })
	}
}

// blank opens a store on a new, empty database.
func (b backend) blank(t *testing.T) *Store {
	t.Helper()
	return b.open(t, sqltest.NewOn(t, b.driver, b.dsn))
}

func (b backend) open(t *testing.T, dsn string) *Store {
	t.Helper()
	cfg := &config.Config{}
	cfg.DB.Driver = b.driver
	cfg.DB.DSN = dsn
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("open %s: %v", b.driver, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrate(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		testMigratedStore(t, b.blank(t))
		testPartialRecovery(t, b.blank(t))
	})
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
	var applied int
	if err := s.db.Get(&applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if want := len(migrationNames()); applied != want {
		t.Fatalf("applied migrations: got %d, want %d", applied, want)
	}
	for _, table := range syncedTables {
		var n int
		if err := s.db.Get(&n, `SELECT COUNT(*) FROM `+table); err != nil {
			t.Fatalf("table %s missing after migration: %v", table, err)
		}
	}
	if s.driver == config.DriverMySQL {
		assertBinaryCollation(t, s)
	}
}

// assertBinaryCollation: on the MySQL family every table and every text
// column uses the binary collation, whichever statement created it.
func assertBinaryCollation(t *testing.T, s *Store) {
	t.Helper()
	var tables []string
	if err := s.db.Select(&tables, `SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_COLLATION <> ?`, s.mysqlCollation); err != nil {
		t.Fatalf("read table collations: %v", err)
	}
	if len(tables) != 0 {
		t.Fatalf("tables on another collation than %s: %v", s.mysqlCollation, tables)
	}
	var columns []string
	if err := s.db.Select(&columns, `SELECT CONCAT(TABLE_NAME, '.', COLUMN_NAME) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND COLLATION_NAME IS NOT NULL AND COLLATION_NAME <> ?`, s.mysqlCollation); err != nil {
		t.Fatalf("read column collations: %v", err)
	}
	if len(columns) != 0 {
		t.Fatalf("columns on another collation than %s: %v", s.mysqlCollation, columns)
	}
}

func TestOwnTables(t *testing.T) {
	own := ownTables()
	for _, table := range append([]string{"schema_migrations", "user", "webauthn_user", "apiary_share"}, syncedTables...) {
		if !slices.Contains(own, table) {
			t.Errorf("ownTables() misses %q: %v", table, own)
		}
	}
}

func TestMigrationDriver(t *testing.T) {
	for name, want := range map[string]config.DBDriver{
		"0001_init.sql":               "",
		"0018_column_types.mysql.sql": config.DriverMySQL,
	} {
		if got := migrationDriver(name); got != want {
			t.Errorf("migrationDriver(%q) = %q, want %q", name, got, want)
		}
	}
	// A dialect suffix that matches no driver would silently never run.
	for _, name := range migrationNames() {
		switch migrationDriver(name) {
		case "", config.DriverPostgres, config.DriverMySQL, config.DriverSQLite:
		default:
			t.Errorf("%s: unknown dialect in file name", name)
		}
	}
}

// lastMigrationIn041 is where an install of release 0.4.1 stands.
const lastMigrationIn041 = "0014_api_keys.sql"

// TestUpgradeFrom041 builds a database as release 0.4.1 left it, fills every
// table that carries user data, applies the migrations added since and
// checks that each stored value is still there.
func TestUpgradeFrom041(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		dsn := sqltest.NewOn(t, b.driver, b.dsn)

		// The old release: its schema, its data, then it shuts down.
		old := b.open(t, dsn)
		if err := old.migrateUpTo(ctx, lastMigrationIn041); err != nil {
			t.Fatalf("migrate to 0.4.1: %v", err)
		}
		var applied []string
		if err := old.db.Select(&applied, `SELECT version FROM schema_migrations ORDER BY version`); err != nil {
			t.Fatalf("read versions: %v", err)
		}
		if len(applied) != 14 || applied[len(applied)-1] != lastMigrationIn041 {
			t.Fatalf("0.4.1 schema: applied %v", applied)
		}
		as041(t, old)
		insert041Rows(t, old)
		before := snapshot(t, old)
		_ = old.Close()

		// The new release starts on the same database.
		s := b.open(t, dsn)
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		assertFullyMigrated(t, s)
		after := snapshot(t, s)

		for table, rows := range before {
			for key, row := range rows {
				got, ok := after[table][key]
				if !ok {
					t.Errorf("%s: row %s is gone after the upgrade", table, key)
					continue
				}
				for col, want := range row {
					if have, exists := got[col]; exists && have != want {
						t.Errorf("%s %s: column %s changed from %q to %q", table, key, col, want, have)
					}
				}
			}
		}

		if s.driver == config.DriverMySQL {
			var colType string
			if err := s.db.Get(&colType, `SELECT COLUMN_TYPE FROM information_schema.COLUMNS
				WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_passkey' AND COLUMN_NAME = 'user_id'`); err != nil || colType != "varchar(64)" {
				t.Fatalf("user_passkey.user_id after upgrade: %q, %v", colType, err)
			}
		}

		// Numbers entered with decimals read back as entered.
		var lat, kg float64
		if err := s.db.QueryRow(`SELECT a.lat, h.amount_kg FROM apiary a, harvest h`).Scan(&lat, &kg); err != nil {
			t.Fatalf("read numbers: %v", err)
		}
		if lat != 48.193 || kg != 12.3 {
			t.Fatalf("after upgrade: lat %v, amount_kg %v; want 48.193 and 12.3", lat, kg)
		}

		// The upgraded install serves the same repo calls as a fresh one.
		u, err := s.Users().GetByEmail(ctx, "Keeper@Example.org")
		if err != nil || u.ID != "u-1" || !u.EmailVerified {
			t.Fatalf("user after upgrade: %+v, %v", u, err)
		}
		testLargeText(t, s)
	})
}

// upgradeTables are the tables of release 0.4.1 whose rows must survive an
// upgrade, each with the columns that identify a row.
var upgradeTables = map[string][]string{
	"organization": {"id"}, "users": {"id"}, "member": {"organization_id", "benutzer_id"},
	"apiary": {"id"}, "hive": {"id"}, "queen": {"id"}, "inspection": {"id"}, "task": {"id"},
	"treatment": {"id"}, "harvest": {"id"}, "placement": {"id"}, "event": {"id"},
	"change_log": {"id"}, "seq_counter": {"name"}, "invite": {"id"},
	"user_passkey": {"id"}, "api_key": {"id"},
}

// as041 undoes what the migration runner has learned since release 0.4.1, so
// the tables have the shape a real 0.4.1 install has. On Postgres its REAL
// columns were 4-byte floats. On MariaDB its tables used the server's
// default collation and user_passkey.user_id was TEXT under a prefix index.
func as041(t *testing.T, s *Store) {
	t.Helper()
	switch s.driver {
	case config.DriverPostgres:
		for table, cols := range map[string][]string{
			"apiary":     {"lat", "lng"},
			"inspection": {"honey_kg", "fed_kg", "weight_kg", "temp_hive", "temp_outside", "humidity_hive", "humidity_outside"},
			"harvest":    {"amount_kg", "water_content"},
			"event":      {"amount_kg"},
		} {
			for _, col := range cols {
				s.db.MustExec(`ALTER TABLE ` + table + ` ALTER COLUMN ` + col + ` TYPE REAL`)
			}
		}
	case config.DriverMySQL:
		var serverDefault string
		if err := s.db.Get(&serverDefault, `SELECT @@collation_server`); err != nil {
			t.Fatalf("read server collation: %v", err)
		}
		var tables []string
		if err := s.db.Select(&tables, `SHOW TABLES`); err != nil {
			t.Fatalf("show tables: %v", err)
		}
		for _, table := range tables {
			s.db.MustExec("ALTER TABLE `" + table + "` CONVERT TO CHARACTER SET utf8mb4 COLLATE " + serverDefault)
		}
		// Only MariaDB accepted that index; Oracle MySQL never got this far.
		var version string
		if err := s.db.Get(&version, `SELECT VERSION()`); err != nil {
			t.Fatalf("read version: %v", err)
		}
		if strings.Contains(version, "MariaDB") {
			s.db.MustExec(`DROP INDEX idx_user_passkey_user ON user_passkey`)
			s.db.MustExec(`ALTER TABLE user_passkey MODIFY user_id TEXT NOT NULL`)
			s.db.MustExec(`CREATE INDEX idx_user_passkey_user ON user_passkey (user_id)`)
		}
	}
}

// insert041Rows writes one representative row per table, with the column set
// of release 0.4.1. Timestamps of synced rows are written the way the sync
// path writes them: as the client's ISO string on SQLite, as a point in time
// elsewhere.
func insert041Rows(t *testing.T, s *Store) {
	t.Helper()
	at := time.Date(2026, 7, 11, 10, 5, 0, 0, time.UTC)
	var ts any = at
	if s.driver == config.DriverSQLite {
		ts = at.Format("2006-01-02T15:04:05.000Z")
	}
	photo := "data:image/jpeg;base64," + strings.Repeat("A", 40_000)
	rows := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO organization (id, name, plan, created_at) VALUES (?, ?, ?, ?)`,
			[]any{"org-1", "Meadow Bees", "hobby", at}},
		{`INSERT INTO users (id, email, name, oidc_subject, password_hash, role, email_verified, verification_token, created_at)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"u-1", "keeper@example.org", "Keeper", "", "$2a$10$hash", "admin", true, "", at}},
		{`INSERT INTO member (organization_id, benutzer_id, role) VALUES (?, ?, ?)`,
			[]any{"org-1", "u-1", "owner"}},
		{`INSERT INTO apiary (id, organization_id, name, address, lat, lng, note, created_at, updated_at, updated_hlc, deleted, field_hlc)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"ap-1", "org-1", "Orchard", "Apfelweg 3", 48.193, 7.77, "south slope", ts, ts, "", false, `{"name":"000000000001000:00000:dev"}`}},
		{`INSERT INTO hive (id, organization_id, apiary_id, name, type, status, boxes, colony_origin, note, qr_code, photo, created_at, updated_at, updated_hlc, deleted, field_hlc)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"hv-1", "org-1", "ap-1", "Hive 1", 1, 1, 2, "swarm 2025", "strong", "qr-1", photo, ts, ts, "", false, "{}"}},
		{`INSERT INTO queen (id, organization_id, hive_id, year, marking, origin, breeder_number, introduced_at, replaced_at, active, note, created_at, updated_at, updated_hlc, deleted, field_hlc)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"q-1", "org-1", "hv-1", 2025, 5, "Carnica", "B-12", ts, nil, true, "", ts, ts, "", false, "{}"}},
		{`INSERT INTO inspection (id, organization_id, hive_id, date, weather, queen_seen, eggs_seen, temperament, frames, stores, queen_cells, varroa, honey_kg, note, photo_keys, created_at, updated_hlc, deleted, field_hlc,
		    brood_frames, calmness, fed_kg, frames_added, frames_removed, drone_frame_cut, super_added, weight_kg, youngest_larva, covered_larva, temp_hive, temp_outside, humidity_hive, humidity_outside)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"in-1", "org-1", "hv-1", ts, "sunny, 24°C", true, false, 2, 9, 1, 0, "3 mites/day", 2.5, "calm colony",
				`{"` + photo + `":{"a":["000000000001000:00000:dev"],"r":[]}}`, ts, "", false, "{}",
				4, 3, 1.5, 1, 0, true, false, 42.5, 3, true, 34.8, nil, 55.0, nil}},
		{`INSERT INTO task (id, organization_id, title, hive_id, apiary_id, due_at, done, priority, note, recurrence, assigned_to, created_at, updated_hlc, deleted, field_hlc)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"tk-1", "org-1", "Feed", "hv-1", nil, ts, false, 2, "", "", "", ts, "", false, "{}"}},
		{`INSERT INTO treatment (id, organization_id, apiary_id, hive_id, queen_id, date, product, active_ingredient, dose, method, batch_number, withdrawal_until, reason, note, field_hlc, deleted)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"tr-1", "org-1", "ap-1", "hv-1", "q-1", ts, "Formic acid 60%", "formic acid", "30 ml", "evaporation", "FA-1", ts, "varroa", "", "{}", false}},
		{`INSERT INTO harvest (id, organization_id, apiary_id, date, variety, amount_kg, water_content, batch_number, best_before, note, hive_id, queen_id, field_hlc, deleted)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"ha-1", "org-1", "ap-1", ts, "summer flow", 12.3, 17.2, "B2026-1", ts, "", "hv-1", "q-1", "{}", false}},
		{`INSERT INTO placement (id, organization_id, hive_id, apiary_id, start_at, end_at, field_hlc, deleted)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"pl-1", "org-1", "hv-1", "ap-1", ts, nil, "{}", false}},
		{`INSERT INTO event (id, organization_id, scope_id, type, date, apiary_id, hive_id, queen_id, ref_entity, ref_id, title, amount_kg, detail, author_id, field_hlc, deleted)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"ev-1", "org-1", "ap-1", 7, ts, "ap-1", "hv-1", "q-1", "harvest", "ha-1", "12.5 kg summer flow", 12.5, "{}", "u-1", "{}", false}},
		{`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"cl-1", 1, "ap-1", "hive", "hv-1", 1, `{"name":"Hive 1","photo":"` + photo + `"}`, "000000000001000:00000:dev", "u-1", "org-1"}},
		{`UPDATE seq_counter SET val = ? WHERE name = ?`, []any{1, "change"}},
		{`INSERT INTO invite (id, organization_id, email, role, token, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"iv-1", "org-1", "friend@example.org", "member", "invite-token-1", at}},
		{`INSERT INTO user_passkey (id, user_id, name, cred, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"Y3JlZC1pZA", "u-1", "Laptop", `{"id":"Y3JlZC1pZA"}`, at, nil}},
		{`INSERT INTO api_key (id, user_id, organization_id, name, key_prefix, key_hash, scope, created_at, expires_at, last_used_at)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"ak-1", "u-1", "org-1", "scale", "obh_abcd", strings.Repeat("ab", 32), "write", at, nil, nil}},
	}
	for _, r := range rows {
		if _, err := s.db.Exec(s.db.Rebind(r.q), r.args...); err != nil {
			t.Fatalf("insert 0.4.1 row: %v\n%s", err, r.q)
		}
	}
}

// snapshot reads every row of the upgrade tables as strings, keyed by table
// and row identity.
func snapshot(t *testing.T, s *Store) map[string]map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]map[string]string{}
	for table, keyCols := range upgradeTables {
		rows, err := s.db.Queryx(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		out[table] = map[string]map[string]string{}
		for rows.Next() {
			raw := map[string]any{}
			if err := rows.MapScan(raw); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			row := map[string]string{}
			for col, v := range raw {
				row[col] = cell(v)
			}
			key := make([]string, len(keyCols))
			for i, c := range keyCols {
				key[i] = row[c]
			}
			out[table][strings.Join(key, "/")] = row
		}
		_ = rows.Close()
		if len(out[table]) == 0 {
			t.Fatalf("%s: no rows to compare", table)
		}
	}
	return out
}

// cell renders a scanned value so that the same stored value reads the same
// before and after a migration.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "<null>"
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case float64:
		// Six digits: what a 4-byte float column of an older Postgres
		// install holds exactly, before and after it is widened.
		return strconv.FormatFloat(x, 'g', 6, 64)
	default:
		return fmt.Sprint(x)
	}
}

// TestLargeText: columns that carry inline images hold far more than the
// 64 KB a MySQL-family TEXT column takes.
func TestLargeText(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		s := b.blank(t)
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		testLargeText(t, s)
	})
}

func testLargeText(t *testing.T, s *Store) {
	t.Helper()
	big := strings.Repeat("x", 3<<20) // a photo of about 2 MB, base64-encoded
	now := time.Now().UTC()
	for _, w := range []struct {
		column, q string
		args      []any
	}{
		{"hive.photo", `INSERT INTO hive (id, organization_id, apiary_id, name, photo, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{"big-hive", "org", "ap", "H", big, now, now}},
		{"inspection.photo_keys", `INSERT INTO inspection (id, organization_id, hive_id, date, photo_keys, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"big-insp", "org", "big-hive", now, big, now}},
		{"change_log.payload", `INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"big-change", 1 << 40, "ap", "hive", "big-hive", 1, big, "h", "u", "org"}},
	} {
		if _, err := s.db.Exec(s.db.Rebind(w.q), w.args...); err != nil {
			t.Fatalf("%s: storing %d bytes: %v", w.column, len(big), err)
		}
	}
	for col, q := range map[string]string{
		"hive.photo":            `SELECT photo FROM hive WHERE id = 'big-hive'`,
		"inspection.photo_keys": `SELECT photo_keys FROM inspection WHERE id = 'big-insp'`,
		"change_log.payload":    `SELECT payload FROM change_log WHERE id = 'big-change'`,
	} {
		var got string
		if err := s.db.Get(&got, q); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if got != big {
			t.Fatalf("%s: read back %d bytes, stored %d", col, len(got), len(big))
		}
	}
}

// TestTextComparesExactly: ids, tokens and emails compare the same way on
// every backend. Case, accents and trailing spaces all tell two ids or tokens
// apart; emails are looked up without regard to (ASCII) case only.
func TestTextComparesExactly(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		s := b.blank(t)
		ctx := context.Background()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		now := time.Now().UTC()

		// Primary keys: two ids that differ in case are two rows.
		for _, id := range []string{"org-a", "ORG-A", "org-a "} {
			if err := s.Orgs().Create(ctx, &storage.Organization{ID: id, Name: "name of " + id, CreatedAt: now}); err != nil {
				t.Fatalf("create organization %q: %v", id, err)
			}
		}
		for _, id := range []string{"org-a", "ORG-A", "org-a "} {
			o, err := s.Orgs().Get(ctx, id)
			if err != nil || o.ID != id || o.Name != "name of "+id {
				t.Fatalf("get organization %q: %+v, %v", id, o, err)
			}
		}
		if _, err := s.Orgs().Get(ctx, "Org-A"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("organization lookup ignores case: %v", err)
		}

		// Secret tokens match only as issued.
		inv := &storage.Invite{ID: "inv-1", OrgID: "org-a", Email: "new@example.org", Role: "member", Token: "AbCdEf0123", CreatedAt: now}
		if err := s.Invites().Create(ctx, inv); err != nil {
			t.Fatalf("create invite: %v", err)
		}
		if got, err := s.Invites().GetByToken(ctx, "AbCdEf0123"); err != nil || got.ID != "inv-1" {
			t.Fatalf("invite by its token: %+v, %v", got, err)
		}
		for _, wrong := range []string{"abcdef0123", "ABCDEF0123", "AbCdEf0123 "} {
			if got, err := s.Invites().GetByToken(ctx, wrong); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("invite token %q matched %+v (err %v)", wrong, got, err)
			}
		}
		verify := &storage.User{ID: "u-verify", Email: "verify@example.org", Role: "user", VerifyToken: "VerifyMe", CreatedAt: now}
		if err := s.Users().Create(ctx, verify); err != nil {
			t.Fatalf("create user: %v", err)
		}
		for _, wrong := range []string{"verifyme", "VerifyMe "} {
			if got, err := s.Users().GetByVerifyToken(ctx, wrong); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("verification token %q matched %+v (err %v)", wrong, got, err)
			}
		}

		// Emails: case-insensitive lookup, but an accent is a different address.
		jose := &storage.User{ID: "u-jose", Email: "josé@example.org", Role: "user", CreatedAt: now}
		if err := s.Users().Create(ctx, jose); err != nil {
			t.Fatalf("create user: %v", err)
		}
		if got, err := s.Users().GetByEmail(ctx, "Verify@Example.ORG"); err != nil || got.ID != "u-verify" {
			t.Fatalf("email lookup must ignore case: %+v, %v", got, err)
		}
		if got, err := s.Users().GetByEmail(ctx, "jose@example.org"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("jose@ resolved to %+v (err %v)", got, err)
		}
		plain := &storage.User{ID: "u-jose-plain", Email: "jose@example.org", Role: "user", CreatedAt: now}
		if err := s.Users().Create(ctx, plain); err != nil {
			t.Fatalf("jose@ and josé@ must both be able to register: %v", err)
		}
	})
}

// TestMySQLAdoptsOlderInstall: a MySQL-family database created by a release
// that still used the server's default collation, and that stopped halfway
// through 0013, comes up with binary collation on every table and the
// passkey table in its indexable form.
func TestMySQLAdoptsOlderInstall(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		if b.driver != config.DriverMySQL {
			t.Skip("MySQL family only")
		}
		s := b.blank(t)
		ctx := context.Background()
		if err := s.migrateUpTo(ctx, "0012_rename_user_table.sql"); err != nil {
			t.Fatalf("migrate to 0012: %v", err)
		}
		for _, table := range []string{"users", "organization", "hive", "change_log", "schema_migrations"} {
			s.db.MustExec("ALTER TABLE " + table + " CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci")
		}
		s.db.MustExec(`CREATE TABLE user_passkey (id VARCHAR(255) PRIMARY KEY, user_id TEXT NOT NULL)`)
		// A table of some other application sharing the database is left alone.
		s.db.MustExec(`CREATE TABLE someone_elses (id VARCHAR(64) PRIMARY KEY) COLLATE utf8mb4_general_ci`)
		s.db.MustExec(`INSERT INTO users (id, email, oidc_subject, created_at) VALUES ('u-1', 'Keeper@example.org', '', NOW())`)

		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		var foreign string
		if err := s.db.Get(&foreign, `SELECT TABLE_COLLATION FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'someone_elses'`); err != nil || foreign != "utf8mb4_general_ci" {
			t.Fatalf("foreign table collation: %q, %v", foreign, err)
		}
		s.db.MustExec(`DROP TABLE someone_elses`)
		assertFullyMigrated(t, s)

		if u, err := s.Users().GetByEmail(ctx, "keeper@EXAMPLE.org"); err != nil || u.ID != "u-1" {
			t.Fatalf("user after conversion: %+v, %v", u, err)
		}
		if _, err := s.Users().GetByID(ctx, "U-1"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("id lookup still ignores case: %v", err)
		}
	})
}
