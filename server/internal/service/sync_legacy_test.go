package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// The tests in this file are about apps of release 0.4.x that are still
// installed: what the server sends them must be storable by that release,
// and what they send must still be accepted.

// pastStamp is a fixed stamp well in the past, so the payloads these tests
// produce are the same on every run.
func pastStamp(n int) string { return wsync.Format(1767225600000+int64(n), 0, "new") }

// sampleValue is a value of the column's kind that every backend stores.
func sampleValue(col string, kind colKind) any {
	switch col {
	case "organization_id":
		return "org-a"
	case "apiary_id", "scope_id":
		return "apiary-a"
	case "hive_id":
		return "hive-a"
	case "deleted":
		return 0
	}
	switch kind {
	case kindBool:
		return 1
	case kindInt:
		return 3
	case kindFloat:
		return 1.5
	case kindTime:
		return "2026-01-02T03:04:05.000Z"
	}
	return "s-" + col
}

// everyColumn builds an upsert that writes every synced column of the
// entity. It is derived from the server's whitelist, so a column or entity
// added later takes part in these tests without anyone listing it here.
func everyColumn(t *testing.T, entity, id string, n int) *wv1.Change {
	t.Helper()
	spec := entityCols[entity]
	fields := map[string]any{}
	for col, kind := range kindsOf(entity) {
		if col != "id" {
			fields[col] = sampleValue(col, kind)
		}
	}
	for _, col := range spec.setCols {
		fields[col] = map[string]any{"add": []string{"k1", "k2"}}
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	scope := "apiary-a"
	if entity == "apiary" {
		scope = id // a new apiary opens its own scope
	}
	return &wv1.Change{
		Entity: entity, EntityId: id, ScopeId: scope, Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: string(payload), Hlc: pastStamp(n),
	}
}

// newerColumns lists the synced columns of an entity that release 0.4.1
// does not have.
func newerColumns(entity string) []string {
	var out []string
	spec := entityCols[entity]
	for _, col := range append(append([]string{}, spec.cols...), spec.setCols...) {
		if col != "id" && !legacyColSet[entity][col] {
			out = append(out, col)
		}
	}
	sort.Strings(out)
	return out
}

// writeLikeACurrentApp pushes, for every synced entity, a full row, an
// update that mixes old and newer columns, an update of newer columns only,
// a set removal and a delete.
func writeLikeACurrentApp(t *testing.T, svc *SyncService, ctx context.Context) {
	t.Helper()
	n := 0
	next := func() int { n++; return n }
	for _, entity := range snapshotEntities() {
		spec := entityCols[entity]
		id := entity + "-1"
		changes := []*wv1.Change{everyColumn(t, entity, id, next()), everyColumn(t, entity, entity+"-2", next())}
		scope := changes[0].ScopeId
		upsert := func(fields map[string]any) {
			payload, _ := json.Marshal(fields)
			changes = append(changes, &wv1.Change{
				Entity: entity, EntityId: id, ScopeId: scope, Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
				PayloadJson: string(payload), Hlc: pastStamp(next()),
			})
		}
		kinds := kindsOf(entity)
		if newer := newerColumns(entity); len(newer) > 0 {
			mixed, only := map[string]any{"deleted": 0}, map[string]any{}
			for _, col := range newer {
				if !isSetCol(spec, col) {
					mixed[col], only[col] = sampleValue(col, kinds[col]), sampleValue(col, kinds[col])
				}
			}
			upsert(mixed)
			if len(only) > 0 {
				upsert(only)
			}
		}
		for _, col := range spec.setCols {
			upsert(map[string]any{col: map[string]any{
				"remove": []string{"k1"}, "removed_tags": map[string][]string{"k1": {changes[0].Hlc}},
			}})
		}
		changes = append(changes, &wv1.Change{
			Entity: entity, EntityId: entity + "-2", ScopeId: changes[1].ScopeId,
			Op: wv1.ChangeOp_CHANGE_OP_DELETE, PayloadJson: `{"deleted":1}`, Hlc: pastStamp(next()),
		})
		res := pushRes(t, svc, ctx, changes...)
		if len(res.Rejected) > 0 {
			t.Fatalf("%s: sample change %d refused: %s (%s). Give sampleValue a value this entity accepts.",
				entity, res.Rejected[0].Index, res.Rejected[0].Code, res.Rejected[0].Reason)
		}
	}
}

// writeThroughTheAPI exercises the CRUD services, which write columns
// release 0.4.1 does not know (inspection.source, amended_at).
func writeThroughTheAPI(t *testing.T, f *apiFixture, ctx context.Context) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ap, err := f.apiary.CreateApiary(ctx, &wv1.CreateApiaryRequest{Name: "Orchard"})
	must(err)
	h, err := f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{ApiaryId: ap.Apiary.Id, Name: "H9"})
	must(err)
	_, err = f.hive.RelocateHive(ctx, &wv1.RelocateHiveRequest{Id: h.Hive.Id, TargetApiaryId: "apiary-a"})
	must(err)
	_, err = f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: h.Hive.Id, Year: 2025})
	must(err)
	_, err = f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: h.Hive.Id, Year: 2026})
	must(err)
	in, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: h.Hive.Id, WeightKg: proto.Float64(41), Source: wv1.InspectionSource_INSPECTION_SOURCE_SENSOR,
	})
	must(err)
	_, err = f.insp.UpdateInspection(ctx, &wv1.UpdateInspectionRequest{
		Inspection: &wv1.Inspection{Id: in.Inspection.Id, Frames: proto.Int32(8)},
	})
	must(err)
	tr, err := f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{HiveId: h.Hive.Id, Product: "Oxuvar"})
	must(err)
	_, err = f.treat.UpdateTreatment(ctx, &wv1.UpdateTreatmentRequest{
		Treatment: &wv1.Treatment{Id: tr.Treatment.Id, Dose: "50 ml"},
	})
	must(err)
	ha, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: h.Hive.Id, AmountKg: 18.5})
	must(err)
	_, err = f.harv.UpdateHarvest(ctx, &wv1.UpdateHarvestRequest{Harvest: &wv1.Harvest{Id: ha.Harvest.Id, AmountKg: 12}})
	must(err)
	mine, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "order jars"})
	must(err)
	_, err = f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "varroa check", HiveId: h.Hive.Id})
	must(err)
	_, err = f.task.SetTaskDone(ctx, &wv1.SetTaskDoneRequest{Id: mine.Task.Id, Done: true})
	must(err)
	_, err = f.harv.DeleteHarvest(ctx, &wv1.DeleteHarvestRequest{Id: ha.Harvest.Id})
	must(err)
	_, err = f.hive.DeleteHive(ctx, &wv1.DeleteHiveRequest{Id: h.Hive.Id})
	must(err)
}

