package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// photoFixture is the API fixture with a filesystem blob store attached to
// the sync service and the writer, the way main wires them.
type photoFixture struct {
	*apiFixture
	photos  *Photos
	blobs   blob.Store
	blobDir string
}

func newPhotoFixture(t *testing.T) *photoFixture {
	t.Helper()
	f := newAPIFixture(t)
	cfg := &config.Config{}
	cfg.Blob.Backend = config.BlobFS
	cfg.Blob.BaseDir = filepath.Join(t.TempDir(), "blobs")
	blobs, err := blob.New(cfg)
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}
	photos := NewPhotos(f.db, wsync.NewHLC("srv"), blobs)
	f.sync.UsePhotos(photos)
	f.writer.UsePhotos(photos)
	return &photoFixture{apiFixture: f, photos: photos, blobs: blobs, blobDir: cfg.Blob.BaseDir}
}

// testImage draws a picture that is different for every seed, so two test
// photos never share their bytes.
func testImage(w, h int, seed uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: seed, A: 0xff})
		}
	}
	return img
}

func jpegBytes(t testing.TB, w, h int, seed uint8) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(w, h, seed), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngBytes(t testing.TB, w, h int, seed uint8) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(w, h, seed)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// webpBytes is the smallest thing that is recognisably a WebP file.
func webpBytes() []byte {
	return append([]byte("RIFF\x1a\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 32)...)
}

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func dataURL(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// inlineJPEG is a photo the way a 0.4.x app stores it.
func inlineJPEG(t testing.TB, seed uint8) string {
	t.Helper()
	return dataURL("image/jpeg", jpegBytes(t, 64, 48, seed))
}

func testThumb(t testing.TB) string {
	t.Helper()
	return dataURL("image/jpeg", jpegBytes(t, 20, 15, 1))
}

// photoPayload is the upsert an updated app pushes for a new photo.
func photoPayload(t testing.TB, ownerEntity, ownerID string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"owner_entity": ownerEntity, "owner_id": ownerID, "thumb": testThumb(t),
		"content_type": "image/jpeg", "size_bytes": 1234,
		"created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "deleted": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// photoDeletion is the delete of a photo of tenant A.
func photoDeletion(id string) *wv1.Change {
	return &wv1.Change{
		Entity: "photo", EntityId: id, ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_DELETE,
		PayloadJson: `{"deleted":1}`, Hlc: testHLC.Now(), AuthorId: "dev",
	}
}

// newVisit stores an inspection of tenant A's fixture hive and returns its id.
func (f *photoFixture) newVisit(t *testing.T) string {
	t.Helper()
	res, err := f.insp.CreateInspection(ctxA, &wv1.CreateInspectionRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatalf("create inspection: %v", err)
	}
	return res.Inspection.Id
}

var (
	ctxA = identityCtx("user-a", "org-a")
	ctxB = identityCtx("user-b", "org-b")
)

func (f *photoFixture) photo(t *testing.T, id string) *photoRow {
	t.Helper()
	rows, err := queryPhotos(context.Background(), f.db, `id = ?`, id)
	if err != nil {
		t.Fatalf("read photo: %v", err)
	}
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// photosOf lists every photo row of a record, deleted ones included.
func (f *photoFixture) photosOf(t *testing.T, ownerID string) []photoRow {
	t.Helper()
	rows, err := queryPhotos(context.Background(), f.db, `owner_id = ? ORDER BY id`, ownerID)
	if err != nil {
		t.Fatalf("read photos: %v", err)
	}
	return rows
}

func (f *photoFixture) livePhotosOf(t *testing.T, ownerID string) []photoRow {
	t.Helper()
	var live []photoRow
	for _, p := range f.photosOf(t, ownerID) {
		if !p.Deleted {
			live = append(live, p)
		}
	}
	return live
}

// logPayloads returns the logged payloads of one record, oldest first.
func (f *photoFixture) logPayloads(t *testing.T, entity, id string) []string {
	t.Helper()
	var out []string
	if err := f.db.Select(&out, f.db.Rebind(
		`SELECT payload FROM change_log WHERE entity = ? AND entity_id = ? ORDER BY seq`), entity, id); err != nil {
		t.Fatalf("read change log: %v", err)
	}
	return out
}

func (f *photoFixture) blobKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	if err := f.blobs.List(context.Background(), "", func(o blob.Object) error {
		keys = append(keys, o.Key)
		return nil
	}); err != nil {
		t.Fatalf("list blobs: %v", err)
	}
	return keys
}

func (f *photoFixture) blob(t *testing.T, key string) []byte {
	t.Helper()
	rc, _, err := f.blobs.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get blob %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// anyContains reports whether one of the strings contains the needle.
func anyContains(list []string, needle string) bool {
	for _, s := range list {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func TestPhotoSyncsAsARecordOfItsWorkspace(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	if err := push(f.sync, ctxA, change("photo", "photo-1", "apiary-a", photoPayload(t, ownerInspection, visit))); err != nil {
		t.Fatalf("push photo: %v", err)
	}
	row := f.photo(t, "photo-1")
	if row == nil || row.OrgID != "org-a" || row.OwnerID != visit || row.OwnerEntity != ownerInspection ||
		row.Thumb != testThumb(t) || row.ContentType != "image/jpeg" || row.Size != 1234 || row.CreatedAt.IsZero() {
		t.Fatalf("stored photo: %+v", row)
	}
	got := changesFor(f.pullAll(t, ctxA), "photo")
	if len(got) != 1 || got[0].EntityId != "photo-1" || !strings.Contains(got[0].PayloadJson, testThumb(t)) {
		t.Fatalf("another device must pull the photo with its thumbnail, got %+v", got)
	}
	if n := len(changesFor(f.pullAll(t, ctxB), "photo")); n != 0 {
		t.Fatalf("another workspace pulled %d photo changes", n)
	}

	// The server's own columns cannot be written through a change.
	if err := push(f.sync, ctxA, change("photo", "photo-1", "apiary-a",
		`{"blob_key":"org-b/photos/x","blob_type":"text/html","legacy_key":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	if row := f.photo(t, "photo-1"); row.BlobKey != "" || row.BlobType != "" || row.LegacyKey != "" {
		t.Fatalf("a change wrote server-only columns: %+v", row)
	}

	// Another workspace can neither change nor delete it.
	foreign := photoDeletion("photo-1")
	foreign.ScopeId = "apiary-b"
	res := pushRes(t, f.sync, ctxB, change("photo", "photo-1", "apiary-b", `{"thumb":""}`), foreign)
	wantRejected(t, res, map[int32]string{0: rejWrongTenant, 1: rejWrongTenant})
	if row := f.photo(t, "photo-1"); row.Deleted || row.Thumb == "" {
		t.Fatalf("a foreign workspace changed the photo: %+v", row)
	}
}

func TestPhotoPayloadIsValidated(t *testing.T) {
	f := newPhotoFixture(t)
	big := dataURL("image/jpeg", append(jpegBytes(t, 20, 15, 1), bytes.Repeat([]byte{0}, maxThumbBytes)...))
	cases := map[string]string{
		"thumb that is not an image":  `{"thumb":"data:text/html;base64,PGh0bWw+"}`,
		"thumb that is a remote URL":  `{"thumb":"https://example.org/x.jpg"}`,
		"thumb with a lying type":     `{"thumb":"` + dataURL("image/jpeg", []byte("<svg onload=alert(1)>")) + `"}`,
		"oversized thumb":             `{"thumb":"` + big + `"}`,
		"unknown owner kind":          `{"owner_entity":"task"}`,
		"content type outside images": `{"content_type":"text/html"}`,
		"size above the cap":          `{"size_bytes":99999999}`,
		"negative size":               `{"size_bytes":-1}`,
	}
	for name, payload := range cases {
		res := pushRes(t, f.sync, ctxA, change("photo", "bad-"+name, "apiary-a", payload))
		if len(res.Rejected) != 1 || res.Rejected[0].Code != rejInvalid {
			t.Errorf("%s: want the change rejected as invalid, got %+v", name, res.Rejected)
		}
		if f.photo(t, "bad-"+name) != nil {
			t.Errorf("%s: the refused photo was stored", name)
		}
	}
	// PNG and WebP thumbnails and an empty one are fine.
	for i, thumb := range []string{dataURL("image/png", pngBytes(t, 8, 8, 1)), dataURL("image/webp", webpBytes()), ""} {
		payload, _ := json.Marshal(map[string]any{"owner_entity": ownerHive, "owner_id": "hive-a", "thumb": thumb})
		if err := push(f.sync, ctxA, change("photo", "ok-"+itoa(int64(i)), "apiary-a", string(payload))); err != nil {
			t.Errorf("thumb %d: %v", i, err)
		}
	}
}

// Deleting a photo erases its image data everywhere on the server, and the
// photo cannot come back.
func TestDeletingAPhotoErasesIt(t *testing.T) {
	f := newPhotoFixture(t)
	ctx := context.Background()
	visit := f.newVisit(t)
	thumb := testThumb(t)
	if err := push(f.sync, ctxA,
		change("photo", "photo-1", "apiary-a", photoPayload(t, ownerInspection, visit)),
		// A second logged change that carries the thumbnail again.
		change("photo", "photo-1", "apiary-a", `{"thumb":"`+thumb+`","size_bytes":4321}`),
		change("photo", "photo-2", "apiary-a", photoPayload(t, ownerInspection, visit)),
	); err != nil {
		t.Fatal(err)
	}
	full := jpegBytes(t, 320, 240, 7)
	f.upload(t, "photo-1", full)
	f.upload(t, "photo-2", full)
	key := f.photo(t, "photo-1").BlobKey
	if key != blob.PhotoKey("org-a", "photo-1") || !strings.HasPrefix(key, "org-a/") {
		t.Fatalf("blob key %q is not the workspace-prefixed key", key)
	}

	if err := push(f.sync, ctxA, photoDeletion("photo-1")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	row := f.photo(t, "photo-1")
	if !row.Deleted || row.Thumb != "" {
		t.Fatalf("after the delete: %+v", row)
	}
	for _, payload := range f.logPayloads(t, "photo", "photo-1") {
		if strings.Contains(payload, "data:image") {
			t.Fatalf("the change log still carries the thumbnail: %.80s", payload)
		}
	}
	// What a device pulls from scratch now: the photo without picture, then its delete.
	got := changesFor(f.pullAll(t, ctxA), "photo")
	var deleted bool
	for _, c := range got {
		if c.EntityId == "photo-1" && c.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("devices must be told about the delete: %+v", got)
	}

	// The blob goes right after the transaction.
	if err := f.photos.removeDeletedBlobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.blobs.Get(ctx, key); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("blob of the deleted photo: %v, want ErrNotFound", err)
	}
	if row := f.photo(t, "photo-1"); row.BlobKey != "" {
		t.Fatalf("row still names a blob: %+v", row)
	}
	// The other photo is untouched.
	other := f.photo(t, "photo-2")
	if other.Deleted || other.Thumb != thumb || !bytes.Equal(f.blob(t, other.BlobKey), full) {
		t.Fatalf("another photo was affected: %+v", other)
	}

	// Deletion is final: a later write does not bring the photo back and is
	// not handed to other devices.
	before := len(f.logPayloads(t, "photo", "photo-1"))
	if err := push(f.sync, ctxA, change("photo", "photo-1", "apiary-a", `{"deleted":0,"thumb":"`+thumb+`"}`)); err != nil {
		t.Fatalf("a write to a deleted photo is dropped, not refused: %v", err)
	}
	if row := f.photo(t, "photo-1"); !row.Deleted || row.Thumb != "" {
		t.Fatalf("a deleted photo came back: %+v", row)
	}
	if after := len(f.logPayloads(t, "photo", "photo-1")); after != before {
		t.Fatalf("the dropped change was logged (%d -> %d entries)", before, after)
	}
}

// A delete that loses against a newer write of the same field is not a
// delete: nothing may be erased.
func TestStalePhotoDeleteErasesNothing(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	stale := photoDeletion("photo-1") // stamped before the photo is written
	if err := push(f.sync, ctxA, change("photo", "photo-1", "apiary-a", photoPayload(t, ownerInspection, visit)), stale); err != nil {
		t.Fatal(err)
	}
	if row := f.photo(t, "photo-1"); row.Deleted || row.Thumb == "" {
		t.Fatalf("a stale delete erased the photo: %+v", row)
	}
}

func TestMakeThumb(t *testing.T) {
	decode := func(thumb string) image.Config {
		t.Helper()
		data := decodeDataURL(thumb, maxThumbBytes)
		if sniffPhotoType(data) != "image/jpeg" {
			t.Fatalf("thumb is not a JPEG data URL: %.60s", thumb)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if cfg := decode(makeThumb(jpegBytes(t, 640, 480, 1))); cfg.Width != 200 || cfg.Height != 150 {
		t.Errorf("landscape thumb is %dx%d, want 200x150", cfg.Width, cfg.Height)
	}
	if cfg := decode(makeThumb(pngBytes(t, 300, 900, 1))); cfg.Width != 66 || cfg.Height != 200 {
		t.Errorf("portrait thumb is %dx%d, want 66x200", cfg.Width, cfg.Height)
	}
	if cfg := decode(makeThumb(jpegBytes(t, 40, 30, 1))); cfg.Width != 40 || cfg.Height != 30 {
		t.Errorf("a small image is not enlarged, got %dx%d", cfg.Width, cfg.Height)
	}
	if thumb := makeThumb(jpegBytes(t, 2048, 1536, 1)); len(thumb) > maxThumbBytes {
		t.Errorf("thumb of a full-size photo has %d bytes, above the %d the server accepts", len(thumb), maxThumbBytes)
	}
	// WebP as browsers write it: lossy, and lossless with transparency
	// (testdata/photo-*.webp, made with cwebp).
	lossy, alpha := readTestdata(t, "photo-lossy.webp"), readTestdata(t, "photo-alpha.webp")
	if cfg := decode(makeThumb(lossy)); cfg.Width != 200 || cfg.Height != 100 {
		t.Errorf("WebP thumb is %dx%d, want 200x100", cfg.Width, cfg.Height)
	}
	webpThumb, err := jpeg.Decode(bytes.NewReader(decodeDataURL(makeThumb(alpha), maxThumbBytes)))
	if err != nil || webpThumb.Bounds().Dx() != 100 || webpThumb.Bounds().Dy() != 200 {
		t.Fatalf("thumb of a WebP with alpha: %v %v", webpThumb.Bounds(), err)
	}
	if r, g, b, _ := webpThumb.At(20, 100).RGBA(); r>>8 > 40 || g>>8 < 90 || b>>8 > 40 {
		t.Errorf("opaque WebP pixel became %d,%d,%d, want green", r>>8, g>>8, b>>8)
	}
	if r, g, b, _ := webpThumb.At(80, 100).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent WebP pixel became %d,%d,%d, want white", r>>8, g>>8, b>>8)
	}

	for name, data := range map[string][]byte{"truncated webp": webpBytes(), "garbage": []byte("not an image"), "empty": nil} {
		if thumb := makeThumb(data); thumb != "" {
			t.Errorf("%s: want no thumbnail, got %.40s", name, thumb)
		}
	}

	// Transparent areas become white, not black.
	img := image.NewNRGBA(image.Rect(0, 0, 400, 400))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(bytes.NewReader(decodeDataURL(makeThumb(buf.Bytes()), maxThumbBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := got.At(100, 100).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent pixel became %d,%d,%d", r>>8, g>>8, b>>8)
	}
}

func TestDecodeDataURL(t *testing.T) {
	data := []byte("hello photo")
	for name, in := range map[string]string{
		"padded":   "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
		"unpadded": "data:image/jpeg;base64," + base64.RawStdEncoding.EncodeToString(data),
	} {
		if got := decodeDataURL(in, 100); !bytes.Equal(got, data) {
			t.Errorf("%s: got %q", name, got)
		}
	}
	for name, in := range map[string]string{
		"not a data URL": "https://example.org/a.jpg",
		"not base64":     "data:image/svg+xml,<svg/>",
		"no comma":       "data:image/jpeg;base64",
		"bad base64":     "data:image/jpeg;base64,@@@@",
		"empty":          "data:image/jpeg;base64,",
		"over the limit": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 101)),
	} {
		if got := decodeDataURL(in, 100); got != nil {
			t.Errorf("%s: want nil, got %d bytes", name, len(got))
		}
	}
}

// fileCount counts regular files below dir.
func fileCount(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}
