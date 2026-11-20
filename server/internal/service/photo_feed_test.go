package service

import (
	"strings"
	"testing"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

var legacyA = legacyCtx("user-a", "org-a")

// Both kinds of client sync the same photo, each in its own form: a 0.4.x
// app the inline data URL and no photo record, a current app the record and
// no inline data.
func TestInlinePhotosReachOnlyLegacyClients(t *testing.T) {
	f := newPhotoFixture(t)
	visit := f.newVisit(t)
	inline, picture := inlineJPEG(t, 1), inlineJPEG(t, 2)
	if err := push(f.sync, legacyA,
		legacyAdd(visit, inline),
		legacyAdd(visit, inlineJPEG(t, 3)),
		legacyHivePhoto(picture),
		change("hive", "hive-a", "apiary-a", `{"note":"moved","photo":"`+picture+`"}`),
	); err != nil {
		t.Fatal(err)
	}

	old := pullLog(t, f.sync, legacyA)
	if n := len(changesFor(old, "photo")); n != 0 {
		t.Fatalf("a 0.4.x app was sent %d photo records; it has no table for them", n)
	}
	var oldVisit, oldHive []string
	for _, c := range old {
		switch c.Entity {
		case "inspection":
			oldVisit = append(oldVisit, c.PayloadJson)
		case "hive":
			oldHive = append(oldHive, c.PayloadJson)
		}
	}
	if !anyContains(oldVisit, inline) || !anyContains(oldHive, picture) {
		t.Fatal("a 0.4.x app must still receive the inline photos")
	}

	current := pullLog(t, f.sync, ctxA)
	if n := len(changesFor(current, "photo")); n != 3 {
		t.Fatalf("a current app was sent %d photo records, want 3", n)
	}
	var visitChanges, hiveChanges []string
	for _, c := range current {
		if c.Entity == "photo" {
			continue // its thumbnail is a data URL, and belongs there
		}
		if strings.Contains(c.PayloadJson, "data:image") {
			t.Fatalf("a current app was sent inline image data in a %s change", c.Entity)
		}
		switch c.Entity {
		case "inspection":
			visitChanges = append(visitChanges, c.PayloadJson)
		case "hive":
			hiveChanges = append(hiveChanges, c.PayloadJson)
		}
	}
	// The rest of a change arrives; a change that was only images is not sent.
	if !anyContains(hiveChanges, `"note":"moved"`) {
		t.Fatalf("the rest of the changes must arrive: %v %v", visitChanges, hiveChanges)
	}
	if anyContains(visitChanges, "photo_keys") {
		t.Fatalf("a current app was sent the legacy photo set: %v", visitChanges)
	}
	if got, want := len(changesFor(current, "inspection")), len(changesFor(old, "inspection"))-2; got != want {
		t.Fatalf("a current app was sent %d inspection changes, want %d (the image-only ones left out)", got, want)
	}
	if anyContains(hiveChanges, `"photo"`) {
		t.Fatalf("hive changes for a current app still name the inline column: %v", hiveChanges)
	}

	// The same holds for a device that starts from a snapshot.
	rows, _, _ := snapshotAll(t, f.sync, ctxA)
	var photos int
	for key, row := range rows {
		entity, _, _ := strings.Cut(key, "/")
		if entity == "photo" {
			photos++
			if thumb, _ := row.fields["thumb"].(string); !strings.HasPrefix(thumb, "data:image/jpeg") {
				t.Errorf("%s: snapshot row without thumbnail", key)
			}
			continue
		}
		for col, v := range row.fields {
			if s, ok := v.(string); ok && strings.Contains(s, "data:image") {
				t.Errorf("%s: snapshot carries inline image data in %s", key, col)
			}
		}
		if _, ok := row.fields["photo_keys"]; ok {
			t.Errorf("%s: snapshot carries the legacy photo set", key)
		}
	}
	if photos != 3 {
		t.Fatalf("snapshot has %d photo rows, want 3", photos)
	}
	// The server keeps the inline copies for the old apps all the same.
	if !visible(f.inlineSet(t, visit), inline) || f.hiveInline(t, "hive-a") != picture {
		t.Fatal("stripping what is sent must not change what is stored")
	}
}

func TestForCurrentLeavesOtherChangesAlone(t *testing.T) {
	thumb := `{"thumb":"data:image/jpeg;base64,AAAA","owner_entity":"hive"}`
	for name, ch := range map[string]*wv1.Change{
		"photo record":     {Entity: "photo", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: thumb},
		"delete":           {Entity: "hive", Op: wv1.ChangeOp_CHANGE_OP_DELETE, PayloadJson: `{"deleted":1}`},
		"hive without":     {Entity: "hive", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: `{"name":"data: not a photo"}`},
		"cleared picture":  {Entity: "hive", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: `{"photo":""}`},
		"note with a link": {Entity: "inspection", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: `{"note":"see \"data:x\""}`},
		"not JSON":         {Entity: "inspection", Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: `"data:`},
	} {
		before := ch.PayloadJson
		if !forCurrent(ch) || ch.PayloadJson != before {
			t.Errorf("%s: changed to %q (kept: %v)", name, ch.PayloadJson, forCurrent(ch))
		}
	}
	// A change of nothing but the photo set is dropped; a mixed one keeps the rest.
	only := &wv1.Change{Entity: "inspection", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: `{"photo_keys":{"remove":["data:image/jpeg;base64,AA"],"removed_tags":{"data:image/jpeg;base64,AA":["t"]}}}`}
	if forCurrent(only) {
		t.Errorf("an image-only remove must not be sent, got %q", only.PayloadJson)
	}
	mixed := &wv1.Change{Entity: "inspection", Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: `{"note":"n","photo_keys":{"add":["data:image/jpeg;base64,AA"],"remove":["k1"],"removed_tags":{"k1":["t"]}}}`}
	if !forCurrent(mixed) || mixed.PayloadJson != `{"note":"n"}` {
		t.Errorf("mixed change became %q", mixed.PayloadJson)
	}
}
