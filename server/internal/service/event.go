package service

import (
	"context"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type EventService struct {
	openbeehivev1connect.UnimplementedEventServiceHandler
	w *Writer
}

func NewEventService(w *Writer) *EventService { return &EventService{w: w} }

func (s *EventService) ListEvents(ctx context.Context, req *wv1.ListEventsRequest) (*wv1.ListEventsResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	// Personal events (scope "user:<id>") are their owner's alone, as in
	// Pull, Snapshot and the export.
	q := `SELECT * FROM event WHERE organization_id = ? AND deleted = FALSE
		AND (COALESCE(scope_id, '') NOT LIKE 'user:%' OR scope_id = ?)`
	args := []any{id.OrgID, personalScope(id.UserID)}
	for col, v := range map[string]string{"apiary_id": req.ApiaryId, "hive_id": req.HiveId, "queen_id": req.QueenId} {
		if v != "" {
			q += ` AND ` + col + ` = ?`
			args = append(args, v)
		}
	}
	driver := s.w.db.DriverName()
	order := ` ORDER BY ` + tsSort(driver, "date") + ` DESC`
	if driver == "sqlite" {
		// One operation can write several events with the same timestamp (a
		// queen replaced and her successor introduced). SQLite returns such
		// ties in no particular order once the sort key is an expression;
		// rowid keeps them in the order they were written, latest first.
		order += `, rowid DESC`
	}
	rows, err := queryMaps(ctx, s.w.db, q+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return &wv1.ListEventsResponse{Events: out}, nil
}
