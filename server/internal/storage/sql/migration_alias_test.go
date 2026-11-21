package sqlstore

import (
	"context"
	"testing"
)

// A database that ran 0015 and 0017 before they were split per dialect has
// them recorded under their old names; starting the server again must not
// run them a second time (on SQLite that fails: the columns they move are
// gone).
func TestRenamedMigrationsCountAsApplied(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		s := b.blank(t)
		ctx := context.Background()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		for name, old := range migrationAliases {
			if err := s.exec(ctx, `DELETE FROM schema_migrations WHERE version = ?`, name); err != nil {
				t.Fatal(err)
			}
			if err := s.exec(ctx, `DELETE FROM schema_migrations WHERE version = ?`, old); err != nil {
				t.Fatal(err)
			}
			if err := s.exec(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, CURRENT_TIMESTAMP)`, old); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate a database that recorded the old names: %v", err)
		}
		for name := range migrationAliases {
			done, err := s.migrationApplied(ctx, name)
			if err != nil || !done {
				t.Errorf("%s applied = %v (%v), want true", name, done, err)
			}
		}
	})
}
