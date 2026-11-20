package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// What a 0.4.x app pushes when a photo is added to a visit: a set add whose
// element is the whole image.
func legacyAdd(visit string, inline ...string) *wv1.Change {
	payload, _ := json.Marshal(map[string]any{"photo_keys": map[string]any{"add": inline}})
	return change("inspection", visit, "apiary-a", string(payload))
}

// What it pushes when the photo is removed: the element and the add tags the
// device had seen.
func legacyRemove(visit, inline string, tags ...string) *wv1.Change {
	payload, _ := json.Marshal(map[string]any{"photo_keys": map[string]any{
		"remove": []string{inline}, "removed_tags": map[string][]string{inline: tags},
	}})
	return change("inspection", visit, "apiary-a", string(payload))
}

// deleteOnDevice deletes a photo the way an updated app does after pulling
// it: the device's clock has seen the record, so its delete is the newer
// write.
func (f *photoFixture) deleteOnDevice(t *testing.T, p photoRow) {
	t.Helper()
	if err := testHLC.Recv(p.Clock["deleted"]); err != nil {
		t.Fatal(err)
	}
	if err := push(f.sync, ctxA, photoDeletion(p.ID)); err != nil {
		t.Fatalf("delete photo: %v", err)
	}
}

func (f *photoFixture) inlineSet(t *testing.T, visit string) wsync.ORSet {
	t.Helper()
	var raw string
	if err := f.db.Get(&raw, f.db.Rebind(`SELECT photo_keys FROM inspection WHERE id = ?`), visit); err != nil {
		t.Fatal(err)
	}
	return wsync.ParseORSet(raw)
}

