// Package sqlstore implements storage.Store for PostgreSQL, MySQL and SQLite.
//
// A single codebase serves all three dialects:
//   - placeholders are written generically as "?" and translated by sqlx.Rebind
//     into the driver's bindvar style ($1 for Postgres, ? for MySQL/SQLite)
//     .
//   - The schema uses only portable types (TEXT, INTEGER, REAL, TIMESTAMP,
//     BOOLEAN; VARCHAR(n) where MySQL needs an indexable width), IDs are
//     string UUIDs - no dialect-specific AUTO_INCREMENT/SERIAL. Boolean
//     literals are written TRUE/FALSE (valid in all three dialects; Postgres
//     rejects 0/1).
package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// mysqlTextDefault matches literal defaults on TEXT columns (see translate).
var mysqlTextDefault = regexp.MustCompile(`TEXT NOT NULL DEFAULT ('[^']*')`)

type Store struct {
	db     *sqlx.DB
	driver config.DBDriver
}

func Open(cfg *config.Config) (*Store, error) {
	driverName := map[config.DBDriver]string{
		config.DriverPostgres: "pgx",
		config.DriverMySQL:    "mysql",
		config.DriverSQLite:   "sqlite",
	}[cfg.DB.Driver]
	if driverName == "" {
		return nil, fmt.Errorf("unknown driver %q", cfg.DB.Driver)
	}

	dsn := cfg.DB.DSN
	if cfg.DB.Driver == config.DriverMySQL {
		// time.Time round-trips (created_at etc.) need parseTime; force it on
		// so a DSN without the flag doesn't fail on the first timestamp scan.
		if mycfg, err := mysql.ParseDSN(dsn); err == nil {
			mycfg.ParseTime = true
			dsn = mycfg.FormatDSN()
		}
	}
	db, err := sqlx.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	// SQLite tolerates only one writer.
	if cfg.DB.Driver == config.DriverSQLite {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(25)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return &Store{db: db, driver: cfg.DB.Driver}, nil
}

func (s *Store) Apiaries() storage.ApiaryRepo { return &apiaryRepo{s} }
func (s *Store) Hives() storage.HiveRepo      { return &hiveRepo{s} }
func (s *Store) Users() storage.UserRepo      { return &userRepo{s} }
func (s *Store) Orgs() storage.OrgRepo        { return &orgRepo{s} }
func (s *Store) Members() storage.MemberRepo  { return &memberRepo{s} }
func (s *Store) Invites() storage.InviteRepo  { return &inviteRepo{s} }
func (s *Store) Close() error                 { return s.db.Close() }

// DB exposes the sqlx handle (used by the sync service).
func (s *Store) DB() *sqlx.DB { return s.db }

// Migrate runs all embedded .sql files exactly once.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, s.translate(
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP)`)); err != nil {
		return err
	}
	entries, _ := migrationsFS.ReadDir("migrations")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var dummy string
		err := s.db.GetContext(ctx, &dummy,
			s.db.Rebind(`SELECT version FROM schema_migrations WHERE version = ?`), name)
		if err == nil {
			continue // already applied
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.runMigration(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// runMigration executes one .sql file and records its version, both in a
// single transaction: a mid-file failure rolls back cleanly on Postgres and
// SQLite (transactional DDL) instead of leaving half-applied ALTERs behind.
// MySQL auto-commits DDL, so there this degrades to statement granularity.
func (s *Store) runMigration(ctx context.Context, name string) error {
	raw, _ := migrationsFS.ReadFile("migrations/" + name)
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	// run statements individually - not every driver executes
	// multiple statements in one Exec (e.g. MySQL without multiStatements).
	for _, stmt := range splitStatements(string(raw)) {
		if _, err := tx.ExecContext(ctx, s.translate(stmt)); err != nil {
			if s.driver == config.DriverMySQL && isMySQLDuplicate(err) {
				// MySQL auto-commits DDL, so a migration that failed halfway
				// has already persisted its earlier statements; replaying
				// them raises duplicate column/index errors. Treat those as
				// "already applied" (the IF NOT EXISTS the other dialects
				// handle natively).
				continue
			}
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		tx.Rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`),
		name, time.Now().UTC()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migration %s: record version: %w", name, err)
	}
	return tx.Commit()
}

// isMySQLDuplicate reports duplicate column (1060) / duplicate index (1061)
// errors, which replaying already-applied DDL raises on MySQL.
func isMySQLDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1060 || me.Number == 1061)
}

// translate minimally adapts the portable migration statements to dialects.
// The base schema is deliberately portable; only a few tokens differ.
func (s *Store) translate(stmt string) string {
	switch s.driver {
	case config.DriverPostgres:
		// BOOLEAN/TIMESTAMP are native; nothing to do
		return stmt
	case config.DriverMySQL:
		r := strings.NewReplacer(
			// MySQL cannot index TEXT without a prefix length; PK ids are
			// short strings. 255 keeps room for non-UUID keys (base64url
			// WebAuthn credential ids) and stays under the 3072-byte limit.
			"TEXT PRIMARY KEY", "VARCHAR(255) PRIMARY KEY",
			"BOOLEAN", "TINYINT(1)",
			// TIMESTAMP in MySQL ends at 2038 and carries implicit
			// default/on-update magic; DATETIME(6) behaves like the others.
			"TIMESTAMP", "DATETIME(6)",
			// Double-quoted identifiers need ANSI_QUOTES in MySQL.
			`"user"`, "`user`",
			// MySQL has no CREATE INDEX IF NOT EXISTS; runMigration treats
			// the duplicate-index error as "already exists" instead.
			"CREATE UNIQUE INDEX IF NOT EXISTS", "CREATE UNIQUE INDEX",
			"CREATE INDEX IF NOT EXISTS", "CREATE INDEX",
		)
		// MySQL forbids plain literal defaults on TEXT columns, but since
		// 8.0.13 accepts them as (parenthesized) expression defaults.
		return mysqlTextDefault.ReplaceAllString(r.Replace(stmt),
			"TEXT NOT NULL DEFAULT ($1)")
	case config.DriverSQLite:
		// SQLite treats BOOLEAN as an alias and TIMESTAMP as TEXT/NUMERIC - ok.
		return stmt
	}
	return stmt
}

// helper: Rebind + Exec
func (s *Store) exec(ctx context.Context, q string, args ...any) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(q), args...)
	return err
}

// splitStatements splits a .sql file into individual statements (on ";"),
// strips line comments (--) and empty fragments. Comments are stripped
// BEFORE splitting so a ";" inside a comment does not break a statement.
func splitStatements(sql string) []string {
	var clean strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		clean.WriteString(line)
		clean.WriteString("\n")
	}
	var out []string
	for _, part := range strings.Split(clean.String(), ";") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}
