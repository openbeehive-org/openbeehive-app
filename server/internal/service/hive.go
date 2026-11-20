package service

import (
	"context"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type HiveService struct {
	openbeehivev1connect.UnimplementedHiveServiceHandler
	repo storage.HiveRepo
	w    *Writer
}

func NewHiveService(repo storage.HiveRepo, w *Writer) *HiveService {
	return &HiveService{repo: repo, w: w}
}

func hiveToProto(m *storage.Hive) *wv1.Hive {
	return &wv1.Hive{
		Id:             m.ID,
		OrganizationId: m.OrgID,
		ApiaryId:       m.ApiaryID,
		Name:           m.Name,
		Type:           wv1.HiveType(m.Type),
		Status:         wv1.HiveStatus(m.Status),
		Boxes:          m.Boxes,
		ColonyOrigin:   m.ColonyOrigin,
		Note:           m.Note,
		QrCode:         m.QRCode,
		CreatedAt:      timestamppb.New(m.CreatedAt),
		UpdatedAt:      timestamppb.New(m.UpdatedAt),
	}
}

// attachHivePictures fills in the picture of the given hives: the newest
// photo that belongs to the hive itself.
func attachHivePictures(ctx context.Context, q queryer, orgID string, hives []*wv1.Hive) error {
	ids := make([]string, 0, len(hives))
	for _, h := range hives {
		ids = append(ids, h.Id)
	}
	photos, err := photosByOwner(ctx, q, orgID, ownerHive, ids)
	if err != nil {
		return err
	}
	for _, h := range hives {
		if list := photos[h.Id]; len(list) > 0 {
			h.Picture = list[len(list)-1]
		}
	}
	return nil
}

// hiveResponse loads a hive with its picture.
func (s *HiveService) hiveResponse(ctx context.Context, orgID, id string) (*wv1.Hive, error) {
	m, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	out := hiveToProto(m)
	return out, attachHivePictures(ctx, s.w.db, orgID, []*wv1.Hive{out})
}

// CreateHive mirrors the app: hive row, open placement interval and a
// CREATED event, so the location history starts on day one.
func (s *HiveService) CreateHive(ctx context.Context, req *wv1.CreateHiveRequest) (*wv1.CreateHiveResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	if req.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEmptyName.Error()).WithCause(errEmptyName)
	}
	if req.ApiaryId == "" {
		return nil, invalid("apiary_id is required")
	}
	var hid string
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		ok, err := t.apiaryExists(req.ApiaryId)
		if err != nil {
			return err
		}
		if !ok {
			return storage.ErrNotFound
		}
		hid, err = t.createHive(hiveInput{
			apiaryID: req.ApiaryId, name: req.Name, typ: int32(req.Type), boxes: req.Boxes,
			colonyOrigin: req.ColonyOrigin, note: req.Note, at: jsTimeNow(),
		})
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	out, err := s.hiveResponse(ctx, id.OrgID, hid)
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.CreateHiveResponse{Hive: out}, nil
}

func (s *HiveService) GetHive(ctx context.Context, req *wv1.GetHiveRequest) (*wv1.GetHiveResponse, error) {
	id, _ := auth.FromContext(ctx)
	out, err := s.hiveResponse(ctx, id.OrgID, req.Id)
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.GetHiveResponse{Hive: out}, nil
}

func (s *HiveService) ListHives(ctx context.Context, req *wv1.ListHivesRequest) (*wv1.ListHivesResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Page)
	rows, total, err := s.repo.List(ctx, id.OrgID, req.ApiaryId, limit, offset)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Hive, 0, len(rows))
	for i := range rows {
		out = append(out, hiveToProto(&rows[i]))
	}
	if err := attachHivePictures(ctx, s.w.db, id.OrgID, out); err != nil {
		return nil, writeErr(err)
	}
	return &wv1.ListHivesResponse{Hives: out, Page: pageResponse(total, offset, len(rows), limit)}, nil
}

// hiveUpdatable lists the fields UpdateHive may change.
var hiveUpdatable = []string{"name", "type", "status", "boxes", "colony_origin", "note", "qr_code"}

// UpdateHive changes only the fields sent (see UpdateHiveRequest). It does
// not move the hive (use RelocateHive, which keeps the location history) and
// does not touch its picture.
func (s *HiveService) UpdateHive(ctx context.Context, req *wv1.UpdateHiveRequest) (*wv1.UpdateHiveResponse, error) {
	id, _ := auth.FromContext(ctx)
	p := req.Hive
	if p == nil || p.Id == "" {
		return nil, invalid("hive.id is required")
	}
	fields, err := updatePayload(p.ProtoReflect(), req.UpdateMask, hiveUpdatable)
	if err != nil {
		return nil, err
	}
	if err := requireValue(fields, "name", "status"); err != nil {
		return nil, err
	}
	if p.Boxes < 0 {
		return nil, invalid("boxes must not be negative")
	}
	fields["updated_at"] = jsTimeNow()
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		h, err := t.hive(p.Id)
		if err != nil {
			return err
		}
		return t.upsert("hive", p.Id, rs(h["apiary_id"]), fields)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	out, err := s.hiveResponse(ctx, id.OrgID, p.Id)
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.UpdateHiveResponse{Hive: out}, nil
}

// DeleteHive soft-deletes the hive and its open tasks: a task for a hive that
// no longer exists can never be done. The hive's records keep their rows.
func (s *HiveService) DeleteHive(ctx context.Context, req *wv1.DeleteHiveRequest) (*wv1.DeleteHiveResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		h, err := t.hive(req.Id)
		if err != nil {
			return err
		}
		if err := t.delOpenTasks(req.Id); err != nil {
			return err
		}
		return t.del("hive", req.Id, rs(h["apiary_id"]))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteHiveResponse{}, nil
}

// RelocateHive mirrors the app's move: closes the open placement interval,
// opens a new one at the target apiary and records a MOVED event.
func (s *HiveService) RelocateHive(ctx context.Context, req *wv1.RelocateHiveRequest) (*wv1.RelocateHiveResponse, error) {
	id, _ := auth.FromContext(ctx)
	if req.TargetApiaryId == "" {
		return nil, invalid("target_apiary_id is required")
	}
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		h, err := t.hive(req.Id)
		if err != nil {
			return err
		}
		ok, err := t.apiaryExists(req.TargetApiaryId)
		if err != nil {
			return err
		}
		if !ok {
			return storage.ErrNotFound
		}
		return t.moveHive(req.Id, rs(h["apiary_id"]), req.TargetApiaryId, tsOrNow(req.Date))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	out, err := s.hiveResponse(ctx, id.OrgID, req.Id)
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.RelocateHiveResponse{Hive: out}, nil
}
