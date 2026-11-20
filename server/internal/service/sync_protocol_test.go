package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

// upgradeDetail returns the UpgradeRequired detail of an error, or nil.
func upgradeDetail(err error) *wv1.UpgradeRequired {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	for _, d := range ce.Details() {
		if msg, derr := connectproto.UnmarshalErrorDetail(d); derr == nil {
			if up, ok := msg.(*wv1.UpgradeRequired); ok {
				return up
			}
		}
	}
	return nil
}

// TestMinimumProtocolRefusesOlderApps: with a minimum configured, every sync
// call of an older app fails in a way the app can recognise; nothing of it
// is applied.
func TestMinimumProtocolRefusesOlderApps(t *testing.T) {
	svc := newSyncFixture(t)
	old, current := legacyCtx("user-a", "org-a"), identityCtx("user-a", "org-a")

	// The default admits apps that announce nothing.
	if err := push(svc, old, change("hive", "hive-a", "apiary-a", `{"name":"from 0.4"}`)); err != nil {
		t.Fatalf("default minimum must accept legacy apps: %v", err)
	}

	svc.MinProtocol = protocolSnapshots
	_, err := svc.Pull(old, &wv1.PullRequest{})
	wantUpgradeRequired(t, err, protocolSnapshots)
	_, err = svc.Push(old, &wv1.PushRequest{Changes: []*wv1.Change{change("hive", "hive-a", "apiary-a", `{"name":"refused"}`)}})
	wantUpgradeRequired(t, err, protocolSnapshots)
	_, err = svc.Snapshot(old, &wv1.SnapshotRequest{})
	wantUpgradeRequired(t, err, protocolSnapshots)
	err = svc.Subscribe(old, &wv1.SubscribeRequest{}, openbeehivev1connect.SyncServiceSubscribeServerStream{})
	wantUpgradeRequired(t, err, protocolSnapshots)
	if got := hiveName(t, svc, "hive-a"); got != "from 0.4" {
		t.Fatalf("a refused push was applied: hive is %q", got)
	}

	if _, err := svc.Pull(current, &wv1.PullRequest{}); err != nil {
		t.Fatalf("current app: %v", err)
	}
	svc.MinProtocol = protocolSnapshots + 1
	_, err = svc.Pull(current, &wv1.PullRequest{})
	wantUpgradeRequired(t, err, protocolSnapshots+1)
}

// TestProtocolIsAnnouncedInARequestHeader goes through a real HTTP server:
// the header decides what Pull returns, and the upgrade failure reaches the
// client with its detail.
func TestProtocolIsAnnouncedInARequestHeader(t *testing.T) {
	svc := newSyncFixture(t)
	asUserA := func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(auth.WithIdentity(ctx, auth.Identity{UserID: "user-a", OrgID: "org-a"}), spec, stream)
		}
	}
	server := connect.NewServer(asUserA)
	openbeehivev1connect.RegisterSyncServiceHandler(server, svc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := openbeehivev1connect.NewSyncServiceClient(connect.NewClient(connecthttp.NewTransport(ts.Client(), ts.URL)))
	with := func(headers ...string) context.Context {
		ctx, info := connect.NewClientContext(t.Context())
		for i := 0; i < len(headers); i += 2 {
			info.RequestHeader().Set(headers[i], headers[i+1])
		}
		return ctx
	}

	if err := push(svc, identityCtx("user-a", "org-a"),
		change("inspection", "in-1", "apiary-a", `{"hive_id":"hive-a","date":"2026-05-01T10:00:00.000Z","created_at":"2026-05-01T10:00:00.000Z","source":2}`)); err != nil {
		t.Fatal(err)
	}

	res, err := client.Pull(with(HeaderSyncProtocol, "1", HeaderAppVersion, "0.5.0"), &wv1.PullRequest{})
	if err != nil || len(res.Changes) != 1 || !strings.Contains(res.Changes[0].PayloadJson, `"source":2`) {
		t.Fatalf("with the header: %+v %v", res, err)
	}
	if _, ok := parseCursor(res.NextCursor); !ok {
		t.Fatalf("a current app gets a cursor bound to the database, got %q", res.NextCursor)
	}
	if snap, err := client.Snapshot(with(HeaderSyncProtocol, "1"), &wv1.SnapshotRequest{}); err != nil || len(snap.Rows) == 0 {
		t.Fatalf("snapshot with the header: %+v %v", snap, err)
	}

	// No header, or one that is not a version: a 0.4.x app.
	for _, ctx := range []context.Context{with(), with(HeaderSyncProtocol, "latest"), with(HeaderSyncProtocol, "-1")} {
		res, err = client.Pull(ctx, &wv1.PullRequest{})
		if err != nil || len(res.Changes) != 1 || strings.Contains(res.Changes[0].PayloadJson, "source") || res.NextCursor != "1" {
			t.Fatalf("without the header: %+v %v", res, err)
		}
	}
	_, err = client.Snapshot(with(), &wv1.SnapshotRequest{})
	wantUpgradeRequired(t, err, protocolSnapshots)

	svc.MinProtocol = 2
	_, err = client.Pull(with(HeaderSyncProtocol, "1"), &wv1.PullRequest{})
	wantUpgradeRequired(t, err, 2)
	if !strings.Contains(err.Error(), "Update the app") {
		t.Fatalf("the message must say what to do: %v", err)
	}
}

