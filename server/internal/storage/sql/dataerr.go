package sqlstore

import (
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

// SQLite primary result codes for errors caused by the data of a statement.
const (
	sqliteTooBig     = 18
	sqliteConstraint = 19
	sqliteMismatch   = 20
)

// mysqlDataErrors are the server errors MySQL and MariaDB raise for a value
// that does not fit its column.
var mysqlDataErrors = map[uint16]bool{
	1048: true, // column cannot be null
	1062: true, // duplicate key
	1138: true, // invalid use of NULL
	1264: true, // out of range
	1265: true, // data truncated
	1292: true, // incorrect value (e.g. a date)
	1364: true, // NOT NULL column without a default was not given a value
	1366: true, // incorrect value for the column type
	1367: true, // illegal value found during parsing
	1406: true, // data too long
	3819: true, // check constraint (MySQL)
	4025: true, // check constraint (MariaDB)
}

// IsDataError reports whether err was caused by the values of the statement
// (wrong type, too long, constraint violated) rather than by the state of
// the database or the connection. Running the same statement again fails the
// same way, so callers must not retry it.
func IsDataError(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// Class 22: data exception. Class 23: integrity constraint violation.
		return len(pg.Code) == 5 && (pg.Code[:2] == "22" || pg.Code[:2] == "23")
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		return mysqlDataErrors[my.Number]
	}
	var lite *sqlite.Error
	if errors.As(err, &lite) {
		switch lite.Code() & 0xff {
		case sqliteTooBig, sqliteConstraint, sqliteMismatch:
			return true
		}
	}
	return false
}
