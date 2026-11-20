package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// A cursor says how far a device has read the change log. For clients that
// announce a protocol version it has three parts, "<epoch>:<seq>:<tag>":
//
//	epoch  the random identity of the database (sync_state), so a cursor from
//	       another database is recognised;
//	seq    the sequence number the device has read up to;
//	tag    a fingerprint of the log entry stored under seq when the cursor
//	       was issued ("" when there was none).
//
// The tag is what catches a restored backup. A restored database has the
// same epoch as before and hands out the sequence numbers after the backup a
// second time, to different changes. A device that had read further than the
// backup would then skip them. Its cursor either lies beyond the restored
// log, or names an entry whose fingerprint is no longer the one it saw.
//
// Legacy (0.4.x) clients keep the bare sequence number they always had.
type cursor struct {
	epoch, seq int64
	tag        string
}

func (c cursor) String() string { return fmt.Sprintf("%d:%d:%s", c.epoch, c.seq, c.tag) }

func parseCursor(s string) (cursor, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return cursor{}, false
	}
	epoch, err1 := strconv.ParseInt(parts[0], 10, 64)
	seq, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || epoch <= 0 || seq < 0 {
		return cursor{}, false
	}
	return cursor{epoch: epoch, seq: seq, tag: parts[2]}, true
}

// entryTag is the fingerprint of a log entry: a short hash of its random id.
func entryTag(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return fmt.Sprintf("%08x", h.Sum32())
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

// tagAt returns the fingerprint of the log entry stored under seq, or ""
// when there is none (compacted, removed with its workspace, or seq 0).
func (s *SyncService) tagAt(ctx context.Context, seq int64) (string, error) {
	if seq <= 0 {
		return "", nil
	}
	var id string
	err := s.db.GetContext(ctx, &id, s.db.Rebind(`SELECT id FROM change_log WHERE seq = ?`), seq)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return entryTag(id), nil
}

// checkCursor decides whether the log can be read on from the cursor of a
// client that announced a protocol version. It returns the sequence number
// to read after, or the status that tells the client what to do instead.
func (s *SyncService) checkCursor(ctx context.Context, st syncState, raw string) (int64, wv1.PullStatus, error) {
	const ok = wv1.PullStatus_PULL_STATUS_UNSPECIFIED
	if raw == "" {
		// No cursor: from the start, if the start is still there.
		if st.horizon > 0 {
			return 0, wv1.PullStatus_PULL_STATUS_SNAPSHOT_REQUIRED, nil
		}
		return 0, ok, nil
	}
	c, valid := parseCursor(raw)
	if !valid || c.epoch != st.epoch || c.seq > st.head {
		return 0, wv1.PullStatus_PULL_STATUS_RESET_REQUIRED, nil
	}
	if c.seq < st.horizon {
		return 0, wv1.PullStatus_PULL_STATUS_SNAPSHOT_REQUIRED, nil
	}
	// An entry that is gone proves nothing (compaction and deleted
	// workspaces remove entries); one that is there must be the one the
	// device saw.
	tag, err := s.tagAt(ctx, c.seq)
	if err != nil {
		return 0, ok, err
	}
	if tag != "" && tag != c.tag {
		return 0, wv1.PullStatus_PULL_STATUS_RESET_REQUIRED, nil
	}
	return c.seq, ok, nil
}

// cursorAt builds the cursor for a position in the current log.
func (s *SyncService) cursorAt(ctx context.Context, st syncState, seq int64) (string, error) {
	tag, err := s.tagAt(ctx, seq)
	if err != nil {
		return "", err
	}
	return cursor{epoch: st.epoch, seq: seq, tag: tag}.String(), nil
}
