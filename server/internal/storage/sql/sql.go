// Package sqlstore implements storage.Store for PostgreSQL, the MySQL family
// (MariaDB is the tested target) and SQLite.
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
//   - Text compares byte for byte everywhere: SQLite and Postgres do so by
//     default, MySQL-family tables are created with a binary collation (see
//     mysqlBinaryCollation).
package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// mysqlTextDefault matches literal defaults on TEXT columns (see translate).
var mysqlTextDefault = regexp.MustCompile(`TEXT NOT NULL DEFAULT ('[^']*')`)

// postgresReal matches the REAL column type (see translate).
var postgresReal = regexp.MustCompile(`\bREAL\b`)

type Store struct {
	db     *sqlx.DB
	driver config.DBDriver
	// mysqlCollation is the collation of every table on the MySQL family.
	mysqlCollation string
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
	var db *sqlx.DB
	var err error
	if cfg.DB.Driver == config.DriverPostgres {
		db, err = openPostgres(dsn)
	} else {
		db, err = sqlx.Open(driverName, dsn)
	}
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
	s := &Store{db: db, driver: cfg.DB.Driver}
	if s.driver == config.DriverMySQL {
		if s.mysqlCollation, err = mysqlBinaryCollation(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("db collation: %w", err)
		}
	}
	return s, nil
}

// openPostgres opens the pool with utcTimestampCodec on every connection.
func openPostgres(dsn string) (*sqlx.DB, error) {
	pcfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*pcfg, stdlib.OptionAfterConnect(func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name: "timestamp", OID: pgtype.TimestampOID,
			Codec: utcTimestampCodec{&pgtype.TimestampCodec{}},
		})
		return nil
	}))
	return sqlx.NewDb(db, "pgx"), nil
}

// utcTimestampCodec writes a time.Time argument to a TIMESTAMP column as the
// instant it denotes, in UTC. pgx on its own drops the zone and stores the
// wall clock, so a value in any other zone (a client timestamp with an
// offset, a time.Now() on a host that does not run in UTC) lands hours off,
// while the MySQL driver and SQLite keep the instant.
type utcTimestampCodec struct{ *pgtype.TimestampCodec }

func (c utcTimestampCodec) PlanEncode(m *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	if _, ok := value.(time.Time); !ok {
		return c.TimestampCodec.PlanEncode(m, oid, format, value)
	}
	plan := c.TimestampCodec.PlanEncode(m, oid, format, pgtype.Timestamp{})
	if plan == nil {
		return nil
	}
	return utcTimestampPlan{plan}
}

type utcTimestampPlan struct{ next pgtype.EncodePlan }

func (p utcTimestampPlan) Encode(value any, buf []byte) ([]byte, error) {
	t, _ := value.(time.Time)
	return p.next.Encode(pgtype.Timestamp{Time: t.UTC(), Valid: true}, buf)
}

// mysqlBinaryCollation picks the utf8mb4 collation that compares like SQLite
// and Postgres do: case- and accent-sensitive, trailing spaces significant.
// The server defaults (utf8mb4_uca1400_ai_ci on MariaDB, utf8mb4_0900_ai_ci
// on MySQL 8) would treat "ABC", "abc" and, on MariaDB, "abc " as the same
// id, token or email. MariaDB calls the collation utf8mb4_nopad_bin, MySQL 8
// utf8mb4_0900_bin; utf8mb4_bin (binary, but trailing spaces ignored) is the
// fallback for servers that know neither.
func mysqlBinaryCollation(db *sqlx.DB) (string, error) {
	var known []string
	if err := db.Select(&known, `SELECT COLLATION_NAME FROM information_schema.COLLATIONS
		WHERE COLLATION_NAME IN ('utf8mb4_nopad_bin', 'utf8mb4_0900_bin')`); err != nil {
		return "", err
	}
	for _, name := range []string{"utf8mb4_nopad_bin", "utf8mb4_0900_bin"} {
		if slices.Contains(known, name) {
			return name, nil
		}
	}
	return "utf8mb4_bin", nil
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

// Migrate runs all embedded .sql files exactly once, then gives accounts from
// earlier releases their unique email key (see claimEmailKeys).
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.migrateUpTo(ctx, ""); err != nil {
		return err
	}
	return s.reportEmailConflicts(ctx)
}

