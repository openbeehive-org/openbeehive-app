package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// The photo RPCs keep their request shapes (a data URL in, a data URL or an
// id to remove) and store through photo records and the blob store.
func TestInspectionPhotoRPCs(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	image := jpegBytes(t, 640, 480, 5)
	photo := dataURL("image/jpeg", image)

	res, err := f.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: photo})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	got := res.Inspection.Photos
	if len(got) != 1 {
		t.Fatalf("photos in the response: %+v", got)
	}
	p := got[0]
	if p.Id == "" || p.Path != "/photos/"+p.Id || p.ContentType != "image/jpeg" || p.SizeBytes != int64(len(image)) ||
		!strings.HasPrefix(p.Thumbnail, "data:image/jpeg;base64,") || len(p.Thumbnail) >= len(photo) || p.CreatedAt == nil {
		t.Fatalf("photo in the response: id=%q path=%q type=%q size=%d thumb=%d bytes", p.Id, p.Path, p.ContentType, p.SizeBytes, len(p.Thumbnail))
	}
	if len(res.Inspection.PhotoKeys) != 0 {
		t.Fatalf("the image must not be stored inline: %d photo_keys", len(res.Inspection.PhotoKeys))
	}
	// The full image is what was sent, served through the transfer endpoint.
	if dl := f.transfer(t, "GET", p.Path, userA, nil); dl.status != 200 || !bytes.Equal(dl.body, image) {
		t.Fatalf("download: %d, %d bytes", dl.status, len(dl.body))
	}
	// Devices receive it as a photo record; nothing inline is logged.
	chs := f.pullAll(t, ctxA)
	if n := len(changesFor(chs, "photo")); n != 1 {
		t.Fatalf("devices must pull one photo record, got %d", n)
	}
	for _, c := range chs {
		if strings.Contains(c.PayloadJson, photo) {
			t.Fatalf("the full image was logged in a %s change", c.Entity)
		}
	}

	// Adding the same image again changes nothing.
	res, err = f.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: photo})
	if err != nil || len(res.Inspection.Photos) != 1 || len(f.blobKeys(t)) != 1 {
		t.Fatalf("second add: %v, %d photos, %d blobs", err, len(res.Inspection.Photos), len(f.blobKeys(t)))
	}
	// A PNG as second photo; listed oldest first.
	second := dataURL("image/png", pngBytes(t, 50, 50, 6))
	res, err = f.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: second})
	if err != nil || len(res.Inspection.Photos) != 2 || res.Inspection.Photos[0].Id != p.Id || res.Inspection.Photos[1].ContentType != "image/png" {
		t.Fatalf("add png: %v, %+v", err, res.Inspection.Photos)
	}
	// A WebP gets a thumbnail like any other photo.
	webp := dataURL("image/webp", readTestdata(t, "photo-lossy.webp"))
	res, err = f.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: webp})
	if err != nil || len(res.Inspection.Photos) != 3 || res.Inspection.Photos[2].ContentType != "image/webp" ||
		!strings.HasPrefix(res.Inspection.Photos[2].Thumbnail, "data:image/jpeg;base64,") {
		t.Fatalf("add webp: %v, %+v", err, res.Inspection.Photos)
	}
	rm, err := f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: webp})
	if err != nil || len(rm.Inspection.Photos) != 2 {
		t.Fatalf("remove webp: %v, %+v", err, rm.Inspection.Photos)
	}
	list, err := f.insp.ListInspections(ctxA, &wv1.ListInspectionsRequest{HiveId: "hive-a"})
	if err != nil || len(list.Inspections) != 1 || len(list.Inspections[0].Photos) != 2 {
		t.Fatalf("list: %v, %+v", err, list.Inspections)
	}
	dash, err := f.stats.GetDashboard(ctxA, &wv1.DashboardRequest{})
	if err != nil || len(dash.RecentInspections) != 1 || len(dash.RecentInspections[0].Photos) != 2 {
		t.Fatalf("dashboard: %v", err)
	}

	// Remove by the value that was added, and by id.
	rm, err = f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: photo})
	if err != nil || len(rm.Inspection.Photos) != 1 {
		t.Fatalf("remove by value: %v, %+v", err, rm.Inspection.Photos)
	}
	rm, err = f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: rm.Inspection.Photos[0].Id})
	if err != nil || len(rm.Inspection.Photos) != 0 {
		t.Fatalf("remove by id: %v, %+v", err, rm.Inspection.Photos)
	}
	if err := f.photos.removeDeletedBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Fatalf("blobs left after removing both photos: %v", keys)
	}
	// Removing what is not there is not an error.
	if _, err := f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: photo}); err != nil {
		t.Fatalf("removing again: %v", err)
	}
}

