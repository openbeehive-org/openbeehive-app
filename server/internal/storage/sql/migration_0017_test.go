package sqlstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// 0017 makes the optional numbers of an inspection nullable (SQLite by
// rebuilding the table, the others in place). Every stored value must be
// kept, the result must accept NULL, and the new columns must exist, on
// every dialect.
func TestInspectionNumbersBecomeNullable(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) { testInspectionNumbersNullable(t, b.blank(t)) })
}

func testInspectionNumbersNullable(t *testing.T, s *Store) {
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO inspection
		(id, organization_id, hive_id, date, created_at, weather, queen_seen, frames, weight_kg, temperament,
		 temp_hive, photo_keys, field_hlc, deleted)
		VALUES (?, ?, ?, ?, ?, ?, TRUE, ?, ?, ?, ?, ?, ?, TRUE)`),
		"i-kept", "org", "hive", now, now, "sunny", 7, 41.5, 3, 34.25, `{"k":1}`, `{"frames":"h"}`); err != nil {
		t.Fatalf("insert with values: %v", err)
	}

	// Run 0017 again over a table that holds data: this is what an
	// upgrading instance runs (SQLite rebuilds the table, the others change
	// it in place, and those must be able to repeat it after a crash), and
	// every value must come through.
	name := "0017_record_semantics." + string(s.driver) + ".sql"
	if s.driver == config.DriverSQLite {
		raw, _ := migrationsFS.ReadFile("migrations/" + name)
		for _, stmt := range splitStatements(string(raw)) {
			if strings.Contains(stmt, "amended_at") {
				continue // plain additions, already applied
			}
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("replaying %q: %v", stmt, err)
			}
		}
	} else {
		if err := s.exec(ctx, `DELETE FROM schema_migrations WHERE version = ?`, name); err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("running %s again: %v", name, err)
		}
	}

	var kept int
	if err := s.db.Get(&kept, s.db.Rebind(`SELECT COUNT(*) FROM inspection
		WHERE id = 'i-kept' AND organization_id = 'org' AND hive_id = 'hive' AND weather = 'sunny'
		  AND queen_seen = TRUE AND eggs_seen = FALSE AND frames = 7 AND weight_kg = 41.5 AND temperament = 3
		  AND temp_hive = 34.25 AND temp_outside IS NULL AND photo_keys = ? AND field_hlc = ?
		  AND deleted = TRUE AND source = 0 AND date = created_at`), `{"k":1}`, `{"frames":"h"}`); err != nil || kept != 1 {
		t.Fatalf("values lost in the rebuild: %d matching rows (%v)", kept, err)
	}

	// Every optional number accepts NULL; source defaults to 0.
	if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO inspection
		(id, organization_id, hive_id, date, created_at, temperament, frames, stores, queen_cells, honey_kg,
		 brood_frames, calmness, fed_kg, frames_added, frames_removed, weight_kg, youngest_larva,
		 temp_hive, temp_outside, humidity_hive, humidity_outside)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`),
		"i-null", "org", "hive", now, now); err != nil {
		t.Fatalf("insert with NULL numbers: %v", err)
	}
	var unset int
	if err := s.db.Get(&unset, `SELECT COUNT(*) FROM inspection
		WHERE id = 'i-null' AND frames IS NULL AND weight_kg IS NULL AND calmness IS NULL AND source = 0`); err != nil || unset != 1 {
		t.Fatalf("NULL numbers / default source: %d rows (%v)", unset, err)
	}

	// Corrections of treatments and harvests carry a nullable timestamp.
	for _, table := range []string{"treatment", "harvest"} {
		var n int
		if err := s.db.Get(&n, `SELECT COUNT(*) FROM `+table+` WHERE amended_at IS NOT NULL`); err != nil {
			t.Fatalf("%s.amended_at: %v", table, err)
		}
	}
}