func (f *photoFixture) hiveInline(t *testing.T, hive string) string {
	t.Helper()
	var raw string
	if err := f.db.Get(&raw, f.db.Rebind(`SELECT photo FROM hive WHERE id = ?`), hive); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacyVisitPhotoBecomesAPhotoRecord(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	image := jpegBytes(t, 640, 480, 1)
	inline := dataURL("image/jpeg", image)
	add := legacyAdd(visit, inline)
	if err := push(f.sync, ctxA, add); err != nil {
		t.Fatalf("legacy push: %v", err)
	}

	// For updated apps: a record with a thumbnail, the image in the blob store.
	photos := f.livePhotosOf(t, visit)
	if len(photos) != 1 {
		t.Fatalf("photo records: %+v", photos)
	}
	p := photos[0]
	if p.OrgID != "org-a" || p.OwnerEntity != ownerInspection || p.ContentType != "image/jpeg" || p.Size != int64(len(image)) ||
		p.LegacyKey != legacyKey(inline) || !strings.HasPrefix(p.Thumb, "data:image/jpeg;base64,") || len(p.Thumb) > maxThumbBytes {
		t.Fatalf("converted photo: %+v", p)
	}
	if !bytes.Equal(f.blob(t, p.BlobKey), image) || !strings.HasPrefix(p.BlobKey, "org-a/photos/") {
		t.Fatalf("blob %q does not hold the image", p.BlobKey)
	}
	// Its creation time is when the device added it, not when it was pushed.
	if want := stampTime(add.Hlc, time.Time{}); !p.CreatedAt.Equal(want) {
		t.Errorf("created_at %v, want %v", p.CreatedAt, want)
	}
	chs := f.pullAll(t, ctxA)
	recs := changesFor(chs, "photo")
	if len(recs) != 1 || recs[0].EntityId != p.ID || recs[0].ScopeId != "apiary-a" || strings.Contains(recs[0].PayloadJson, inline) {
		t.Fatalf("the photo change devices pull: %+v", recs)
	}

	// For 0.4.x apps: the change as they sent it, and the element in the row.
	if !anyContains(f.logPayloads(t, "inspection", visit), inline) {
		t.Fatal("0.4.x devices must still receive the inline photo")
	}
	if set := f.inlineSet(t, visit); !visible(set, inline) {
		t.Fatalf("the element must stay in the set: %v", set)
	}
	res, err := f.insp.ListInspections(ctxA, &wv1.ListInspectionsRequest{HiveId: "hive-a"})
	if err != nil || len(res.Inspections[0].Photos) != 1 || len(res.Inspections[0].PhotoKeys) != 1 {
		t.Fatalf("API view: %v %+v", err, res.Inspections)
	}

	// The same change delivered twice converts nothing twice.
	if err := push(f.sync, ctxA, add); err != nil {
		t.Fatal(err)
	}
	if n, blobs := len(f.photosOf(t, visit)), len(f.blobKeys(t)); n != 1 || blobs != 1 {
		t.Fatalf("after a repeated push: %d photo rows, %d blobs", n, blobs)
	}
}

func TestLegacyRemoveDeletesThePhoto(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	gone, kept := inlineJPEG(t, 1), inlineJPEG(t, 2)
	add := legacyAdd(visit, gone, kept)
	if err := push(f.sync, ctxA, add); err != nil {
		t.Fatal(err)
	}
	if n := len(f.livePhotosOf(t, visit)); n != 2 {
		t.Fatalf("converted %d photos, want 2", n)
	}
	if err := push(f.sync, ctxA, legacyRemove(visit, gone, add.Hlc)); err != nil {
		t.Fatalf("legacy remove: %v", err)
	}

	live := f.livePhotosOf(t, visit)
	if len(live) != 1 || live[0].LegacyKey != legacyKey(kept) {
		t.Fatalf("live photos after the remove: %+v", live)
	}
	if err := f.photos.removeDeletedBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if keys := f.blobKeys(t); len(keys) != 1 || keys[0] != live[0].BlobKey {
		t.Fatalf("blobs after the remove: %v", keys)
	}
	// The inline copy is erased: gone from the row (no tombstone is kept)
	// and from the logged add. The logged remove still names it, for 0.4.x
	// devices that have not seen it yet.
	set := f.inlineSet(t, visit)
	if _, ok := set[gone]; ok || !visible(set, kept) {
		t.Fatalf("set after the remove: %d elements", len(set))
	}
	var adds, removes int
	for _, payload := range f.logPayloads(t, "inspection", visit) {
		d, ok := photoKeysDelta(payload)
		if !ok {
			continue
		}
		for _, e := range d.Add {
			if e == gone {
				adds++
			}
			if e != gone && e != kept {
				t.Fatalf("unexpected element in a logged add: %.40s", e)
			}
		}
		for _, e := range d.Remove {
			if e == gone {
				removes++
			}
		}
	}
	if adds != 0 || removes != 1 {
		t.Fatalf("logged copies of the removed image: %d adds, %d removes; want 0 and 1", adds, removes)
	}
	if !anyContains(f.logPayloads(t, "inspection", visit), kept) {
		t.Fatal("the other photo's add must stay logged")
	}
	// Updated devices are told through the photo record.
	var deletes int
	for _, c := range changesFor(f.pullAll(t, ctxA), "photo") {
		if c.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("photo deletes in the feed: %d", deletes)
	}
}

// Add-wins survives the conversion: a remove that observed only one of two
// adds leaves the image visible, so its photo stays.
func TestLegacyRemoveOfOneTagKeepsThePhoto(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	inline := inlineJPEG(t, 1)
	first, second := legacyAdd(visit, inline), legacyAdd(visit, inline)
	if err := push(f.sync, ctxA, first, second, legacyRemove(visit, inline, first.Hlc)); err != nil {
		t.Fatal(err)
	}
	if n := len(f.livePhotosOf(t, visit)); n != 1 {
		t.Fatalf("live photos: %d, want 1", n)
	}
	if !visible(f.inlineSet(t, visit), inline) || len(f.photosOf(t, visit)) != 1 {
		t.Fatal("the image must stay, as one photo")
	}
}

// A photo taken with a 0.4.x app and deleted on an updated device (or
// through the API) must disappear on 0.4.x devices too.
func TestDeletingAConvertedPhotoReachesLegacyDevices(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	inline, other := inlineJPEG(t, 1), inlineJPEG(t, 2)
	if err := push(f.sync, ctxA, legacyAdd(visit, inline), legacyAdd(visit, other)); err != nil {
		t.Fatal(err)
	}
	var target photoRow
	for _, p := range f.livePhotosOf(t, visit) {
		if p.LegacyKey == legacyKey(inline) {
			target = p
		}
	}
	f.deleteOnDevice(t, target)

	// The server derived the set remove a 0.4.x device understands.
	var removes int
	for _, c := range changesFor(pullLog(t, f.sync, legacyA), "inspection") {
		if d, ok := photoKeysDelta(c.PayloadJson); ok && len(d.Remove) == 1 && d.Remove[0] == inline {
			removes++
			if d.RemovedTags != nil || c.ScopeId != "apiary-a" || !wsync.Valid(c.Hlc) {
				t.Errorf("derived remove: %+v", c)
			}
		}
	}
	if removes != 1 {
		t.Fatalf("derived removes in the feed: %d, want 1", removes)
	}
	set := f.inlineSet(t, visit)
	if _, ok := set[inline]; ok || !visible(set, other) {
		t.Fatal("the deleted image must leave the row, the other one stays")
	}
	for _, payload := range f.logPayloads(t, "inspection", visit) {
		if d, ok := photoKeysDelta(payload); ok {
			for _, e := range d.Add {
				if e == inline {
					t.Fatal("the logged add still carries the deleted image")
				}
			}
		}
	}
	if row := f.photo(t, target.ID); !row.Deleted || row.Thumb != "" {
		t.Fatalf("photo row: %+v", row)
	}
	if live := f.livePhotosOf(t, visit); len(live) != 1 || live[0].LegacyKey != legacyKey(other) {
		t.Fatalf("live photos: %+v", live)
	}
}

// A 0.4.x device whose push of an add is delivered again after the photo was
// deleted must not bring the photo back.
func TestReplayedLegacyAddDoesNotResurrect(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	inline := inlineJPEG(t, 1)
	add := legacyAdd(visit, inline)
	if err := push(f.sync, ctxA, add); err != nil {
		t.Fatal(err)
	}
	photo := f.livePhotosOf(t, visit)[0]
	f.deleteOnDevice(t, photo)
	if err := push(f.sync, ctxA, add); err != nil { // the same change, same stamp
		t.Fatal(err)
	}
	if live := f.livePhotosOf(t, visit); len(live) != 0 {
		t.Fatalf("the photo came back: %+v", live)
	}
	if _, ok := f.inlineSet(t, visit)[inline]; ok {
		t.Fatal("the image is back in the row")
	}
	for _, payload := range f.logPayloads(t, "inspection", visit) {
		if d, ok := photoKeysDelta(payload); ok && len(d.Add) > 0 {
			t.Fatal("the replayed add was logged with the image")
		}
	}
	// Adding the same picture anew, later, is a new photo.
	if err := push(f.sync, ctxA, legacyAdd(visit, inline)); err != nil {
		t.Fatal(err)
	}
	if live := f.livePhotosOf(t, visit); len(live) != 1 || live[0].ID == photo.ID {
		t.Fatalf("a fresh add must create a new photo: %+v", live)
	}
}

// Data URLs that are not images stay in the set as they are.
func TestNonImageElementsAreNotConverted(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	notImage := dataURL("image/jpeg", []byte("not really a jpeg"))
	if err := push(f.sync, ctxA, legacyAdd(visit, notImage)); err != nil {
		t.Fatal(err)
	}
	if n := len(f.photosOf(t, visit)); n != 0 || len(f.blobKeys(t)) != 0 {
		t.Fatalf("converted %d photos from values that are no images", n)
	}
	if set := f.inlineSet(t, visit); !visible(set, notImage) {
		t.Fatalf("the value must stay in the set: %v", set)
	}
	// The API removes such a value the old way.
	res, err := f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: notImage})
	if err != nil || len(res.Inspection.PhotoKeys) != 0 {
		t.Fatalf("remove an inline value: %v %v", err, res.GetInspection().GetPhotoKeys())
	}
}