// --- cursors and the epoch ---

// restoreLogTo makes the database look like a backup taken when seq was the
// last log entry: later entries and server starts are gone and the sequence
// numbers are free again. The server is not started on it yet (restart).
func restoreLogTo(t *testing.T, svc *SyncService, seq int64) {
	t.Helper()
	svc.db.MustExec(svc.db.Rebind(`DELETE FROM change_log WHERE seq > ?`), seq)
	svc.db.MustExec(svc.db.Rebind(`DELETE FROM sync_boot WHERE head > ?`), seq)
	svc.db.MustExec(svc.db.Rebind(`UPDATE seq_counter SET val = ? WHERE name = 'change'`), seq)
}

// restart is the server starting on the database as it is now.
func restart(t *testing.T, svc *SyncService) {
	t.Helper()
	if err := RecordBoot(context.Background(), svc.db); err != nil {
		t.Fatalf("record boot: %v", err)
	}
}

func pullStatus(t *testing.T, svc *SyncService, ctx context.Context, cur string) *wv1.PullResponse {
	t.Helper()
	res, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: cur, Limit: 500})
	if err != nil {
		t.Fatalf("pull from %q: %v", cur, err)
	}
	if res.Status != wv1.PullStatus_PULL_STATUS_UNSPECIFIED && (len(res.Changes) > 0 || res.HasMore) {
		t.Fatalf("pull from %q: status %v came with changes", cur, res.Status)
	}
	return res
}

func rename(svc *SyncService, ctx context.Context, name string) error {
	return push(svc, ctx, change("hive", "hive-a", "apiary-a", fmt.Sprintf(`{"name":%q}`, name)))
}

