package service

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Inline photos of 0.4.x apps.
//
// A 0.4.x app stores a photo as a data URL: as an element of the OR-Set
// inspection.photo_keys, or as the scalar hive.photo. Those apps keep
// syncing, so the server speaks both formats:
//
//   - Their pushes are merged and logged as before; that is the feed other
//     0.4.x devices read. On top of that, every inline image that is visible
//     after the merge becomes a photo record with a blob, which is what
//     updated apps read. Updated apps never write the two legacy columns and
//     ignore them in what they pull.
//   - Removing an inline image on a 0.4.x device deletes its photo record.
//     Deleting the photo on an updated device or through the API makes the
//     server derive the matching legacy change (a set remove, or an emptied
//     hive.photo), so the photo disappears on 0.4.x devices too.
//   - Photos added by updated apps exist only as records; 0.4.x devices do
//     not show them.
//
// Inline data is kept only for as long as a 0.4.x device needs it, so it
// does not pile up:
//
//   - A live inline photo has its data URL in the row and in the logged
//     change that added it. Nothing else.
//   - When it is deleted, both copies are erased in the same transaction.
//     The element leaves the set entirely: the server keeps no tombstones
//     for inline elements. The deleted photo record remembers the image by
//     hash (legacy_key) and when it was deleted, which is enough to recognise
//     a late replay of the add.
//   - The one copy left is the logged remove, which 0.4.x devices that were
//     offline need in order to drop their own copy. It is erased after
//     legacyRemoveRetention (photo_sweep.go).
//   - hive.photo is a scalar: only its current value is kept, older values
//     are erased from the log when it changes.
//
// Elements that are not data URLs are left alone in every respect.

// legacyRemoveRetention is how long a logged remove keeps the data URL it
// names. A 0.4.x device that stays offline longer keeps showing the photo
// until it is updated.
const legacyRemoveRetention = 90 * 24 * time.Hour

// photoKeysDelta extracts the photo_keys delta of a normalised inspection
// payload.
func photoKeysDelta(payload string) (wsync.SetDelta, bool) {
	var fields map[string]any
	if json.Unmarshal([]byte(payload), &fields) != nil {
		return wsync.SetDelta{}, false
	}
	raw, ok := fields["photo_keys"]
	if !ok {
		return wsync.SetDelta{}, false
	}
	d, err := wsync.ParseSetDelta(raw)
	return d, err == nil
}

// legacyVisitPhotos brings the photo records of an inspection in line with
// its photo_keys set after a 0.4.x change to it.
func (p *Photos) legacyVisitPhotos(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change, orgID string) error {
	d, ok := photoKeysDelta(ch.PayloadJson)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, elem := range append(append([]string{}, d.Add...), d.Remove...) {
		if seen[elem] || !isDataURL(elem) {
			continue
		}
		seen[elem] = true
		if err := p.reconcileVisitPhoto(ctx, tx, ch, orgID, elem); err != nil {
			return err
		}
	}
	return nil
}

func (p *Photos) reconcileVisitPhoto(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change, orgID, elem string) error {
	set, found, err := loadORSet(ctx, tx, "inspection", "photo_keys", ch.EntityId, orgID)
	if err != nil || !found {
		return err
	}
	key := legacyKey(elem)
	photos, err := queryPhotos(ctx, tx, `owner_id = ? AND owner_entity = ? AND organization_id = ? AND legacy_key = ?`,
		ch.EntityId, ownerInspection, orgID, key)
	if err != nil {
		return err
	}
	var live *photoRow
	replayed := false
	for i := range photos {
		switch {
		case !photos[i].Deleted:
			live = &photos[i]
		case wsync.Compare(photos[i].Clock["deleted"], ch.Hlc) > 0:
			// The same image was deleted after this add was stamped: the add
			// is a late delivery, not a new photo.
			replayed = true
		}
	}
	switch {
	case visible(set, elem) && live != nil:
		return nil
	case visible(set, elem) && !replayed:
		return p.createFromInline(ctx, tx, ch, orgID, ownerInspection, ch.EntityId, elem, stampTime(ch.Hlc, p.now()))
	case live != nil:
		return p.deletePhoto(ctx, tx, ch, live, false)
	}
	return dropInlineElement(ctx, tx, orgID, ch.EntityId, key)
}

// stampTime is the wall-clock part of a stamp.
func stampTime(stamp string, fallback time.Time) time.Time {
	if ms, _, _, ok := wsync.Parse(stamp); ok {
		return time.UnixMilli(ms)
	}
	return fallback
}

