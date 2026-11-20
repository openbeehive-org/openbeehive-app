package service

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type TreatmentService struct {
	openbeehivev1connect.UnimplementedTreatmentServiceHandler
	w *Writer
}

func NewTreatmentService(w *Writer) *TreatmentService { return &TreatmentService{w: w} }

func (s *TreatmentService) CreateTreatment(ctx context.Context, req *wv1.CreateTreatmentRequest) (*wv1.CreateTreatmentResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	m := req
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
		tid, err := t.recordTreatment("", m.HiveId, tsOrNow(m.Date), fields)
		if err != nil {
			return err
		}
		out, err = t.row("treatment", tid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.CreateTreatmentResponse{Treatment: treatmentFromRow(out)}, nil
}

func (s *TreatmentService) ListTreatments(ctx context.Context, req *wv1.ListTreatmentsRequest) (*wv1.ListTreatmentsResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Page)
	where := ` FROM treatment WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
	if req.HiveId != "" {
		where += ` AND hive_id = ?`
		args = append(args, req.HiveId)
	}
	total, err := countRows(ctx, s.w.db, `SELECT COUNT(*)`+where, args...)
	if err != nil {
		return nil, writeErr(err)
	}
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+` ORDER BY `+tsSort(s.w.db.DriverName(), "date")+` DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Treatment, 0, len(rows))
	for _, r := range rows {
		out = append(out, treatmentFromRow(r))
	}
	return &wv1.ListTreatmentsResponse{Treatments: out, Page: pageResponse(total, offset, len(rows), limit)}, nil
}

// treatmentUpdatable lists the fields UpdateTreatment may change.
var treatmentUpdatable = []string{"date", "product", "active_ingredient", "dose", "method", "batch_number", "withdrawal_until", "reason", "note"}

// UpdateTreatment corrects a saved treatment; only the fields sent are
// changed (see UpdateTreatmentRequest).
func (s *TreatmentService) UpdateTreatment(ctx context.Context, req *wv1.UpdateTreatmentRequest) (*wv1.UpdateTreatmentResponse, error) {
	id, _ := auth.FromContext(ctx)
	p := req.Treatment
	if p == nil || p.Id == "" {
		return nil, invalid("treatment.id is required")
	}
	fields, err := updatePayload(p.ProtoReflect(), req.UpdateMask, treatmentUpdatable)
	if err != nil {
		return nil, err
	}
	if err := requireValue(fields, "date", "product"); err != nil {
		return nil, err
	}
	var out map[string]any
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		out, err = t.amendRecord("treatment", p.Id, fields)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.UpdateTreatmentResponse{Treatment: treatmentFromRow(out)}, nil
}

// DeleteTreatment soft-deletes the treatment and its timeline event.
func (s *TreatmentService) DeleteTreatment(ctx context.Context, req *wv1.DeleteTreatmentRequest) (*wv1.DeleteTreatmentResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("treatment", req.Id)
		if err != nil {
			return err
		}
		scope, err := t.recordScope("treatment", cur)
		if err != nil {
			return err
		}
		return t.delRecord("treatment", req.Id, scope)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteTreatmentResponse{}, nil
}
