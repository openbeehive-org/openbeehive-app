package service

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type InspectionService struct {
	openbeehivev1connect.UnimplementedInspectionServiceHandler
	w *Writer
}

func NewInspectionService(w *Writer) *InspectionService { return &InspectionService{w: w} }

// maxPhotoBytes bounds an inline photo. The app stores ~480px JPEG
// thumbnails (tens of KB); a data URL is replicated to every device.
const maxPhotoBytes = 512 * 1024

func (s *InspectionService) CreateInspection(ctx context.Context, req *connect.Request[wv1.CreateInspectionRequest]) (*connect.Response[wv1.CreateInspectionResponse], error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	m := req.Msg
	if m.HiveId == "" {
		return nil, invalid("hive_id is required")
	}
	fields := map[string]any{
		"weather": m.Weather, "queen_seen": b2i(m.QueenSeen), "eggs_seen": b2i(m.EggsSeen),
		"temperament": int32(m.Temperament), "frames": m.Frames, "stores": int32(m.Stores),
		"queen_cells": m.QueenCells, "varroa": m.Varroa, "honey_kg": m.HoneyKg, "note": m.Note,
		"temp_hive": m.TempHive, "temp_outside": m.TempOutside,
		"humidity_hive": m.HumidityHive, "humidity_outside": m.HumidityOutside,
		"brood_frames": m.BroodFrames, "calmness": m.Calmness, "fed_kg": m.FedKg,
		"frames_added": m.FramesAdded, "frames_removed": m.FramesRemoved,
		"drone_frame_cut": b2i(m.DroneFrameCut), "super_added": b2i(m.SuperAdded),
		"weight_kg": m.WeightKg, "youngest_larva": m.YoungestLarva, "covered_larva": b2i(m.CoveredLarva),
	}
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		if _, err := t.hive(m.HiveId); err != nil {
			return err
		}
		iid, err := t.recordInspection(m.HiveId, tsOrNow(m.Date), fields)
		if err != nil {
			return err
		}
		out, err = t.row("inspection", iid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return connect.NewResponse(&wv1.CreateInspectionResponse{Inspection: inspectionFromRow(out)}), nil
}

func (s *InspectionService) ListInspections(ctx context.Context, req *connect.Request[wv1.ListInspectionsRequest]) (*connect.Response[wv1.ListInspectionsResponse], error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Msg.Page)
	where := ` FROM inspection WHERE organization_id = ? AND deleted = FALSE`
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
	out := make([]*wv1.Inspection, 0, len(rows))
	for _, r := range rows {
		out = append(out, inspectionFromRow(r))
	}
	return connect.NewResponse(&wv1.ListInspectionsResponse{Inspections: out, Page: pageResponse(total, offset, len(rows), limit)}), nil
}

func (s *InspectionService) DeleteInspection(ctx context.Context, req *connect.Request[wv1.DeleteInspectionRequest]) (*connect.Response[wv1.DeleteInspectionResponse], error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("inspection", req.Msg.Id)
		if err != nil {
			return err
		}
		scope, _, err := t.resolveContext(rs(cur["hive_id"]), rs(cur["date"]))
		if err != nil {
			return err
		}
		return t.del("inspection", req.Msg.Id, scope)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return connect.NewResponse(&wv1.DeleteInspectionResponse{}), nil
}

func (s *InspectionService) AddInspectionPhoto(ctx context.Context, req *connect.Request[wv1.AddInspectionPhotoRequest]) (*connect.Response[wv1.AddInspectionPhotoResponse], error) {
	id, _ := auth.FromContext(ctx)
	if !strings.HasPrefix(req.Msg.Photo, "data:image/") {
		return nil, invalid("photo must be a data URL (data:image/...)")
	}
	if len(req.Msg.Photo) > maxPhotoBytes {
		return nil, invalid("photo exceeds 512 KiB; send a thumbnail")
	}
	out, err := s.photoDelta(ctx, id, req.Msg.InspectionId, map[string]any{"add": []string{req.Msg.Photo}})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&wv1.AddInspectionPhotoResponse{Inspection: out}), nil
}

func (s *InspectionService) RemoveInspectionPhoto(ctx context.Context, req *connect.Request[wv1.RemoveInspectionPhotoRequest]) (*connect.Response[wv1.RemoveInspectionPhotoResponse], error) {
	id, _ := auth.FromContext(ctx)
	// No removed_tags: the server is the origin and tombstones every add tag
	// it currently holds (the legacy remove-all form both sides accept).
	out, err := s.photoDelta(ctx, id, req.Msg.InspectionId, map[string]any{"remove": []string{req.Msg.Photo}})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&wv1.RemoveInspectionPhotoResponse{Inspection: out}), nil
}

// photoDelta applies an OR-Set delta to photo_keys in the inspection's scope.
func (s *InspectionService) photoDelta(ctx context.Context, id auth.Identity, inspectionID string, delta map[string]any) (*wv1.Inspection, error) {
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("inspection", inspectionID)
		if err != nil {
			return err
		}
		scope, _, err := t.resolveContext(rs(cur["hive_id"]), rs(cur["date"]))
		if err != nil {
			return err
		}
		if err := t.upsert("inspection", inspectionID, scope, map[string]any{"photo_keys": delta}); err != nil {
			return err
		}
		out, err = t.row("inspection", inspectionID)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return inspectionFromRow(out), nil
}
