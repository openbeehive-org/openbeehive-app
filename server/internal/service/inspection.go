package service

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"

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

// inspectionCols lists the observation fields of an inspection, named alike
// in CreateInspectionRequest, in Inspection and in the table. The optional
// numbers among them (counts, amounts, climate readings) are stored as NULL
// when not sent: "not recorded" is not the same as an observed 0.
var inspectionCols = []string{
	"weather", "queen_seen", "eggs_seen", "temperament", "frames", "stores", "queen_cells",
	"varroa", "honey_kg", "note", "brood_frames", "calmness", "fed_kg", "frames_added",
	"frames_removed", "drone_frame_cut", "super_added", "weight_kg", "youngest_larva",
	"covered_larva", "temp_hive", "temp_outside", "humidity_hive", "humidity_outside", "source",
}

// inspectionUpdatable lists the fields UpdateInspection may change.
var inspectionUpdatable = append([]string{"date"}, inspectionCols...)

// sourceSensor marks an automated reading (InspectionSource); such rows are
// measurements, not visits.
const sourceSensor = int32(wv1.InspectionSource_INSPECTION_SOURCE_SENSOR)

func (s *InspectionService) CreateInspection(ctx context.Context, req *wv1.CreateInspectionRequest) (*wv1.CreateInspectionResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	m := req
	if m.HiveId == "" {
		return nil, invalid("hive_id is required")
	}
	fields, err := createPayload(m.ProtoReflect(), inspectionCols)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		if _, err := t.hive(m.HiveId); err != nil {
			return err
		}
		iid, err := t.recordInspection("", m.HiveId, tsOrNow(m.Date), fields)
		if err != nil {
			return err
		}
		out, err = t.row("inspection", iid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.CreateInspectionResponse{Inspection: inspectionFromRow(out)}, nil
}

func (s *InspectionService) ListInspections(ctx context.Context, req *wv1.ListInspectionsRequest) (*wv1.ListInspectionsResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Page)
	where := ` FROM inspection WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
	if req.HiveId != "" {
		where += ` AND hive_id = ?`
		args = append(args, req.HiveId)
	}
	switch req.Source {
	case wv1.InspectionSource_INSPECTION_SOURCE_MANUAL:
		where += ` AND source <> ?`
		args = append(args, sourceSensor)
	case wv1.InspectionSource_INSPECTION_SOURCE_SENSOR:
		where += ` AND source = ?`
		args = append(args, sourceSensor)
	}
	total, err := countRows(ctx, s.w.db, `SELECT COUNT(*)`+where, args...)
	if err != nil {
		return nil, writeErr(err)
	}
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+` ORDER BY date DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Inspection, 0, len(rows))
	for _, r := range rows {
		out = append(out, inspectionFromRow(r))
	}
	return &wv1.ListInspectionsResponse{Inspections: out, Page: pageResponse(total, offset, len(rows), limit)}, nil
}

// UpdateInspection corrects a saved inspection; only the fields sent are
// changed (see UpdateInspectionRequest).
func (s *InspectionService) UpdateInspection(ctx context.Context, req *wv1.UpdateInspectionRequest) (*wv1.UpdateInspectionResponse, error) {
	id, _ := auth.FromContext(ctx)
	p := req.Inspection
	if p == nil || p.Id == "" {
		return nil, invalid("inspection.id is required")
	}
	fields, err := updatePayload(p.ProtoReflect(), req.UpdateMask, inspectionUpdatable)
	if err != nil {
		return nil, err
	}
	if err := requireValue(fields, "date"); err != nil {
		return nil, err
	}
	var out map[string]any
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		out, err = t.amendRecord("inspection", p.Id, fields)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.UpdateInspectionResponse{Inspection: inspectionFromRow(out)}, nil
}

// DeleteInspection soft-deletes the inspection and its timeline event.
func (s *InspectionService) DeleteInspection(ctx context.Context, req *wv1.DeleteInspectionRequest) (*wv1.DeleteInspectionResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("inspection", req.Id)
		if err != nil {
			return err
		}
		scope, err := t.recordScope("inspection", cur)
		if err != nil {
			return err
		}
		return t.delRecord("inspection", req.Id, scope)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteInspectionResponse{}, nil
}

func (s *InspectionService) AddInspectionPhoto(ctx context.Context, req *wv1.AddInspectionPhotoRequest) (*wv1.AddInspectionPhotoResponse, error) {
	id, _ := auth.FromContext(ctx)
	if !strings.HasPrefix(req.Photo, "data:image/") {
		return nil, invalid("photo must be a data URL (data:image/...)")
	}
	if len(req.Photo) > maxPhotoBytes {
		return nil, invalid("photo exceeds 512 KiB; send a thumbnail")
	}
	out, err := s.photoDelta(ctx, id, req.InspectionId, map[string]any{"add": []string{req.Photo}})
	if err != nil {
		return nil, err
	}
	return &wv1.AddInspectionPhotoResponse{Inspection: out}, nil
}

func (s *InspectionService) RemoveInspectionPhoto(ctx context.Context, req *wv1.RemoveInspectionPhotoRequest) (*wv1.RemoveInspectionPhotoResponse, error) {
	id, _ := auth.FromContext(ctx)
	// No removed_tags: the server is the origin and tombstones every add tag
	// it currently holds (the legacy remove-all form both sides accept).
	out, err := s.photoDelta(ctx, id, req.InspectionId, map[string]any{"remove": []string{req.Photo}})
	if err != nil {
		return nil, err
	}
	return &wv1.RemoveInspectionPhotoResponse{Inspection: out}, nil
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
