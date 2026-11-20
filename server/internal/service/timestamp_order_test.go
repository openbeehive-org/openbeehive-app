package service

import (
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// TestListsOrderByInstant: lists, the dashboard and "the latest visit" follow
// the instants the timestamps denote. Devices and scripts write them with
// and without milliseconds, with a zone offset or as a bare date; ordered as
// text, each group below would come out in a different order.
func TestListsOrderByInstant(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	inspection := func(id, date string) *wv1.Change {
		return change("inspection", id, "apiary-a",
			`{"hive_id":"hive-a","date":"`+date+`","created_at":"2026-07-11T10:00:00Z","deleted":0}`)
	}
	task := func(id, due string) *wv1.Change {
		return change("task", id, "apiary-a",
			`{"title":"`+id+`","apiary_id":"apiary-a","due_at":"`+due+`","done":0,"priority":2,"created_at":"2026-07-11T10:00:00Z","deleted":0}`)
	}
	queen := func(id, introduced string) *wv1.Change {
		return change("queen", id, "apiary-a",
			`{"hive_id":"hive-a","year":2025,"introduced_at":"`+introduced+`","replaced_at":"2026-06-01T00:00:00Z","active":0,`+
				`"created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z","deleted":0}`)
	}
	if err := push(f.sync, ctx,
		inspection("insp-early", "2026-07-11T11:59:00+02:00"), // 09:59:00 UTC
		inspection("insp-mid", "2026-07-11T10:00:00Z"),
		inspection("insp-late", "2026-07-11T10:00:00.500Z"),
		task("task-1st", "2026-08-01T01:00:00+02:00"), // 31 July, 23:00 UTC
		task("task-2nd", "2026-07-31T23:30:00Z"),
		task("task-3rd", "2026-08-01"),
		queen("queen-older", "2026-05-01T01:00:00+02:00"), // 30 April, 23:00 UTC
		queen("queen-newer", "2026-04-30T23:30:00Z"),
	); err != nil {
		t.Fatalf("push: %v", err)
	}
	same := func(what string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", what, got, want)
			}
		}
	}

	visits, err := f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatalf("list inspections: %v", err)
	}
	var ids []string
	for _, v := range visits.Inspections {
		ids = append(ids, v.Id)
	}
	same("inspections, latest first", ids, "insp-late", "insp-mid", "insp-early")

	tasks, err := f.task.ListTasks(ctx, &wv1.ListTasksRequest{})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	ids = nil
	for _, v := range tasks.Tasks {
		ids = append(ids, v.Id)
	}
	same("tasks, next due first", ids, "task-1st", "task-2nd", "task-3rd")

	queens, err := f.queen.ListQueens(ctx, &wv1.ListQueensRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatalf("list queens: %v", err)
	}
	ids = nil
	for _, v := range queens.Queens {
		ids = append(ids, v.Id)
	}
	same("queens, latest first", ids, "queen-newer", "queen-older")

	dash, err := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	ids = nil
	for _, v := range dash.RecentInspections {
		ids = append(ids, v.Id)
	}
	same("dashboard: recent visits", ids, "insp-late", "insp-mid", "insp-early")
	ids = nil
	for _, v := range dash.NextTasks {
		ids = append(ids, v.Id)
	}
	same("dashboard: next tasks", ids, "task-1st", "task-2nd", "task-3rd")

	// The latest visit of a hive, which the due list is computed from.
	var raw any
	if err := f.db.QueryRow(`SELECT ` + tsMax(f.db.DriverName(), "date") + ` FROM inspection WHERE hive_id = 'hive-a'`).Scan(&raw); err != nil {
		t.Fatalf("latest visit: %v", err)
	}
	latest, ok := rtime(raw)
	if want := time.Date(2026, 7, 11, 10, 0, 0, 500_000_000, time.UTC); !ok || !latest.Equal(want) {
		t.Fatalf("latest visit: got %v (%v), want %v", latest, raw, want)
	}
}

// TestEventsOfOneInstantKeepWriteOrder: one operation can write several
// events with the same timestamp. On SQLite, where the timeline sorts by an
// expression, they come back latest-written first on every call.
func TestEventsOfOneInstantKeepWriteOrder(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	event := func(id, date string) *wv1.Change {
		return change("event", id, "apiary-a",
			`{"scope_id":"apiary-a","type":4,"date":"`+date+`","apiary_id":"apiary-a","hive_id":"hive-a","title":"`+id+`","deleted":0}`)
	}
	if err := push(f.sync, ctx,
		event("ev-early", "2026-07-11T11:59:00+02:00"), // 09:59 UTC
		event("ev-1", "2026-07-11T10:00:00.000Z"),
		event("ev-2", "2026-07-11T10:00:00Z"),
		event("ev-3", "2026-07-11T12:00:00+02:00"),
	); err != nil {
		t.Fatalf("push: %v", err)
	}
	for range 3 {
		res, err := f.event.ListEvents(ctx, &wv1.ListEventsRequest{HiveId: "hive-a"})
		if err != nil || len(res.Events) != 4 {
			t.Fatalf("list events: %v, %d events", err, len(res.GetEvents()))
		}
		if last := res.Events[3].Id; last != "ev-early" {
			t.Fatalf("earliest event: got %s, want ev-early", last)
		}
		if f.db.DriverName() != "sqlite" {
			continue // the other backends have no write-order key to break the tie with
		}
		if got := res.Events[0].Id + " " + res.Events[1].Id + " " + res.Events[2].Id; got != "ev-3 ev-2 ev-1" {
			t.Fatalf("events of one instant: got %s, want ev-3 ev-2 ev-1", got)
		}
	}
}
