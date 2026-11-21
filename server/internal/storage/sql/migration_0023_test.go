package sqlstore

import (
	"context"
	"strings"
	"testing"
)

// Migration 0023 indexes the change log by record, keeping the entries
// that are already there.
func TestMigration0023(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		s := b.blank(t)
		ctx := context.Background()
		runMigrationsBefore(t, s, "0023")
		long := strings.Repeat("x", 255)
		if err := s.exec(ctx, `INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
			VALUES ('c1', 1, 'ap-1', 'inspection', ?, 1, '{}', '000000000000001:00000:dev', 'u', 'org-a')`, long); err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if n := countIndex(t, s, "idx_change_record"); n != 1 {
			t.Fatalf("index idx_change_record: found %d", n)
		}
		var n int
		if err := s.db.Get(&n, s.db.Rebind(`SELECT COUNT(*) FROM change_log
			WHERE org_id = 'org-a' AND entity = 'inspection' AND entity_id = ?`), long); err != nil || n != 1 {
			t.Fatalf("entry after the migration: %d %v", n, err)
		}
	})
}
