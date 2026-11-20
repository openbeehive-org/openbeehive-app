package sqlstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// firstMigrationAfter041 is the first file a 0.4.1 install has not run.
const firstMigrationAfter041 = "0015"

// TestMigrationsSurviveACrash stops each migration added since 0.4.1 after
// every one of its statements, as a crash would, and starts again: the run
// must finish and leave the same tables, columns and indexes as a fresh
// install. MySQL-family databases commit each DDL statement on its own, so
// there a crash really leaves a file half applied. Postgres runs a file in
// one transaction, but its files are written to be repeatable all the same.
// SQLite is left out: its table rebuilds rely on the file's transaction.
func TestMigrationsSurviveACrash(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		if b.driver == config.DriverSQLite {
			t.Skip("SQLite applies a migration file in one transaction")
		}
		ctx := context.Background()
		fresh := b.blank(t)
		if err := fresh.Migrate(ctx); err != nil {
			t.Fatalf("fresh install: %v", err)
		}
		want := catalogue(t, fresh)
		_ = fresh.Close()

		for _, name := range migrationNames() {
			if name < firstMigrationAfter041 {
				continue
			}
			if only := migrationDriver(name); only != "" && only != b.driver {
				continue
			}
			raw, _ := migrationsFS.ReadFile("migrations/" + name)
			stmts := splitStatements(string(raw))
			for done := 1; done <= len(stmts); done++ {
				s := b.blank(t)
				runMigrationsBefore(t, s, name)
				for _, stmt := range stmts[:done] {
					if _, err := s.db.ExecContext(ctx, s.translate(stmt)); err != nil {
						t.Fatalf("%s, statement %d: %v", name, done, err)
					}
				}
				if err := s.Migrate(ctx); err != nil {
					t.Fatalf("restart after statement %d of %s: %v", done, name, err)
				}
				assertSameCatalogue(t, fmt.Sprintf("%s, crash after statement %d", name, done), want, catalogue(t, s))
				_ = s.Close()
			}
		}
	})
}

// assertSameCatalogue fails with the differences between two catalogues.
func assertSameCatalogue(t *testing.T, what string, want, got []string) {
	t.Helper()
	var missing, extra []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			extra = append(extra, g)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("%s: schema differs from a fresh install\nmissing: %s\nextra:   %s",
			what, strings.Join(missing, "\n         "), strings.Join(extra, "\n         "))
	}
}

// catalogue lists every column (type, nullability, default) and every index
// (columns, uniqueness) of the database, sorted, in a form that is the same
// for two databases of the same shape. Column order is left out: a column
// that a migration adds sits at the end, wherever it would be on a fresh
// install.
func catalogue(t *testing.T, s *Store) []string {
	t.Helper()
	var queries []string
	switch s.driver {
	case config.DriverMySQL:
		queries = []string{
			`SELECT CONCAT_WS(' | ', TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE,
				IFNULL(COLUMN_DEFAULT, '<none>'), IFNULL(COLLATION_NAME, ''))
			 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()`,
			`SELECT CONCAT_WS(' | ', TABLE_NAME, INDEX_NAME, NON_UNIQUE,
				GROUP_CONCAT(CONCAT(COLUMN_NAME, '(', IFNULL(SUB_PART, ''), ')') ORDER BY SEQ_IN_INDEX))
			 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
			 GROUP BY TABLE_NAME, INDEX_NAME, NON_UNIQUE`,
		}
	case config.DriverPostgres:
		queries = []string{
			`SELECT concat_ws(' | ', table_name, column_name, data_type, character_maximum_length,
				is_nullable, coalesce(column_default, '<none>'))
			 FROM information_schema.columns WHERE table_schema = current_schema()`,
			`SELECT tablename || ' | ' || indexname || ' | ' || replace(indexdef, current_schema() || '.', '')
			 FROM pg_indexes WHERE schemaname = current_schema()`,
		}
	case config.DriverSQLite:
		queries = []string{
			`SELECT m.name || ' | ' || p.name || ' | ' || p.type || ' | ' || p."notnull" || ' | ' ||
				IFNULL(p.dflt_value, '<none>') || ' | ' || p.pk
			 FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table'`,
			`SELECT m.name || ' | ' || il.name || ' | ' || il."unique" || ' | ' ||
				(SELECT group_concat(ii.name) FROM pragma_index_info(il.name) ii)
			 FROM sqlite_master m, pragma_index_list(m.name) il WHERE m.type = 'table'`,
		}
	}
	var out []string
	for _, q := range queries {
		var rows []string
		if err := s.db.Select(&rows, q); err != nil {
			t.Fatalf("read catalogue: %v", err)
		}
		out = append(out, rows...)
	}
	slices.Sort(out)
	return out
}