// photoAt is a photo record a device pushes, created at the given time and
// stamped with the given counter of that millisecond.
func photoAt(t *testing.T, id, ownerEntity, ownerID string, created time.Time, counter int64) *wv1.Change {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"owner_entity": ownerEntity, "owner_id": ownerID, "thumb": testThumb(t),
		"content_type": "image/jpeg", "size_bytes": 1234, "created_at": jsTime(created), "deleted": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch := change("photo", id, "apiary-a", string(payload))
	ch.Hlc = wsync.Format(created.UnixMilli(), counter, "phone")
	return ch
}

func photoIDs(list []*wv1.Photo) []string {
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.Id)
	}
	return ids
}

// Photos created in the same millisecond are listed in the order they were
// created (the order of their stamps), not by their random ids. The ids here
// sort the other way round, so ordering by id fails every time.
func TestPhotosOfTheSameMillisecondKeepTheirOrder(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	at := time.Now().Truncate(time.Millisecond)
	if err := push(f.sync, ctxA,
		photoAt(t, "photo-c", ownerInspection, visit, at, 1),
		photoAt(t, "photo-b", ownerInspection, visit, at, 2),
		photoAt(t, "photo-a", ownerInspection, visit, at, 3),
		photoAt(t, "picture-b", ownerHive, "hive-a", at, 4),
		photoAt(t, "picture-a", ownerHive, "hive-a", at, 5),
	); err != nil {
		t.Fatalf("push photos: %v", err)
	}
	want := "photo-c photo-b photo-a"

	list, err := f.insp.ListInspections(ctxA, &wv1.ListInspectionsRequest{HiveId: "hive-a"})
	if err != nil || len(list.Inspections) != 1 {
		t.Fatalf("list: %v, %+v", err, list)
	}
	if got := strings.Join(photoIDs(list.Inspections[0].Photos), " "); got != want {
		t.Errorf("list: photos %s, want %s", got, want)
	}
	dash, err := f.stats.GetDashboard(ctxA, &wv1.DashboardRequest{})
	if err != nil || len(dash.RecentInspections) != 1 {
		t.Fatalf("dashboard: %v", err)
	}
	if got := strings.Join(photoIDs(dash.RecentInspections[0].Photos), " "); got != want {
		t.Errorf("dashboard: photos %s, want %s", got, want)
	}
	rm, err := f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: "photo-b"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := strings.Join(photoIDs(rm.Inspection.Photos), " "); got != "photo-c photo-a" {
		t.Errorf("after remove: photos %s, want photo-c photo-a", got)
	}
	// The hive's picture is the newest of its photos.
	hive, err := f.hive.GetHive(ctxA, &wv1.GetHiveRequest{Id: "hive-a"})
	if err != nil || hive.Hive.Picture == nil || hive.Hive.Picture.Id != "picture-a" {
		t.Errorf("hive picture: %v, %+v", err, hive.GetHive().GetPicture())
	}
}

