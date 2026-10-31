package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// apiFixture bundles every CRUD service over the sync fixture's store, so
// tests can write through the API and read back through Pull like a device.
type apiFixture struct {
	sync   *SyncService
	apiary *ApiaryService
	hive   *HiveService
	queen  *QueenService
	insp   *InspectionService
	task   *TaskService
	treat  *TreatmentService
	harv   *HarvestService
	event  *EventService
	stats  *StatsService
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	store := newFixtureStore(t)
	s := NewSyncService(store.DB(), wsync.NewHLC("test"))
	w := NewWriter(store.DB(), wsync.NewHLC("api"))
	return &apiFixture{
		sync:   s,
		apiary: NewApiaryService(store.Apiaries(), w),
		hive:   NewHiveService(store.Hives(), w),
		queen:  NewQueenService(w),
		insp:   NewInspectionService(w),
		task:   NewTaskService(w),
		treat:  NewTreatmentService(w),
		harv:   NewHarvestService(w),
		event:  NewEventService(w),
		stats:  NewStatsService(w),
	}
}

// pullAll returns every change visible to the identity, oldest first.
func (f *apiFixture) pullAll(t *testing.T, ctx context.Context) []*wv1.Change {
	t.Helper()
	res, err := f.sync.Pull(ctx, &wv1.PullRequest{Limit: 500})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	return res.Changes
}

func changesFor(chs []*wv1.Change, entity string) []*wv1.Change {
	var out []*wv1.Change
	for _, c := range chs {
		if c.Entity == entity {
			out = append(out, c)
		}
	}
	return out
}

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != code {
		t.Fatalf("want %v, got %v", code, err)
	}
}

func TestCreateInspectionReachesDevicesThroughPull(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")

	res, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: "hive-a", WeightKg: 42.5, TempHive: 34.2, Note: "from a scale",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got := res.Inspection
	if got.HiveId != "hive-a" || got.WeightKg != 42.5 || got.TempHive != 34.2 || got.Date == nil {
		t.Fatalf("unexpected inspection: %+v", got)
	}

	chs := f.pullAll(t, ctx)
	ins := changesFor(chs, "inspection")
	if len(ins) != 1 || ins[0].EntityId != got.Id || ins[0].ScopeId != "apiary-a" {
		t.Fatalf("device must see the inspection in the apiary scope, got %+v", ins)
	}
	if !strings.Contains(ins[0].PayloadJson, `"weight_kg":42.5`) {
		t.Fatalf("payload must carry the field: %s", ins[0].PayloadJson)
	}
	evs := changesFor(chs, "event")
	if len(evs) != 1 || !strings.Contains(evs[0].PayloadJson, `"type":5`) {
		t.Fatalf("an INSPECTION event must accompany the visit, got %+v", evs)
	}
	if ins[0].AuthorId != "user-a" {
		t.Fatalf("author must be the caller, got %q", ins[0].AuthorId)
	}

	// The other tenant sees nothing.
	if n := len(f.pullAll(t, identityCtx("user-b", "org-b"))); n != 0 {
		t.Fatalf("tenant B must not see tenant A's changes, got %d", n)
	}
}

