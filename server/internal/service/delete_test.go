package service

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// hiveEvents lists the live timeline events of tenant A's fixture hive.
func (f *apiFixture) hiveEvents(t *testing.T, ctx context.Context) []*wv1.Event {
	t.Helper()
	res, err := f.event.ListEvents(ctx, &wv1.ListEventsRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return res.Events
}

func TestDeletesRemoveTheirEvents(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	q, _ := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026})
	in, _ := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a"})
	tr, _ := f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{HiveId: "hive-a", Product: "Oxuvar"})
	hv, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: "hive-a", AmountKg: 9})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.hiveEvents(t, ctx)); n != 4 {
		t.Fatalf("want 4 events before deleting, got %d", n)
	}

	if _, err := f.harv.DeleteHarvest(ctx, &wv1.DeleteHarvestRequest{Id: hv.Harvest.Id}); err != nil {
		t.Fatal(err)
	}
	hs, _ := f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
	d, _ := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if len(hs.PerYear) != 0 || len(hs.PerQueen) != 0 || d.HoneyKgSeason != 0 || len(d.HoneyHistory) != 0 {
		t.Fatalf("a deleted harvest must leave the honey statistics: %+v / season %v", hs, d.HoneyKgSeason)
	}
	if _, err := f.insp.DeleteInspection(ctx, &wv1.DeleteInspectionRequest{Id: in.Inspection.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.treat.DeleteTreatment(ctx, &wv1.DeleteTreatmentRequest{Id: tr.Treatment.Id}); err != nil {
		t.Fatal(err)
	}
	evs := f.hiveEvents(t, ctx)
	if len(evs) != 1 || evs[0].Type != wv1.EventType_EVENT_TYPE_QUEEN_INTRODUCED {
		t.Fatalf("record events must be gone, got %+v", evs)
	}
	// Devices are told: one event tombstone per deleted record.
	var tombstones int
	for _, c := range changesFor(f.pullAll(t, ctx), "event") {
		if c.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
			tombstones++
		}
	}
	if tombstones != 3 {
		t.Fatalf("want 3 event tombstones in the change feed, got %d", tombstones)
	}
	if _, err := f.queen.DeleteQueen(ctx, &wv1.DeleteQueenRequest{Id: q.Queen.Id}); err != nil {
		t.Fatal(err)
	}
	if evs := f.hiveEvents(t, ctx); len(evs) != 0 {
		t.Fatalf("queen events must be gone, got %+v", evs)
	}
}

func TestHoneyStatsIgnoreDeletedHivesAndOrphanedEvents(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	h2, err := f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{ApiaryId: "apiary-a", Name: "H2"})
	if err != nil {
		t.Fatal(err)
	}
	keep, _ := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: "hive-a", AmountKg: 10})
	if _, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: "hive-a", AmountKg: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: h2.Hive.Id, AmountKg: 7}); err != nil {
		t.Fatal(err)
	}
	total := func() float64 {
		t.Helper()
		hs, err := f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		d, err := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
		if err != nil {
			t.Fatal(err)
		}
		var kg float64
		for _, y := range hs.PerYear {
			kg += y.Kg
		}
		if kg != d.HoneyKgSeason {
			t.Fatalf("honey stats (%v) and dashboard (%v) disagree", kg, d.HoneyKgSeason)
		}
		return kg
	}
	if kg := total(); kg != 21 {
		t.Fatalf("before: %v", kg)
	}
	if _, err := f.hive.DeleteHive(ctx, &wv1.DeleteHiveRequest{Id: h2.Hive.Id}); err != nil {
		t.Fatal(err)
	}
	if kg := total(); kg != 14 {
		t.Fatalf("a deleted hive's honey must not count: %v", kg)
	}
	// An older app deletes a harvest without its event; the honey must go too.
	if err := push(f.sync, ctx, &wv1.Change{
		Entity: "harvest", EntityId: keep.Harvest.Id, ScopeId: "apiary-a",
		Op: wv1.ChangeOp_CHANGE_OP_DELETE, PayloadJson: `{"deleted":1}`, Hlc: "999999999999999:00000:dev", AuthorId: "dev",
	}); err != nil {
		t.Fatal(err)
	}
	if kg := total(); kg != 4 {
		t.Fatalf("a harvest deleted on a device must not count: %v", kg)
	}
}

func TestDeleteApiaryWithHivesIsRejected(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	_, err := f.apiary.DeleteApiary(ctx, &wv1.DeleteApiaryRequest{Id: "apiary-a"})
	wantCode(t, err, connect.CodeFailedPrecondition)
	if !strings.Contains(err.Error(), "1 hive") {
		t.Fatalf("the message must say why: %v", err)
	}
	if _, err := f.apiary.GetApiary(ctx, &wv1.GetApiaryRequest{Id: "apiary-a"}); err != nil {
		t.Fatalf("the apiary must still exist: %v", err)
	}
	if _, err := f.hive.DeleteHive(ctx, &wv1.DeleteHiveRequest{Id: "hive-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.apiary.DeleteApiary(ctx, &wv1.DeleteApiaryRequest{Id: "apiary-a"}); err != nil {
		t.Fatalf("an empty apiary can be deleted: %v", err)
	}
	_, err = f.apiary.DeleteApiary(ctx, &wv1.DeleteApiaryRequest{Id: "apiary-b"})
	wantCode(t, err, connect.CodeNotFound)
}

func TestDeleteHiveDeletesItsOpenTasks(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	open, _ := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "varroa check", HiveId: "hive-a"})
	done, _ := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "fed", HiveId: "hive-a"})
	other, _ := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "order jars"})
	if _, err := f.task.SetTaskDone(ctx, &wv1.SetTaskDoneRequest{Id: done.Task.Id, Done: true}); err != nil {
		t.Fatal(err)
	}
	// A task a device tied to the hive but kept in the personal scope.
	if err := push(f.sync, ctx, change("task", "task-dev", "user:user-a",
		`{"organization_id":"org-a","title":"device task","hive_id":"hive-a","apiary_id":"","due_at":"","done":0,"priority":0,"created_at":"2026-05-01T10:00:00.000Z","deleted":0}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.hive.DeleteHive(ctx, &wv1.DeleteHiveRequest{Id: "hive-a"}); err != nil {
		t.Fatal(err)
	}
	ls, _ := f.task.ListTasks(ctx, &wv1.ListTasksRequest{})
	got := map[string]bool{}
	for _, x := range ls.Tasks {
		got[x.Id] = true
	}
	if len(got) != 2 || !got[done.Task.Id] || !got[other.Task.Id] {
		t.Fatalf("only the hive's open tasks must go: kept %v (open %s)", got, open.Task.Id)
	}
	scopes := map[string]string{}
	for _, c := range changesFor(f.pullAll(t, ctx), "task") {
		if c.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
			scopes[c.EntityId] = c.ScopeId
		}
	}
	if scopes[open.Task.Id] != "apiary-a" || scopes["task-dev"] != "user:user-a" || len(scopes) != 2 {
		t.Fatalf("tombstones must reach the scope each task lives in: %v", scopes)
	}
}
