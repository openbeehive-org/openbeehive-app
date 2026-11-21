package service

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type InspectionService struct {
	openbeehivev1connect.UnimplementedInspectionServiceHandler
	w *Writer
}

func NewInspectionService(w *Writer) *InspectionService { return &InspectionService{w: w} }

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

// maxInspectionAhead is how far in the future an inspection date may lie:
// a day covers every time zone, a later date is a typo.
const maxInspectionAhead = 24 * time.Hour

// checkInspectionDate refuses a date more than maxInspectionAhead ahead.
func checkInspectionDate(date *timestamppb.Timestamp) error {
	if date != nil && date.AsTime().After(time.Now().Add(maxInspectionAhead)) {
		return invalid("date: an inspection cannot be more than a day in the future")
	}
	return nil
}

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
	if err := checkInspectionDate(m.Date); err != nil {
		return nil, err
	}
	fields, err := createPayload(m.ProtoReflect(), inspectionCols)
	if err != nil {
		return nil, err
	}
	if fields["source"] == int32(0) {
		// An unset source stays out of the payload: the column defaults to 0,
		// and a device whose app version has no such column yet can still
		// apply the change.
		delete(fields, "source")
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
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+` ORDER BY `+visitOrder(s.w.db.DriverName())+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Inspection, 0, len(rows))
	for _, r := range rows {
		out = append(out, inspectionFromRow(r))
	}
	if err := attachInspectionPhotos(ctx, s.w.db, id.OrgID, out); err != nil {
		return nil, writeErr(err)
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
	if _, ok := fields["date"]; ok {
		if err := checkInspectionDate(p.Date); err != nil {
			return nil, err
		}
	}
	var out *wv1.Inspection
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		if _, err := t.amendRecord("inspection", p.Id, fields); err != nil {
			return err
		}
		out, err = t.inspectionWithPhotos(p.Id)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.UpdateInspectionResponse{Inspection: out}, nil
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

// attachInspectionPhotos fills in the photos of the given inspections.
func attachInspectionPhotos(ctx context.Context, q queryer, orgID string, inspections []*wv1.Inspection) error {
	ids := make([]string, 0, len(inspections))
	for _, in := range inspections {
		ids = append(ids, in.Id)
	}
	photos, err := photosByOwner(ctx, q, orgID, ownerInspection, ids)
	if err != nil {
		return err
	}
	for _, in := range inspections {
		in.Photos = photos[in.Id]
	}
	return nil
}

// inspectionWithPhotos renders one inspection row inside a writer transaction.
func (t *txWriter) inspectionWithPhotos(id string) (*wv1.Inspection, error) {
	row, err := t.row("inspection", id)
	if err != nil {
		return nil, err
	}
	out := inspectionFromRow(row)
	return out, attachInspectionPhotos(t.ctx, t.tx, t.id.OrgID, []*wv1.Inspection{out})
}

// AddInspectionPhoto stores the image as a photo of the inspection: the
// image in the blob store, a record with a thumbnail in the sync feed.
func (s *InspectionService) AddInspectionPhoto(ctx context.Context, req *wv1.AddInspectionPhotoRequest) (*wv1.AddInspectionPhotoResponse, error) {
	id, _ := auth.FromContext(ctx)
	if !strings.HasPrefix(req.Photo, "data:image/") {
		return nil, invalid("photo must be a data URL (data:image/...)")
	}
	if sniffPhotoType(decodeDataURL(req.Photo, MaxPhotoBytes)) == "" {
		return nil, invalid("photo must be a base64 data URL of a JPEG, PNG or WebP image of at most 5 MiB")
	}
	photos := s.w.photos
	if photos.blobs == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "photo storage is not configured")
	}
	// Decoded and stored before the write lock is taken (see prepareInline),
	// once the inspection is known to exist; the transaction checks again.
	found, err := queryMaps(ctx, s.w.db, `SELECT id FROM inspection WHERE id = ? AND organization_id = ? AND deleted = FALSE`,
		req.InspectionId, id.OrgID)
	if err != nil {
		return nil, writeErr(err)
	}
	if len(found) == 0 {
		return nil, writeErr(storage.ErrNotFound)
	}
	ctx, release, err := photos.prepareInline(ctx, id.OrgID, []inlineImage{{ownerInspection, req.InspectionId, req.Photo}}, 1)
	if err != nil {
		return nil, writeErr(err)
	}
	defer release()
	var out *wv1.Inspection
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("inspection", req.InspectionId)
		if err != nil {
			return err
		}
		scope, err := t.recordScope("inspection", cur)
		if err != nil {
			return err
		}
		same, err := queryPhotos(t.ctx, t.tx, `owner_id = ? AND owner_entity = ? AND organization_id = ?
			AND legacy_key = ? AND deleted = FALSE`, req.InspectionId, ownerInspection, id.OrgID, legacyKey(req.Photo))
		if err != nil {
			return err
		}
		if len(same) == 0 {
			cause := &wv1.Change{ScopeId: scope, AuthorId: id.UserID}
			if err := photos.createFromInline(t.ctx, t.tx, cause, id.OrgID, ownerInspection, req.InspectionId, req.Photo, photos.now()); err != nil {
				return err
			}
		}
		out, err = t.inspectionWithPhotos(req.InspectionId)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.AddInspectionPhotoResponse{Inspection: out}, nil
}

