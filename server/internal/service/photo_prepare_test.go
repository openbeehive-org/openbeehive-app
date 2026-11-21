package service

import (
	"fmt"
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// A 0.4.x Push that fails as a whole (one change stamped too far ahead)
// stores nothing, however often the app retries it.
func TestFailedLegacyPushLeavesNoBlobs(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	before := len(f.blobKeys(t))
	legacy := legacyCtx("user-a", "org-a")
	for range 3 {
		ok := legacyAdd(visit, inlineJPEG(t, 7))
		ahead := change("inspection", visit, "apiary-a", `{"note":"fast clock"}`)
		ahead.Hlc = wsync.Format(time.Now().Add(time.Hour).UnixMilli(), 0, "fastdev")
		if _, err := f.sync.Push(legacy, &wv1.PushRequest{Changes: []*wv1.Change{ok, ahead}}); err == nil {
			t.Fatal("expected the legacy push to fail as a whole")
		}
	}
	if n := len(f.blobKeys(t)) - before; n > 0 {
		t.Errorf("%d orphan blobs after 3 failed retries of one legacy push", n)
	}
}

// The same with the fast change on another row, and two images.
func TestRetriedLegacyPushStoresNoBlobs(t *testing.T) {
	f := newPhotoFixture(t)
	ctx := legacyCtx("user-a", "org-a")
	ok := &wv1.Change{Entity: "inspection", EntityId: "insp-1", ScopeId: "apiary-a", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: fmt.Sprintf(`{"hive_id":"hive-a","date":"2026-07-11","photo_keys":{"add":[%q,%q]}}`, inlineJPEG(t, 1), inlineJPEG(t, 2)),
		Hlc:         testHLC.Now()}
	bad := change("hive", "hive-a", "apiary-a", `{"name":"x"}`)
	bad.Hlc = wsync.Format(time.Now().Add(time.Hour).UnixMilli(), 0, "dev")
	for range 5 {
		if _, err := f.sync.Push(ctx, &wv1.PushRequest{Changes: []*wv1.Change{ok, bad}}); err == nil {
			t.Fatal("expected the legacy push to fail as a whole")
		}
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Errorf("after 5 failed retries: %d blobs, want 0", len(keys))
	}
}

// An image-carrying change refused inside the transaction (here: a scope
// the caller cannot write) leaves no blob; the accepted one keeps its blob.
func TestRefusedInlineChangeLeavesNoBlob(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	refused := legacyAdd(visit, inlineJPEG(t, 3))
	refused.EntityId = "insp-elsewhere"
	refused.ScopeId = "apiary-of-someone-else"
	accepted := legacyAdd(visit, inlineJPEG(t, 4))
	res := pushRes(t, f.sync, legacyCtx("user-a", "org-a"), refused, accepted)
	if len(res.Rejected) != 1 || res.Rejected[0].Index != 0 {
		t.Fatalf("rejected = %v, want only the first change", res.Rejected)
	}
	live := f.livePhotosOf(t, visit)
	if len(live) != 1 {
		t.Fatalf("live photos = %d, want 1", len(live))
	}
	keys := f.blobKeys(t)
	if len(keys) != 1 || keys[0] != live[0].BlobKey {
		t.Errorf("blobs = %v, want only the accepted photo's %q", keys, live[0].BlobKey)
	}
}

// A photo converted from a 0.4.x Push stores its image under its record's
// key, so after a restored database lost the row and a device sent the
// record again, the sweep gives the image back.
func TestRestoredConvertedPhotoAdoptsBlob(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	if err := push(f.sync, ctxA, legacyAdd(visit, inlineJPEG(t, 5))); err != nil {
		t.Fatal(err)
	}
	p := f.livePhotosOf(t, visit)[0]
	if _, err := f.db.Exec(f.db.Rebind(`DELETE FROM photo WHERE id = ?`), p.ID); err != nil {
		t.Fatal(err)
	}
	if err := push(f.sync, ctxA, change("photo", p.ID, "apiary-a", photoPayload(t, ownerInspection, visit))); err != nil {
		t.Fatal(err)
	}
	f.photos.Sweep(t.Context())
	if got := f.photo(t, p.ID); got.BlobKey != p.BlobKey {
		t.Errorf("blob_key after re-send and sweep = %q, want %q", got.BlobKey, p.BlobKey)
	}
}

// An image that already has a photo record in the workspace is not decoded
// again when another visit adds it: the new record takes the thumbnail of
// the existing one and gets a blob of its own.
func TestKnownImageReusesThumbnail(t *testing.T) {
	f := newPhotoFixture(t)
	img := inlineJPEG(t, 8)
	first := f.newVisit(t)
	if err := push(f.sync, ctxA, legacyAdd(first, img)); err != nil {
		t.Fatal(err)
	}
	decodes := 0
	orig := makeThumb
	makeThumb = func(data []byte) string { decodes++; return orig(data) }
	t.Cleanup(func() { makeThumb = orig })
	second := f.newVisit(t)
	if err := push(f.sync, ctxA, legacyAdd(second, img)); err != nil {
		t.Fatal(err)
	}
	a, b := f.livePhotosOf(t, first), f.livePhotosOf(t, second)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("photos = %d and %d, want 1 each", len(a), len(b))
	}
	if decodes != 0 {
		t.Errorf("image decoded %d times, want 0", decodes)
	}
	if b[0].Thumb == "" || b[0].Thumb != a[0].Thumb {
		t.Errorf("second photo's thumbnail differs from the first's")
	}
	if b[0].BlobKey == a[0].BlobKey || b[0].BlobKey != blob.PhotoKey("org-a", b[0].ID) {
		t.Errorf("second photo's blob key %q, want its own record key", b[0].BlobKey)
	}
	if keys := f.blobKeys(t); len(keys) != 2 {
		t.Errorf("blobs = %v, want 2", keys)
	}
}