// legacyHivePhoto brings the picture records of a hive in line with
// hive.photo after a 0.4.x change to it. The newest picture counts: an
// inline photo that is older than a picture set by an updated app is not
// converted, and setting or clearing hive.photo removes the pictures that
// came before it.
func (p *Photos) legacyHivePhoto(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change, orgID string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(ch.PayloadJson), &fields) != nil {
		return nil
	}
	if _, writesPhoto := fields["photo"]; !writesPhoto {
		return nil
	}
	rows, err := queryMaps(ctx, tx, `SELECT photo, field_hlc FROM hive WHERE id = ? AND organization_id = ?`, ch.EntityId, orgID)
	if err != nil || len(rows) == 0 {
		return err
	}
	inline := rs(rows[0]["photo"])
	setAt := stampTime(wsync.ParseFieldClock(rs(rows[0]["field_hlc"]))["photo"], p.now())
	key := ""
	if isDataURL(inline) {
		key = legacyKey(inline)
	}
	pictures, err := livePhotosOf(ctx, tx, orgID, ownerHive, ch.EntityId)
	if err != nil {
		return err
	}
	have := false
	for i := range pictures {
		pic := &pictures[i]
		switch {
		case key != "" && pic.LegacyKey == key:
			have = true
		case pic.CreatedAt.After(setAt):
			return nil // a newer picture exists: this one never shows
		default:
			if err := p.deletePhoto(ctx, tx, ch, pic, false); err != nil {
				return err
			}
		}
	}
	if key != "" && !have {
		if err := p.createFromInline(ctx, tx, ch, orgID, ownerHive, ch.EntityId, inline, setAt); err != nil {
			return err
		}
	}
	// Only the current inline value is kept in the log.
	return editPayloads(ctx, tx, orgID, "hive", ch.EntityId, func(fields map[string]json.RawMessage, _ string) bool {
		return blankInlinePhoto(fields, func(k string) bool { return k != key })
	})
}

// createFromInline turns an inline image into a photo record: the image
// itself goes to the blob store, a thumbnail made from it into the record.
// An inline value that is not a JPEG, PNG or WebP image stays what it is.
func (p *Photos) createFromInline(ctx context.Context, tx *sqlx.Tx, cause *wv1.Change, orgID, ownerEntity, ownerID, inline string, created time.Time) error {
	if p.blobs == nil {
		return nil
	}
	prepared := preparedFrom(ctx)
	img, ok := prepared.take(inline)
	if !ok {
		// Not prepared (the start-up conversion): do the work here. A
		// request prepares every image it carries; should one still get
		// here, it is stored without a thumbnail rather than decoded while
		// the write lock is held.
		data := decodeDataURL(inline, MaxPhotoBytes)
		img = preparedImage{id: uuid.NewString(), typ: sniffPhotoType(data), size: len(data)}
		if img.typ == "" {
			return nil
		}
		img.key = blob.PhotoKey(orgID, img.id)
		if prepared == nil {
			img.thumb = makeThumb(data)
		}
		// Stored before the record exists: if the transaction fails, the
		// sweep removes the blob as one without a record.
		if err := p.blobs.Put(ctx, img.key, bytes.NewReader(data), int64(len(data)), img.typ); err != nil {
			return err
		}
	}
	id, key, typ := img.id, img.key, img.typ
	payload, err := json.Marshal(map[string]any{
		"organization_id": orgID, "owner_entity": ownerEntity, "owner_id": ownerID,
		"thumb": img.thumb, "content_type": typ, "size_bytes": img.size,
		"created_at": jsTime(created), "deleted": 0,
	})
	if err != nil {
		return err
	}
	if err := p.derive(ctx, tx, cause, orgID, &wv1.Change{
		Entity: "photo", EntityId: id, Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: string(payload), Hlc: p.hlc.Now(),
	}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, tx.Rebind(
		`UPDATE photo SET blob_key = ?, blob_type = ?, blob_size = ?, legacy_key = ? WHERE id = ?`),
		key, typ, img.size, legacyKey(inline), id)
	return err
}

// eraseLegacy removes the inline copy of a deleted photo that was converted
// from the 0.4.x format. With notifyLegacy the server first derives the
// change that removes it on 0.4.x devices; without, the change that caused
// the delete already is that change.
func (p *Photos) eraseLegacy(ctx context.Context, tx *sqlx.Tx, row *photoRow, cause *wv1.Change, notifyLegacy bool) error {
	if row.OwnerEntity == ownerHive {
		return p.eraseLegacyHivePhoto(ctx, tx, row, cause, notifyLegacy)
	}
	set, found, err := loadORSet(ctx, tx, "inspection", "photo_keys", row.OwnerID, row.OrgID)
	if err != nil {
		return err
	}
	if elem := elementByKey(set, row.LegacyKey); found && notifyLegacy && elem != "" && visible(set, elem) {
		// Without removed_tags: 0.4.x devices drop every add tag they hold.
		payload, err := marshalPlain(map[string]any{"photo_keys": wsync.SetDelta{Remove: []string{elem}}})
		if err != nil {
			return err
		}
		if err := p.derive(ctx, tx, cause, row.OrgID, &wv1.Change{
			Entity: "inspection", EntityId: row.OwnerID, Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
			PayloadJson: payload, Hlc: p.hlc.Now(),
		}); err != nil {
			return err
		}
	}
	return dropInlineElement(ctx, tx, row.OrgID, row.OwnerID, row.LegacyKey)
}

