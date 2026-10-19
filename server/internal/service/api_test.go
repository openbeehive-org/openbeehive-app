package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
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
	res, err := f.sync.Pull(ctx, connect.NewRequest(&wv1.PullRequest{Limit: 500}))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	return res.Msg.Changes
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

	res, err := f.insp.CreateInspection(ctx, connect.NewRequest(&wv1.CreateInspectionRequest{
		HiveId: "hive-a", WeightKg: 42.5, TempHive: 34.2, Note: "from a scale",
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got := res.Msg.Inspection
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
		connect.NewRequest(&wv1.CreateInspectionRequest{HiveId: "hive-a"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = f.insp.CreateInspection(identityCtx("user-a", "org-a"),
		connect.NewRequest(&wv1.CreateInspectionRequest{}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestListInspectionsFiltersAndPaginates(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	for i := 0; i < 3; i++ {
		d := timestamppb.New(time.Date(2026, 5, 1+i, 12, 0, 0, 0, time.UTC))
		if _, err := f.insp.CreateInspection(ctx, connect.NewRequest(&wv1.CreateInspectionRequest{HiveId: "hive-a", Date: d})); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.insp.ListInspections(ctx, connect.NewRequest(&wv1.ListInspectionsRequest{
		HiveId: "hive-a", Page: &wv1.PageRequest{PageSize: 2},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Inspections) != 2 || res.Msg.Page.Total != 3 || res.Msg.Page.NextPageToken != "2" {
		t.Fatalf("page 1: %d rows, total %d, next %q", len(res.Msg.Inspections), res.Msg.Page.Total, res.Msg.Page.NextPageToken)
	}
	if res.Msg.Inspections[0].Date.AsTime().Day() != 3 {
		t.Fatalf("newest first, got day %d", res.Msg.Inspections[0].Date.AsTime().Day())
	}
	res, err = f.insp.ListInspections(ctx, connect.NewRequest(&wv1.ListInspectionsRequest{
		HiveId: "hive-a", Page: &wv1.PageRequest{PageSize: 2, PageToken: "2"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Inspections) != 1 || res.Msg.Page.NextPageToken != "" {
		t.Fatalf("page 2: %d rows, next %q", len(res.Msg.Inspections), res.Msg.Page.NextPageToken)
	}
	// Other tenant: empty.
	res, _ = f.insp.ListInspections(identityCtx("user-b", "org-b"), connect.NewRequest(&wv1.ListInspectionsRequest{}))
	if len(res.Msg.Inspections) != 0 {
		t.Fatalf("tenant isolation broken")
	}
}

func TestInspectionPhotosUseTheORSet(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.insp.CreateInspection(ctx, connect.NewRequest(&wv1.CreateInspectionRequest{HiveId: "hive-a"}))
	if err != nil {
		t.Fatal(err)
	}
	const photo = "data:image/jpeg;base64,AAAA"
	res, err := f.insp.AddInspectionPhoto(ctx, connect.NewRequest(&wv1.AddInspectionPhotoRequest{InspectionId: cr.Msg.Inspection.Id, Photo: photo}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Inspection.PhotoKeys) != 1 || res.Msg.Inspection.PhotoKeys[0] != photo {
		t.Fatalf("photo not stored: %v", res.Msg.Inspection.PhotoKeys)
	}
	_, err = f.insp.AddInspectionPhoto(ctx, connect.NewRequest(&wv1.AddInspectionPhotoRequest{InspectionId: cr.Msg.Inspection.Id, Photo: "http://x"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	rm, err := f.insp.RemoveInspectionPhoto(ctx, connect.NewRequest(&wv1.RemoveInspectionPhotoRequest{InspectionId: cr.Msg.Inspection.Id, Photo: photo}))
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Msg.Inspection.PhotoKeys) != 0 {
		t.Fatalf("photo not removed: %v", rm.Msg.Inspection.PhotoKeys)
	}
}

func TestCreateQueenReplacesThePreviousOne(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q1, err := f.queen.CreateQueen(ctx, connect.NewRequest(&wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2025}))
	if err != nil {
		t.Fatal(err)
	}
	if q1.Msg.Queen.Marking != wv1.MarkingColor(markingForYear(2025)) || !q1.Msg.Queen.Active {
		t.Fatalf("queen 1: %+v", q1.Msg.Queen)
	}
	q2, err := f.queen.CreateQueen(ctx, connect.NewRequest(&wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026, Origin: "own rearing"}))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := f.queen.ListQueens(ctx, connect.NewRequest(&wv1.ListQueensRequest{HiveId: "hive-a"}))
	if len(all.Msg.Queens) != 2 {
		t.Fatalf("want 2 queens, got %d", len(all.Msg.Queens))
	}
	active, _ := f.queen.ListQueens(ctx, connect.NewRequest(&wv1.ListQueensRequest{HiveId: "hive-a", OnlyActive: true}))
	if len(active.Msg.Queens) != 1 || active.Msg.Queens[0].Id != q2.Msg.Queen.Id {
		t.Fatalf("only the new queen may be active: %+v", active.Msg.Queens)
	}
	for _, q := range all.Msg.Queens {
		if q.Id == q1.Msg.Queen.Id && (q.Active || q.ReplacedAt == nil) {
			t.Fatalf("old queen must be replaced: %+v", q)
		}
	}
	// Events: introduced, replaced, introduced.
	evs, _ := f.event.ListEvents(ctx, connect.NewRequest(&wv1.ListEventsRequest{HiveId: "hive-a"}))
	types := map[wv1.EventType]int{}
	for _, e := range evs.Msg.Events {
		types[e.Type]++
	}
	if types[wv1.EventType_EVENT_TYPE_QUEEN_INTRODUCED] != 2 || types[wv1.EventType_EVENT_TYPE_QUEEN_REPLACED] != 1 {
		t.Fatalf("events: %v", types)
	}
	// A visit now freezes the new queen as context.
	cr, _ := f.insp.CreateInspection(ctx, connect.NewRequest(&wv1.CreateInspectionRequest{HiveId: "hive-a"}))
	evs, _ = f.event.ListEvents(ctx, connect.NewRequest(&wv1.ListEventsRequest{HiveId: "hive-a", Limit: 1}))
	if e := evs.Msg.Events[0]; e.RefId != cr.Msg.Inspection.Id || e.QueenId != q2.Msg.Queen.Id || e.ApiaryId != "apiary-a" {
		t.Fatalf("frozen context wrong: %+v", e)
	}
	// Update and delete.
	q2.Msg.Queen.Note = "gentle"
	up, err := f.queen.UpdateQueen(ctx, connect.NewRequest(&wv1.UpdateQueenRequest{Queen: q2.Msg.Queen}))
	if err != nil || up.Msg.Queen.Note != "gentle" || !up.Msg.Queen.Active {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, err := f.queen.DeleteQueen(ctx, connect.NewRequest(&wv1.DeleteQueenRequest{Id: q1.Msg.Queen.Id})); err != nil {
		t.Fatal(err)
	}
	all, _ = f.queen.ListQueens(ctx, connect.NewRequest(&wv1.ListQueensRequest{HiveId: "hive-a"}))
	if len(all.Msg.Queens) != 1 {
		t.Fatalf("soft delete failed, got %d", len(all.Msg.Queens))
	}
	_, err = f.queen.DeleteQueen(identityCtx("user-b", "org-b"), connect.NewRequest(&wv1.DeleteQueenRequest{Id: q2.Msg.Queen.Id}))
	wantCode(t, err, connect.CodeNotFound)
}

func TestTasksScopeAndDone(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	personal, err := f.task.CreateTask(ctx, connect.NewRequest(&wv1.CreateTaskRequest{Title: "order jars"}))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := f.task.CreateTask(ctx, connect.NewRequest(&wv1.CreateTaskRequest{
		Title: "varroa check", HiveId: "hive-a", DueAt: timestamppb.New(time.Now().Add(-24 * time.Hour)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if shared.Msg.Task.ApiaryId != "apiary-a" {
		t.Fatalf("hive task must inherit the apiary, got %+v", shared.Msg.Task)
	}
	chs := changesFor(f.pullAll(t, ctx), "task")
	scopes := map[string]string{}
	for _, c := range chs {
		scopes[c.EntityId] = c.ScopeId
	}
	if scopes[personal.Msg.Task.Id] != "user:user-a" || scopes[shared.Msg.Task.Id] != "apiary-a" {
		t.Fatalf("scopes: %v", scopes)
	}
	_, err = f.task.CreateTask(ctx, connect.NewRequest(&wv1.CreateTaskRequest{Title: "x", ApiaryId: "apiary-b"}))
	wantCode(t, err, connect.CodeInvalidArgument)

	done, err := f.task.SetTaskDone(ctx, connect.NewRequest(&wv1.SetTaskDoneRequest{Id: shared.Msg.Task.Id, Done: true}))
	if err != nil || !done.Msg.Task.Done {
		t.Fatalf("done: %v %+v", err, done)
	}
	open, _ := f.task.ListTasks(ctx, connect.NewRequest(&wv1.ListTasksRequest{OnlyOpen: true}))
	if len(open.Msg.Tasks) != 1 || open.Msg.Tasks[0].Id != personal.Msg.Task.Id {
		t.Fatalf("open tasks: %+v", open.Msg.Tasks)
	}
	if _, err := f.task.DeleteTask(ctx, connect.NewRequest(&wv1.DeleteTaskRequest{Id: personal.Msg.Task.Id})); err != nil {
		t.Fatal(err)
	}
	all, _ := f.task.ListTasks(ctx, connect.NewRequest(&wv1.ListTasksRequest{}))
	if len(all.Msg.Tasks) != 1 {
		t.Fatalf("after delete: %+v", all.Msg.Tasks)
	}
}

func TestTreatmentFreezesContext(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q, _ := f.queen.CreateQueen(ctx, connect.NewRequest(&wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026}))
	res, err := f.treat.CreateTreatment(ctx, connect.NewRequest(&wv1.CreateTreatmentRequest{
		HiveId: "hive-a", Product: "Oxuvar", WithdrawalUntil: timestamppb.New(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Msg.Treatment
	if tr.ApiaryId != "apiary-a" || tr.QueenId != q.Msg.Queen.Id || tr.Reason != "varroa" || tr.WithdrawalUntil == nil {
		t.Fatalf("treatment: %+v", tr)
	}
	_, err = f.treat.CreateTreatment(ctx, connect.NewRequest(&wv1.CreateTreatmentRequest{HiveId: "hive-a"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	list, _ := f.treat.ListTreatments(ctx, connect.NewRequest(&wv1.ListTreatmentsRequest{HiveId: "hive-a"}))
	if len(list.Msg.Treatments) != 1 {
		t.Fatalf("list: %+v", list.Msg.Treatments)
	}
	if _, err := f.treat.DeleteTreatment(ctx, connect.NewRequest(&wv1.DeleteTreatmentRequest{Id: tr.Id})); err != nil {
		t.Fatal(err)
	}
	list, _ = f.treat.ListTreatments(ctx, connect.NewRequest(&wv1.ListTreatmentsRequest{}))
	if len(list.Msg.Treatments) != 0 {
		t.Fatalf("delete: %+v", list.Msg.Treatments)
	}
}

func TestHiveLifecycleThroughTheAPI(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	ap, err := f.apiary.CreateApiary(ctx, connect.NewRequest(&wv1.CreateApiaryRequest{Name: "Orchard"}))
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.hive.CreateHive(ctx, connect.NewRequest(&wv1.CreateHiveRequest{ApiaryId: ap.Msg.Apiary.Id, Name: "H9"}))
	if err != nil {
		t.Fatal(err)
	}
	if h.Msg.Hive.Status != wv1.HiveStatus_HIVE_STATUS_ACTIVE {
		t.Fatalf("new hive must be active: %+v", h.Msg.Hive)
	}
	chs := f.pullAll(t, ctx)
	if len(changesFor(chs, "apiary")) != 1 || len(changesFor(chs, "hive")) != 1 || len(changesFor(chs, "placement")) != 1 {
		t.Fatalf("apiary + hive + placement must be in the change log: %d changes", len(chs))
	}
	ev := changesFor(chs, "event")
	if len(ev) != 1 || !strings.Contains(ev[0].PayloadJson, `"type":1`) {
		t.Fatalf("CREATED event missing: %+v", ev)
	}
	_, err = f.hive.CreateHive(ctx, connect.NewRequest(&wv1.CreateHiveRequest{ApiaryId: "apiary-b", Name: "H"}))
	wantCode(t, err, connect.CodeNotFound)

	// Relocate: placement closed + opened, hive moved, MOVED event.
	mv, err := f.hive.RelocateHive(ctx, connect.NewRequest(&wv1.RelocateHiveRequest{Id: h.Msg.Hive.Id, TargetApiaryId: "apiary-a"}))
	if err != nil || mv.Msg.Hive.ApiaryId != "apiary-a" {
		t.Fatalf("relocate: %v %+v", err, mv)
	}
	chs = f.pullAll(t, ctx)
	if len(changesFor(chs, "placement")) != 3 { // open, close, open
		t.Fatalf("placements after move: %d", len(changesFor(chs, "placement")))
	}
	evs, _ := f.event.ListEvents(ctx, connect.NewRequest(&wv1.ListEventsRequest{HiveId: h.Msg.Hive.Id, Limit: 1}))
	if evs.Msg.Events[0].Type != wv1.EventType_EVENT_TYPE_MOVED {
		t.Fatalf("latest event must be MOVED: %+v", evs.Msg.Events[0])
	}

	// Update keeps the apiary; delete is soft and reaches devices.
	h.Msg.Hive.Name = "H9b"
	h.Msg.Hive.ApiaryId = ap.Msg.Apiary.Id
	up, err := f.hive.UpdateHive(ctx, connect.NewRequest(&wv1.UpdateHiveRequest{Hive: h.Msg.Hive}))
	if err != nil || up.Msg.Hive.Name != "H9b" || up.Msg.Hive.ApiaryId != "apiary-a" {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, err := f.hive.DeleteHive(ctx, connect.NewRequest(&wv1.DeleteHiveRequest{Id: h.Msg.Hive.Id})); err != nil {
		t.Fatal(err)
	}
	_, err = f.hive.GetHive(ctx, connect.NewRequest(&wv1.GetHiveRequest{Id: h.Msg.Hive.Id}))
	wantCode(t, err, connect.CodeNotFound)
	last := f.pullAll(t, ctx)
	if c := last[len(last)-1]; c.Entity != "hive" || c.Op != wv1.ChangeOp_CHANGE_OP_DELETE {
		t.Fatalf("delete must be the last change: %+v", c)
	}
	if _, err := f.apiary.DeleteApiary(ctx, connect.NewRequest(&wv1.DeleteApiaryRequest{Id: ap.Msg.Apiary.Id})); err != nil {
		t.Fatal(err)
	}
	ls, _ := f.apiary.ListApiaries(ctx, connect.NewRequest(&wv1.ListApiariesRequest{}))
	if len(ls.Msg.Apiaries) != 1 || ls.Msg.Page.Total != 1 {
		t.Fatalf("soft-deleted apiary must vanish from list/total: %+v", ls.Msg)
	}
}

func TestDashboardAndHoneyStats(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if _, err := f.queen.CreateQueen(ctx, connect.NewRequest(&wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026})); err != nil {
		t.Fatal(err)
	}
	old := timestamppb.New(time.Now().Add(-40 * 24 * time.Hour))
	if _, err := f.insp.CreateInspection(ctx, connect.NewRequest(&wv1.CreateInspectionRequest{HiveId: "hive-a", Date: old})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.task.CreateTask(ctx, connect.NewRequest(&wv1.CreateTaskRequest{Title: "late", DueAt: timestamppb.New(time.Now().Add(-time.Hour))})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.task.CreateTask(ctx, connect.NewRequest(&wv1.CreateTaskRequest{Title: "soon"})); err != nil {
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
	if _, err := f.sync.Push(ctx, connect.NewRequest(push)); err != nil {
		t.Fatalf("push harvest: %v", err)
	}

	d, err := f.stats.GetDashboard(ctx, connect.NewRequest(&wv1.DashboardRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := d.Msg
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
	if _, err := f.insp.CreateInspection(ctx, connect.NewRequest(&wv1.CreateInspectionRequest{HiveId: "hive-a"})); err != nil {
		t.Fatal(err)
	}
	d, _ = f.stats.GetDashboard(ctx, connect.NewRequest(&wv1.DashboardRequest{}))
	if len(d.Msg.DueInspections) != 0 {
		t.Fatalf("due after visit: %+v", d.Msg.DueInspections)
	}

	hs, err := f.stats.GetHoneyStats(ctx, connect.NewRequest(&wv1.HoneyStatsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(hs.Msg.PerYear) != 2 || len(hs.Msg.PerApiary) != 1 || hs.Msg.PerApiary[0].Label != "A" || hs.Msg.PerApiary[0].Kg != 15.5 {
		t.Fatalf("honey stats: %+v", hs.Msg)
	}
	hs, _ = f.stats.GetHoneyStats(ctx, connect.NewRequest(&wv1.HoneyStatsRequest{Year: int32(year)}))
	if len(hs.Msg.PerYear) != 1 || hs.Msg.PerYear[0].Kg != 12.5 {
		t.Fatalf("honey stats %d: %+v", year, hs.Msg)
	}
	// Tenant B sees an empty dashboard.
	d, _ = f.stats.GetDashboard(identityCtx("user-b", "org-b"), connect.NewRequest(&wv1.DashboardRequest{}))
	if d.Msg.Hives != 0 || d.Msg.Apiaries != 1 || d.Msg.HoneyKgSeason != 0 {
		t.Fatalf("tenant B dashboard: %+v", d.Msg)
	}
}

// TestAPIWritesMergeWithDeviceWrites: a device's later edit of a field wins
// over the API's earlier value and vice versa, per field, like any two devices.
func TestAPIWritesMergeWithDeviceWrites(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.task.CreateTask(ctx, connect.NewRequest(&wv1.CreateTaskRequest{Title: "api title", HiveId: "hive-a"}))
	if err != nil {
		t.Fatal(err)
	}
	// A device pushes an older-stamped title (must lose) and a newer-stamped
	// note (must win): fields merge independently.
	_, err = f.sync.Push(ctx, connect.NewRequest(&wv1.PushRequest{Changes: []*wv1.Change{
		{Entity: "task", EntityId: cr.Msg.Task.Id, ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
			Hlc: "000000000000001:00000:dev", AuthorId: "user-a", PayloadJson: `{"title":"stale device title"}`},
		{Entity: "task", EntityId: cr.Msg.Task.Id, ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
			Hlc: "999999999999999:00000:dev", AuthorId: "user-a", PayloadJson: `{"note":"device note"}`},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	ls, _ := f.task.ListTasks(ctx, connect.NewRequest(&wv1.ListTasksRequest{}))
	if got := ls.Msg.Tasks[0]; got.Title != "api title" || got.Note != "device note" {
		t.Fatalf("per-field merge: %+v", got)
	}
}

func TestHarvestFeedsHoneyStats(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q, _ := f.queen.CreateQueen(ctx, connect.NewRequest(&wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026}))
	res, err := f.harv.CreateHarvest(ctx, connect.NewRequest(&wv1.CreateHarvestRequest{
		HiveId: "hive-a", AmountKg: 18.5, Variety: "spring flow", WaterContent: 17.2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := res.Msg.Harvest
	if h.ApiaryId != "apiary-a" || h.QueenId != q.Msg.Queen.Id || h.AmountKg != 18.5 || h.Date == nil {
		t.Fatalf("harvest: %+v", h)
	}
	_, err = f.harv.CreateHarvest(ctx, connect.NewRequest(&wv1.CreateHarvestRequest{HiveId: "hive-a"}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// The HARVEST event carries the amount, so the stats see it.
	evs, _ := f.event.ListEvents(ctx, connect.NewRequest(&wv1.ListEventsRequest{HiveId: "hive-a", Limit: 1}))
	if e := evs.Msg.Events[0]; e.Type != wv1.EventType_EVENT_TYPE_HARVEST || e.AmountKg != 18.5 || e.RefId != h.Id || e.Title != "18.5 kg spring flow" {
		t.Fatalf("harvest event: %+v", e)
	}
	hs, _ := f.stats.GetHoneyStats(ctx, connect.NewRequest(&wv1.HoneyStatsRequest{}))
	if len(hs.Msg.PerQueen) != 1 || hs.Msg.PerQueen[0].Kg != 18.5 || hs.Msg.PerQueen[0].Label != "Queen 2026" {
		t.Fatalf("per queen: %+v", hs.Msg.PerQueen)
	}
	d, _ := f.stats.GetDashboard(ctx, connect.NewRequest(&wv1.DashboardRequest{}))
	if d.Msg.HoneyKgSeason != 18.5 {
		t.Fatalf("season: %v", d.Msg.HoneyKgSeason)
	}

	ls, _ := f.harv.ListHarvests(ctx, connect.NewRequest(&wv1.ListHarvestsRequest{ApiaryId: "apiary-a"}))
	if len(ls.Msg.Harvests) != 1 {
		t.Fatalf("list: %+v", ls.Msg.Harvests)
	}
	chs := changesFor(f.pullAll(t, ctx), "harvest")
	if len(chs) != 1 || chs[0].ScopeId != "apiary-a" {
		t.Fatalf("device must see the harvest: %+v", chs)
	}
	if _, err := f.harv.DeleteHarvest(ctx, connect.NewRequest(&wv1.DeleteHarvestRequest{Id: h.Id})); err != nil {
		t.Fatal(err)
	}
	ls, _ = f.harv.ListHarvests(ctx, connect.NewRequest(&wv1.ListHarvestsRequest{}))
	if len(ls.Msg.Harvests) != 0 {
		t.Fatalf("delete: %+v", ls.Msg.Harvests)
	}
	_, err = f.harv.DeleteHarvest(identityCtx("user-b", "org-b"), connect.NewRequest(&wv1.DeleteHarvestRequest{Id: h.Id}))
	wantCode(t, err, connect.CodeNotFound)
}
