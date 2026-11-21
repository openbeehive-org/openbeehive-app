package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Photos.
//
// A photo is a synced record ("photo" in entityCols) that belongs to an
// inspection or to a hive (the hive's picture). The record carries a small
// thumbnail, so every device can show the photo at once and offline. The
// full-size image is not part of the sync feed: the device that took the
// photo uploads it to /photos/<id> when it is online, other devices fetch it
// from there the first time someone opens the photo (photo_http.go).
//
// Deleting a photo is final. When the delete is applied (photo_sync.go) the
// thumbnail is erased from the row and from the change log, and the blob is
// removed right after the transaction (photo_sweep.go). Later changes to a
// deleted photo are dropped, so it cannot come back half-empty.

const (
	// MaxPhotoBytes caps a full-size image.
	MaxPhotoBytes = 5 << 20
	// maxThumbBytes caps the inline thumbnail (a data URL). The app sends a
	// few KB; the cap stays below what a TEXT column holds on MariaDB.
	maxThumbBytes = 48 << 10

	ownerInspection = "inspection"
	ownerHive       = "hive"

	// PhotoPathPrefix is where the transfer endpoints are mounted.
	PhotoPathPrefix = "/photos/"
)

// photoTypes are the image formats accepted as full-size images, keyed by
// the content type found in the bytes themselves.
var photoTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// sniffPhotoType returns the image type the data starts with, or "" when it
// is not one of the accepted formats. What a client claims is never used.
func sniffPhotoType(data []byte) string {
	if t := http.DetectContentType(data); photoTypes[t] {
		return t
	}
	return ""
}

// Photos holds what the photo code needs besides the transaction at hand:
// the server clock for the changes it derives and the blob store. blobs is
// nil in setups without one (some tests); everything that needs it is then
// skipped.
type Photos struct {
	db    *sqlx.DB
	hlc   *wsync.HLC
	blobs blob.Store
	// wake nudges the background worker after a photo was deleted.
	wake chan struct{}
	// wakeDelay lets the transaction that deleted a photo commit before the
	// worker looks for its blob.
	wakeDelay time.Duration
	// now is the wall clock, replaceable in tests.
	now func() time.Time
	// OrphanGrace is how long a blob that no photo row knows is kept (see
	// photo_sweep.go).
	OrphanGrace time.Duration
	// inflight counts, per blob key, the requests that prepared that blob
	// and have not finished yet (see prepareInline).
	inflightMu sync.Mutex
	inflight   map[string]int
}

func NewPhotos(db *sqlx.DB, hlc *wsync.HLC, blobs blob.Store) *Photos {
	return &Photos{
		db: db, hlc: hlc, blobs: blobs,
		wake: make(chan struct{}, 1), wakeDelay: 2 * time.Second, now: time.Now,
		OrphanGrace: DefaultOrphanGrace,
	}
}

type photosKey struct{}

// withPhotos makes p available to the write path of this request.
func withPhotos(ctx context.Context, p *Photos) context.Context {
	return context.WithValue(ctx, photosKey{}, p)
}

func photosFrom(ctx context.Context) *Photos {
	p, _ := ctx.Value(photosKey{}).(*Photos)
	return p
}

// stampAfter returns a server stamp that wins over cur, the stamp currently
// on a field: what the server derives must not lose against the state it was
// derived from.
func (p *Photos) stampAfter(cur string) string {
	_ = p.hlc.Recv(cur)
	return p.hlc.Now()
}

// photoRow is a photo as the server code reads it.
type photoRow struct {
	ID, OrgID            string
	OwnerEntity, OwnerID string
	Thumb, ContentType   string
	Size                 int64
	CreatedAt            time.Time
	Deleted              bool
	Clock                wsync.FieldClock
	BlobKey, BlobType    string
	BlobSize             int64
	LegacyKey            string
}

const photoCols = `id, organization_id, owner_entity, owner_id, content_type, size_bytes, created_at,
	deleted, field_hlc, blob_key, blob_type, blob_size, legacy_key, thumb`

func photoFromMap(r map[string]any) photoRow {
	created, _ := rtime(r["created_at"])
	return photoRow{
		ID: rs(r["id"]), OrgID: rs(r["organization_id"]),
		OwnerEntity: rs(r["owner_entity"]), OwnerID: rs(r["owner_id"]),
		Thumb: rs(r["thumb"]), ContentType: rs(r["content_type"]), Size: ri(r["size_bytes"]),
		CreatedAt: created, Deleted: rb(r["deleted"]),
		Clock:   wsync.ParseFieldClock(rs(r["field_hlc"])),
		BlobKey: rs(r["blob_key"]), BlobType: rs(r["blob_type"]), BlobSize: ri(r["blob_size"]),
		LegacyKey: rs(r["legacy_key"]),
	}
}