// migrateUpTo applies the pending migrations in order and stops after the
// file named last ("" applies all of them).
func (s *Store) migrateUpTo(ctx context.Context, last string) error {
	if err := s.convertMySQLCollation(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, s.translate(
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP)`)); err != nil {
		return err
	}
	for _, name := range migrationNames() {
		if last != "" && name > last {
			break
		}
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

// migrationNames lists the embedded migration files in the order they apply.
func migrationNames() []string {
	entries, _ := migrationsFS.ReadDir("migrations")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// migrationDriver returns the one dialect a migration is written for, taken
// from its file name (NNNN_name.<driver>.sql), or "" for a portable file.
func migrationDriver(name string) config.DBDriver {
	parts := strings.Split(name, ".")
	if len(parts) == 3 {
		return config.DBDriver(parts[1])
	}
	return ""
}

// migrationTable matches the table a migration statement creates or renames to.
var migrationTable = regexp.MustCompile(`(?:CREATE TABLE IF NOT EXISTS|RENAME TO) "?(\w+)`)

// ownTables lists every table name the migrations have ever created.
func ownTables() []string {
	tables := []string{"schema_migrations"}
	for _, name := range migrationNames() {
		raw, _ := migrationsFS.ReadFile("migrations/" + name)
		for _, m := range migrationTable.FindAllStringSubmatch(string(raw), -1) {
			tables = append(tables, m[1])
		}
	}
	return tables
}

// convertMySQLCollation moves tables that an earlier release created with the
// server's default collation to the binary one. Only the application's own
// tables are touched, in case the database is shared. Once they are
// converted this is a single catalogue query per start.
func (s *Store) convertMySQLCollation(ctx context.Context) error {
	if s.driver != config.DriverMySQL {
		return nil
	}
	q, args, err := sqlx.In(`SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
		  AND TABLE_COLLATION <> ? AND TABLE_NAME IN (?)`, s.mysqlCollation, ownTables())
	if err != nil {
		return err
	}
	var tables []string
	if err := s.db.SelectContext(ctx, &tables, q, args...); err != nil {
		return fmt.Errorf("read table collations: %w", err)
	}
	for _, table := range tables {
		log.Printf("database: converting table %s to collation %s", table, s.mysqlCollation)
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			"ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE %s", table, s.mysqlCollation)); err != nil {
			return fmt.Errorf("convert table %s to %s: %w", table, s.mysqlCollation, err)
		}
	}
	return nil
}

// runMigration executes one .sql file and records its version, both in a
// single transaction: a mid-file failure rolls back cleanly on Postgres and
// SQLite (transactional DDL) instead of leaving half-applied ALTERs behind.
// MySQL auto-commits DDL, so there this degrades to statement granularity.
// A file written for another dialect is recorded without being run, so the
// version list reads the same on every backend.
func (s *Store) runMigration(ctx context.Context, name string) error {
	var raw []byte
	if only := migrationDriver(name); only == "" || only == s.driver {
		raw, _ = migrationsFS.ReadFile("migrations/" + name)
	}
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
// errors, which replaying already-applied DDL raises on MySQL and MariaDB.
func isMySQLDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1060 || me.Number == 1061)
}

// translate minimally adapts the portable migration statements to dialects.
// The base schema is deliberately portable; only a few tokens differ.
func (s *Store) translate(stmt string) string {
	switch s.driver {
	case config.DriverPostgres:
		// BOOLEAN/TIMESTAMP are native. REAL is a 4-byte float in Postgres
		// but an 8-byte double in SQLite and MariaDB.
		return postgresReal.ReplaceAllString(stmt, "DOUBLE PRECISION")
	case config.DriverMySQL:
		r := strings.NewReplacer(
			// The MySQL family cannot use TEXT as a primary key; PK ids are
			// short strings. 255 keeps room for non-UUID keys (base64url
			// WebAuthn credential ids) and stays under the 3072-byte limit.
			"TEXT PRIMARY KEY", "VARCHAR(255) PRIMARY KEY",
			"BOOLEAN", "TINYINT(1)",
			// TIMESTAMP in MySQL ends at 2038 and carries implicit
			// default/on-update magic; DATETIME(6) behaves like the others.
			"TIMESTAMP", "DATETIME(6)",
			// Double-quoted identifiers need ANSI_QUOTES in MySQL.
			`"user"`, "`user`",
			// Oracle MySQL has no CREATE INDEX IF NOT EXISTS (MariaDB does);
			// runMigration treats the duplicate-index error, which both
			// raise, as "already exists" instead.
			"CREATE UNIQUE INDEX IF NOT EXISTS", "CREATE UNIQUE INDEX",
			"CREATE INDEX IF NOT EXISTS", "CREATE INDEX",
		)
		// Oracle MySQL forbids plain literal defaults on TEXT columns, but
		// since 8.0.13 accepts them as (parenthesized) expression defaults;
		// MariaDB accepts both forms.
		out := mysqlTextDefault.ReplaceAllString(r.Replace(stmt),
			"TEXT NOT NULL DEFAULT ($1)")
		if strings.HasPrefix(out, "CREATE TABLE") {
			out += " DEFAULT CHARACTER SET utf8mb4 COLLATE " + s.mysqlCollation
		}
		return out
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
