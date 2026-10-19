package service

import (
	"context"

	"connectrpc.com/connect"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type TreatmentService struct {
	openbeehivev1connect.UnimplementedTreatmentServiceHandler
	w *Writer
}

func NewTreatmentService(w *Writer) *TreatmentService { return &TreatmentService{w: w} }

func (s *TreatmentService) CreateTreatment(ctx context.Context, req *connect.Request[wv1.CreateTreatmentRequest]) (*connect.Response[wv1.CreateTreatmentResponse], error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	m := req.Msg
	if m.HiveId == "" {
		return nil, invalid("hive_id is required")
	}
	if m.Product == "" {
		return nil, invalid("product is required")
	}
	reason := m.Reason
	if reason == "" {
		reason = "varroa" // the app's default
	}
	fields := map[string]any{
		"product": m.Product, "active_ingredient": m.ActiveIngredient, "dose": m.Dose,
		"method": m.Method, "batch_number": m.BatchNumber, "withdrawal_until": tsOrEmpty(m.WithdrawalUntil),
		"reason": reason, "note": m.Note,
	}
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		if _, err := t.hive(m.HiveId); err != nil {
			return err
		}
		tid, err := t.recordTreatment(m.HiveId, tsOrNow(m.Date), fields)
		if err != nil {
			return err
		}
		out, err = t.row("treatment", tid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return connect.NewResponse(&wv1.CreateTreatmentResponse{Treatment: treatmentFromRow(out)}), nil
}

func (s *TreatmentService) ListTreatments(ctx context.Context, req *connect.Request[wv1.ListTreatmentsRequest]) (*connect.Response[wv1.ListTreatmentsResponse], error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Msg.Page)
	where := ` FROM treatment WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
	if req.Msg.HiveId != "" {
		where += ` AND hive_id = ?`
		args = append(args, req.Msg.HiveId)
	}
	total, err := countRows(ctx, s.w.db, `SELECT COUNT(*)`+where, args...)
	if err != nil {
		return nil, writeErr(err)
	}
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+` ORDER BY date DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Treatment, 0, len(rows))
	for _, r := range rows {
		out = append(out, treatmentFromRow(r))
	}
	return connect.NewResponse(&wv1.ListTreatmentsResponse{Treatments: out, Page: pageResponse(total, offset, len(rows), limit)}), nil
}

func (s *TreatmentService) DeleteTreatment(ctx context.Context, req *connect.Request[wv1.DeleteTreatmentRequest]) (*connect.Response[wv1.DeleteTreatmentResponse], error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("treatment", req.Msg.Id)
		if err != nil {
			return err
		}
		return t.del("treatment", req.Msg.Id, rs(cur["apiary_id"]))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return connect.NewResponse(&wv1.DeleteTreatmentResponse{}), nil
}
