// Package sqltest hands every test its own empty database on the backend
// under test.
//
// The backend is chosen by one pair of environment variables, read by every
// test package that touches the database:
//
//	BEEHIVE_TEST_DATABASE_DRIVER   sqlite (default) | postgres | mysql
//	BEEHIVE_TEST_DATABASE_DSN      DSN of an existing, otherwise unused database
//
// SQLite tests get a file in the test's temp dir. On Postgres each test gets
// a schema of its own inside the given database; on the MySQL family each
// test gets a database of its own, named after the given one plus a random
// suffix (the test user therefore needs the right to create and drop
// databases). Both are dropped when the test ends, so packages can run in
// parallel and the suite can run any number of times against one server.
package sqltest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

const (
	EnvDriver = "BEEHIVE_TEST_DATABASE_DRIVER"
	EnvDSN    = "BEEHIVE_TEST_DATABASE_DSN"
)

// Selected returns the backend under test. The DSN is empty for SQLite.
func Selected() (config.DBDriver, string) {
	driver := config.DBDriver(os.Getenv(EnvDriver))
	if driver == "" || driver == config.DriverSQLite {
		return config.DriverSQLite, ""
	}
	return driver, os.Getenv(EnvDSN)
}

// New returns the driver under test and the DSN of an empty database that
// belongs to this test alone.
func New(t testing.TB) (config.DBDriver, string) {
	t.Helper()
	driver, dsn := Selected()
	return driver, NewOn(t, driver, dsn)
}

// NewOn is New for an explicitly named backend.
func NewOn(t testing.TB, driver config.DBDriver, dsn string) string {
	t.Helper()
	switch driver {
	case config.DriverSQLite:
		return "file:" + filepath.Join(t.TempDir(), "test.db")
	case config.DriverPostgres:
		return newPostgresSchema(t, dsn)
	case config.DriverMySQL:
		return newMySQLDatabase(t, dsn)
	}
	t.Fatalf("%s: unknown driver %q", EnvDriver, driver)
	return ""
}

func scratchName(t testing.TB) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random name: %v", err)
	}
	return "t" + hex.EncodeToString(b)
}

// newPostgresSchema creates a schema and returns a DSN whose connections
// have it as their only search path.
func newPostgresSchema(t testing.TB, dsn string) string {
	t.Helper()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("%s: %v", EnvDSN, err)
	}
	schema := scratchName(t)
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	// URL form or keyword/value form; search_path is a run-time parameter in both.
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema
}

// newMySQLDatabase creates a database next to the one in the DSN and returns
// a DSN that points at it.
func newMySQLDatabase(t testing.TB, dsn string) string {
	t.Helper()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("%s: %v", EnvDSN, err)
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	name := cfg.DBName + "_" + scratchName(t)
	if _, err := admin.Exec("CREATE DATABASE `" + name + "`"); err != nil {
		_ = admin.Close()
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE `" + name + "`")
		_ = admin.Close()
	})
	cfg.DBName = name
	return cfg.FormatDSN()
}