func TestCreateInspectionRejectsForeignHive(t *testing.T) {
	f := newAPIFixture(t)
	_, err := f.insp.CreateInspection(identityCtx("user-b", "org-b"),
		&wv1.CreateInspectionRequest{HiveId: "hive-a"})
	wantCode(t, err, connect.CodeNotFound)
	_, err = f.insp.CreateInspection(identityCtx("user-a", "org-a"),
		&wv1.CreateInspectionRequest{})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestListInspectionsFiltersAndPaginates(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	for i := 0; i < 3; i++ {
		d := timestamppb.New(time.Date(2026, 5, 1+i, 12, 0, 0, 0, time.UTC))
		if _, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a", Date: d}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{
		HiveId: "hive-a", Page: &wv1.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inspections) != 2 || res.Page.Total != 3 || res.Page.NextPageToken != "2" {
		t.Fatalf("page 1: %d rows, total %d, next %q", len(res.Inspections), res.Page.Total, res.Page.NextPageToken)
	}
	if res.Inspections[0].Date.AsTime().Day() != 3 {
		t.Fatalf("newest first, got day %d", res.Inspections[0].Date.AsTime().Day())
	}
	res, err = f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{
		HiveId: "hive-a", Page: &wv1.PageRequest{PageSize: 2, PageToken: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inspections) != 1 || res.Page.NextPageToken != "" {
		t.Fatalf("page 2: %d rows, next %q", len(res.Inspections), res.Page.NextPageToken)
	}
	// Other tenant: empty.
	res, _ = f.insp.ListInspections(identityCtx("user-b", "org-b"), &wv1.ListInspectionsRequest{})
	if len(res.Inspections) != 0 {
		t.Fatalf("tenant isolation broken")
	}
}

func TestInspectionPhotosUseTheORSet(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatal(err)
	}
	const photo = "data:image/jpeg;base64,AAAA"
	res, err := f.insp.AddInspectionPhoto(ctx, &wv1.AddInspectionPhotoRequest{InspectionId: cr.Inspection.Id, Photo: photo})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inspection.PhotoKeys) != 1 || res.Inspection.PhotoKeys[0] != photo {
		t.Fatalf("photo not stored: %v", res.Inspection.PhotoKeys)
	}
	_, err = f.insp.AddInspectionPhoto(ctx, &wv1.AddInspectionPhotoRequest{InspectionId: cr.Inspection.Id, Photo: "http://x"})
	wantCode(t, err, connect.CodeInvalidArgument)
	rm, err := f.insp.RemoveInspectionPhoto(ctx, &wv1.RemoveInspectionPhotoRequest{InspectionId: cr.Inspection.Id, Photo: photo})
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Inspection.PhotoKeys) != 0 {
		t.Fatalf("photo not removed: %v", rm.Inspection.PhotoKeys)
	}
}

func TestCreateQueenReplacesThePreviousOne(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q1, err := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2025})
	if err != nil {
		t.Fatal(err)
	}
	if q1.Queen.Marking != wv1.MarkingColor(markingForYear(2025)) || !q1.Queen.Active {
		t.Fatalf("queen 1: %+v", q1.Queen)
	}
	q2, err := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026, Origin: "own rearing"})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := f.queen.ListQueens(ctx, &wv1.ListQueensRequest{HiveId: "hive-a"})
	if len(all.Queens) != 2 {
		t.Fatalf("want 2 queens, got %d", len(all.Queens))
	}
	active, _ := f.queen.ListQueens(ctx, &wv1.ListQueensRequest{HiveId: "hive-a", OnlyActive: true})
	if len(active.Queens) != 1 || active.Queens[0].Id != q2.Queen.Id {
		t.Fatalf("only the new queen may be active: %+v", active.Queens)
	}
	for _, q := range all.Queens {
		if q.Id == q1.Queen.Id && (q.Active || q.ReplacedAt == nil) {
			t.Fatalf("old queen must be replaced: %+v", q)
		}
	}
	// Events: introduced, replaced, introduced.
	evs, _ := f.event.ListEvents(ctx, &wv1.ListEventsRequest{HiveId: "hive-a"})
	types := map[wv1.EventType]int{}
	for _, e := range evs.Events {
		types[e.Type]++
	}
	if types[wv1.EventType_EVENT_TYPE_QUEEN_INTRODUCED] != 2 || types[wv1.EventType_EVENT_TYPE_QUEEN_REPLACED] != 1 {
		t.Fatalf("events: %v", types)
	}
	// A visit now freezes the new queen as context.
	cr, _ := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a"})
	evs, _ = f.event.ListEvents(ctx, &wv1.ListEventsRequest{HiveId: "hive-a", Limit: 1})
	if e := evs.Events[0]; e.RefId != cr.Inspection.Id || e.QueenId != q2.Queen.Id || e.ApiaryId != "apiary-a" {
		t.Fatalf("frozen context wrong: %+v", e)
	}
	// Update and delete.
	q2.Queen.Note = "gentle"
	up, err := f.queen.UpdateQueen(ctx, &wv1.UpdateQueenRequest{Queen: q2.Queen})
	if err != nil || up.Queen.Note != "gentle" || !up.Queen.Active {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, err := f.queen.DeleteQueen(ctx, &wv1.DeleteQueenRequest{Id: q1.Queen.Id}); err != nil {
		t.Fatal(err)
	}
	all, _ = f.queen.ListQueens(ctx, &wv1.ListQueensRequest{HiveId: "hive-a"})
	if len(all.Queens) != 1 {
		t.Fatalf("soft delete failed, got %d", len(all.Queens))
	}
	_, err = f.queen.DeleteQueen(identityCtx("user-b", "org-b"), &wv1.DeleteQueenRequest{Id: q2.Queen.Id})
	wantCode(t, err, connect.CodeNotFound)
}