func (p *Photos) eraseLegacyHivePhoto(ctx context.Context, tx *sqlx.Tx, row *photoRow, cause *wv1.Change, notifyLegacy bool) error {
	rows, err := queryMaps(ctx, tx, `SELECT photo, field_hlc FROM hive WHERE id = ? AND organization_id = ?`, row.OwnerID, row.OrgID)
	if err != nil {
		return err
	}
	current := len(rows) > 0 && isDataURL(rs(rows[0]["photo"])) && legacyKey(rs(rows[0]["photo"])) == row.LegacyKey
	switch {
	case current && notifyLegacy:
		clock := wsync.ParseFieldClock(rs(rows[0]["field_hlc"]))
		if err := p.derive(ctx, tx, cause, row.OrgID, &wv1.Change{
			Entity: "hive", EntityId: row.OwnerID, Op: wv1.ChangeOp_CHANGE_OP_UPSERT,
			PayloadJson: `{"photo":""}`, Hlc: p.stampAfter(clock["photo"]),
		}); err != nil {
			return err
		}
	case current:
		// The hive itself is deleted: no device shows it, the value just goes.
		if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE hive SET photo = '' WHERE id = ?`), row.OwnerID); err != nil {
			return err
		}
	}
	return editPayloads(ctx, tx, row.OrgID, "hive", row.OwnerID, func(fields map[string]json.RawMessage, _ string) bool {
		return blankInlinePhoto(fields, func(key string) bool { return key == row.LegacyKey })
	})
}

// blankInlinePhoto empties the "photo" field of a hive payload when it holds
// an inline image whose key matches.
func blankInlinePhoto(fields map[string]json.RawMessage, match func(key string) bool) bool {
	var inline string
	if json.Unmarshal(fields["photo"], &inline) != nil || !isDataURL(inline) || !match(legacyKey(inline)) {
		return false
	}
	fields["photo"] = json.RawMessage(`""`)
	return true
}

// elementByKey finds the inline element with the given legacy key.
func elementByKey(set wsync.ORSet, key string) string {
	for elem := range set {
		if isDataURL(elem) && legacyKey(elem) == key {
			return elem
		}
	}
	return ""
}

// dropInlineElement erases one inline image of an inspection: the element
// leaves the stored set, and the logged changes that added it no longer
// carry it. Logged removes keep naming it (see legacyRemoveRetention).
func dropInlineElement(ctx context.Context, tx *sqlx.Tx, orgID, inspectionID, key string) error {
	set, found, err := loadORSet(ctx, tx, "inspection", "photo_keys", inspectionID, orgID)
	if err != nil {
		return err
	}
	if elem := elementByKey(set, key); found && elem != "" {
		delete(set, elem)
		if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE inspection SET photo_keys = ? WHERE id = ?`),
			set.Marshal(), inspectionID); err != nil {
			return err
		}
	}
	return editPayloads(ctx, tx, orgID, "inspection", inspectionID, func(fields map[string]json.RawMessage, _ string) bool {
		return editPhotoKeys(fields, func(d *wsync.SetDelta) bool {
			return dropAdds(d, func(elem string) bool { return legacyKey(elem) == key })
		})
	})
}

// editPhotoKeys applies edit to the photo_keys delta of an inspection
// payload and stores the result when edit reports a change.
func editPhotoKeys(fields map[string]json.RawMessage, edit func(d *wsync.SetDelta) bool) bool {
	raw, ok := fields["photo_keys"]
	if !ok {
		return false
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return false
	}
	d, err := wsync.ParseSetDelta(decoded)
	if err != nil || !edit(&d) {
		return false
	}
	out, err := marshalPlain(d)
	if err != nil {
		return false
	}
	fields["photo_keys"] = json.RawMessage(out)
	return true
}

// dropAdds removes the inline elements that match from the delta's adds.
func dropAdds(d *wsync.SetDelta, match func(elem string) bool) bool {
	kept := d.Add[:0:0]
	for _, elem := range d.Add {
		if !isDataURL(elem) || !match(elem) {
			kept = append(kept, elem)
		}
	}
	changed := len(kept) != len(d.Add)
	d.Add = kept
	return changed
}

// dropRemoves removes the inline elements that match from the delta's
// removes and from the tags listed for them.
func dropRemoves(d *wsync.SetDelta, match func(elem string) bool) bool {
	kept := d.Remove[:0:0]
	for _, elem := range d.Remove {
		if !isDataURL(elem) || !match(elem) {
			kept = append(kept, elem)
		}
	}
	changed := len(kept) != len(d.Remove)
	d.Remove = kept
	for elem := range d.RemovedTags {
		if isDataURL(elem) && match(elem) {
			delete(d.RemovedTags, elem)
			changed = true
		}
	}
	return changed
}
