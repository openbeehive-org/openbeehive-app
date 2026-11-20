package service

import (
	"context"
	"database/sql"
	"log"
	"time"
)

const (
	compactEvery = 24 * time.Hour
	// compactSlice is how many sequence numbers one DELETE covers, so a
	// large first run does not hold one long lock on the change log.
	compactSlice = 5000
)

// Compact deletes the change-log entries the server received before cutoff
// and returns how many it removed. The rows themselves stay: a device whose
// cursor is older than the deleted entries is told to load a snapshot.
//
// The horizon is stored before the first entry is deleted. Pull reads it
// again after it has read its page, so a page with entries missing in the
// middle is never handed out as complete.
func (s *SyncService) Compact(ctx context.Context, cutoff time.Time) (int64, error) {
	var upTo sql.NullInt64
	if err := s.db.GetContext(ctx, &upTo, s.db.Rebind(
		`SELECT MAX(seq) FROM change_log WHERE logged_at < ?`), cutoff.UnixMilli()); err != nil {
		return 0, err
	}
	if !upTo.Valid {
		return 0, nil
	}
	if _, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE sync_state SET val = ? WHERE name = 'horizon' AND val < ?`), upTo.Int64, upTo.Int64); err != nil {
		return 0, err
	}
	var from sql.NullInt64
	if err := s.db.GetContext(ctx, &from, `SELECT MIN(seq) FROM change_log`); err != nil || !from.Valid {
		return 0, err
	}
	var deleted int64
	for lo := from.Int64; lo <= upTo.Int64; lo += compactSlice {
		res, err := s.db.ExecContext(ctx, s.db.Rebind(
			`DELETE FROM change_log WHERE seq >= ? AND seq < ? AND seq <= ?`), lo, lo+compactSlice, upTo.Int64)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

// CompactDaily runs Compact now and then once a day until ctx ends, keeping
// retentionDays of change log. 0 keeps everything and returns at once. It
// assumes it is the only compactor: run one server instance per database.
func (s *SyncService) CompactDaily(ctx context.Context, retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	ticker := time.NewTicker(compactEvery)
	defer ticker.Stop()
	for {
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		if n, err := s.Compact(ctx, cutoff); err != nil {
			log.Printf("sync: compacting the change log failed (retried tomorrow): %v", err)
		} else if n > 0 {
			log.Printf("sync: removed %d change-log entries older than %d days", n, retentionDays)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