// legacyHivePhoto is what a 0.4.x app pushes when the picture of tenant A's
// hive is set ("" clears it).
func legacyHivePhoto(inline string) *wv1.Change {
	payload, _ := json.Marshal(map[string]any{"photo": inline})
	return change("hive", "hive-a", "apiary-a", string(payload))
}

func TestLegacyHivePhoto(t *testing.T) {
	f := newPhotoFixture(t)
	first, second := inlineJPEG(t, 1), inlineJPEG(t, 2)

	// Set: a picture record appears.
	if err := push(f.sync, ctxA, legacyHivePhoto(first)); err != nil {
		t.Fatal(err)
	}
	live := f.livePhotosOf(t, "hive-a")
	if len(live) != 1 || live[0].OwnerEntity != ownerHive || live[0].LegacyKey != legacyKey(first) || live[0].BlobKey == "" {
		t.Fatalf("after the first picture: %+v", live)
	}
	firstID := live[0].ID

	// Replace: the old picture is deleted and its inline copy leaves the log.
	if err := push(f.sync, ctxA, legacyHivePhoto(second)); err != nil {
		t.Fatal(err)
	}
	live = f.livePhotosOf(t, "hive-a")
	if len(live) != 1 || live[0].LegacyKey != legacyKey(second) || !f.photo(t, firstID).Deleted {
		t.Fatalf("after replacing: %+v", live)
	}
	log := f.logPayloads(t, "hive", "hive-a")
	if anyContains(log, first) || !anyContains(log, second) || f.hiveInline(t, "hive-a") != second {
		t.Fatal("only the current inline picture may remain in the log")
	}

	// Deleted on an updated device: hive.photo is emptied for 0.4.x devices.
	f.deleteOnDevice(t, live[0])
	if got := f.hiveInline(t, "hive-a"); got != "" {
		t.Fatalf("hive.photo after the delete: %.40s", got)
	}
	log = f.logPayloads(t, "hive", "hive-a")
	if anyContains(log, "data:image") || log[len(log)-1] != `{"photo":""}` {
		t.Fatalf("log after the delete; last entry %.60s", log[len(log)-1])
	}

	// Set again and then cleared on a 0.4.x device (which has pulled the
	// server's change, so its clock is past it): the picture record goes.
	var clock string
	if err := f.db.Get(&clock, `SELECT field_hlc FROM hive WHERE id = 'hive-a'`); err != nil {
		t.Fatal(err)
	}
	if err := testHLC.Recv(wsync.ParseFieldClock(clock)["photo"]); err != nil {
		t.Fatal(err)
	}
	if err := push(f.sync, ctxA, legacyHivePhoto(first)); err != nil {
		t.Fatal(err)
	}
	if n := len(f.livePhotosOf(t, "hive-a")); n != 1 {
		t.Fatalf("after setting again: %d live pictures", n)
	}
	if err := push(f.sync, ctxA, legacyHivePhoto("")); err != nil {
		t.Fatal(err)
	}
	if n := len(f.livePhotosOf(t, "hive-a")); n != 0 {
		t.Fatalf("after clearing: %d live pictures", n)
	}
	if err := f.photos.removeDeletedBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Fatalf("blobs left: %v", keys)
	}
}

