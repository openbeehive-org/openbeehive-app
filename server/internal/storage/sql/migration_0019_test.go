package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// Migration 0019 gives the database its sync state and moves what the
// change log alone used to know onto the rows: who owns a personal task and
// when each change was received.
func testSyncStateMigration(t *testing.T, s *Store) {
	ctx := context.Background()
	runMigrationsBefore(t, s, "0019")
	now := time.Now().UTC()
	for _, task := range []struct{ id, apiary string }{{"t-personal", ""}, {"t-shared", "ap-1"}, {"t-unlogged", ""}} {
		if err := s.exec(ctx, `INSERT INTO task (id, organization_id, title, apiary_id, created_at)
			VALUES (?, 'org-a', 'x', ?, ?)`, task.id, task.apiary, now); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range []struct{ scope, entity, id string }{
		{"user:u-1", "task", "t-personal"},
		{"user:u-1", "task", "t-personal"},
		{"ap-1", "task", "t-shared"},
		{"user:u-9", "hive", "t-unlogged"}, // another entity under the same id
	} {
		if err := s.exec(ctx, `INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
			VALUES (?, ?, ?, ?, ?, 1, '{}', '000000000000001:00000:dev', 'u', 'org-a')`,
			string(rune('a'+i)), i+1, c.scope, c.entity, c.id); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().Add(-5 * time.Second).UnixMilli()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	owners := map[string]string{}
	rows, err := s.db.Queryx(`SELECT id, owner_id FROM task`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			t.Fatal(err)
		}
		owners[id] = owner
	}
	_ = rows.Close()
	if owners["t-personal"] != "u-1" || owners["t-shared"] != "" || owners["t-unlogged"] != "" || len(owners) != 3 {
		t.Fatalf("task owners after the migration: %v", owners)
	}

	var epoch, horizon int64
	if err := s.db.Get(&epoch, `SELECT val FROM sync_state WHERE name = 'epoch'`); err != nil || epoch <= 0 {
		t.Fatalf("epoch: %d %v", epoch, err)
	}
	if err := s.db.Get(&horizon, `SELECT val FROM sync_state WHERE name = 'horizon'`); err != nil || horizon != 0 {
		t.Fatalf("horizon: %d %v", horizon, err)
	}
	// Existing entries count as received now, so the first compaction
	// after an upgrade removes nothing a device may still be reading.
	var oldest, newest int64
	if err := s.db.QueryRow(`SELECT MIN(logged_at), MAX(logged_at) FROM change_log`).Scan(&oldest, &newest); err != nil {
		t.Fatal(err)
	}
	if after := time.Now().Add(5 * time.Second).UnixMilli(); oldest < before || newest > after {
		t.Fatalf("logged_at of existing entries: %d..%d, want about %d", oldest, newest, before)
	}
	if n := countIndex(t, s, "idx_change_scope_seq"); n != 0 {
		t.Fatalf("idx_change_scope_seq still exists")
	}
	if n := countIndex(t, s, "idx_change_org_seq"); n == 0 {
		t.Fatalf("idx_change_org_seq is gone")
	}
}

// countIndex reports how many indexes of that name the database has.
func countIndex(t *testing.T, s *Store, name string) int {
	t.Helper()
	q := map[config.DBDriver]string{
		config.DriverSQLite:   `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`,
		config.DriverPostgres: `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`,
		config.DriverMySQL: `SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND INDEX_NAME = ?`,
	}[s.driver]
	var n int
	if err := s.db.Get(&n, s.db.Rebind(q), name); err != nil {
		t.Fatalf("look up index %s: %v", name, err)
	}
	return n
}

func TestMigration0019(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) { testSyncStateMigration(t, b.blank(t)) })
}

// Every database gets an epoch of its own: it is what tells a device that
// the server it talks to is not the one it synced with before.
func TestEpochDiffersBetweenDatabases(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		seen := map[int64]bool{}
		for range 3 {
			s := b.blank(t)
			if err := s.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			var epoch int64
			if err := s.db.Get(&epoch, `SELECT val FROM sync_state WHERE name = 'epoch'`); err != nil || epoch <= 0 {
				t.Fatalf("epoch: %d %v", epoch, err)
			}
			if seen[epoch] {
				t.Fatalf("two databases share epoch %d", epoch)
			}
			seen[epoch] = true
		}
	})
}