func queryPhotos(ctx context.Context, q queryer, where string, args ...any) ([]photoRow, error) {
	rows, err := queryMaps(ctx, q, `SELECT `+photoCols+` FROM photo WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	out := make([]photoRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, photoFromMap(r))
	}
	return out, nil
}

// photoByID loads one photo of the workspace, deleted or not.
func photoByID(ctx context.Context, q queryer, orgID, id string) (*photoRow, error) {
	rows, err := queryPhotos(ctx, q, `id = ? AND organization_id = ?`, id, orgID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// livePhotosOf returns the photos of one record, oldest first.
func livePhotosOf(ctx context.Context, q queryer, orgID, ownerEntity, ownerID string) ([]photoRow, error) {
	rows, err := queryPhotos(ctx, q, `owner_id = ? AND owner_entity = ? AND organization_id = ? AND deleted = FALSE`,
		ownerID, ownerEntity, orgID)
	sortPhotos(rows)
	return rows, err
}

// sortPhotos puts photos in the order they were taken: oldest first, a photo
// without a creation time before all others.
//
// created_at has millisecond precision (devices and the server write it that
// way on every backend), and several photos of one record are often created
// within the same millisecond: a device that adds a few at once, or the
// server converting the images of one 0.4.x push. The stamp of the write
// that set created_at breaks those ties. Stamps of one clock strictly
// increase, so photos created on the same device or server keep their
// order; the id is the last resort for photos from different devices. A
// random id alone would list photos of the same millisecond in random order.
// app/src/lib/local/repo.ts (photoRecords) sorts the same way.
func sortPhotos(rows []photoRow) {
	slices.SortFunc(rows, func(a, b photoRow) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt),
			cmp.Compare(a.Clock["created_at"], b.Clock["created_at"]),
			cmp.Compare(a.ID, b.ID))
	})
}

// toProto renders a photo for API responses. The path is set once the
// full-size image has been uploaded; type and size are then the ones found
// in the stored bytes rather than what the device announced.
func (r *photoRow) toProto() *wv1.Photo {
	out := &wv1.Photo{
		Id: r.ID, Thumbnail: r.Thumb, ContentType: r.ContentType, SizeBytes: r.Size,
	}
	if !r.CreatedAt.IsZero() {
		out.CreatedAt = rt(r.CreatedAt)
	}
	if r.BlobKey != "" {
		out.Path = PhotoPathPrefix + r.ID
		out.ContentType, out.SizeBytes = r.BlobType, r.BlobSize
	}
	return out
}

// photosByOwner returns the live photos of many records of one kind in a
// single query, grouped by owner and oldest first.
func photosByOwner(ctx context.Context, q queryer, orgID, ownerEntity string, ownerIDs []string) (map[string][]*wv1.Photo, error) {
	out := map[string][]*wv1.Photo{}
	if len(ownerIDs) == 0 {
		return out, nil
	}
	query, args, err := sqlx.In(`owner_id IN (?) AND owner_entity = ? AND organization_id = ? AND deleted = FALSE`,
		ownerIDs, ownerEntity, orgID)
	if err != nil {
		return nil, err
	}
	rows, err := queryPhotos(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	sortPhotos(rows)
	for i := range rows {
		out[rows[i].OwnerID] = append(out[rows[i].OwnerID], rows[i].toProto())
	}
	return out, nil
}

// --- inline images (data URLs) ---

func isDataURL(s string) bool { return strings.HasPrefix(s, "data:") }

// legacyKey identifies an inline image without keeping it.
func legacyKey(dataURL string) string {
	sum := sha256.Sum256([]byte(dataURL))
	return hex.EncodeToString(sum[:])
}

// decodeDataURL returns the bytes of a base64 data URL, or nil when it is
// not one or decodes to more than limit bytes.
func decodeDataURL(s string, limit int) []byte {
	meta, body, ok := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	if !isDataURL(s) || !ok || !strings.HasSuffix(meta, ";base64") {
		return nil
	}
	if base64.StdEncoding.DecodedLen(len(body)) > limit+3 {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(body); err != nil {
			return nil
		}
	}
	if len(data) == 0 || len(data) > limit {
		return nil
	}
	return data
}

// --- change-log payloads ---

// editPayloads rewrites the logged payloads of one record. edit receives the
// decoded payload and the stamp of its change and reports whether it changed
// the payload; untouched entries are not written. Entries are found through
// the workspace index and then narrowed down, so the payloads of other
// records are not read.
func editPayloads(ctx context.Context, tx *sqlx.Tx, orgID, entity, entityID string, edit func(fields map[string]json.RawMessage, stamp string) bool) error {
	rows, err := queryMaps(ctx, tx, `SELECT id, hlc, payload FROM change_log
		WHERE org_id = ? AND entity = ? AND entity_id = ?`, orgID, entity, entityID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(rs(r["payload"])), &fields) != nil || fields == nil {
			continue
		}
		if !edit(fields, rs(r["hlc"])) {
			continue
		}
		payload, err := marshalPlain(fields)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE change_log SET payload = ? WHERE id = ?`),
			payload, rs(r["id"])); err != nil {
			return err
		}
	}
	return nil
}

// marshalPlain encodes like json.Marshal without escaping <, > and &, the
// way payloads are stored.
func marshalPlain(v any) (string, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(sb.String(), "\n"), nil
}
