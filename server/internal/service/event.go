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
	q := `SELECT * FROM event WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
	for col, v := range map[string]string{"apiary_id": req.ApiaryId, "hive_id": req.HiveId, "queen_id": req.QueenId} {
		if v != "" {
			q += ` AND ` + col + ` = ?`
			args = append(args, v)
		}
	}
	rows, err := queryMaps(ctx, s.w.db, q+` ORDER BY date DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return &wv1.ListEventsResponse{Events: out}, nil
}