func TestTasksScopeAndDone(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	personal, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "order jars"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{
		Title: "varroa check", HiveId: "hive-a", DueAt: timestamppb.New(time.Now().Add(-24 * time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if shared.Task.ApiaryId != "apiary-a" {
		t.Fatalf("hive task must inherit the apiary, got %+v", shared.Task)
	}
	chs := changesFor(f.pullAll(t, ctx), "task")
	scopes := map[string]string{}
	for _, c := range chs {
		scopes[c.EntityId] = c.ScopeId
	}
	if scopes[personal.Task.Id] != "user:user-a" || scopes[shared.Task.Id] != "apiary-a" {
		t.Fatalf("scopes: %v", scopes)
	}
	_, err = f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "x", ApiaryId: "apiary-b"})
	wantCode(t, err, connect.CodeInvalidArgument)

	done, err := f.task.SetTaskDone(ctx, &wv1.SetTaskDoneRequest{Id: shared.Task.Id, Done: true})
	if err != nil || !done.Task.Done {
		t.Fatalf("done: %v %+v", err, done)
	}
	open, _ := f.task.ListTasks(ctx, &wv1.ListTasksRequest{OnlyOpen: true})
	if len(open.Tasks) != 1 || open.Tasks[0].Id != personal.Task.Id {
		t.Fatalf("open tasks: %+v", open.Tasks)
	}
	if _, err := f.task.DeleteTask(ctx, &wv1.DeleteTaskRequest{Id: personal.Task.Id}); err != nil {
		t.Fatal(err)
	}
	all, _ := f.task.ListTasks(ctx, &wv1.ListTasksRequest{})
	if len(all.Tasks) != 1 {
		t.Fatalf("after delete: %+v", all.Tasks)
	}
}

func TestTreatmentFreezesContext(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q, _ := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026})
	res, err := f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{
		HiveId: "hive-a", Product: "Oxuvar", WithdrawalUntil: timestamppb.New(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Treatment
	if tr.ApiaryId != "apiary-a" || tr.QueenId != q.Queen.Id || tr.Reason != "varroa" || tr.WithdrawalUntil == nil {
		t.Fatalf("treatment: %+v", tr)
	}
	_, err = f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{HiveId: "hive-a"})
	wantCode(t, err, connect.CodeInvalidArgument)
	list, _ := f.treat.ListTreatments(ctx, &wv1.ListTreatmentsRequest{HiveId: "hive-a"})
	if len(list.Treatments) != 1 {
		t.Fatalf("list: %+v", list.Treatments)
	}
	if _, err := f.treat.DeleteTreatment(ctx, &wv1.DeleteTreatmentRequest{Id: tr.Id}); err != nil {
		t.Fatal(err)
	}
	list, _ = f.treat.ListTreatments(ctx, &wv1.ListTreatmentsRequest{})
	if len(list.Treatments) != 0 {
		t.Fatalf("delete: %+v", list.Treatments)
	}
}

