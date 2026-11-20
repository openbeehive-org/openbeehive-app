package service

import (
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

func TestPersonalTasksAreOnlyReturnedToTheirOwner(t *testing.T) {
	f := newAPIFixture(t)
	owner, colleague := identityCtx("user-a", "org-a"), identityCtx("user-a2", "org-a")
	mine, err := f.task.CreateTask(owner, &wv1.CreateTaskRequest{Title: "dentist"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := f.task.CreateTask(owner, &wv1.CreateTaskRequest{Title: "varroa check", HiveId: "hive-a"})
	if err != nil {
		t.Fatal(err)
	}

	ls, _ := f.task.ListTasks(colleague, &wv1.ListTasksRequest{})
	if len(ls.Tasks) != 1 || ls.Tasks[0].Id != shared.Task.Id || ls.Page.Total != 1 {
		t.Fatalf("a colleague sees shared tasks only: %+v", ls.Tasks)
	}
	d, _ := f.stats.GetDashboard(colleague, &wv1.DashboardRequest{})
	if d.OpenTasks != 1 || len(d.NextTasks) != 1 || d.NextTasks[0].Id != shared.Task.Id {
		t.Fatalf("a colleague's dashboard counts shared tasks only: %d %+v", d.OpenTasks, d.NextTasks)
	}
	_, err = f.task.SetTaskDone(colleague, &wv1.SetTaskDoneRequest{Id: mine.Task.Id, Done: true})
	wantCode(t, err, connect.CodeNotFound)
	_, err = f.task.DeleteTask(colleague, &wv1.DeleteTaskRequest{Id: mine.Task.Id})
	wantCode(t, err, connect.CodeNotFound)
	if _, err := f.task.SetTaskDone(colleague, &wv1.SetTaskDoneRequest{Id: shared.Task.Id, Done: true}); err != nil {
		t.Fatalf("shared tasks stay editable by colleagues: %v", err)
	}

	ls, _ = f.task.ListTasks(owner, &wv1.ListTasksRequest{})
	d, _ = f.stats.GetDashboard(owner, &wv1.DashboardRequest{})
	if len(ls.Tasks) != 2 || d.OpenTasks != 1 || d.NextTasks[0].Id != mine.Task.Id {
		t.Fatalf("the owner sees both: %d tasks, %d open", len(ls.Tasks), d.OpenTasks)
	}
	if _, err := f.task.DeleteTask(owner, &wv1.DeleteTaskRequest{Id: mine.Task.Id}); err != nil {
		t.Fatal(err)
	}
}

func TestTasksWithoutDueDateComeLast(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	titles := []string{"undated", "later", "sooner"}
	dues := []*timestamppb.Timestamp{nil, day(2026, 9, 1), day(2026, 6, 1)}
	for i := range titles {
		if _, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: titles[i], HiveId: "hive-a", DueAt: dues[i]}); err != nil {
			t.Fatal(err)
		}
	}
	// A device stores "" for "no due date".
	if err := push(f.sync, ctx, change("task", "task-dev", "apiary-a",
		`{"organization_id":"org-a","title":"device undated","hive_id":"hive-a","apiary_id":"apiary-a","due_at":"","done":0,"priority":0,"created_at":"2026-05-01T10:00:00.000Z","deleted":0}`)); err != nil {
		t.Fatal(err)
	}
	ls, err := f.task.ListTasks(ctx, &wv1.ListTasksRequest{})
	if err != nil || len(ls.Tasks) != 4 {
		t.Fatalf("list: %v %+v", err, ls)
	}
	if ls.Tasks[0].Title != "sooner" || ls.Tasks[1].Title != "later" || ls.Tasks[2].DueAt != nil || ls.Tasks[3].DueAt != nil {
		t.Fatalf("order: %q %q %q %q", ls.Tasks[0].Title, ls.Tasks[1].Title, ls.Tasks[2].Title, ls.Tasks[3].Title)
	}
	d, _ := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if len(d.NextTasks) != 4 || d.NextTasks[0].Title != "sooner" || d.NextTasks[3].DueAt != nil {
		t.Fatalf("dashboard order: %+v", d.NextTasks)
	}
}
