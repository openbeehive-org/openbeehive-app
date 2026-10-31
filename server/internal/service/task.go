package service

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
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
	return "user:" + userID
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
	where := ` FROM task WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
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
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+` ORDER BY done ASC, due_at ASC LIMIT ? OFFSET ?`,
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

func (s *TaskService) SetTaskDone(ctx context.Context, req *wv1.SetTaskDoneRequest) (*wv1.SetTaskDoneResponse, error) {
	id, _ := auth.FromContext(ctx)
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("task", req.Id)
		if err != nil {
			return err
		}
		if err := t.upsert("task", req.Id, taskScope(id.UserID, rs(cur["apiary_id"])),
			map[string]any{"done": b2i(req.Done)}); err != nil {
			return err
		}
		out, err = t.row("task", req.Id)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.SetTaskDoneResponse{Task: taskFromRow(out)}, nil
}

func (s *TaskService) DeleteTask(ctx context.Context, req *wv1.DeleteTaskRequest) (*wv1.DeleteTaskResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("task", req.Id)
		if err != nil {
			return err
		}
		return t.del("task", req.Id, taskScope(id.UserID, rs(cur["apiary_id"])))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteTaskResponse{}, nil
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
