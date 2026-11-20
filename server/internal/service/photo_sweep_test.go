package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

func (f *photoFixture) putBlob(t *testing.T, key string) {
	t.Helper()
	if err := f.blobs.Put(context.Background(), key, bytes.NewReader([]byte("x")), 1, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
}

// The sweep removes blobs that no live photo names, but not one that is
// young enough to belong to an upload still in flight.
func TestSweepRemovesBlobsWithoutAPhoto(t *testing.T) {
	f := newPhotoFixture(t)
	ctx := context.Background()
	f.newPhoto(t, "photo-1")
	f.upload(t, "photo-1", jpegBytes(t, 32, 32, 1))
	live := f.photo(t, "photo-1").BlobKey
	orphan := blob.PhotoKey("org-a", "never-recorded")
	gone := blob.PhotoKey("org-deleted", "p")
	f.putBlob(t, orphan)
	f.putBlob(t, gone)

	// Just written: an upload stores the blob before it names it on the row.
	if n, err := f.photos.sweepBlobs(ctx); err != nil || n != 0 {
		t.Fatalf("sweep of fresh blobs removed %d (%v)", n, err)
	}
	if len(f.blobKeys(t)) != 3 {
		t.Fatal("a fresh blob was removed")
	}

	f.photos.now = func() time.Time { return time.Now().Add(blobGrace + time.Minute) }
	n, err := f.photos.sweepBlobs(ctx)
	if err != nil || n != 2 {
		t.Fatalf("sweep removed %d (%v), want 2", n, err)
	}
	if keys := f.blobKeys(t); len(keys) != 1 || keys[0] != live {
		t.Fatalf("blobs after the sweep: %v, want only %s", keys, live)
	}

	// A deleted photo's blob is removed by the sweep as well, should the
	// prompt removal have failed.
	if err := push(f.sync, ctxA, photoDeletion("photo-1")); err != nil {
		t.Fatal(err)
	}
	if n, err := f.photos.sweepBlobs(ctx); err != nil || n != 1 {
		t.Fatalf("sweep after the delete removed %d (%v)", n, err)
	}
}

// Photos of a deleted inspection or hive are deleted once the record has
// been gone for a while; until then a restore brings everything back.
func TestPhotosOfDeletedRecordsAreDeletedAfterAGrace(t *testing.T) {
	f := newPhotoFixture(t)
	ctx := context.Background()
	visit, keptVisit := f.newVisit(t), f.newVisit(t)
	inline := inlineJPEG(t, 9)
	if err := push(f.sync, ctxA,
		change("photo", "of-visit", "apiary-a", photoPayload(t, ownerInspection, visit)),
		change("photo", "of-kept", "apiary-a", photoPayload(t, ownerInspection, keptVisit)),
		change("photo", "of-hive", "apiary-a", photoPayload(t, ownerHive, "hive-a")),
		legacyAdd(visit, inline),
	); err != nil {
		t.Fatal(err)
	}
	f.upload(t, "of-visit", jpegBytes(t, 32, 32, 1))
	if _, err := f.insp.DeleteInspection(ctxA, &wv1.DeleteInspectionRequest{Id: visit}); err != nil {
		t.Fatal(err)
	}

	f.photos.Tidy(ctx)
	if n := len(f.livePhotosOf(t, visit)); n != 2 {
		t.Fatalf("photos were deleted with their inspection at once (%d left of 2)", n)
	}

	f.photos.now = func() time.Time { return time.Now().Add(ownerGrace + time.Minute) }
	f.photos.Tidy(ctx)
	if n := len(f.livePhotosOf(t, visit)); n != 0 {
		t.Fatalf("%d photos of the deleted inspection are still live", n)
	}
	if f.photo(t, "of-kept").Deleted || f.photo(t, "of-hive").Deleted {
		t.Fatal("photos of live records were deleted")
	}
	// Erased like any deleted photo: thumbnail, blob, inline copy.
	if row := f.photo(t, "of-visit"); row.Thumb != "" || row.BlobKey != "" {
		t.Fatalf("deleted photo keeps data: %+v", row)
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Fatalf("blobs left: %v", keys)
	}
	if len(f.inlineSet(t, visit)) != 0 || anyContains(f.logPayloads(t, "inspection", visit), inline) {
		t.Fatal("the inline copy of the deleted inspection's photo is still stored")
	}
	// Devices are told.
	var deletes int
	for _, c := range changesFor(f.pullAll(t, ctxA), "photo") {
		if c.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
			deletes++
			if c.ScopeId != "apiary-a" || c.AuthorId != systemAuthor {
				t.Errorf("derived delete: %+v", c)
			}
		}
	}
	if deletes != 2 {
		t.Fatalf("photo deletes in the feed: %d, want 2", deletes)
	}

	// The same for a hive's picture.
	if _, err := f.hive.DeleteHive(ctxA, &wv1.DeleteHiveRequest{Id: "hive-a"}); err != nil {
		t.Fatal(err)
	}
	f.photos.now = func() time.Time { return time.Now().Add(2*ownerGrace + time.Minute) }
	f.photos.Tidy(ctx)
	if !f.photo(t, "of-hive").Deleted {
		t.Fatal("the picture of a deleted hive is still live")
	}
	if f.photo(t, "of-kept").Deleted {
		t.Fatal("deleting a hive must not delete the photos of its inspections")
	}
}

// The worker removes the blob shortly after the delete was committed.
func TestWorkerRemovesBlobAfterDelete(t *testing.T) {
	f := newPhotoFixture(t)
	f.newPhoto(t, "photo-1")
	f.upload(t, "photo-1", jpegBytes(t, 32, 32, 1))
	f.photos.wakeDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.photos.Run(ctx)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond) // past the upkeep the loop does when it starts
	if err := push(f.sync, ctxA, photoDeletion("photo-1")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(f.blobKeys(t)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the blob of a deleted photo was not removed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
}
