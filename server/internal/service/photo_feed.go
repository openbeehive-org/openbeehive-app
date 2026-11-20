package service

import (
	"encoding/json"
	"strings"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// What a current app is sent about photos.
//
// The inline photos of 0.4.x apps (photo_legacy.go) stay in the rows and in
// the change log for those apps. An app that announces a protocol version
// shows the photo records made from them and has no use for the legacy
// set inspection.photo_keys at all, so Pull and Snapshot leave it out for
// such an app, together with an inline hive.photo: megabytes it would
// download and throw away. A hive.photo that is not an inline image passes
// as it is.
//
// photo_keys only ever holds data URLs (wireSetDelta refuses anything
// else). Elements of any other kind that were stored before that rule are
// left out of what 0.4.x apps receive too (forLegacy): such an app keeps its
// sets in plain objects, and an element named like one of their built-in
// properties ("constructor", "__proto__") breaks every pull.

// forCurrent removes inline photos and the legacy photo set from a logged
// change. It reports false when the change consisted of nothing else, and
// then has nothing to tell a current app.
func forCurrent(ch *wv1.Change) bool {
	if ch.Op != wv1.ChangeOp_CHANGE_OP_UPSERT {
		return true
	}
	switch {
	case ch.Entity == "hive" && strings.Contains(ch.PayloadJson, `"data:`):
	case ch.Entity == "inspection" && strings.Contains(ch.PayloadJson, `"photo_keys"`):
	default:
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(ch.PayloadJson), &fields) != nil || fields == nil {
		return true
	}
	changed := false
	if ch.Entity == "hive" {
		var inline string
		if json.Unmarshal(fields["photo"], &inline) == nil && isDataURL(inline) {
			delete(fields, "photo")
			changed = true
		}
	} else if _, ok := fields["photo_keys"]; ok {
		delete(fields, "photo_keys")
		changed = true
	}
	if !changed {
		return true
	}
	if len(fields) == 0 {
		return false
	}
	payload, err := marshalPlain(fields)
	if err != nil {
		return true
	}
	ch.PayloadJson = payload
	return true
}

// legacyPhotoKeys removes the elements that are not data URLs from the
// photo_keys delta of an inspection payload; a delta that is empty
// afterwards is dropped. It reports whether it changed the payload.
func legacyPhotoKeys(fields map[string]json.RawMessage) bool {
	left := 0
	foreign := func(elem string) bool { return !isDataURL(elem) }
	changed := editPhotoKeys(fields, func(d *wsync.SetDelta) bool {
		kept, keptRemoves := d.Add[:0:0], d.Remove[:0:0]
		for _, e := range d.Add {
			if !foreign(e) {
				kept = append(kept, e)
			}
		}
		for _, e := range d.Remove {
			if !foreign(e) {
				keptRemoves = append(keptRemoves, e)
			}
		}
		edited := len(kept) != len(d.Add) || len(keptRemoves) != len(d.Remove)
		d.Add, d.Remove = kept, keptRemoves
		for e := range d.RemovedTags {
			if foreign(e) {
				delete(d.RemovedTags, e)
				edited = true
			}
		}
		left = len(d.Add) + len(d.Remove)
		return edited
	})
	if changed && left == 0 {
		delete(fields, "photo_keys")
	}
	return changed
}

// withoutInlinePhotos removes inline photos from the payload of a snapshot
// row for a current app: the value of hive.photo, and the legacy set
// photo_keys as a whole.
func withoutInlinePhotos(entity string, payload map[string]any) {
	switch entity {
	case "hive":
		if inline, ok := payload["photo"].(string); ok && isDataURL(inline) {
			payload["photo"] = ""
		}
	case "inspection":
		delete(payload, "photo_keys")
	}
}