// RemoveInspectionPhoto deletes a photo of the inspection, named by its id
// or by the data URL it was added with.
func (s *InspectionService) RemoveInspectionPhoto(ctx context.Context, req *wv1.RemoveInspectionPhotoRequest) (*wv1.RemoveInspectionPhotoResponse, error) {
	id, _ := auth.FromContext(ctx)
	if req.Photo == "" {
		return nil, invalid("photo is required: a photo id or the data URL it was added with")
	}
	var out *wv1.Inspection
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("inspection", req.InspectionId)
		if err != nil {
			return err
		}
		scope, err := t.recordScope("inspection", cur)
		if err != nil {
			return err
		}
		matches, err := queryPhotos(t.ctx, t.tx, `owner_id = ? AND owner_entity = ? AND organization_id = ?
			AND (id = ? OR legacy_key = ?) AND deleted = FALSE`,
			req.InspectionId, ownerInspection, id.OrgID, req.Photo, legacyKey(req.Photo))
		if err != nil {
			return err
		}
		for i := range matches {
			if err := t.delPhoto(&matches[i], scope); err != nil {
				return err
			}
		}
		if len(matches) == 0 {
			if err := t.removeInlineElement(cur, scope, req.Photo); err != nil {
				return err
			}
		}
		out, err = t.inspectionWithPhotos(req.InspectionId)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.RemoveInspectionPhotoResponse{Inspection: out}, nil
}

// delPhoto deletes a photo on behalf of the caller.
//
// The delete is merged per field like any other change, so it only takes
// effect when its stamp is greater than the stamp of the photo's "deleted"
// field. A fresh stamp from the server clock is not enough for that: the
// clock does not survive a restart, and the photo may have been written with
// a stamp ahead of it (a device clock up to sync.MaxDrift fast, or another
// clock of the same millisecond whose node id sorts higher). The delete
// would then lose the merge while the call reports success and the photo
// stays. Folding the photo's stamp into the clock first makes the delete
// win, the way the server's own photo deletes do (Photos.stampAfter).
func (t *txWriter) delPhoto(row *photoRow, scope string) error {
	return t.del("photo", row.ID, scope)
}

// removeInlineElement removes an element of photo_keys that has no photo
// record (an inline value of a 0.4.x app that is not an image the server
// could convert). Removing a value the inspection does not have changes
// nothing.
func (t *txWriter) removeInlineElement(inspection map[string]any, scope, elem string) error {
	for _, have := range rset(inspection["photo_keys"]) {
		if have == elem {
			// No removed_tags: the server is the origin and tombstones every
			// add tag it currently holds.
			return t.upsert("inspection", rs(inspection["id"]), scope,
				map[string]any{"photo_keys": map[string]any{"remove": []string{elem}}})
		}
	}
	return nil
}

// visitOrder lists visits newest first. Visits recorded in the app all carry
// noon of their day, so several on one day are told apart by when they were
// recorded, and the id keeps the order stable beyond that.
func visitOrder(driver string) string {
	return tsSort(driver, "date") + ` DESC, ` + tsSort(driver, "created_at") + ` DESC, id DESC`
}
