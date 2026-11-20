package service

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type TaskService struct {
	openbeehivev1connect.UnimplementedTaskServiceHandler
	w *Writer
}

func NewTaskService(w *Writer) *TaskService { return &TaskService{w: w} }

// taskScope mirrors repo.ts: personal scope unless tied to an apiary.
func taskScope(userID, apiaryID string) string {
	if apiaryID != "" {
		return apiaryID
	}
	return personalScope(userID)
}

// personalScope is the sync scope only one user's devices pull.
func personalScope(userID string) string { return "user:" + userID }

// visibleTasks restricts a task query to what one member may see: the tasks
// shared with the workspace (tied to an apiary) and the member's own personal
// ones. The owner of a personal task is recorded in task.owner_id when the
// task is first written in a personal scope (claimOwner). The query takes the
// member's user id as its argument.
const visibleTasks = ` AND (COALESCE(apiary_id, '') <> '' OR owner_id = ?)`

// tasksInOrder: open tasks first, then by due date with undated tasks last.
// The dialects disagree on where NULL sorts, so the order spells it out.
func tasksInOrder(driver string) string {
	return ` ORDER BY done ASC, CASE WHEN due_at IS NULL THEN 1 ELSE 0 END, ` + tsSort(driver, "due_at") + ` ASC, id ASC`
}

func (s *TaskService) CreateTask(ctx context.Context, req *wv1.CreateTaskRequest) (*wv1.CreateTaskResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	m := req
	if m.Title == "" {
		return nil, invalid("title is required")
	}
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		apiaryID, err := t.resolveApiary(m.HiveId, m.ApiaryId)
		if err != nil {
			return err
		}
		tid := uuid.NewString()
		if err := t.upsert("task", tid, taskScope(id.UserID, apiaryID), map[string]any{
			"organization_id": id.OrgID, "title": m.Title, "apiary_id": apiaryID, "hive_id": m.HiveId,
			"due_at": tsOrEmpty(m.DueAt), "done": 0, "priority": int32(m.Priority), "note": m.Note,
			"recurrence": m.Recurrence, "created_at": jsTimeNow(), "deleted": 0,
		}); err != nil {
			return err
		}
		out, err = t.row("task", tid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.CreateTaskResponse{Task: taskFromRow(out)}, nil
}

func (s *TaskService) ListTasks(ctx context.Context, req *wv1.ListTasksRequest) (*wv1.ListTasksResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Page)
	where := ` FROM task WHERE organization_id = ? AND deleted = FALSE` + visibleTasks
	args := []any{id.OrgID, id.UserID}
	if req.HiveId != "" {
		where += ` AND hive_id = ?`
		args = append(args, req.HiveId)
	}
	if req.OnlyOpen {
		where += ` AND done = FALSE`
	}
	total, err := countRows(ctx, s.w.db, `SELECT COUNT(*)`+where, args...)
	if err != nil {
		return nil, writeErr(err)
	}
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+tasksInOrder(s.w.db.DriverName())+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Task, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskFromRow(r))
	}
	return &wv1.ListTasksResponse{Tasks: out, Page: pageResponse(total, offset, len(rows), limit)}, nil
}

// taskUpdatable lists the fields UpdateTask may change.
var taskUpdatable = []string{"title", "due_at", "done", "priority", "note", "recurrence", "assigned_to"}

// UpdateTask changes only the fields sent (see UpdateTaskRequest).
func (s *TaskService) UpdateTask(ctx context.Context, req *wv1.UpdateTaskRequest) (*wv1.UpdateTaskResponse, error) {
	id, _ := auth.FromContext(ctx)
	p := req.Task
	if p == nil || p.Id == "" {
		return nil, invalid("task.id is required")
	}
	fields, err := updatePayload(p.ProtoReflect(), req.UpdateMask, taskUpdatable)
	if err != nil {
		return nil, err
	}
	if err := requireValue(fields, "title"); err != nil {
		return nil, err
	}
	out, err := s.write(ctx, id, p.Id, fields)
	if err != nil {
		return nil, err
	}
	return &wv1.UpdateTaskResponse{Task: out}, nil
}

func (s *TaskService) SetTaskDone(ctx context.Context, req *wv1.SetTaskDoneRequest) (*wv1.SetTaskDoneResponse, error) {
	id, _ := auth.FromContext(ctx)
	out, err := s.write(ctx, id, req.Id, map[string]any{"done": b2i(req.Done)})
	if err != nil {
		return nil, err
	}
	return &wv1.SetTaskDoneResponse{Task: out}, nil
}

// write applies fields to a task the caller may see and returns the result.
func (s *TaskService) write(ctx context.Context, id auth.Identity, taskID string, fields map[string]any) (*wv1.Task, error) {
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		scope, err := t.taskScope(taskID)
		if err != nil {
			return err
		}
		if err := t.upsert("task", taskID, scope, fields); err != nil {
			return err
		}
		out, err = t.row("task", taskID)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return taskFromRow(out), nil
}

func (s *TaskService) DeleteTask(ctx context.Context, req *wv1.DeleteTaskRequest) (*wv1.DeleteTaskResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		scope, err := t.taskScope(req.Id)
		if err != nil {
			return err
		}
		return t.del("task", req.Id, scope)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteTaskResponse{}, nil
}

// taskScope returns the sync scope of a task the caller may see, or
// storage.ErrNotFound (also for another member's personal task).
func (t *txWriter) taskScope(id string) (string, error) {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT apiary_id FROM task
		WHERE id = ? AND organization_id = ? AND deleted = FALSE`+visibleTasks,
		id, t.id.OrgID, t.id.UserID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", storage.ErrNotFound
	}
	return taskScope(t.id.UserID, rs(rows[0]["apiary_id"])), nil
}

// delOpenTasks tombstones the open tasks of a hive, each in the scope it
// lives in: the apiary's for shared tasks, its owner's personal scope
// otherwise.
func (t *txWriter) delOpenTasks(hiveID string) error {
	tasks, err := queryMaps(t.ctx, t.tx, `SELECT id, apiary_id, owner_id FROM task
		WHERE organization_id = ? AND hive_id = ? AND deleted = FALSE AND done = FALSE`,
		t.id.OrgID, hiveID)
	if err != nil {
		return err
	}
	for _, r := range tasks {
		scope := rs(r["apiary_id"])
		if scope == "" {
			owner := rs(r["owner_id"])
			if owner == "" {
				owner = t.id.UserID
			}
			scope = personalScope(owner)
		}
		if err := t.del("task", rs(r["id"]), scope); err != nil {
			return err
		}
	}
	return nil
}

// resolveApiary returns the apiary a task belongs to: the hive's when a hive
// is given and no apiary, else the given one, which must exist.
func (t *txWriter) resolveApiary(hiveID, apiaryID string) (string, error) {
	if hiveID != "" {
		h, err := t.hive(hiveID)
		if err != nil {
			return "", err
		}
		if apiaryID == "" {
			apiaryID = rs(h["apiary_id"])
		}
	}
	if apiaryID == "" {
		return "", nil
	}
	ok, err := t.apiaryExists(apiaryID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", invalid("apiary_id does not exist")
	}
	return apiaryID, nil
}
