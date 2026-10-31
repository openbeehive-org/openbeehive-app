package service

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type HarvestService struct {
	openbeehivev1connect.UnimplementedHarvestServiceHandler
	w *Writer
}

func NewHarvestService(w *Writer) *HarvestService { return &HarvestService{w: w} }

func (s *HarvestService) CreateHarvest(ctx context.Context, req *wv1.CreateHarvestRequest) (*wv1.CreateHarvestResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	m := req
	if m.HiveId == "" {
		return nil, invalid("hive_id is required")
	}
	if m.AmountKg <= 0 {
		return nil, invalid("amount_kg must be positive")
	}
	fields := map[string]any{
		"variety": m.Variety, "amount_kg": m.AmountKg, "water_content": m.WaterContent,
		"batch_number": m.BatchNumber, "best_before": tsOrEmpty(m.BestBefore), "note": m.Note,
	}
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		if _, err := t.hive(m.HiveId); err != nil {
			return err
		}
		hid, err := t.recordHarvest(m.HiveId, tsOrNow(m.Date), fields)
		if err != nil {
			return err
		}
		out, err = t.row("harvest", hid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.CreateHarvestResponse{Harvest: harvestFromRow(out)}, nil
}

func (s *HarvestService) ListHarvests(ctx context.Context, req *wv1.ListHarvestsRequest) (*wv1.ListHarvestsResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Page)
	where := ` FROM harvest WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
	if req.HiveId != "" {
		where += ` AND hive_id = ?`
		args = append(args, req.HiveId)
	}
	if req.ApiaryId != "" {
		where += ` AND apiary_id = ?`
		args = append(args, req.ApiaryId)
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
	out := make([]*wv1.Harvest, 0, len(rows))
	for _, r := range rows {
		out = append(out, harvestFromRow(r))
	}
	return &wv1.ListHarvestsResponse{Harvests: out, Page: pageResponse(total, offset, len(rows), limit)}, nil
}

func (s *HarvestService) DeleteHarvest(ctx context.Context, req *wv1.DeleteHarvestRequest) (*wv1.DeleteHarvestResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("harvest", req.Id)
		if err != nil {
			return err
		}
		return t.del("harvest", req.Id, rs(cur["apiary_id"]))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteHarvestResponse{}, nil
}
