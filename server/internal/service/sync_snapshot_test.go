package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"connectrpc.com/connect/v2"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

type snapRow struct {
	fields map[string]any
	clock  map[string]string
}

// snapshotAll fetches every page and returns the rows by "entity/id", the
// cursor and the number of pages.
func snapshotAll(t *testing.T, svc *SyncService, ctx context.Context) (map[string]snapRow, string, int) {
	t.Helper()
	rows := map[string]snapRow{}
	cur, token, pages := "", "", 0
	for {
		res, err := svc.Snapshot(ctx, &wv1.SnapshotRequest{PageToken: token})
		if err != nil {
			t.Fatalf("snapshot page %d: %v", pages, err)
		}
		pages++
		if cur != "" && res.Cursor != cur {
			t.Fatalf("cursor changed between pages: %q then %q", cur, res.Cursor)
		}
		cur = res.Cursor
		if res.ServerTimeMs == 0 {
			t.Fatal("snapshot without server time")
		}
		for _, r := range res.Rows {
			key := r.Entity + "/" + r.EntityId
			if _, dup := rows[key]; dup {
				t.Fatalf("row %s delivered twice", key)
			}
			var row snapRow
			if err := json.Unmarshal([]byte(r.PayloadJson), &row.fields); err != nil {
				t.Fatalf("%s: payload %q: %v", key, r.PayloadJson, err)
			}
			if err := json.Unmarshal([]byte(r.FieldHlcJson), &row.clock); err != nil {
				t.Fatalf("%s: field clock %q: %v", key, r.FieldHlcJson, err)
			}
			rows[key] = row
		}
		if token = res.NextPageToken; token == "" {
			return rows, cur, pages
		}
		if pages > 10_000 {
			t.Fatal("snapshot never ends")
		}
	}
}

