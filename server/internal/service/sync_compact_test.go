package service

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// compactUpTo ages the log entries up to seq and compacts them away.
func compactUpTo(t *testing.T, svc *SyncService, seq int64) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	svc.db.MustExec(svc.db.Rebind(`UPDATE change_log SET logged_at = ? WHERE seq <= ?`), old.UnixMilli(), seq)
	if _, err := svc.Compact(context.Background(), time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatalf("compact: %v", err)
	}
	st, err := svc.state(context.Background())
	if err != nil || st.horizon != seq {
		t.Fatalf("horizon after compaction: %+v %v, want %d", st, err, seq)
	}
}

func logSeqs(t *testing.T, svc *SyncService) []int64 {
	t.Helper()
	var seqs []int64
	if err := svc.db.Select(&seqs, `SELECT seq FROM change_log ORDER BY seq`); err != nil {
		t.Fatal(err)
	}
	return seqs
}

func TestCompactionRemovesOldEntriesAndRecordsTheHorizon(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	before := time.Now().Add(-time.Second).UnixMilli()
	for i := range 6 {
		if err := rename(svc, ctx, fmt.Sprintf("n%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var stamped int
	if err := svc.db.Get(&stamped, svc.db.Rebind(`SELECT COUNT(*) FROM change_log WHERE logged_at >= ?`), before); err != nil || stamped != 6 {
		t.Fatalf("entries with a receive time: %d %v", stamped, err)
	}
	first := pullStatus(t, svc, ctx, "")
	early, _ := parseCursor(first.NextCursor)
	early.seq, early.tag = 2, ""

	// Nothing is old enough yet.
	if n, err := svc.Compact(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("compact with nothing to do: %d %v", n, err)
	}
	svc.db.MustExec(svc.db.Rebind(`UPDATE change_log SET logged_at = ? WHERE seq <= 4`), time.Now().Add(-48*time.Hour).UnixMilli())
	n, err := svc.Compact(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 4 {
		t.Fatalf("compact: %d %v", n, err)
	}
	if got := logSeqs(t, svc); len(got) != 2 || got[0] != 5 {
		t.Fatalf("log after compaction: %v", got)
	}
	st, _ := svc.state(ctx)
	if st.horizon != 4 || st.head != 6 {
		t.Fatalf("state after compaction: %+v", st)
	}
	// The rows themselves are untouched.
	if got := hiveName(t, svc, "hive-a"); got != "n5" {
		t.Fatalf("hive after compaction: %q", got)
	}

	const snapshot = wv1.PullStatus_PULL_STATUS_SNAPSHOT_REQUIRED
	if got := pullStatus(t, svc, ctx, early.String()); got.Status != snapshot {
		t.Fatalf("cursor behind the horizon: status %v", got.Status)
	}
	if got := pullStatus(t, svc, ctx, ""); got.Status != snapshot {
		t.Fatalf("no cursor with a compacted log: status %v", got.Status)
	}
	if got := pullStatus(t, svc, ctx, first.NextCursor); got.Status != 0 {
		t.Fatalf("up-to-date device: status %v", got.Status)
	}
	// A snapshot leads back to a cursor Pull accepts.
	snap, err := svc.Snapshot(ctx, &wv1.SnapshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pullStatus(t, svc, ctx, snap.Cursor); got.Status != 0 || len(got.Changes) != 0 {
		t.Fatalf("pull from the snapshot cursor: %+v", got)
	}

	// The horizon never moves back, and a second run is a no-op.
	if n, err := svc.Compact(ctx, time.Now().Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
	if st, _ := svc.state(ctx); st.horizon != 4 {
		t.Fatalf("horizon moved: %+v", st)
	}
}

func TestCompactDailyRunsOnceAndStopsWithItsContext(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := rename(svc, ctx, "old"); err != nil {
		t.Fatal(err)
	}
	if err := rename(svc, ctx, "recent"); err != nil {
		t.Fatal(err)
	}
	svc.db.MustExec(`UPDATE change_log SET logged_at = 1000 WHERE seq = 1`)

	// 0 days keeps everything and does not even start.
	svc.CompactDaily(context.Background(), 0)
	if got := logSeqs(t, svc); len(got) != 2 {
		t.Fatalf("retention 0 must keep the log: %v", got)
	}

	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.CompactDaily(run, 180); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for len(logSeqs(t, svc)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first run did not happen")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CompactDaily did not stop")
	}
}

// TestQuietWorkspaceStaysAheadOfTheHorizon: a device whose workspace has no
// news still moves its cursor to the end of the log, so the log is compacted
// behind a device that syncs regularly and never past it.
func TestQuietWorkspaceStaysAheadOfTheHorizon(t *testing.T) {
	svc := newSyncFixture(t)
	a, b := identityCtx("user-a", "org-a"), identityCtx("user-b", "org-b")
	if err := rename(svc, a, "mine"); err != nil {
		t.Fatal(err)
	}
	cur := pullStatus(t, svc, a, "").NextCursor
	for i := range 3 {
		if err := push(svc, b, change("apiary", "apiary-b", "apiary-b", fmt.Sprintf(`{"name":"B%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	res := pullStatus(t, svc, a, cur)
	if c, _ := parseCursor(res.NextCursor); len(res.Changes) != 0 || c.seq != 4 || c.tag == "" {
		t.Fatalf("idle pull: %d changes, cursor %q", len(res.Changes), res.NextCursor)
	}
	compactUpTo(t, svc, 3)
	if got := pullStatus(t, svc, a, res.NextCursor); got.Status != 0 {
		t.Fatalf("a device that kept syncing is behind the horizon: %v", got.Status)
	}
	// The entry the cursor names can go away (compaction, a deleted
	// workspace) without that being a restore.
	compactUpTo(t, svc, 4)
	if got := pullStatus(t, svc, a, res.NextCursor); got.Status != 0 {
		t.Fatalf("cursor at the horizon: %v", got.Status)
	}
}

// TestLegacyClientBehindTheHorizonIsToldToUpdate: once the log no longer
// reaches back to a 0.4.x app's cursor, it gets a failure, not a gap.
func TestLegacyClientBehindTheHorizonIsToldToUpdate(t *testing.T) {
	svc := newSyncFixture(t)
	writer, ctx := identityCtx("user-a", "org-a"), legacyCtx("user-a", "org-a")
	for i := range 4 {
		if err := push(svc, writer, change("hive", "hive-a", "apiary-a", fmt.Sprintf(`{"name":"n%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	compactUpTo(t, svc, 3)

	for _, cur := range []string{"", "1", "2"} {
		_, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: cur})
		wantUpgradeRequired(t, err, protocolSnapshots)
	}
	res, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: "3"})
	if err != nil || len(res.Changes) != 1 || res.NextCursor != "4" {
		t.Fatalf("pull from the horizon: %+v %v", res, err)
	}
	// Uploading still works: what the device recorded is not held hostage.
	if err := push(svc, ctx, change("hive", "hive-a", "apiary-a", `{"note":"from the old app"}`)); err != nil {
		t.Fatalf("push from a legacy client behind the horizon: %v", err)
	}
}

// TestSnapshotAfterCompactionMatchesTheLog: a device that bootstraps from a
// snapshot plus the remaining log has what a device that read the whole log
// has, per column and stamp.
func TestSnapshotAfterCompactionMatchesTheLog(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	writeLikeACurrentApp(t, svc, ctx)
	before, _, _ := snapshotAll(t, svc, ctx)
	st, _ := svc.state(ctx)
	svc.db.MustExec(`UPDATE change_log SET logged_at = 1000`)
	if _, err := svc.Compact(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := logSeqs(t, svc); len(got) != 0 {
		t.Fatalf("log after compacting everything: %v", got)
	}
	after, cur, _ := snapshotAll(t, svc, ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("compaction changed what a snapshot returns")
	}
	if c, _ := parseCursor(cur); c.seq != st.head || c.tag != "" {
		t.Fatalf("cursor of a snapshot over an empty log: %q", cur)
	}
	if res := pullStatus(t, svc, ctx, cur); res.Status != 0 {
		t.Fatalf("pull from it: %v", res.Status)
	}
}
