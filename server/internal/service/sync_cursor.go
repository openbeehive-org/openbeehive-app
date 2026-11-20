package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// A cursor says how far a device has read the change log. For clients that
// announce a protocol version it has three parts, "<epoch>:<seq>:<mark>":
//
//	epoch  the random identity of the database (sync_state), so a cursor from
//	       another database is recognised;
//	seq    the sequence number the device has read up to;
//	mark   the tag of the last server start before that position (sync_boot;
//	       0 when there was none).
//
// The mark is what catches a restored backup. A restored database has the
// same epoch as before and hands out the sequence numbers after the backup a
// second time, to different changes; a device that had read further than the
// backup would skip them. But the server has to be started on the restored
// database, and that start leaves a mark at the position the backup ends
// at. For every position after it, the mark is now a different one than any
// cursor issued before the restore carries.
//
// Legacy (0.4.x) clients keep the bare sequence number they always had.
type cursor struct {
	epoch, seq, mark int64
}

func (c cursor) String() string { return fmt.Sprintf("%d:%d:%d", c.epoch, c.seq, c.mark) }

func parseCursor(s string) (cursor, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return cursor{}, false
	}
	var c cursor
	for i, dst := range []*int64{&c.epoch, &c.seq, &c.mark} {
		n, err := strconv.ParseInt(parts[i], 10, 64)
		if err != nil || n < 0 {
			return cursor{}, false
		}
		*dst = n
	}
	return c, c.epoch > 0
}

// RecordBoot marks the start of the server at the current end of the change
// log (see cursor). Call it once per process, after the migrations and
// before anything writes: a change logged before the mark would move the
// mark past positions that devices may hold from before a restore.
func RecordBoot(ctx context.Context, db *sqlx.DB) error {
	var tag [8]byte
	if _, err := rand.Read(tag[:]); err != nil {
		return err
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var head int64
	if err := tx.GetContext(ctx, &head, `SELECT val FROM seq_counter WHERE name = 'change'`); err != nil {
		return err
	}
	// A second start at the same position replaces the mark: that is the
	// restore of a backup taken right after a start.
	if _, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM sync_boot WHERE head = ?`), head); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO sync_boot (head, tag) VALUES (?, ?)`),
		head, int64(binary.BigEndian.Uint64(tag[:])>>1)|1); err != nil {
		return err
	}
	return tx.Commit()
}

// markBefore returns the tag of the last server start at a position before
// seq, or 0 when there is none.
func (s *SyncService) markBefore(ctx context.Context, seq int64) (int64, error) {
	var tag int64
	err := s.db.GetContext(ctx, &tag, s.db.Rebind(
		`SELECT tag FROM sync_boot WHERE head < ? ORDER BY head DESC LIMIT 1`), seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return tag, err
}

// cursorSeq is the sequence number of a cursor in either form (0 for none).
func cursorSeq(c string) int64 {
	if full, ok := parseCursor(c); ok {
		return full.seq
	}
	return cursorToSeq(c)
}

// syncState is what the server knows about its change log as a whole.
type syncState struct {
	epoch   int64 // identity of this database
	horizon int64 // entries up to this sequence number have been compacted away
	head    int64 // the last sequence number handed out
}

func (s *SyncService) state(ctx context.Context) (syncState, error) {
	rows, err := s.db.QueryxContext(ctx, `SELECT name, val FROM sync_state
		UNION ALL SELECT name, val FROM seq_counter WHERE name = 'change'`)
	if err != nil {
		return syncState{}, err
	}
	defer func() { _ = rows.Close() }()
	var st syncState
	for rows.Next() {
		var name string
		var val int64
		if err := rows.Scan(&name, &val); err != nil {
			return syncState{}, err
		}
		switch name {
		case "epoch":
			st.epoch = val
		case "horizon":
			st.horizon = val
		case "change":
			st.head = val
		}
	}
	if err := rows.Err(); err != nil {
		return syncState{}, err
	}
	if st.epoch == 0 {
		return syncState{}, errors.New("sync_state has no epoch: migration 0019 did not complete")
	}
	return st, nil
}

// checkCursor decides whether the log can be read on from the cursor of a
// client that announced a protocol version. It returns the sequence number
// to read after, or the status that tells the client what to do instead.
func (s *SyncService) checkCursor(ctx context.Context, st syncState, raw string) (int64, wv1.PullStatus, error) {
	const (
		ok       = wv1.PullStatus_PULL_STATUS_UNSPECIFIED
		snapshot = wv1.PullStatus_PULL_STATUS_SNAPSHOT_REQUIRED
		reset    = wv1.PullStatus_PULL_STATUS_RESET_REQUIRED
	)
	if raw == "" {
		// No cursor: from the start, if the start is still there.
		if st.horizon > 0 {
			return 0, snapshot, nil
		}
		return 0, ok, nil
	}
	c, valid := parseCursor(raw)
	if !valid {
		// A bare sequence number is the cursor of a server before protocol
		// 1: the same history, but nothing to check it against. The current
		// rows bring such a device up to date. Anything else is not a cursor
		// this server ever issued.
		if _, err := strconv.ParseUint(raw, 10, 63); err == nil {
			return 0, snapshot, nil
		}
		return 0, reset, nil
	}
	if c.epoch != st.epoch || c.seq > st.head {
		return 0, reset, nil
	}
	// Before the horizon check: a device from before a restore has to send
	// its rows again however long it stayed away.
	mark, err := s.markBefore(ctx, c.seq)
	if err != nil {
		return 0, ok, err
	}
	if mark != c.mark {
		return 0, reset, nil
	}
	if c.seq < st.horizon {
		return 0, snapshot, nil
	}
	return c.seq, ok, nil
}

// cursorAt builds the cursor for a position in the current log.
func (s *SyncService) cursorAt(ctx context.Context, st syncState, seq int64) (string, error) {
	mark, err := s.markBefore(ctx, seq)
	if err != nil {
		return "", err
	}
	return cursor{epoch: st.epoch, seq: seq, mark: mark}.String(), nil
}
