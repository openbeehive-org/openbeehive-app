package sqlstore

import (
	"context"
	"testing"
)

// runMigrationsBefore applies the chain up to, but excluding, the given file.
func runMigrationsBefore(t *testing.T, s *Store, stop string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, s.translate(
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP)`)); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= stop {
			return
		}
		if err := s.runMigration(ctx, e.Name()); err != nil {
			t.Fatal(err)
		}
	}
}

// Migration 0015 rebuilds change_log.org_id as an indexable column. Every
// logged change must keep its tenant: Pull filters by it.
func testChangeLogKeepsTenants(t *testing.T, s *Store) {
	ctx := context.Background()
	runMigrationsBefore(t, s, "0015")
	for i, org := range []string{"org-a", "org-b", "org-a"} {
		if err := s.exec(ctx, `INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
			VALUES (?, ?, 'apiary-1', 'hive', 'h1', 1, '{}', '000000000000001:00000:dev', 'u', ?)`,
			string(rune('a'+i)), i+1, org); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var orgs []string
	if err := s.db.Select(&orgs, `SELECT org_id FROM change_log ORDER BY seq`); err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 3 || orgs[0] != "org-a" || orgs[1] != "org-b" || orgs[2] != "org-a" {
		t.Fatalf("tenants after the migration: %v", orgs)
	}
	// Writers that do not name the column list in a new order keep working.
	if err := s.exec(ctx, `INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
		VALUES ('d', 4, 'apiary-1', 'hive', 'h1', 1, '{}', '000000000000002:00000:dev', 'u', 'org-b')`); err != nil {
		t.Fatalf("insert after the migration: %v", err)
	}
	var n int
	if err := s.db.Get(&n, `SELECT COUNT(*) FROM apiary_share`); err == nil {
		t.Fatal("apiary_share still exists")
	}
}

func TestMigration0015(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) { testChangeLogKeepsTenants(t, b.blank(t)) })
}
