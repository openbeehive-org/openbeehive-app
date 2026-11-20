package service

import (
	"strings"
	"testing"
)

// Every column the sync whitelist accepts must exist in its table after the
// migrations have run: a push naming a missing column would fail, and a
// migration that rebuilds a table must not lose one.
func TestWhitelistedColumnsExist(t *testing.T) {
	db := newFixtureStore(t).DB()
	for entity, spec := range entityCols {
		cols := append(append([]string{}, spec.cols...), spec.setCols...)
		rows, err := db.Query("SELECT " + strings.Join(cols, ", ") + ", field_hlc FROM " + spec.table + " WHERE 1 = 0")
		if err != nil {
			t.Fatalf("%s: %v", entity, err)
		}
		_ = rows.Close()
	}
}