func TestHiveLifecycleThroughTheAPI(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	ap, err := f.apiary.CreateApiary(ctx, &wv1.CreateApiaryRequest{Name: "Orchard"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{ApiaryId: ap.Apiary.Id, Name: "H9"})
	if err != nil {
		t.Fatal(err)
	}
	if h.Hive.Status != wv1.HiveStatus_HIVE_STATUS_ACTIVE {
		t.Fatalf("new hive must be active: %+v", h.Hive)
	}
	chs := f.pullAll(t, ctx)
	if len(changesFor(chs, "apiary")) != 1 || len(changesFor(chs, "hive")) != 1 || len(changesFor(chs, "placement")) != 1 {
		t.Fatalf("apiary + hive + placement must be in the change log: %d changes", len(chs))
	}
	ev := changesFor(chs, "event")
	if len(ev) != 1 || !strings.Contains(ev[0].PayloadJson, `"type":1`) {
		t.Fatalf("CREATED event missing: %+v", ev)
	}
	_, err = f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{ApiaryId: "apiary-b", Name: "H"})
	wantCode(t, err, connect.CodeNotFound)

	// Relocate: placement closed + opened, hive moved, MOVED event.
	mv, err := f.hive.RelocateHive(ctx, &wv1.RelocateHiveRequest{Id: h.Hive.Id, TargetApiaryId: "apiary-a"})
	if err != nil || mv.Hive.ApiaryId != "apiary-a" {
		t.Fatalf("relocate: %v %+v", err, mv)
	}
	chs = f.pullAll(t, ctx)
	if len(changesFor(chs, "placement")) != 3 { // open, close, open
		t.Fatalf("placements after move: %d", len(changesFor(chs, "placement")))
	}
	evs, _ := f.event.ListEvents(ctx, &wv1.ListEventsRequest{HiveId: h.Hive.Id, Limit: 1})
	if evs.Events[0].Type != wv1.EventType_EVENT_TYPE_MOVED {
		t.Fatalf("latest event must be MOVED: %+v", evs.Events[0])
	}

	// Update keeps the apiary; delete is soft and reaches devices.
	h.Hive.Name = "H9b"
	h.Hive.ApiaryId = ap.Apiary.Id
	up, err := f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{Hive: h.Hive})
	if err != nil || up.Hive.Name != "H9b" || up.Hive.ApiaryId != "apiary-a" {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, err := f.hive.DeleteHive(ctx, &wv1.DeleteHiveRequest{Id: h.Hive.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = f.hive.GetHive(ctx, &wv1.GetHiveRequest{Id: h.Hive.Id})
	wantCode(t, err, connect.CodeNotFound)
	last := f.pullAll(t, ctx)
	if c := last[len(last)-1]; c.Entity != "hive" || c.Op != wv1.ChangeOp_CHANGE_OP_DELETE {
		t.Fatalf("delete must be the last change: %+v", c)
	}
	if _, err := f.apiary.DeleteApiary(ctx, &wv1.DeleteApiaryRequest{Id: ap.Apiary.Id}); err != nil {
		t.Fatal(err)
	}
	ls, _ := f.apiary.ListApiaries(ctx, &wv1.ListApiariesRequest{})
	if len(ls.Apiaries) != 1 || ls.Page.Total != 1 {
		t.Fatalf("soft-deleted apiary must vanish from list/total: %+v", ls)
	}
}

func TestDashboardAndHoneyStats(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if _, err := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026}); err != nil {
		t.Fatal(err)
	}
	old := timestamppb.New(time.Now().Add(-40 * 24 * time.Hour))
	if _, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a", Date: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "late", DueAt: timestamppb.New(time.Now().Add(-time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "soon"}); err != nil {
		t.Fatal(err)
	}
	// A harvest as a device would push it (there is no harvest RPC).
	year := time.Now().Year()
	push := &wv1.PushRequest{Changes: []*wv1.Change{
		{Entity: "event", EntityId: "ev-h1", ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, Hlc: "000000000000001:00000:dev", AuthorId: "user-a",
			PayloadJson: `{"organization_id":"org-a","scope_id":"apiary-a","type":7,"date":"` + itoa(int64(year)) + `-06-01T10:00:00.000Z","apiary_id":"apiary-a","hive_id":"hive-a","queen_id":"","amount_kg":12.5,"deleted":0}`},
		{Entity: "event", EntityId: "ev-h2", ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, Hlc: "000000000000002:00000:dev", AuthorId: "user-a",
			PayloadJson: `{"organization_id":"org-a","scope_id":"apiary-a","type":7,"date":"` + itoa(int64(year-1)) + `-07-01T10:00:00.000Z","apiary_id":"apiary-a","hive_id":"hive-a","queen_id":"","amount_kg":3,"deleted":0}`},
	}}
	if _, err := f.sync.Push(ctx, push); err != nil {
		t.Fatalf("push harvest: %v", err)
	}

	d, err := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := d
	if m.Apiaries != 1 || m.Hives != 1 || m.ActiveQueens != 1 || m.OpenTasks != 2 || m.OverdueTasks != 1 {
		t.Fatalf("counts: %+v", m)
	}
	if m.HoneyKgSeason != 12.5 || len(m.HoneyHistory) != 2 {
		t.Fatalf("honey: season %v history %+v", m.HoneyKgSeason, m.HoneyHistory)
	}
	if len(m.DueInspections) != 1 || m.DueInspections[0].HiveId != "hive-a" || m.DueInspections[0].DaysSinceLast < 39 || m.DueInspections[0].ApiaryName != "A" {
		t.Fatalf("due: %+v", m.DueInspections)
	}
	if len(m.NextTasks) != 2 || len(m.RecentInspections) != 1 {
		t.Fatalf("lists: %d tasks %d inspections", len(m.NextTasks), len(m.RecentInspections))
	}
	// A fresh visit clears the due list under the default threshold.
	if _, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a"}); err != nil {
		t.Fatal(err)
	}
	d, _ = f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if len(d.DueInspections) != 0 {
		t.Fatalf("due after visit: %+v", d.DueInspections)
	}

	hs, err := f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hs.PerYear) != 2 || len(hs.PerApiary) != 1 || hs.PerApiary[0].Label != "A" || hs.PerApiary[0].Kg != 15.5 {
		t.Fatalf("honey stats: %+v", hs)
	}
	hs, _ = f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{Year: int32(year)})
	if len(hs.PerYear) != 1 || hs.PerYear[0].Kg != 12.5 {
		t.Fatalf("honey stats %d: %+v", year, hs)
	}
	// Tenant B sees an empty dashboard.
	d, _ = f.stats.GetDashboard(identityCtx("user-b", "org-b"), &wv1.DashboardRequest{})
	if d.Hives != 0 || d.Apiaries != 1 || d.HoneyKgSeason != 0 {
		t.Fatalf("tenant B dashboard: %+v", d)
	}
}