func TestSnapshotReturnsTheWorkspaceAsItIs(t *testing.T) {
	svc := newSyncFixture(t)
	a, colleague, b := identityCtx("user-a", "org-a"), identityCtx("user-a2", "org-a"), identityCtx("user-b", "org-b")
	const at = "2026-05-01T10:00:00.000Z"

	create := change("inspection", "in-1", "apiary-a",
		`{"hive_id":"hive-a","date":"`+at+`","queen_seen":1,"eggs_seen":0,"frames":9,"weight_kg":41.5,"note":"calm","temp_hive":null,"source":2,"created_at":"`+at+`","deleted":0}`)
	edit := change("inspection", "in-1", "apiary-a", `{"note":"very calm"}`)
	addP1 := change("inspection", "in-1", "apiary-a", `{"photo_keys":{"add":["p1","p2"]}}`)
	rmP1 := change("inspection", "in-1", "apiary-a", `{"photo_keys":{"remove":["p1"],"removed_tags":{"p1":["`+addP1.Hlc+`"]}}}`)
	gone := change("harvest", "ha-gone", "apiary-a", `{"hive_id":"hive-a","apiary_id":"apiary-a","date":"2026-07-11","amount_kg":3,"deleted":0}`)
	tomb := change("harvest", "ha-gone", "apiary-a", `{"deleted":1}`)
	tomb.Op = wv1.ChangeOp_CHANGE_OP_DELETE
	if err := push(svc, a, create, edit, addP1, rmP1, gone, tomb,
		change("task", "task-mine", "user:user-a", `{"title":"dentist","apiary_id":"","done":0,"created_at":"`+at+`","deleted":0}`),
		change("task", "task-shared", "apiary-a", `{"title":"varroa","apiary_id":"apiary-a","hive_id":"hive-a","due_at":"","done":1,"created_at":"`+at+`","deleted":0}`),
		change("event", "ev-mine", "user:user-a", `{"scope_id":"user:user-a","type":1,"date":"`+at+`","deleted":0}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, colleague,
		change("task", "task-theirs", "user:user-a2", `{"title":"private","apiary_id":"","done":0,"created_at":"`+at+`","deleted":0}`),
		change("event", "ev-theirs", "user:user-a2", `{"scope_id":"user:user-a2","type":1,"date":"`+at+`","deleted":0}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, b, change("hive", "hive-b", "apiary-b", `{"apiary_id":"apiary-b","name":"B1","created_at":"`+at+`","updated_at":"`+at+`"}`)); err != nil {
		t.Fatal(err)
	}

	rows, cur, pages := snapshotAll(t, svc, a)
	want := []string{"apiary/apiary-a", "hive/hive-a", "inspection/in-1", "harvest/ha-gone", "task/task-mine", "task/task-shared", "event/ev-mine"}
	if len(rows) != len(want) || pages != 1 {
		t.Fatalf("snapshot has %d rows in %d pages, want %v: %v", len(rows), pages, want, rows)
	}
	for _, key := range want {
		if _, ok := rows[key]; !ok {
			t.Fatalf("snapshot lacks %s", key)
		}
	}

	// Values in the form of a change payload, every synced column present.
	in := rows["inspection/in-1"]
	for col, wantV := range map[string]any{
		"hive_id": "hive-a", "organization_id": "org-a", "date": at, "queen_seen": 1.0, "eggs_seen": 0.0,
		"frames": 9.0, "weight_kg": 41.5, "note": "very calm", "temp_hive": nil, "source": 2.0, "deleted": 0.0,
	} {
		if got, ok := in.fields[col]; !ok || !reflect.DeepEqual(got, wantV) {
			t.Errorf("inspection.%s = %#v (present %v), want %#v", col, got, ok, wantV)
		}
	}
	if _, has := in.fields["id"]; has {
		t.Error("the payload repeats the id")
	}
	if len(in.fields) != len(entityCols["inspection"].cols)-1+1 {
		t.Errorf("inspection payload has %d keys: %v", len(in.fields), in.fields)
	}
	// Field clocks: each column carries the stamp of the change that wrote it.
	if in.clock["frames"] != create.Hlc || in.clock["note"] != edit.Hlc || in.clock["deleted"] != create.Hlc {
		t.Errorf("inspection clock: %v", in.clock)
	}
	// The set column is the whole OR-Set: p1 added and removed, p2 added.
	wantSet := map[string]any{
		"p1": map[string]any{"a": []any{addP1.Hlc}, "r": []any{addP1.Hlc}},
		"p2": map[string]any{"a": []any{addP1.Hlc}, "r": []any{}},
	}
	if !reflect.DeepEqual(in.fields["photo_keys"], wantSet) {
		t.Errorf("photo_keys = %#v", in.fields["photo_keys"])
	}

	// Tombstones are part of the snapshot.
	ha := rows["harvest/ha-gone"]
	if ha.fields["deleted"] != 1.0 || ha.clock["deleted"] != tomb.Hlc {
		t.Errorf("tombstone: %v %v", ha.fields, ha.clock)
	}
	// SQLite hands back the text the device sent; the other databases store
	// the instant and write it out in full.
	wantDate := "2026-07-11"
	if svc.db.DriverName() != "sqlite" {
		wantDate = "2026-07-11T00:00:00.000Z"
	}
	if ha.fields["date"] != wantDate {
		t.Errorf("harvest.date = %#v, want %q", ha.fields["date"], wantDate)
	}
	// A row that was never written through sync has values and no stamps.
	if hv := rows["hive/hive-a"]; hv.fields["name"] != "H1" || len(hv.clock) != 0 {
		t.Errorf("unstamped row: %v %v", hv.fields, hv.clock)
	}
	// An unset timestamp is null on the databases that store instants.
	if due := rows["task/task-shared"].fields["due_at"]; due != nil && due != "" {
		t.Errorf("task.due_at = %#v", due)
	}
	if _, leaked := rows["task/task-mine"].fields["owner_id"]; leaked {
		t.Error("owner_id is server-side and must not be in the payload")
	}

	// The colleague sees their own personal rows instead of user A's.
	theirs, _, _ := snapshotAll(t, svc, colleague)
	if _, ok := theirs["task/task-theirs"]; !ok || len(theirs) != len(want) {
		t.Fatalf("colleague's snapshot: %d rows", len(theirs))
	}
	for _, key := range []string{"task/task-mine", "event/ev-mine"} {
		if _, ok := theirs[key]; ok {
			t.Errorf("colleague's snapshot contains %s", key)
		}
	}
	other, _, _ := snapshotAll(t, svc, b)
	if len(other) != 2 { // apiary-b and hive-b
		t.Fatalf("workspace B's snapshot has %d rows", len(other))
	}

	// Pull continues exactly where the snapshot ends.
	if res := pullStatus(t, svc, a, cur); res.Status != 0 || len(res.Changes) != 0 {
		t.Fatalf("pull from the snapshot cursor: %+v", res)
	}
	if err := rename(svc, a, "after the snapshot"); err != nil {
		t.Fatal(err)
	}
	if res := pullStatus(t, svc, a, cur); len(res.Changes) != 1 || res.Changes[0].EntityId != "hive-a" {
		t.Fatalf("change after the snapshot: %+v", res)
	}
}

func TestSnapshotIsPagedBySize(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	// More rows than one query reads at a time.
	var changes []*wv1.Change
	for i := range snapshotBatch + 30 {
		changes = append(changes, change("apiary", fmt.Sprintf("ap-%03d", i), fmt.Sprintf("ap-%03d", i),
			fmt.Sprintf(`{"name":"Apiary %d","created_at":"2026-05-01T10:00:00.000Z","updated_at":"2026-05-01T10:00:00.000Z"}`, i)))
	}
	if err := push(svc, ctx, changes...); err != nil {
		t.Fatal(err)
	}
	whole, cur, pages := snapshotAll(t, svc, ctx)
	if len(whole) != len(changes)+2 || pages != 1 {
		t.Fatalf("snapshot: %d rows in %d pages", len(whole), pages)
	}

	// Tiny pages: a page closes after the row that crosses the limit.
	svc.pageBytes = 600
	first, err := svc.Snapshot(ctx, &wv1.SnapshotRequest{})
	if err != nil || first.NextPageToken == "" || len(first.Rows) == 0 || len(first.Rows) > 10 {
		t.Fatalf("first page: %d rows, token %q, %v", len(first.Rows), first.NextPageToken, err)
	}
	// A row changes while the device is between two pages. Whichever state
	// the later page carries, Pull from the snapshot's cursor delivers the
	// change again.
	if err := rename(svc, ctx, "changed between pages"); err != nil {
		t.Fatal(err)
	}
	paged, pagedCur, pages := snapshotAll(t, svc, ctx)
	if pages < 20 || len(paged) != len(whole) {
		t.Fatalf("paged snapshot: %d rows in %d pages", len(paged), pages)
	}
	for key, row := range whole {
		if key != "hive/hive-a" && !reflect.DeepEqual(paged[key], row) {
			t.Fatalf("%s differs between the paged and the whole snapshot", key)
		}
	}
	if res := pullStatus(t, svc, ctx, first.Cursor); len(res.Changes) != 1 || res.Changes[0].EntityId != "hive-a" {
		t.Fatalf("pull from the first page's cursor: %+v", res)
	}
	if first.Cursor != cur || pagedCur == cur {
		t.Fatalf("cursors: first page %q, whole %q, later snapshot %q", first.Cursor, cur, pagedCur)
	}

	// A token that is not one, or that belongs to another database, ends
	// the snapshot instead of returning a wrong page.
	st, _ := svc.state(ctx)
	foreign := snapshotToken{Cursor: cursor{epoch: st.epoch + 1, seq: 1}.String(), Entity: "hive"}.encode()
	for _, token := range []string{"nonsense", foreign, snapshotToken{Cursor: cur, Entity: "no-such-table"}.encode()} {
		if _, err := svc.Snapshot(ctx, &wv1.SnapshotRequest{PageToken: token}); connect.CodeOf(err) != connect.CodeAborted {
			t.Errorf("token %q: %v", token, err)
		}
	}
}
