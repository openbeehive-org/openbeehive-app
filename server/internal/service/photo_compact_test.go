package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

// Photos together with the snapshot and the compaction of the change log.

// compactAll ages every log entry and compacts them away.
func (f *photoFixture) compactAll(t *testing.T) {
	t.Helper()
	var head int64
	if err := f.db.Get(&head, `SELECT COALESCE(MAX(seq), 0) FROM change_log`); err != nil {
		t.Fatal(err)
	}
	compactUpTo(t, f.sync, head)
}

// A device that starts from a snapshot gets each photo's record with its
// thumbnail; the full image stays on the server until it is opened.
func TestSnapshotCarriesPhotoRecordsNotImages(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	if err := push(f.sync, ctxA,
		change("photo", "photo-1", "apiary-a", photoPayload(t, ownerInspection, visit)),
		change("photo", "photo-2", "apiary-a", photoPayload(t, ownerHive, "hive-a")),
	); err != nil {
		t.Fatal(err)
	}
	full := jpegBytes(t, 320, 240, 9)
	f.upload(t, "photo-1", full)
	if err := push(f.sync, ctxA, photoDeletion("photo-2")); err != nil {
		t.Fatal(err)
	}

	rows, _, _ := snapshotAll(t, f.sync, ctxA)
	live, ok := rows["photo/photo-1"]
	if !ok {
		t.Fatalf("snapshot without the photo: %v", rows)
	}
	if live.fields["thumb"] != testThumb(t) || live.fields["owner_id"] != visit ||
		live.fields["owner_entity"] != ownerInspection || live.fields["content_type"] != "image/jpeg" {
		t.Fatalf("photo row in the snapshot: %v", live.fields)
	}
	for col := range live.fields {
		if strings.HasPrefix(col, "blob") {
			t.Errorf("the snapshot names the blob (%s); devices fetch images through /photos/", col)
		}
	}
	if gone := rows["photo/photo-2"]; gone.fields["deleted"] != float64(1) || gone.fields["thumb"] != "" {
		t.Fatalf("a deleted photo is a tombstone without thumbnail: %v", gone.fields)
	}
	// The image is there for the device to fetch on first view.
	if res := f.transfer(t, "GET", "/photos/photo-1", userA, nil); res.status != 200 || string(res.body) != string(full) {
		t.Fatalf("fetching the image: %d", res.status)
	}
}

// Deleting a photo whose changes were compacted away still erases its
// thumbnail and its image.
func TestDeletingAPhotoAfterCompaction(t *testing.T) {
	f := newPhotoFixture(t)
	ctx := context.Background()
	visit := f.newVisit(t)
	inline := inlineJPEG(t, 4)
	if err := push(f.sync, ctxA,
		change("photo", "photo-1", "apiary-a", photoPayload(t, ownerInspection, visit)),
		legacyAdd(visit, inline),
	); err != nil {
		t.Fatal(err)
	}
	f.upload(t, "photo-1", jpegBytes(t, 320, 240, 5))
	converted := f.livePhotosOf(t, visit)
	f.compactAll(t)

	if err := push(f.sync, ctxA, photoDeletion("photo-1")); err != nil {
		t.Fatal(err)
	}
	for _, p := range converted {
		if p.LegacyKey == legacyKey(inline) {
			f.deleteOnDevice(t, p)
		}
	}
	if err := f.photos.removeDeletedBlobs(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.photosOf(t, visit) {
		if !p.Deleted || p.Thumb != "" || p.BlobKey != "" {
			t.Fatalf("photo after its delete: %+v", p)
		}
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Fatalf("blobs left: %v", keys)
	}
	if visible(f.inlineSet(t, visit), inline) {
		t.Fatal("the inline copy must leave the row")
	}
	// What is logged after the horizon tells 0.4.x devices about the
	// delete, and nothing carries the images any more.
	var removes int
	for _, payload := range f.logPayloads(t, "inspection", visit) {
		if d, ok := photoKeysDelta(payload); ok && len(d.Remove) == 1 && d.Remove[0] == inline {
			removes++
		}
	}
	if removes != 1 {
		t.Fatalf("logged removes: %d, want 1", removes)
	}
	for _, payload := range f.logPayloads(t, "photo", "photo-1") {
		if strings.Contains(payload, "data:image") {
			t.Fatalf("logged photo change with its thumbnail: %.80s", payload)
		}
	}
}

// The retention of logged removes works whether compaction has already
// taken the remove (a retention shorter than legacyRemoveRetention), or
// only the add before it.
func TestLegacyRemoveRetentionAfterCompaction(t *testing.T) {
	for _, compactRemove := range []bool{false, true} {
		name := "add compacted"
		if compactRemove {
			name = "remove compacted"
		}
		t.Run(name, func(t *testing.T) {
			f := newPhotoFixture(t)
			visit := f.newVisit(t)
			gone := inlineJPEG(t, 1)
			add := legacyAdd(visit, gone)
			if err := push(f.sync, ctxA, add); err != nil {
				t.Fatal(err)
			}
			if !compactRemove {
				f.compactAll(t)
			}
			if err := push(f.sync, ctxA, legacyRemove(visit, gone, add.Hlc)); err != nil {
				t.Fatal(err)
			}
			if compactRemove {
				f.compactAll(t)
			} else if !anyContains(f.logPayloads(t, "inspection", visit), gone) {
				t.Fatal("the logged remove must name the image during the retention")
			}

			f.photos.now = func() time.Time { return time.Now().Add(legacyRemoveRetention + time.Hour) }
			if err := f.photos.eraseOldLegacyRemoves(t.Context()); err != nil {
				t.Fatal(err)
			}
			if anyContains(f.logPayloads(t, "inspection", visit), gone) {
				t.Fatal("the image is still in the log after the retention")
			}
			for _, p := range f.photosOf(t, visit) {
				if !p.Deleted || p.LegacyKey != "" {
					t.Fatalf("photo after the retention: %+v", p)
				}
			}
		})
	}
}

// A blob whose photo is gone from the log is not mistaken for a stray one.
func TestSweepKeepsImagesOfCompactedPhotos(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	if err := push(f.sync, ctxA, change("photo", "photo-1", "apiary-a", photoPayload(t, ownerInspection, visit))); err != nil {
		t.Fatal(err)
	}
	f.upload(t, "photo-1", jpegBytes(t, 64, 48, 3))
	f.compactAll(t)
	f.photos.now = func() time.Time { return time.Now().Add(2 * blobGrace) }
	if _, err := f.photos.sweepBlobs(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.blobs.Get(t.Context(), blob.PhotoKey("org-a", "photo-1")); errors.Is(err, blob.ErrNotFound) {
		t.Fatal("the sweep removed the image of a live photo")
	}
}