// The newest picture counts. One set by an updated app is not replaced by an
// inline picture that was taken before it and arrives late; a later inline
// picture does replace it.
func TestLegacyHivePhotoAndPicturesOfUpdatedApps(t *testing.T) {
	f := newPhotoFixture(t)
	late := legacyHivePhoto(inlineJPEG(t, 1)) // stamped now, pushed below
	future := time.Now().Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	modern := `{"owner_entity":"hive","owner_id":"hive-a","thumb":"` + testThumb(t) + `","created_at":"` + future + `","deleted":0}`
	if err := push(f.sync, ctxA, change("photo", "modern", "apiary-a", modern), late); err != nil {
		t.Fatal(err)
	}
	live := f.livePhotosOf(t, "hive-a")
	if len(live) != 1 || live[0].ID != "modern" {
		t.Fatalf("an older inline picture replaced a newer one: %+v", live)
	}

	g := newPhotoFixture(t)
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	old := `{"owner_entity":"hive","owner_id":"hive-a","thumb":"` + testThumb(t) + `","created_at":"` + past + `","deleted":0}`
	if err := push(g.sync, ctxA, change("photo", "modern", "apiary-a", old), legacyHivePhoto(inlineJPEG(t, 2))); err != nil {
		t.Fatal(err)
	}
	live = g.livePhotosOf(t, "hive-a")
	if len(live) != 1 || live[0].ID == "modern" || live[0].LegacyKey == "" {
		t.Fatalf("a newer inline picture must replace the older one: %+v", live)
	}
}

