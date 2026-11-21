package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

// A 7000x7000 one-bit PNG is a few KB. Decoding it costs about a second and
// hundreds of MB; the header alone has to rule it out.
func TestThumbRefusesDecodingBombs(t *testing.T) {
	img := image.NewPaletted(image.Rect(0, 0, 7000, 7000), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	small := image.NewPaletted(image.Rect(0, 0, 3000, 3000), color.Palette{color.Black, color.White})
	var smallBuf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&smallBuf, small); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	for name, data := range map[string][]byte{"49 MP": buf.Bytes(), "9 MP of a few KB": smallBuf.Bytes()} {
		if thumb := makeThumb(data); thumb != "" {
			t.Errorf("%s: made a thumbnail", name)
		}
	}
	runtime.ReadMemStats(&after)
	if took, mb := time.Since(start), (after.TotalAlloc-before.TotalAlloc)>>20; took > 200*time.Millisecond || mb > 16 {
		t.Fatalf("refusing took %v and %d MB", took, mb)
	}
}

// lockProbe is a blob store that checks, on every Put, that no transaction
// holds the write lock: it takes the lock itself with a short deadline.
type lockProbe struct {
	blob.Store
	db     *sqlx.DB
	locked int
	puts   int
}

func (p *lockProbe) Put(ctx context.Context, key string, r io.Reader, size int64, typ string) error {
	p.puts++
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := p.db.ExecContext(probe, "UPDATE seq_counter SET val = val WHERE name = 'change'"); err != nil {
		p.locked++
	}
	return p.Store.Put(ctx, key, r, size, typ)
}

// Inline photos of a Push and of AddInspectionPhoto are decoded and stored
// before the write lock is taken, which every other write waits for.
func TestInlinePhotosAreStoredOutsideTheWriteLock(t *testing.T) {
	f := newPhotoFixture(t)
	probe := &lockProbe{Store: f.blobs, db: f.db}
	f.photos.blobs = probe
	visit := f.newVisit(t)
	if err := push(f.sync, legacyA, legacyAdd(visit, inlineJPEG(t, 1), inlineJPEG(t, 2)), legacyHivePhoto(inlineJPEG(t, 3))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: inlineJPEG(t, 4)}); err != nil {
		t.Fatal(err)
	}
	if probe.puts != 4 || probe.locked != 0 {
		t.Fatalf("%d blobs stored, %d of them under the write lock", probe.puts, probe.locked)
	}
	if n := len(f.livePhotosOf(t, visit)); n != 3 {
		t.Fatalf("%d photos of the visit, want 3", n)
	}
	// A repeated push stores nothing new.
	if err := push(f.sync, legacyA, legacyAdd(visit, inlineJPEG(t, 1))); err != nil {
		t.Fatal(err)
	}
	if probe.puts != 4 {
		t.Fatalf("a replay stored %d more blobs", probe.puts-4)
	}
}

// Thumbnails per Push are bounded; photos beyond the bound are stored
// without one.
func TestPushMakesBoundedThumbnails(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	var inline []string
	for i := range maxThumbsPerPush + 2 {
		inline = append(inline, inlineJPEG(t, uint8(i+10)))
	}
	if err := push(f.sync, legacyA, legacyAdd(visit, inline...)); err != nil {
		t.Fatal(err)
	}
	live := f.livePhotosOf(t, visit)
	with := 0
	for _, p := range live {
		if p.Thumb != "" {
			with++
		}
	}
	if len(live) != len(inline) || with != maxThumbsPerPush {
		t.Fatalf("%d photos, %d with a thumbnail; want %d and %d", len(live), with, len(inline), maxThumbsPerPush)
	}
}
