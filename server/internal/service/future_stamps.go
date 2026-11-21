package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Field stamps far ahead of real time can sit in the database from before
// Push refused them (0.4.x accepted any stamp). Such a field wins every merge
// until real time reaches its stamp: edits to it from devices and from the
// API are silently lost. The server clock no longer follows these stamps
// (HLC.Recv), so they are harmless otherwise, but the affected fields stay
// stuck. docs/ARCHITECTURE.md ("Stamps from the future") describes the
// repair.

// futureStamps is what ReportFutureStamps found in one table.
type futureStamps struct {
	fields int      // field stamps beyond the limit
	rows   []string // ids of the rows that carry them, at most maxReportedRows
}

const maxReportedRows = 10

// countFutureStamps counts, per synced table, the stored field stamps that
// lie more than MaxDrift ahead of now.
func countFutureStamps(ctx context.Context, db *sqlx.DB, now time.Time) (map[string]futureStamps, error) {
	out := map[string]futureStamps{}
	for _, spec := range entityCols {
		found, err := scanFutureStamps(ctx, db, spec.table, now)
		if err != nil {
			return nil, err
		}
		if found.fields > 0 {
			out[spec.table] = found
		}
	}
	return out, nil
}

func scanFutureStamps(ctx context.Context, db *sqlx.DB, table string, now time.Time) (futureStamps, error) {
	var found futureStamps
	rows, err := db.QueryxContext(ctx, "SELECT id, field_hlc FROM "+table)
	if err != nil {
		return found, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var clock sql.NullString
		if err := rows.Scan(&id, &clock); err != nil {
			return found, err
		}
		n := aheadStamps(wsync.ParseFieldClock(clock.String), now)
		if n > 0 {
			found.fields += n
			if len(found.rows) < maxReportedRows {
				found.rows = append(found.rows, id)
			}
		}
	}
	return found, rows.Err()
}

// aheadStamps counts the stamps of a field clock beyond the drift limit.
func aheadStamps(clock wsync.FieldClock, now time.Time) int {
	n := 0
	for _, stamp := range clock {
		if ahead, ok := wsync.AheadBy(stamp, now); ok && ahead > wsync.MaxDrift {
			n++
		}
	}
	return n
}

// ReportFutureStamps logs, once at start-up, how many stored field stamps lie
// beyond the clock drift limit, per table, and where the repair is described.
// It logs nothing when there are none.
func ReportFutureStamps(ctx context.Context, db *sqlx.DB) error {
	found, err := countFutureStamps(ctx, db, time.Now())
	if err != nil || len(found) == 0 {
		return err
	}
	tables := make([]string, 0, len(found))
	for t := range found {
		tables = append(tables, t)
	}
	slices.Sort(tables)
	parts := make([]string, 0, len(tables))
	for _, t := range tables {
		parts = append(parts, fmt.Sprintf("%s: %d fields in rows %s", t, found[t].fields, strings.Join(found[t].rows, ", ")))
	}
	log.Printf("sync: stored field stamps lie more than %s in the future, so edits to those fields are lost until then (%s)",
		wsync.MaxDrift, strings.Join(parts, "; "))
	log.Printf("sync: to repair them, see \"Stamps from the future\" in docs/ARCHITECTURE.md")
	return nil
}
