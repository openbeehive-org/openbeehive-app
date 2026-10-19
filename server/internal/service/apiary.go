// Package service holds the Connect-RPC handlers. They map between the
// generated protobuf types and the storage models and enforce tenant
// isolation via the identity from the context.
//
// Reads go to the tables directly; every write goes through Writer, i.e.
// through the same merge and change log as SyncService.Push, so API writes
// reach devices and merge with theirs.
//
// The imports from internal/gen are produced by `make proto` (buf generate).
package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// Implements the generated openbeehivev1connect.ApiaryServiceHandler interface.
type ApiaryService struct {
	openbeehivev1connect.UnimplementedApiaryServiceHandler
	repo storage.ApiaryRepo
	w    *Writer
}

func NewApiaryService(repo storage.ApiaryRepo, w *Writer) *ApiaryService {
	return &ApiaryService{repo: repo, w: w}
}

func (s *ApiaryService) CreateApiary(
	ctx context.Context, req *connect.Request[wv1.CreateApiaryRequest],
) (*connect.Response[wv1.CreateApiaryResponse], error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEmptyName)
	}
	aid := uuid.NewString()
	now := jsTimeNow()
	// scope = own id: a new apiary opens its own sync scope, as in the app.
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		return t.upsert("apiary", aid, aid, map[string]any{
			"organization_id": id.OrgID, "name": req.Msg.Name, "address": req.Msg.Address,
			"lat": req.Msg.Lat, "lng": req.Msg.Lng, "note": req.Msg.Note,
			"created_at": now, "updated_at": now, "deleted": 0,
		})
	})
	if err != nil {
		return nil, writeErr(err)
	}
	m, err := s.repo.Get(ctx, id.OrgID, aid)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&wv1.CreateApiaryResponse{Apiary: toProto(m, 0)}), nil
}

func (s *ApiaryService) GetApiary(
	ctx context.Context, req *connect.Request[wv1.GetApiaryRequest],
) (*connect.Response[wv1.GetApiaryResponse], error) {
	id, _ := auth.FromContext(ctx)
	m, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	cnt, _ := s.repo.HiveCount(ctx, id.OrgID, m.ID)
	return connect.NewResponse(&wv1.GetApiaryResponse{Apiary: toProto(m, cnt)}), nil
}

func (s *ApiaryService) ListApiaries(
	ctx context.Context, req *connect.Request[wv1.ListApiariesRequest],
) (*connect.Response[wv1.ListApiariesResponse], error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Msg.Page)
	rows, total, err := s.repo.List(ctx, id.OrgID, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*wv1.Apiary, 0, len(rows))
	for i := range rows {
		cnt, _ := s.repo.HiveCount(ctx, id.OrgID, rows[i].ID)
		out = append(out, toProto(&rows[i], cnt))
	}
	return connect.NewResponse(&wv1.ListApiariesResponse{
		Apiaries: out,
		Page:     pageResponse(total, offset, len(rows), limit),
	}), nil
}

func (s *ApiaryService) UpdateApiary(
	ctx context.Context, req *connect.Request[wv1.UpdateApiaryRequest],
) (*connect.Response[wv1.UpdateApiaryResponse], error) {
	id, _ := auth.FromContext(ctx)
	if _, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id); err != nil {
		return nil, mapErr(err)
	}
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		return t.upsert("apiary", req.Msg.Id, req.Msg.Id, map[string]any{
			"name": req.Msg.Name, "address": req.Msg.Address, "lat": req.Msg.Lat, "lng": req.Msg.Lng,
			"note": req.Msg.Note, "updated_at": jsTimeNow(),
		})
	})
	if err != nil {
		return nil, writeErr(err)
	}
	m, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	cnt, _ := s.repo.HiveCount(ctx, id.OrgID, m.ID)
	return connect.NewResponse(&wv1.UpdateApiaryResponse{Apiary: toProto(m, cnt)}), nil
}

// DeleteApiary soft-deletes the apiary (like the app). Its hives keep their
// rows and stay reachable through the API and sync.
func (s *ApiaryService) DeleteApiary(
	ctx context.Context, req *connect.Request[wv1.DeleteApiaryRequest],
) (*connect.Response[wv1.DeleteApiaryResponse], error) {
	id, _ := auth.FromContext(ctx)
	if _, err := s.repo.Get(ctx, id.OrgID, req.Msg.Id); err != nil {
		return nil, mapErr(err)
	}
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		return t.del("apiary", req.Msg.Id, req.Msg.Id)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return connect.NewResponse(&wv1.DeleteApiaryResponse{}), nil
}

// --- Helpers ---

func toProto(m *storage.Apiary, hiveCount int) *wv1.Apiary {
	return &wv1.Apiary{
		Id:             m.ID,
		OrganizationId: m.OrgID,
		Name:           m.Name,
		Address:        m.Address,
		Lat:            m.Lat,
		Lng:            m.Lng,
		Note:           m.Note,
		HiveCount:      i32(int64(hiveCount)),
		CreatedAt:      timestamppb.New(m.CreatedAt),
		UpdatedAt:      timestamppb.New(m.UpdatedAt),
	}
}

// pageParams: page_size (default 50, max 500) and page_token as a decimal
// offset, as returned in PageResponse.next_page_token.
func pageParams(p *wv1.PageRequest) (limit, offset int) {
	limit = 50
	if p != nil && p.PageSize > 0 {
		limit = int(p.PageSize)
	}
	if limit > 500 {
		limit = 500
	}
	if p != nil && p.PageToken != "" {
		offset, _ = strconv.Atoi(p.PageToken)
		if offset < 0 {
			offset = 0
		}
	}
	return limit, offset
}

// pageResponse reports the total and the token for the next page ("" when
// this page was the last one).
func pageResponse(total, offset, got, limit int) *wv1.PageResponse {
	r := &wv1.PageResponse{Total: i32(int64(total))}
	if got == limit && offset+got < total {
		r.NextPageToken = strconv.Itoa(offset + got)
	}
	return r
}

func jsTimeNow() string { return jsTime(time.Now()) }

func mapErr(err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

var errEmptyName = errors.New("name must not be empty")