// pullLog reads the whole log for ctx in small pages, the way an app does.
func pullLog(t *testing.T, svc *SyncService, ctx context.Context) []*wv1.Change {
	t.Helper()
	var out []*wv1.Change
	cur := ""
	for range 10_000 {
		res, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: cur, Limit: 7})
		if err != nil {
			t.Fatalf("pull from %q: %v", cur, err)
		}
		if res.Status != wv1.PullStatus_PULL_STATUS_UNSPECIFIED {
			t.Fatalf("pull from %q: status %v", cur, res.Status)
		}
		out = append(out, res.Changes...)
		if cur = res.NextCursor; !res.HasMore {
			return out
		}
	}
	t.Fatal("pull never ends")
	return nil
}

func payloadKeys(t *testing.T, ch *wv1.Change) []string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ch.PayloadJson), &fields); err != nil {
		t.Fatalf("%s %s: payload is not a JSON object: %q", ch.Entity, ch.EntityId, ch.PayloadJson)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestLegacyPullStaysInsideTheFrozenSet: whatever current apps and the API
// write, a 0.4.x app is sent only tables and columns it has.
func TestLegacyPullStaysInsideTheFrozenSet(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	writeLikeACurrentApp(t, f.sync, ctx)
	writeThroughTheAPI(t, f, ctx)

	// The check has teeth only if there is something to hold back.
	full := pullLog(t, f.sync, ctx)
	newer := map[string]bool{}
	for _, ch := range full {
		for _, key := range payloadKeys(t, ch) {
			if !legacyColSet[ch.Entity][key] {
				newer[ch.Entity+"."+key] = true
			}
		}
	}
	for _, want := range []string{"inspection.source", "treatment.amended_at", "harvest.amended_at"} {
		if !newer[want] {
			t.Fatalf("a current app must receive %s; newer columns seen: %v", want, newer)
		}
	}

	legacy := pullLog(t, f.sync, legacyCtx("user-a", "org-a"))
	if len(legacy) == 0 || len(legacy) >= len(full) {
		t.Fatalf("legacy pull returned %d of %d changes; updates of newer columns only must be left out", len(legacy), len(full))
	}
	seen := map[string]bool{}
	for _, ch := range legacy {
		known, ok := legacyColSet[ch.Entity]
		if !ok {
			t.Errorf("entity %q is unknown to release 0.4.1", ch.Entity)
			continue
		}
		seen[ch.Entity] = true
		keys := payloadKeys(t, ch)
		if len(keys) == 0 {
			t.Errorf("%s %s: empty payload", ch.Entity, ch.EntityId)
		}
		for _, key := range keys {
			if !known[key] {
				t.Errorf("%s.%s is unknown to release 0.4.1 (payload %s)", ch.Entity, key, ch.PayloadJson)
			}
		}
	}
	for entity := range legacyCols {
		if !seen[entity] {
			t.Errorf("no %s change reached the legacy client", entity)
		}
	}
}

// TestLegacyColsAreFrozen pins the list itself. It describes installed
// software; if this test fails, the list was edited, and the edit is wrong
// unless release 0.4.1 really had the column.
func TestLegacyColsAreFrozen(t *testing.T) {
	var lines []string
	for entity, cols := range legacyCols {
		sorted := slices.Clone(cols)
		sort.Strings(sorted)
		lines = append(lines, entity+":"+strings.Join(sorted, ","))
		for _, col := range cols {
			if _, synced := kindsOf(entity)[col]; !synced && !isSetCol(entityCols[entity], col) {
				t.Errorf("%s.%s is no longer a synced column", entity, col)
			}
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	const want = "7a1e14de2d86e94e9e8207635485be3f10573cb00d7ebe1c2ee531fa600fd83e"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("legacyCols changed (fingerprint %s). It lists what release 0.4.1 can store and must not change.", got)
	}
}

// goldenChange is one pulled change as the app's tests read it.
type goldenChange struct {
	Entity      string `json:"entity"`
	EntityID    string `json:"entityId"`
	ScopeID     string `json:"scopeId"`
	Op          int32  `json:"op"`
	PayloadJSON string `json:"payloadJson"`
	HLC         string `json:"hlc"`
}

const legacyGolden = "testdata/legacy_pull_v0.4.1.json"

// TestLegacyPullGolden records what a 0.4.x app is sent for the writes of a
// current app. app/src/lib/local/legacy.test.ts applies the recorded changes
// with the pull code of release 0.4.1. The file is the same on every
// backend; UPDATE_GOLDEN=1 rewrites it.
func TestLegacyPullGolden(t *testing.T) {
	svc := newSyncFixture(t)
	writeLikeACurrentApp(t, svc, identityCtx("user-a", "org-a"))
	var got []goldenChange
	for _, ch := range pullLog(t, svc, legacyCtx("user-a", "org-a")) {
		got = append(got, goldenChange{ch.Entity, ch.EntityId, ch.ScopeId, int32(ch.Op), ch.PayloadJson, ch.Hlc})
	}
	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(legacyGolden, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(legacyGolden)
	if err != nil {
		t.Fatalf("%v (run the test once with UPDATE_GOLDEN=1)", err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("%s is out of date: what a legacy client receives has changed. If that is intended and still "+
			"within release 0.4.1's schema, run this test with UPDATE_GOLDEN=1 and then the app tests.", legacyGolden)
	}
}

// TestLegacyClientPushesAreAccepted sends what release 0.4.1 writes into its
// outbox (app/src/lib/local/repo.ts and history.ts at tag v0.4.1): full rows
// on create, single fields on edit, 0/1 for booleans, "" for unset
// timestamps, {"deleted":1} with a delete, personal tasks in "user:<id>".
func TestLegacyClientPushesAreAccepted(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := legacyCtx("user-a", "org-a")
	const now = "2026-11-01T09:30:00.000Z"
	up := func(entity, id, scope, payload string) *wv1.Change { return change(entity, id, scope, payload) }
	del := func(entity, id, scope string) *wv1.Change {
		c := change(entity, id, scope, `{"deleted":1}`)
		c.Op = wv1.ChangeOp_CHANGE_OP_DELETE
		return c
	}
	event := func(id string, typ int, extra string) *wv1.Change {
		return up("event", id, "ap-1", fmt.Sprintf(`{"organization_id":"org-a","scope_id":"ap-1","type":%d,"date":%q,
			"apiary_id":"ap-1","hive_id":"hv-1","queen_id":"","ref_entity":"","ref_id":"","title":"","amount_kg":0,
			"detail":"{}","author_id":"user-a","deleted":0%s}`, typ, now, extra))
	}
	changes := []*wv1.Change{
		// apiaries.create / rename / update
		up("apiary", "ap-1", "ap-1", `{"organization_id":"org-a","name":"Orchard","address":"","lat":0,"lng":0,"note":"",
			"created_at":"`+now+`","updated_at":"`+now+`","deleted":0}`),
		up("apiary", "ap-1", "ap-1", `{"name":"Orchard meadow"}`),
		up("apiary", "ap-1", "ap-1", `{"lat":48.2,"lng":16.37,"address":"Hauptstr. 1","updated_at":"`+now+`"}`),
		// history.createHive: hive + placement + CREATED event
		up("hive", "hv-1", "ap-1", `{"organization_id":"org-a","apiary_id":"ap-1","name":"H1","type":1,"status":1,"boxes":2,
			"colony_origin":"","note":"","created_at":"`+now+`","updated_at":"`+now+`","deleted":0}`),
		up("placement", "pl-1", "ap-1", `{"organization_id":"org-a","hive_id":"hv-1","apiary_id":"ap-1","start_at":"`+now+`","end_at":null,"deleted":0}`),
		event("ev-1", 1, `,"title":"H1"`),
		up("hive", "hv-1", "ap-1", `{"photo":"data:image/jpeg;base64,AAAA"}`),
		up("hive", "hv-1", "ap-1", `{"qr_code":"ab12cd"}`),
		// history.setQueen: retire the old queen, introduce the new one
		up("queen", "qu-1", "ap-1", `{"organization_id":"org-a","hive_id":"hv-1","year":2025,"marking":5,"origin":"","breeder_number":"",
			"note":"","introduced_at":"`+now+`","replaced_at":null,"active":1,"created_at":"`+now+`","updated_at":"`+now+`","deleted":0}`),
		up("queen", "qu-1", "ap-1", `{"active":0,"replaced_at":"`+now+`"}`),
		event("ev-2", 3, `,"queen_id":"qu-1"`),
		// history.recordInspection and the photo set
		up("inspection", "in-1", "ap-1", `{"organization_id":"org-a","hive_id":"hv-1","date":"`+now+`","weather":"sunny","queen_seen":1,
			"eggs_seen":0,"temperament":2,"frames":9,"stores":2,"queen_cells":0,"varroa":"","honey_kg":0,"note":"calm",
			"brood_frames":4,"calmness":3,"fed_kg":1.5,"frames_added":1,"frames_removed":0,"drone_frame_cut":1,"super_added":0,
			"weight_kg":41.2,"youngest_larva":1,"covered_larva":1,"temp_hive":34.5,"temp_outside":18,"humidity_hive":60,
			"humidity_outside":55,"created_at":"`+now+`","deleted":0}`),
		event("ev-3", 5, `,"ref_entity":"inspection","ref_id":"in-1","title":"Inspection"`),
		up("inspection", "in-1", "ap-1", `{"photo_keys":{"add":["data:image/jpeg;base64,BBBB"]}}`),
		// history.recordHarvest / recordTreatment
		up("harvest", "ha-1", "ap-1", `{"organization_id":"org-a","apiary_id":"ap-1","hive_id":"hv-1","queen_id":"qu-1","date":"`+now+`",
			"variety":"spring","amount_kg":12.5,"water_content":17.2,"batch_number":"L1","best_before":"","note":"","deleted":0}`),
		event("ev-4", 7, `,"ref_entity":"harvest","ref_id":"ha-1","amount_kg":12.5,"title":"12.5 kg spring"`),
		up("treatment", "tr-1", "ap-1", `{"organization_id":"org-a","apiary_id":"ap-1","hive_id":"hv-1","queen_id":"qu-1","date":"`+now+`",
			"product":"Oxuvar","active_ingredient":"oxalic acid","dose":"50 ml","method":"trickle","batch_number":"OX-1",
			"withdrawal_until":"","reason":"varroa","note":"","deleted":0}`),
		// tasks: personal scope without an apiary, the apiary's scope with one
		up("task", "ta-1", "user:user-a", `{"organization_id":"org-a","title":"order jars","apiary_id":"","hive_id":"","due_at":"",
			"done":0,"priority":0,"created_at":"`+now+`","deleted":0}`),
		up("task", "ta-1", "user:user-a", `{"done":1}`),
		up("task", "ta-2", "ap-1", `{"organization_id":"org-a","title":"varroa check","apiary_id":"ap-1","hive_id":"hv-1",
			"due_at":"2026-11-05","done":0,"priority":1,"created_at":"`+now+`","deleted":0}`),
		// remove()
		del("task", "ta-2", "ap-1"),
		del("harvest", "ha-1", "ap-1"),
	}
	// setRemove names the add tags the device observed.
	add := changes[13].Hlc
	changes = append(changes, up("inspection", "in-1", "ap-1",
		`{"photo_keys":{"remove":["data:image/jpeg;base64,BBBB"],"removed_tags":{"data:image/jpeg;base64,BBBB":["`+add+`"]}}}`))

	res, err := svc.Push(ctx, &wv1.PushRequest{Changes: changes})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	for _, r := range res.Rejected {
		t.Errorf("change %d (%s) refused: %s: %s", r.Index, changes[r.Index].Entity, r.Code, r.Reason)
	}
	// The response is one a 0.4.1 app handles: it would store a non-empty
	// server_cursor as its pull position.
	if res.ServerCursor != "" {
		t.Fatalf("server_cursor = %q", res.ServerCursor)
	}
	// And everything comes back to it, in a form it can store.
	back := pullLog(t, svc, ctx)
	if len(back) != len(changes) {
		t.Fatalf("pulled %d of %d changes", len(back), len(changes))
	}
}

// TestLegacyCursorIsAPlainSequenceNumber: a 0.4.x app keeps the cursor it
// always had and pages through the log with it.
func TestLegacyCursorIsAPlainSequenceNumber(t *testing.T) {
	svc := newSyncFixture(t)
	writer, ctx := identityCtx("user-a", "org-a"), legacyCtx("user-a", "org-a")
	for i := range 5 {
		if err := push(svc, writer, change("hive", "hive-a", "apiary-a", fmt.Sprintf(`{"name":"n%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.Pull(ctx, &wv1.PullRequest{Limit: 2})
	if err != nil || len(first.Changes) != 2 || !first.HasMore || first.NextCursor != "2" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	rest, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: "2", Limit: 500})
	if err != nil || len(rest.Changes) != 3 || rest.HasMore || rest.NextCursor != "5" {
		t.Fatalf("second page: %+v %v", rest, err)
	}
	idle, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: "5"})
	if err != nil || len(idle.Changes) != 0 || idle.NextCursor != "5" || idle.Status != wv1.PullStatus_PULL_STATUS_UNSPECIFIED {
		t.Fatalf("idle pull: %+v %v", idle, err)
	}

	// A change that touches only columns the app does not have is left out,
	// and the cursor still moves past it.
	if err := push(svc, writer, change("inspection", "in-1", "apiary-a", `{"hive_id":"hive-a","date":"2026-05-01T10:00:00.000Z","created_at":"2026-05-01T10:00:00.000Z"}`),
		change("inspection", "in-1", "apiary-a", `{"source":2}`)); err != nil {
		t.Fatal(err)
	}
	next, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: "5"})
	if err != nil || len(next.Changes) != 1 || next.NextCursor != "7" || strings.Contains(next.Changes[0].PayloadJson, "source") {
		t.Fatalf("pull past a newer-column change: %+v %v", next, err)
	}

	// A position beyond the end of the log (the database was restored from
	// an older backup) starts over instead of waiting for the log to catch up.
	again, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: "900", Limit: 500})
	if err != nil || len(again.Changes) != 6 || again.NextCursor != "7" {
		t.Fatalf("pull beyond the log: %+v %v", again, err)
	}
}

func wantUpgradeRequired(t *testing.T, err error, minProtocol int32) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("want an upgrade-required failure, got %v", err)
	}
	if got := upgradeDetail(err); got == nil || got.MinProtocol != minProtocol {
		t.Fatalf("want an UpgradeRequired detail with min_protocol %d, got %v (%v)", minProtocol, got, err)
	}
}