// Only a change that writes hive.photo is a statement about the picture: a
// hive edit that merely mentions the word leaves the pictures alone.
func TestHiveEditsDoNotTouchThePicture(t *testing.T) {
	f := newPhotoFixture(t)
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	pic := `{"owner_entity":"hive","owner_id":"hive-a","thumb":"` + testThumb(t) + `","created_at":"` + past + `","deleted":0}`
	if err := push(f.sync, ctxA, change("photo", "pic", "apiary-a", pic),
		change("hive", "hive-a", "apiary-a", `{"name":"the \"photo\" hive","note":"photo"}`)); err != nil {
		t.Fatal(err)
	}
	if live := f.livePhotosOf(t, "hive-a"); len(live) != 1 {
		t.Fatalf("a hive edit deleted the picture: %+v", f.photosOf(t, "hive-a"))
	}
}

// After the retention, the logged remove stops naming the image and the
// photo row forgets its hash.
func TestLegacyRemoveRetention(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	gone, kept := inlineJPEG(t, 1), inlineJPEG(t, 2)
	add := legacyAdd(visit, gone, kept)
	if err := push(f.sync, ctxA, add, legacyRemove(visit, gone, add.Hlc)); err != nil {
		t.Fatal(err)
	}
	if !anyContains(f.logPayloads(t, "inspection", visit), gone) {
		t.Fatal("the logged remove names the image while 0.4.x devices may need it")
	}
	ctx := t.Context()
	f.photos.now = func() time.Time { return time.Now().Add(legacyRemoveRetention - time.Hour) }
	if err := f.photos.eraseOldLegacyRemoves(ctx); err != nil {
		t.Fatal(err)
	}
	if !anyContains(f.logPayloads(t, "inspection", visit), gone) {
		t.Fatal("erased before the retention was over")
	}
	f.photos.now = func() time.Time { return time.Now().Add(legacyRemoveRetention + time.Hour) }
	if err := f.photos.eraseOldLegacyRemoves(ctx); err != nil {
		t.Fatal(err)
	}
	log := f.logPayloads(t, "inspection", visit)
	if anyContains(log, gone) || !anyContains(log, kept) {
		t.Fatal("after the retention only the live image may remain in the log")
	}
	for _, p := range f.photosOf(t, visit) {
		if p.Deleted && p.LegacyKey != "" {
			t.Fatalf("the deleted photo still carries its hash: %+v", p)
		}
		if !p.Deleted && p.LegacyKey != legacyKey(kept) {
			t.Fatalf("the live photo lost its link: %+v", p)
		}
	}
	// Nothing more to do on the next run.
	before := strings.Join(log, "\n")
	if err := f.photos.eraseOldLegacyRemoves(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.logPayloads(t, "inspection", visit), "\n") != before {
		t.Fatal("a second run rewrote the log")
	}
}

// Without a blob store nothing is converted, and inline photos keep working
// as they did.
func TestLegacyPhotosWithoutBlobStore(t *testing.T) {
	f := newAPIFixture(t)
	cr, err := f.insp.CreateInspection(ctxA, &wv1.CreateInspectionRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatal(err)
	}
	inline := inlineJPEG(t, 1)
	if err := push(f.sync, ctxA, legacyAdd(cr.Inspection.Id, inline), legacyHivePhoto(inline)); err != nil {
		t.Fatal(err)
	}
	if n := len(changesFor(f.pullAll(t, ctxA), "photo")); n != 0 {
		t.Fatalf("%d photo records without a blob store", n)
	}
	res, err := f.insp.ListInspections(ctxA, &wv1.ListInspectionsRequest{})
	if err != nil || len(res.Inspections[0].PhotoKeys) != 1 {
		t.Fatalf("inline photo: %v", err)
	}
}

const legacyPhotoGolden = "testdata/legacy_photo_v0.4.1.json"

// legacyPhotoFeed is what a 0.4.x device is sent around one photo's life.
type legacyPhotoFeed struct {
	// Added: the device's first pull after it added a visit photo and a hive
	// picture, while an updated device also added a photo to the visit.
	Added []goldenChange `json:"added"`
	// Deleted: its next pull, after an updated device deleted one of its
	// visit photos and the hive picture.
	Deleted []goldenChange `json:"deleted"`
	// Fresh: the whole log for a 0.4.x device that never synced before.
	Fresh []goldenChange `json:"fresh"`
}

