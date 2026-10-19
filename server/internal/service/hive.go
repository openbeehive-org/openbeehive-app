package service

import (
	"context"

	"connectrpc.com/connect"
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

// CreateHive mirrors the app: hive row, open placement interval and a
// CREATED event, so the location history starts on day one.
func (s *HiveService) CreateHive(ctx context.Context, req *connect.Request[wv1.CreateHiveRequest]) (*connect.Response[wv1.CreateHiveResponse], error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEmptyName)
	}
	if req.Msg.ApiaryId == "" {
		return nil, invalid("apiary_id is required")
	}
	var hid string
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		ok, err := t.apiaryExists(req.Msg.ApiaryId)
		if err != nil {
			return err
		}
		if !ok {
			return storage.ErrNotFound
		}
		hid, err = t.createHive(req.Msg.ApiaryId, req.Msg.Name, int32(req.Msg.Type), req.Msg.Boxes,
			req.Msg.ColonyOrigin, req.Msg.Note)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	m, err := s.repo.Get(ctx, id.OrgID, hid)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&wv1.CreateHiveResponse{Hive: hiveToProto(m)}), nil
}

func (s *HiveService) GetHive(ctx context.Context, req *connect.Request[wv1.GetHiveRequest]) (*connect.Response[wv1.GetHiveResponse], error) {
	id, _ := auth.FromContext(ctx)
	m, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&wv1.GetHiveResponse{Hive: hiveToProto(m)}), nil
}

func (s *HiveService) ListHives(ctx context.Context, req *connect.Request[wv1.ListHivesRequest]) (*connect.Response[wv1.ListHivesResponse], error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Msg.Page)
	rows, total, err := s.repo.List(ctx, id.OrgID, req.Msg.ApiaryId, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*wv1.Hive, 0, len(rows))
	for i := range rows {
		out = append(out, hiveToProto(&rows[i]))
	}
	return connect.NewResponse(&wv1.ListHivesResponse{Hives: out, Page: pageResponse(total, offset, len(rows), limit)}), nil
}

// UpdateHive sets name, type, status, boxes, colony_origin, note and qr_code.
// It does not move the hive (use RelocateHive, which keeps the location
// history) and does not touch the photo the app stores.
func (s *HiveService) UpdateHive(ctx context.Context, req *connect.Request[wv1.UpdateHiveRequest]) (*connect.Response[wv1.UpdateHiveResponse], error) {
	id, _ := auth.FromContext(ctx)
	p := req.Msg.Hive
	if p == nil || p.Id == "" {
		return nil, invalid("hive.id is required")
	}
	m, err := s.repo.Get(ctx, id.OrgID, p.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		return t.upsert("hive", p.Id, m.ApiaryID, map[string]any{
			"name": p.Name, "type": int32(p.Type), "status": int32(p.Status), "boxes": p.Boxes,
			"colony_origin": p.ColonyOrigin, "note": p.Note, "qr_code": p.QrCode, "updated_at": jsTimeNow(),
		})
	})
	if err != nil {
		return nil, writeErr(err)
	}
	m, err = s.repo.Get(ctx, id.OrgID, p.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&wv1.UpdateHiveResponse{Hive: hiveToProto(m)}), nil
}

func (s *HiveService) DeleteHive(ctx context.Context, req *connect.Request[wv1.DeleteHiveRequest]) (*connect.Response[wv1.DeleteHiveResponse], error) {
	id, _ := auth.FromContext(ctx)
	m, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		return t.del("hive", req.Msg.Id, m.ApiaryID)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return connect.NewResponse(&wv1.DeleteHiveResponse{}), nil
}

// RelocateHive mirrors the app's move: closes the open placement interval,
// opens a new one at the target apiary and records a MOVED event.
func (s *HiveService) RelocateHive(ctx context.Context, req *connect.Request[wv1.RelocateHiveRequest]) (*connect.Response[wv1.RelocateHiveResponse], error) {
	id, _ := auth.FromContext(ctx)
	if req.Msg.TargetApiaryId == "" {
		return nil, invalid("target_apiary_id is required")
	}
	m, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		ok, err := t.apiaryExists(req.Msg.TargetApiaryId)
		if err != nil {
			return err
		}
		if !ok {
			return storage.ErrNotFound
		}
		return t.moveHive(m.ID, m.ApiaryID, req.Msg.TargetApiaryId, tsOrNow(req.Msg.Date))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	m, err = s.repo.Get(ctx, id.OrgID, req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&wv1.RelocateHiveResponse{Hive: hiveToProto(m)}), nil
}