// Prepared blobs are keyed by fresh record ids: two copies of one image,
// or the same image in two workspaces, never share a key.
func TestPreparedBlobKeysAreRecordKeys(t *testing.T) {
	f := newPhotoFixture(t)
	img := inlineJPEG(t, 5)
	keysOf := func(org string) []string {
		ctx, release, err := f.photos.prepareInline(t.Context(), org, []inlineImage{{ownerInspection, "v1", img}, {ownerInspection, "v1", img}}, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		var keys []string
		for _, p := range preparedFrom(ctx)[legacyKey(img)] {
			if p.key != blob.PhotoKey(org, p.id) {
				t.Errorf("key %q is not the record key of %q", p.key, p.id)
			}
			keys = append(keys, p.key)
		}
		return keys
	}
	first, other := keysOf("org-a"), keysOf("org-b")
	if len(first) != 2 || first[0] == first[1] {
		t.Fatalf("keys of two copies = %v, want two different keys", first)
	}
	if first[0] == other[0] {
		t.Errorf("two workspaces share the key %q", first[0])
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Errorf("released preparations left blobs %v", keys)
	}
}

// A blob another running request has prepared under the same key survives
// the release of a request that did not use it.
func TestReleaseKeepsBlobHeldByAnotherRequest(t *testing.T) {
	f := newPhotoFixture(t)
	img := inlineJPEG(t, 6)
	_, releaseA, err := f.photos.prepareInline(t.Context(), "org-a", []inlineImage{{ownerInspection, "v1", img}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseB, err := f.photos.prepareInline(t.Context(), "org-a", []inlineImage{{ownerInspection, "v1", img}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	releaseB()
	if n := len(f.blobKeys(t)); n != 1 {
		t.Fatalf("blobs after one release = %d, want 1", n)
	}
	releaseA()
	if n := len(f.blobKeys(t)); n != 0 {
		t.Errorf("blobs after both releases = %d, want 0", n)
	}
}