func TestInspectionPhotoRPCsRefuseBadInput(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	for name, photo := range map[string]string{
		"a link":           "http://example.org/x.jpg",
		"not an image":     dataURL("image/jpeg", []byte("<html></html>")),
		"an svg":           "data:image/svg+xml;base64,PHN2Zy8+",
		"not base64":       "data:image/jpeg,rawbytes",
		"an oversized one": dataURL("image/jpeg", append(jpegBytes(t, 8, 8, 1), bytes.Repeat([]byte{0}, MaxPhotoBytes)...)),
	} {
		_, err := f.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: photo})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: want invalid_argument, got %v", name, err)
		}
	}
	ok := inlineJPEG(t, 1)
	// Another workspace cannot attach to, or remove from, this inspection.
	_, err := f.insp.AddInspectionPhoto(ctxB, &wv1.AddInspectionPhotoRequest{InspectionId: visit, Photo: ok})
	wantCode(t, err, connect.CodeNotFound)
	_, err = f.insp.RemoveInspectionPhoto(ctxB, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit, Photo: ok})
	wantCode(t, err, connect.CodeNotFound)
	_, err = f.insp.RemoveInspectionPhoto(ctxA, &wv1.RemoveInspectionPhotoRequest{InspectionId: visit})
	wantCode(t, err, connect.CodeInvalidArgument)
	if len(f.blobKeys(t)) != 0 || len(f.photosOf(t, visit)) != 0 {
		t.Fatal("a refused request stored something")
	}

	// Without a blob store the RPC says so instead of pretending.
	plain := newAPIFixture(t)
	cr, err := plain.insp.CreateInspection(ctxA, &wv1.CreateInspectionRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = plain.insp.AddInspectionPhoto(ctxA, &wv1.AddInspectionPhotoRequest{InspectionId: cr.Inspection.Id, Photo: ok})
	wantCode(t, err, connect.CodeUnavailable)
}

func TestHivePictureInResponses(t *testing.T) {
	f := newPhotoFixture(t)
	get := func() *wv1.Hive {
		t.Helper()
		res, err := f.hive.GetHive(ctxA, &wv1.GetHiveRequest{Id: "hive-a"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Hive
	}
	if get().Picture != nil {
		t.Fatal("a hive without photos has no picture")
	}
	// Two pictures were set one after the other; the newer one counts.
	older := `{"owner_entity":"hive","owner_id":"hive-a","thumb":"` + testThumb(t) + `","created_at":"2026-05-01T10:00:00.000Z","deleted":0}`
	newer := `{"owner_entity":"hive","owner_id":"hive-a","thumb":"` + testThumb(t) + `","created_at":"2026-06-01T10:00:00.000Z","deleted":0}`
	if err := push(f.sync, ctxA, change("photo", "pic-new", "apiary-a", newer), change("photo", "pic-old", "apiary-a", older)); err != nil {
		t.Fatal(err)
	}
	pic := get().Picture
	if pic == nil || pic.Id != "pic-new" || pic.Thumbnail == "" || pic.Path != "" {
		t.Fatalf("picture before upload: %+v", pic)
	}
	f.upload(t, "pic-new", pngBytes(t, 40, 40, 2))
	if pic := get().Picture; pic.Path != "/photos/pic-new" || pic.ContentType != "image/png" {
		t.Fatalf("picture after upload: %+v", pic)
	}
	list, err := f.hive.ListHives(ctxA, &wv1.ListHivesRequest{})
	if err != nil || len(list.Hives) != 1 || list.Hives[0].Picture.GetId() != "pic-new" {
		t.Fatalf("list: %v %+v", err, list.Hives)
	}
	// An inspection's photo is not the hive's picture.
	visit := f.newVisit(t)
	if err := push(f.sync, ctxA, photoDeletion("pic-new"), photoDeletion("pic-old"),
		change("photo", "visit-photo", "apiary-a", photoPayload(t, ownerInspection, visit))); err != nil {
		t.Fatal(err)
	}
	if get().Picture != nil {
		t.Fatal("picture after deleting both")
	}
}
