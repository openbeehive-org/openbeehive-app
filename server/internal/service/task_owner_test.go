package service

import (
	"testing"

	"connectrpc.com/connect/v2"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// TestPersonalTaskOwnershipDoesNotNeedTheChangeLog: who owns a personal task
// is stored on the task. With the whole log gone the owner still sees it,
// the colleague still does not, and deleting its hive still reaches the
// owner.
func TestPersonalTaskOwnershipDoesNotNeedTheChangeLog(t *testing.T) {
	f := newAPIFixture(t)
	owner, colleague := identityCtx("user-a", "org-a"), identityCtx("user-a2", "org-a")
	mine, err := f.task.CreateTask(owner, &wv1.CreateTaskRequest{Title: "dentist"})
	if err != nil {
		t.Fatal(err)
	}
	// A device keeps a task of the hive in its owner's personal scope.
	if err := push(f.sync, owner, change("task", "task-dev", "user:user-a",
		`{"organization_id":"org-a","title":"device task","hive_id":"hive-a","apiary_id":"","done":0,"created_at":"2026-05-01T10:00:00.000Z","deleted":0}`)); err != nil {
		t.Fatal(err)
	}
	var owners []string
	if err := f.db.Select(&owners, `SELECT owner_id FROM task ORDER BY title`); err != nil || len(owners) != 2 ||
		owners[0] != "user-a" || owners[1] != "user-a" {
		t.Fatalf("owners: %v %v", owners, err)
	}
	// A colleague writing the same id in their own scope is refused and does
	// not take it over.
	wantRejected(t, pushRes(t, f.sync, colleague, change("task", "task-dev", "user:user-a2", `{"note":"mine now"}`)),
		map[int32]string{0: rejForbiddenScope})
	if err := f.db.Get(&owners[0], `SELECT owner_id FROM task WHERE id = 'task-dev'`); err != nil || owners[0] != "user-a" {
		t.Fatalf("owner after a foreign write: %q %v", owners[0], err)
	}

	f.db.MustExec(`DELETE FROM change_log`)

	ls, _ := f.task.ListTasks(owner, &wv1.ListTasksRequest{})
	if len(ls.Tasks) != 2 {
		t.Fatalf("the owner sees %d tasks, want 2", len(ls.Tasks))
	}
	d, _ := f.stats.GetDashboard(owner, &wv1.DashboardRequest{})
	if d.OpenTasks != 2 {
		t.Fatalf("the owner's dashboard counts %d open tasks", d.OpenTasks)
	}
	ls, _ = f.task.ListTasks(colleague, &wv1.ListTasksRequest{})
	d, _ = f.stats.GetDashboard(colleague, &wv1.DashboardRequest{})
	if len(ls.Tasks) != 0 || d.OpenTasks != 0 {
		t.Fatalf("a colleague sees %d personal tasks (%d on the dashboard)", len(ls.Tasks), d.OpenTasks)
	}
	_, err = f.task.SetTaskDone(colleague, &wv1.SetTaskDoneRequest{Id: mine.Task.Id, Done: true})
	wantCode(t, err, connect.CodeNotFound)
	if _, err := f.task.SetTaskDone(owner, &wv1.SetTaskDoneRequest{Id: mine.Task.Id, Done: true}); err != nil {
		t.Fatalf("the owner edits the task: %v", err)
	}

	// The colleague deletes the hive: the open task tied to it goes, and the
	// tombstone is written where only its owner pulls it.
	if _, err := f.hive.DeleteHive(colleague, &wv1.DeleteHiveRequest{Id: "hive-a"}); err != nil {
		t.Fatal(err)
	}
	var scope string
	if err := f.db.Get(&scope, `SELECT scope_id FROM change_log WHERE entity = 'task' AND entity_id = 'task-dev' AND op = 2`); err != nil || scope != "user:user-a" {
		t.Fatalf("tombstone scope: %q %v", scope, err)
	}
}
