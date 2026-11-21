package service

import (
	"fmt"
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
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

// Prepared blobs are keyed by workspace and content: preparing the same
// images twice yields the same keys, and two copies of one image in a
// request get different keys.
func TestPreparedBlobKeysFollowContent(t *testing.T) {
	f := newPhotoFixture(t)
	img := inlineJPEG(t, 5)
	keysOf := func(org string) []string {
		ctx, release, err := f.photos.prepareInline(t.Context(), org, []string{img, img}, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		var keys []string
		for _, p := range preparedFrom(ctx)[legacyKey(img)] {
			keys = append(keys, p.key)
		}
		return keys
	}
	first, again, other := keysOf("org-a"), keysOf("org-a"), keysOf("org-b")
	if len(first) != 2 || first[0] == first[1] {
		t.Fatalf("keys of two copies = %v, want two different keys", first)
	}
	if fmt.Sprint(first) != fmt.Sprint(again) {
		t.Errorf("keys changed between attempts: %v then %v", first, again)
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
	_, releaseA, err := f.photos.prepareInline(t.Context(), "org-a", []string{img}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseB, err := f.photos.prepareInline(t.Context(), "org-a", []string{img}, 1)
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