// pullFrom reads the log after cursor for ctx and returns the changes and
// where to continue.
func pullFrom(t *testing.T, svc *SyncService, ctx context.Context, cursor string) ([]*wv1.Change, string) {
	t.Helper()
	var out []*wv1.Change
	for range 1000 {
		res, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: cursor, Limit: 50})
		if err != nil {
			t.Fatalf("pull from %q: %v", cursor, err)
		}
		out = append(out, res.Changes...)
		if cursor = res.NextCursor; !res.HasMore {
			return out, cursor
		}
	}
	t.Fatal("pull never ends")
	return nil, ""
}

// TestLegacyPhotoGolden records what a 0.4.x device is sent when a photo it
// added is deleted on an updated device. app/src/lib/local/legacy.test.ts
// applies it with the pull code of release 0.4.1. Stamps the server makes
// itself depend on the time of the run; they are replaced by fixed ones in
// the same order. UPDATE_GOLDEN=1 rewrites the file.
func TestLegacyPhotoGolden(t *testing.T) {
	f := newPhotoFixture(t)
	img := func(seed uint8) string { return dataURL("image/jpeg", jpegBytes(t, 16, 12, seed)) }
	kept, gone, picture := img(1), img(2), img(3)
	stamped := func(c *wv1.Change, n int) *wv1.Change { c.Hlc = pastStamp(n); return c }
	visit := `{"organization_id":"org-a","hive_id":"hive-a","date":"2026-01-02","note":"two photos","created_at":"2026-01-02T08:00:00.000Z","deleted":0,` +
		`"photo_keys":{"add":["` + kept + `","` + gone + `"]}}`
	if err := push(f.sync, legacyA,
		stamped(change("inspection", "visit-1", "apiary-a", visit), 1),
		stamped(legacyHivePhoto(picture), 2),
	); err != nil {
		t.Fatal(err)
	}
	if err := push(f.sync, ctxA, change("photo", "photo-new", "apiary-a", photoPayload(t, ownerInspection, "visit-1"))); err != nil {
		t.Fatal(err)
	}
	added, cursor := pullFrom(t, f.sync, legacyA, "")

	for _, p := range append(f.livePhotosOf(t, "visit-1"), f.livePhotosOf(t, "hive-a")...) {
		if p.LegacyKey == legacyKey(gone) || p.LegacyKey == legacyKey(picture) {
			f.deleteOnDevice(t, p)
		}
	}
	deleted, _ := pullFrom(t, f.sync, legacyA, cursor)
	fresh, _ := pullFrom(t, f.sync, legacyA, "")

	// One order-preserving mapping of every stamp to a fixed one.
	var stamps []string
	for _, list := range [][]*wv1.Change{added, deleted, fresh} {
		for _, c := range list {
			if !slices.Contains(stamps, c.Hlc) {
				stamps = append(stamps, c.Hlc)
			}
		}
	}
	slices.SortFunc(stamps, wsync.Compare)
	golden := func(list []*wv1.Change) []goldenChange {
		out := []goldenChange{}
		for _, c := range list {
			if _, ok := legacyColSet[c.Entity]; !ok {
				t.Fatalf("a 0.4.x device was sent a %s change", c.Entity)
			}
			if c.Op != wv1.ChangeOp_CHANGE_OP_DELETE {
				for _, key := range payloadKeys(t, c) {
					if !legacyColSet[c.Entity][key] {
						t.Fatalf("%s.%s is unknown to release 0.4.1", c.Entity, key)
					}
				}
			}
			out = append(out, goldenChange{c.Entity, c.EntityId, c.ScopeId, int32(c.Op), c.PayloadJson,
				pastStamp(1000 + slices.Index(stamps, c.Hlc))})
		}
		return out
	}
	feed := legacyPhotoFeed{Added: golden(added), Deleted: golden(deleted), Fresh: golden(fresh)}
	if len(feed.Added) == 0 || len(feed.Deleted) == 0 {
		t.Fatalf("nothing to record: %+v", feed)
	}
	out, err := json.MarshalIndent(feed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(legacyPhotoGolden, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(legacyPhotoGolden)
	if err != nil {
		t.Fatalf("%v (run the test once with UPDATE_GOLDEN=1)", err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("%s is out of date: what a legacy client receives about photos has changed. If that is intended, "+
			"run this test with UPDATE_GOLDEN=1 and then the app tests.", legacyPhotoGolden)
	}
}
