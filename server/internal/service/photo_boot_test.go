package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// legacyData builds, by direct inserts, what a 0.4.1 server has stored: rows
// with inline photos and the change-log entries that carried them.
type legacyData struct {
	t   *testing.T
	f   *photoFixture
	seq int64
}

func stampAt(at time.Time, counter int64) string {
	return wsync.Format(at.UnixMilli(), counter, "phone")
}

func (l *legacyData) exec(query string, args ...any) {
	l.t.Helper()
	if _, err := l.f.db.Exec(l.f.db.Rebind(query), args...); err != nil {
		l.t.Fatalf("seed: %v", err)
	}
}

func (l *legacyData) visit(id, org, hive string, deleted bool, set wsync.ORSet) {
	now := time.Now().UTC()
	l.exec(`INSERT INTO inspection (id, organization_id, hive_id, date, created_at, deleted, photo_keys) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, org, hive, now, now, deleted, set.Marshal())
}

// log appends a change-log entry of tenant A.
func (l *legacyData) log(entity, id, stamp string, payload any) {
	raw, err := marshalPlain(payload)
	if err != nil {
		l.t.Fatal(err)
	}
	if err := l.f.db.Get(&l.seq, `SELECT val FROM seq_counter WHERE name = 'change'`); err != nil {
		l.t.Fatal(err)
	}
	l.seq++
	l.exec(`UPDATE seq_counter SET val = ? WHERE name = 'change'`, l.seq)
	l.exec(`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, 'phone', 'org-a')`, "seed-"+itoa(l.seq), l.seq, "apiary-a", entity, id, raw, stamp)
}

func setDelta(add, remove []string) map[string]any {
	d := map[string]any{}
	if add != nil {
		d["add"] = add
	}
	if remove != nil {
		d["remove"] = remove
	}
	return map[string]any{"photo_keys": d}
}

func TestConvertInlinePhotosAtBoot(t *testing.T) {
	f := newPhotoFixture(t)
	ctx := context.Background()
	l := &legacyData{t: t, f: f}
	now := time.Now()
	recent, old := now.Add(-48*time.Hour), now.Add(-legacyRemoveRetention-48*time.Hour)

	liveImg := jpegBytes(t, 640, 480, 1)
	live, removed, longGone := dataURL("image/jpeg", liveImg), inlineJPEG(t, 2), inlineJPEG(t, 3)
	ofDeleted, otherOrg := inlineJPEG(t, 4), dataURL("image/png", pngBytes(t, 30, 30, 5))
	tLive, tRemoved, tGone := stampAt(recent, 1), stampAt(recent, 2), stampAt(old, 1)

	// An inspection with a live photo, one removed recently, one removed long
	// ago, and a set element that is not an image.
	l.visit("v1", "org-a", "hive-a", false, wsync.ORSet{
		live:     {A: []string{tLive}},
		removed:  {A: []string{tRemoved}, R: []string{tRemoved}},
		longGone: {A: []string{tGone}, R: []string{tGone}},
		"note-1": {A: []string{tLive}},
	})
	l.log("inspection", "v1", tGone, setDelta([]string{longGone}, nil))
	l.log("inspection", "v1", stampAt(old, 2), setDelta(nil, []string{longGone}))
	l.log("inspection", "v1", tLive, map[string]any{"note": "kept <b>", "photo_keys": map[string]any{"add": []string{live, removed, "note-1"}}})
	l.log("inspection", "v1", stampAt(recent, 3), setDelta(nil, []string{removed}))
	// A deleted inspection that still holds a photo.
	l.visit("v2", "org-a", "hive-a", true, wsync.ORSet{ofDeleted: {A: []string{tLive}}})
	l.log("inspection", "v2", tLive, setDelta([]string{ofDeleted}, nil))
	// Another workspace.
	l.exec(`INSERT INTO hive (id, organization_id, apiary_id, name, created_at, updated_at) VALUES ('hive-b', 'org-b', 'apiary-b', 'B1', ?, ?)`, now.UTC(), now.UTC())
	l.visit("v3", "org-b", "hive-b", false, wsync.ORSet{otherOrg: {A: []string{tLive}}})
	// An inspection without inline photos is not looked at.
	l.visit("v4", "org-a", "hive-a", false, wsync.ORSet{"note-2": {A: []string{tLive}}})

	// Hive pictures: hive-a was given a second picture; hive-gone is deleted.
	firstPic, currentPic, gonePic := inlineJPEG(t, 6), inlineJPEG(t, 7), inlineJPEG(t, 8)
	clock, _ := json.Marshal(map[string]string{"photo": tLive})
	l.exec(`UPDATE hive SET photo = ?, field_hlc = ? WHERE id = 'hive-a'`, currentPic, string(clock))
	l.log("hive", "hive-a", tGone, map[string]any{"name": "H1", "photo": firstPic})
	l.log("hive", "hive-a", tLive, map[string]any{"photo": currentPic})
	l.exec(`INSERT INTO hive (id, organization_id, apiary_id, name, created_at, updated_at, deleted, photo)
		VALUES ('hive-gone', 'org-a', 'apiary-a', 'Gone', ?, ?, TRUE, ?)`, now.UTC(), now.UTC(), gonePic)
	l.log("hive", "hive-gone", tLive, map[string]any{"photo": gonePic})

	logBefore := countAll(t, f, "change_log")
	if err := f.photos.ConvertInlinePhotos(ctx); err != nil {
		t.Fatalf("convert: %v", err)
	}

	// --- photo records ---
	v1 := f.photosOf(t, "v1")
	if len(v1) != 1 || v1[0].Deleted || v1[0].LegacyKey != legacyKey(live) || v1[0].OrgID != "org-a" ||
		v1[0].ContentType != "image/jpeg" || v1[0].Size != int64(len(liveImg)) || !strings.HasPrefix(v1[0].Thumb, "data:image/jpeg") {
		t.Fatalf("photos of v1: %+v", v1)
	}
	if !v1[0].CreatedAt.Equal(time.UnixMilli(recent.UnixMilli())) {
		t.Errorf("created_at %v, want the time of the add (%v)", v1[0].CreatedAt, recent)
	}
	if !bytes.Equal(f.blob(t, v1[0].BlobKey), liveImg) || !strings.HasPrefix(v1[0].BlobKey, blob.WorkspacePrefix("org-a")) {
		t.Fatalf("blob of the live photo: %q", v1[0].BlobKey)
	}
	if n := len(f.photosOf(t, "v2")); n != 0 {
		t.Fatalf("a deleted inspection's photo was converted (%d rows)", n)
	}
	v3 := f.photosOf(t, "v3")
	if len(v3) != 1 || v3[0].OrgID != "org-b" || v3[0].ContentType != "image/png" || !strings.HasPrefix(v3[0].BlobKey, blob.WorkspacePrefix("org-b")) {
		t.Fatalf("photos of the other workspace: %+v", v3)
	}
	pics := f.photosOf(t, "hive-a")
	if len(pics) != 1 || pics[0].OwnerEntity != ownerHive || pics[0].LegacyKey != legacyKey(currentPic) {
		t.Fatalf("hive pictures: %+v", pics)
	}
	if n := len(f.photosOf(t, "hive-gone")); n != 0 {
		t.Fatalf("a deleted hive's picture was converted")
	}
	if keys := f.blobKeys(t); len(keys) != 3 {
		t.Fatalf("blobs: %v, want 3", keys)
	}

	// --- rows: removed elements are dropped, live ones and plain ones stay ---
	set := f.inlineSet(t, "v1")
	if len(set) != 2 || !visible(set, live) || !visible(set, "note-1") {
		t.Fatalf("v1 set after conversion has %d elements", len(set))
	}
	if set := f.inlineSet(t, "v2"); len(set) != 0 {
		t.Fatalf("deleted inspection keeps %d inline elements", len(set))
	}
	if f.hiveInline(t, "hive-a") != currentPic || f.hiveInline(t, "hive-gone") != "" {
		t.Fatal("hive.photo: the live hive keeps its picture for 0.4.x devices, the deleted one loses it")
	}

	// --- change log ---
	logV1 := f.logPayloads(t, "inspection", "v1")
	if anyContains(logV1, longGone) {
		t.Error("an image removed long ago is still in the log")
	}
	if !anyContains(logV1, live) || !anyContains(logV1, `"note":"kept <b>"`) || !anyContains(logV1, "note-1") {
		t.Error("the live image, plain elements and other fields must stay in the log")
	}
	var recentAdds, recentRemoves int
	for _, p := range logV1 {
		d, _ := photoKeysDelta(p)
		for _, e := range d.Add {
			if e == removed {
				recentAdds++
			}
		}
		for _, e := range d.Remove {
			if e == removed {
				recentRemoves++
			}
		}
	}
	if recentAdds != 0 || recentRemoves != 1 {
		t.Errorf("recently removed image: %d adds and %d removes in the log, want 0 and 1", recentAdds, recentRemoves)
	}
	if anyContains(f.logPayloads(t, "inspection", "v2"), "data:image") {
		t.Error("the deleted inspection's image is still in the log")
	}
	logHive := f.logPayloads(t, "hive", "hive-a")
	if anyContains(logHive, firstPic) || !anyContains(logHive, currentPic) || !anyContains(logHive, `"name":"H1"`) {
		t.Error("hive log: only the current picture may remain, other fields stay")
	}
	if anyContains(f.logPayloads(t, "hive", "hive-gone"), "data:image") {
		t.Error("the deleted hive's picture is still in the log")
	}

	// Devices receive the new records, in their workspace only.
	var a, b int
	for _, c := range changesFor(f.pullAll(t, ctxA), "photo") {
		a++
		if c.ScopeId != "apiary-a" || c.Op != wv1.ChangeOp_CHANGE_OP_UPSERT {
			t.Errorf("photo change: %+v", c)
		}
	}
	b = len(changesFor(f.pullAll(t, ctxB), "photo"))
	if a != 2 || b != 1 {
		t.Fatalf("photo changes pulled: %d in org-a, %d in org-b; want 2 and 1", a, b)
	}
	if got := countAll(t, f, "change_log"); got != logBefore+3 {
		t.Fatalf("change log grew by %d entries, want 3", got-logBefore)
	}

	// --- a second start does nothing ---
	snapshot := func() string {
		rows, err := queryMaps(ctx, f.db, `SELECT seq, payload FROM change_log ORDER BY seq`)
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		for _, r := range rows {
			sb.WriteString(rs(r["payload"]))
		}
		return sb.String() + strings.Join(f.blobKeys(t), ",")
	}
	before := snapshot()
	if err := f.photos.ConvertInlinePhotos(ctx); err != nil {
		t.Fatal(err)
	}
	// Nor does a start that follows an interrupted run (no completion mark).
	if _, err := f.db.Exec(`DELETE FROM photo_migration`); err != nil {
		t.Fatal(err)
	}
	if err := f.photos.ConvertInlinePhotos(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before || countAll(t, f, "photo") != 3 || countAll(t, f, "photo_migration") != 1 {
		t.Fatal("running the conversion again changed data")
	}
}

func countAll(t *testing.T, f *photoFixture, table string) int {
	t.Helper()
	n, err := countRows(context.Background(), f.db, `SELECT COUNT(*) FROM `+table)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// More rows than one batch, so the walk has to continue past the first page.
func TestConvertInlinePhotosInBatches(t *testing.T) {
	f := newPhotoFixture(t)
	l := &legacyData{t: t, f: f}
	const visits = convertBatch*2 + 3
	tag := stampAt(time.Now().Add(-time.Hour), 0)
	for i := range visits {
		l.visit("visit-"+itoa(int64(i)), "org-a", "hive-a", false, wsync.ORSet{inlineJPEG(t, uint8(i)): {A: []string{tag}}})
	}
	if err := f.photos.ConvertInlinePhotos(context.Background()); err != nil {
		t.Fatal(err)
	}
	if photos, blobs := countAll(t, f, "photo"), len(f.blobKeys(t)); photos != visits || blobs != visits {
		t.Fatalf("converted %d photos into %d blobs, want %d", photos, blobs, visits)
	}
}