// TestAPIWritesMergeWithDeviceWrites: a device's later edit of a field wins
// over the API's earlier value and vice versa, per field, like any two devices.
func TestAPIWritesMergeWithDeviceWrites(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "api title", HiveId: "hive-a"})
	if err != nil {
		t.Fatal(err)
	}
	// A device pushes an older-stamped title (must lose) and a newer-stamped
	// note (must win): fields merge independently.
	_, err = f.sync.Push(ctx, &wv1.PushRequest{Changes: []*wv1.Change{
		{Entity: "task", EntityId: cr.Task.Id, ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
			Hlc: "000000000000001:00000:dev", AuthorId: "user-a", PayloadJson: `{"title":"stale device title"}`},
		{Entity: "task", EntityId: cr.Task.Id, ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
			Hlc: "999999999999999:00000:dev", AuthorId: "user-a", PayloadJson: `{"note":"device note"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ls, _ := f.task.ListTasks(ctx, &wv1.ListTasksRequest{})
	if got := ls.Tasks[0]; got.Title != "api title" || got.Note != "device note" {
		t.Fatalf("per-field merge: %+v", got)
	}
}

func TestHarvestFeedsHoneyStats(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q, _ := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026})
	res, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{
		HiveId: "hive-a", AmountKg: 18.5, Variety: "spring flow", WaterContent: 17.2,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := res.Harvest
	if h.ApiaryId != "apiary-a" || h.QueenId != q.Queen.Id || h.AmountKg != 18.5 || h.Date == nil {
		t.Fatalf("harvest: %+v", h)
	}
	_, err = f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: "hive-a"})
	wantCode(t, err, connect.CodeInvalidArgument)

	// The HARVEST event carries the amount, so the stats see it.
	evs, _ := f.event.ListEvents(ctx, &wv1.ListEventsRequest{HiveId: "hive-a", Limit: 1})
	if e := evs.Events[0]; e.Type != wv1.EventType_EVENT_TYPE_HARVEST || e.AmountKg != 18.5 || e.RefId != h.Id || e.Title != "18.5 kg spring flow" {
		t.Fatalf("harvest event: %+v", e)
	}
	hs, _ := f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
	if len(hs.PerQueen) != 1 || hs.PerQueen[0].Kg != 18.5 || hs.PerQueen[0].Label != "Queen 2026" {
		t.Fatalf("per queen: %+v", hs.PerQueen)
	}
	d, _ := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if d.HoneyKgSeason != 18.5 {
		t.Fatalf("season: %v", d.HoneyKgSeason)
	}

	ls, _ := f.harv.ListHarvests(ctx, &wv1.ListHarvestsRequest{ApiaryId: "apiary-a"})
	if len(ls.Harvests) != 1 {
		t.Fatalf("list: %+v", ls.Harvests)
	}
	chs := changesFor(f.pullAll(t, ctx), "harvest")
	if len(chs) != 1 || chs[0].ScopeId != "apiary-a" {
		t.Fatalf("device must see the harvest: %+v", chs)
	}
	if _, err := f.harv.DeleteHarvest(ctx, &wv1.DeleteHarvestRequest{Id: h.Id}); err != nil {
		t.Fatal(err)
	}
	ls, _ = f.harv.ListHarvests(ctx, &wv1.ListHarvestsRequest{})
	if len(ls.Harvests) != 0 {
		t.Fatalf("delete: %+v", ls.Harvests)
	}
	_, err = f.harv.DeleteHarvest(identityCtx("user-b", "org-b"), &wv1.DeleteHarvestRequest{Id: h.Id})
	wantCode(t, err, connect.CodeNotFound)
}
