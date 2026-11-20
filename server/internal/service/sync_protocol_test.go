package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// restoreLogTo makes the change log look like a backup taken when seq was
// its last entry: later entries are gone and their numbers are free again.
func restoreLogTo(t *testing.T, svc *SyncService, seq int64) {
	t.Helper()
	svc.db.MustExec(svc.db.Rebind(`DELETE FROM change_log WHERE seq > ?`), seq)
	svc.db.MustExec(svc.db.Rebind(`UPDATE seq_counter SET val = ? WHERE name = 'change'`), seq)
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
	if !ok || cur.epoch != st.epoch || cur.seq != 3 || cur.tag == "" || len(res.Changes) != 3 {
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
	for name, c := range map[string]string{
		"another database": other.String(), "beyond the log": beyond.String(),
		"a bare number": "3", "garbage": "x:y:z", "negative": fmt.Sprintf("%d:-1:", st.epoch),
	} {
		if got := pullStatus(t, svc, ctx, c); got.Status != reset {
			t.Errorf("%s (%q): status %v, want a reset", name, c, got.Status)
		}
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

// TestRestoredBackupIsDetectedByTheCursor: after a restore the server hands
// out sequence numbers a second time. A device that had read further than
// the backup must not continue from its old position, whether it comes back
// before or after the log has grown past it.
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
}
