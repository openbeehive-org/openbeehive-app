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
// shows the photo records made from them, so the inline copies would only
// be megabytes it downloads and throws away: Pull and Snapshot leave them
// out for such an app. Set elements and values that are not inline images
// pass as they are.

// forCurrent removes inline photos from a logged change. It reports false
// when the change consisted of nothing else, and then has nothing to tell a
// current app.
func forCurrent(ch *wv1.Change) bool {
	if ch.Op != wv1.ChangeOp_CHANGE_OP_UPSERT || (ch.Entity != "inspection" && ch.Entity != "hive") ||
		!strings.Contains(ch.PayloadJson, `"data:`) {
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
	} else {
		changed = stripInlineDelta(fields)
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

// stripInlineDelta takes the inline images out of the photo_keys delta of an
// inspection payload; a delta that is empty afterwards is dropped.
func stripInlineDelta(fields map[string]json.RawMessage) bool {
	left := 0
	changed := editPhotoKeys(fields, func(d *wsync.SetDelta) bool {
		all := func(string) bool { return true }
		edited := dropAdds(d, all)
		edited = dropRemoves(d, all) || edited
		left = len(d.Add) + len(d.Remove)
		return edited
	})
	if changed && left == 0 {
		delete(fields, "photo_keys")
	}
	return changed
}

// withoutInlinePhotos removes inline photos from the payload of a snapshot
// row: the value of hive.photo, and the inline elements of the photo_keys
// set.
func withoutInlinePhotos(entity string, payload map[string]any) {
	switch entity {
	case "hive":
		if inline, ok := payload["photo"].(string); ok && isDataURL(inline) {
			payload["photo"] = ""
		}
	case "inspection":
		set, ok := payload["photo_keys"].(wsync.ORSet)
		if !ok {
			return
		}
		for elem := range set {
			if isDataURL(elem) {
				delete(set, elem)
			}
		}
	}
}