func TestCursorCarriesTheEpochOfTheDatabase(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	for _, name := range []string{"one", "two", "three"} {
		if err := rename(svc, ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	st, err := svc.state(ctx)
	if err != nil || st.epoch <= 0 || st.head != 3 || st.horizon != 0 {
		t.Fatalf("state: %+v %v", st, err)
	}
	res := pullStatus(t, svc, ctx, "")
	cur, ok := parseCursor(res.NextCursor)
	if !ok || cur.epoch != st.epoch || cur.seq != 3 || cur.mark == 0 || len(res.Changes) != 3 {
		t.Fatalf("cursor %q, %d changes", res.NextCursor, len(res.Changes))
	}
	if idle := pullStatus(t, svc, ctx, res.NextCursor); idle.Status != 0 || idle.NextCursor != res.NextCursor {
		t.Fatalf("idle pull: %+v", idle)
	}

	const reset = wv1.PullStatus_PULL_STATUS_RESET_REQUIRED
	other := cur
	other.epoch++
	beyond := cur
	beyond.seq = 99
	unmarked := cur
	unmarked.mark = 0
	for name, c := range map[string]string{
		"another database": other.String(), "beyond the log": beyond.String(), "another start": unmarked.String(),
		"garbage": "x:y:z", "negative": fmt.Sprintf("%d:-1:0", st.epoch), "two parts": "3:4",
	} {
		if got := pullStatus(t, svc, ctx, c); got.Status != reset {
			t.Errorf("%s (%q): status %v, want a reset", name, c, got.Status)
		}
	}
	// A bare number is what a server before protocol 1 handed out: the same
	// history, so the current rows are enough and nothing is sent again.
	if got := pullStatus(t, svc, ctx, "3"); got.Status != wv1.PullStatus_PULL_STATUS_SNAPSHOT_REQUIRED {
		t.Errorf("bare cursor: status %v, want the snapshot signal", got.Status)
	}

	// Another database has another epoch, so its cursors are refused here.
	twin := newSyncFixture(t)
	if tst, err := twin.state(ctx); err != nil || tst.epoch == st.epoch {
		t.Fatalf("two databases share epoch %d (%v)", tst.epoch, err)
	}
	if got := pullStatus(t, twin, ctx, res.NextCursor); got.Status != reset {
		t.Fatalf("cursor of another database: status %v", got.Status)
	}
}

// Subscribe only needs the position of a cursor, whichever form it has.
func TestCursorSeqReadsBothForms(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "17": 17, "4711:17:99": 17, "4711:17:0": 17, "junk": 0, "1:2": 0} {
		if got := cursorSeq(in); got != want {
			t.Errorf("cursorSeq(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestRestoredBackupIsDetectedByTheCursor: after a restore the server hands
// out sequence numbers a second time. A device that had read further than
// the backup must not continue from its old position, whether it comes back
// before or after the log has grown past it, and whatever has happened to
// the entries in between.
func TestRestoredBackupIsDetectedByTheCursor(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := rename(svc, ctx, "in the backup"); err != nil {
		t.Fatal(err)
	}
	behind := pullStatus(t, svc, ctx, "").NextCursor // a device that stops here
	for _, name := range []string{"lost 1", "lost 2"} {
		if err := rename(svc, ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	ahead := pullStatus(t, svc, ctx, behind).NextCursor // a device that saw the lost changes

	restoreLogTo(t, svc, 1)
	restart(t, svc)
	const reset = wv1.PullStatus_PULL_STATUS_RESET_REQUIRED
	if got := pullStatus(t, svc, ctx, ahead); got.Status != reset {
		t.Fatalf("ahead of the restored log: status %v", got.Status)
	}
	// Others keep working and the log grows past the old position.
	for _, name := range []string{"new 1", "new 2", "new 3"} {
		if err := rename(svc, ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if got := pullStatus(t, svc, ctx, ahead); got.Status != reset {
		t.Fatalf("position re-used by other changes: status %v", got.Status)
	}
	// The device that had not read past the backup simply continues.
	res := pullStatus(t, svc, ctx, behind)
	if res.Status != 0 || len(res.Changes) != 3 {
		t.Fatalf("device behind the backup: %+v", res)
	}
	// The entries at the re-used positions disappear again (a workspace is
	// deleted, the demo is rebuilt, the log is compacted): still a reset.
	svc.db.MustExec(`DELETE FROM change_log`)
	if got := pullStatus(t, svc, ctx, ahead); got.Status != reset {
		t.Fatalf("after the re-used entries were removed: status %v", got.Status)
	}
	svc.db.MustExec(svc.db.Rebind(`UPDATE sync_state SET val = ? WHERE name = 'horizon'`), 4)
	if got := pullStatus(t, svc, ctx, ahead); got.Status != reset {
		t.Fatalf("behind the horizon as well: status %v, a reset must win", got.Status)
	}
}

// TestRestartsDoNotResetDevices: an ordinary restart leaves every cursor
// valid, also the one that sits exactly where the server started, and a
// restart at the same position twice.
func TestRestartsDoNotResetDevices(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	var cursors []string
	cur := ""
	for i := range 4 {
		if err := rename(svc, ctx, fmt.Sprintf("n%d", i)); err != nil {
			t.Fatal(err)
		}
		cur = pullStatus(t, svc, ctx, cur).NextCursor
		cursors = append(cursors, cur)
		restart(t, svc)
		if i == 2 {
			restart(t, svc)
		}
	}
	for i, c := range cursors {
		if got := pullStatus(t, svc, ctx, c); got.Status != 0 || len(got.Changes) != 3-i {
			t.Errorf("cursor %d (%q): status %v, %d changes", i, c, got.Status, len(got.Changes))
		}
	}
	var marks int
	if err := svc.db.Get(&marks, `SELECT COUNT(*) FROM sync_boot`); err != nil || marks != 5 {
		t.Fatalf("one mark per position the server started at: %d %v", marks, err)
	}
}

// A restore to exactly the position of an earlier start: the new start
// replaces that mark, so devices that had read on are still found out.
func TestRestoreToTheLastStartIsDetected(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := rename(svc, ctx, "one"); err != nil {
		t.Fatal(err)
	}
	restart(t, svc) // a start at position 1; the backup is taken right after
	atBackup := pullStatus(t, svc, ctx, "").NextCursor
	if err := rename(svc, ctx, "lost"); err != nil {
		t.Fatal(err)
	}
	ahead := pullStatus(t, svc, ctx, atBackup).NextCursor

	restoreLogTo(t, svc, 1)
	restart(t, svc)
	if err := rename(svc, ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if got := pullStatus(t, svc, ctx, ahead); got.Status != wv1.PullStatus_PULL_STATUS_RESET_REQUIRED {
		t.Fatalf("ahead of a backup taken at a start: status %v", got.Status)
	}
	if got := pullStatus(t, svc, ctx, atBackup); got.Status != 0 || len(got.Changes) != 1 {
		t.Fatalf("device at the backup: %+v", got)
	}
}

// TestResentRowsRecreateWhatTheServerLost: after a reset an app sends every
// local row again, one change per stamp (app/src/lib/local/sync.ts,
// rowChanges). The server has columns that must not be empty, so a row it
// no longer has can only be created by a change that carries all of them:
// the oldest change of a row brings every column, the later ones set the
// columns they stamped. The row and its field clock end up as on the device.
func TestResentRowsRecreateWhatTheServerLost(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	const made, edited = "2026-10-01T08:00:00.000Z", "2026-10-02T09:00:00.000Z"
	t1, t2, t3 := pastStamp(1), pastStamp(2), pastStamp(3)
	at := func(c *wv1.Change, stamp string) *wv1.Change { c.Hlc = stamp; return c }

	// An apiary created at t1 and renamed at t2, a hive created in it at t2
	// and renamed at t3. On the device, name and updated_at carry the later
	// stamp.
	res := pushRes(t, svc, ctx,
		at(change("apiary", "ap-lost", "ap-lost", `{"organization_id":"org-a","name":"Renamed","address":"","lat":0,"lng":0,
			"note":"","created_at":"`+made+`","updated_at":"`+edited+`","deleted":0}`), t1),
		at(change("apiary", "ap-lost", "ap-lost", `{"name":"Renamed","updated_at":"`+edited+`"}`), t2),
		at(change("hive", "hv-lost", "ap-lost", `{"organization_id":"org-a","apiary_id":"ap-lost","name":"H2","type":1,
			"status":1,"boxes":2,"created_at":"`+edited+`","updated_at":"`+edited+`","deleted":0}`), t2),
		at(change("hive", "hv-lost", "ap-lost", `{"name":"H2","updated_at":"`+edited+`"}`), t3),
	)
	wantRejected(t, res, map[int32]string{})

	rows, _, _ := snapshotAll(t, svc, ctx)
	ap, hv := rows["apiary/ap-lost"], rows["hive/hv-lost"]
	if ap.fields["name"] != "Renamed" || ap.clock["name"] != t2 || ap.clock["updated_at"] != t2 || ap.clock["created_at"] != t1 {
		t.Fatalf("apiary: %v %v", ap.fields, ap.clock)
	}
	if hv.fields["name"] != "H2" || hv.clock["name"] != t3 || hv.clock["apiary_id"] != t2 {
		t.Fatalf("hive: %v %v", hv.fields, hv.clock)
	}

	// The same changes on a server that still has the rows change nothing.
	before := rows
	res = pushRes(t, svc, ctx,
		at(change("apiary", "ap-lost", "ap-lost", `{"organization_id":"org-a","name":"Renamed","created_at":"`+made+`","updated_at":"`+edited+`","deleted":0}`), t1),
		at(change("apiary", "ap-lost", "ap-lost", `{"name":"Renamed","updated_at":"`+edited+`"}`), t2),
	)
	wantRejected(t, res, map[int32]string{})
	if after, _, _ := snapshotAll(t, svc, ctx); !reflect.DeepEqual(before, after) {
		t.Fatal("re-sent changes altered rows the server already had")
	}

	// Without the full first change the row cannot be created: this is what
	// the app must not send.
	res = pushRes(t, svc, ctx, at(change("apiary", "ap-partial", "ap-partial", `{"organization_id":"org-a","created_at":"`+made+`","deleted":0}`), t1))
	wantRejected(t, res, map[int32]string{0: rejInvalid})
}
