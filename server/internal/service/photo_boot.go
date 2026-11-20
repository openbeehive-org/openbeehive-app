package service

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// One-time conversion of the inline photos an installation has collected
// under 0.4.x (see photo_legacy.go for the two formats). It runs at start-up
// before the server accepts requests:
//
//   - every inline image that is still visible becomes a photo record with
//     a blob, logged like any other change so that devices receive it;
//   - set elements that were removed (or belong to a deleted inspection) are
//     not converted: they are dropped from the row, and from the log as far
//     as no 0.4.x device can still need them;
//   - of hive.photo only the current value is kept in the log.
//
// Work is done row by row in small transactions. A photo that already has
// its record is skipped, so an interrupted run continues where it stopped;
// once it has completed, photo_migration records that and later starts do
// not look at the data again.

const (
	stepInlinePhotos = "inline_photos"
	convertBatch     = 20
)

// ConvertInlinePhotos runs the conversion unless it has completed before.
func (p *Photos) ConvertInlinePhotos(ctx context.Context) error {
	if p.blobs == nil {
		return nil
	}
	done, err := countRows(ctx, p.db, `SELECT COUNT(*) FROM photo_migration WHERE step = ?`, stepInlinePhotos)
	if err != nil || done > 0 {
		return err
	}
	// The server listens only once this is done. Each record is converted in
	// a transaction of its own and skipped when it is converted already, so
	// a restart in between (a health check that gave up) keeps the progress.
	log.Printf("photos: converting the inline photos of 0.4.x, once; the server starts listening when this is done")
	visits, err := p.convertRows(ctx, "inspection", `photo_keys LIKE '%data:%'`, p.convertVisit)
	if err != nil {
		return err
	}
	hives, err := p.convertRows(ctx, "hive", `photo LIKE 'data:%'`, p.convertHive)
	if err != nil {
		return err
	}
	if err := p.eraseOldHivePhotos(ctx); err != nil {
		return err
	}
	if visits+hives > 0 {
		log.Printf("photos: converted the inline photos of %d inspections and %d hives", visits, hives)
	}
	_, err = p.db.ExecContext(ctx, p.db.Rebind(`INSERT INTO photo_migration (step, done_at) VALUES (?, ?)`),
		stepInlinePhotos, p.now().UTC())
	return err
}

// convertRows walks the rows of a table that match where in id order, in
// batches, and converts each in a transaction of its own. It returns the
// number of rows it went through.
func (p *Photos) convertRows(ctx context.Context, table, where string, convert func(ctx context.Context, tx *sqlx.Tx, id string) error) (int, error) {
	total, after := 0, ""
	for {
		var ids []string
		if err := p.db.SelectContext(ctx, &ids, p.db.Rebind(
			`SELECT id FROM `+table+` WHERE id > ? AND `+where+` ORDER BY id LIMIT ?`), after, convertBatch); err != nil {
			return total, err
		}
		for _, id := range ids {
			if err := p.inTx(ctx, func(tx *sqlx.Tx) error { return convert(ctx, tx, id) }); err != nil {
				return total, err
			}
			after = id
		}
		total += len(ids)
		if len(ids) < convertBatch {
			return total, nil
		}
		log.Printf("photos: converting inline photos: %d %s rows done", total, table)
	}
}

// convertVisit converts the inline photos of one inspection.
func (p *Photos) convertVisit(ctx context.Context, tx *sqlx.Tx, id string) error {
	rows, err := queryMaps(ctx, tx, `SELECT organization_id, hive_id, deleted, photo_keys FROM inspection WHERE id = ?`, id)
	if err != nil || len(rows) == 0 {
		return err
	}
	orgID, gone := rs(rows[0]["organization_id"]), rb(rows[0]["deleted"])
	set := wsync.ParseORSet(rs(rows[0]["photo_keys"]))
	cause, err := systemCause(ctx, tx, rs(rows[0]["hive_id"]))
	if err != nil {
		return err
	}
	dead := map[string]bool{}
	for elem, tags := range set {
		switch {
		case !isDataURL(elem):
		case gone || !visible(set, elem):
			dead[legacyKey(elem)] = true
			delete(set, elem)
		default:
			if err := p.convertElement(ctx, tx, cause, orgID, id, elem, firstAdd(tags.A, p.now())); err != nil {
				return err
			}
		}
	}
	if len(dead) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE inspection SET photo_keys = ? WHERE id = ?`), set.Marshal(), id); err != nil {
		return err
	}
	// Adds of dropped elements go at once. Removes stay for the 0.4.x devices
	// that have not seen them yet, unless they are past their retention or
	// the whole inspection is deleted.
	cutoff := p.now().Add(-legacyRemoveRetention)
	isDead := func(elem string) bool { return dead[legacyKey(elem)] }
	return editPayloads(ctx, tx, orgID, "inspection", id, func(fields map[string]json.RawMessage, stamp string) bool {
		return editPhotoKeys(fields, func(d *wsync.SetDelta) bool {
			changed := dropAdds(d, isDead)
			if gone || stampTime(stamp, time.Time{}).Before(cutoff) {
				changed = dropRemoves(d, isDead) || changed
			}
			return changed
		})
	})
}

// convertElement creates the photo record of one visible inline image
// unless it already has one.
func (p *Photos) convertElement(ctx context.Context, tx *sqlx.Tx, cause *wv1.Change, orgID, inspectionID, elem string, added time.Time) error {
	have, err := countRows(ctx, tx, `SELECT COUNT(*) FROM photo WHERE owner_id = ? AND owner_entity = ?
		AND organization_id = ? AND legacy_key = ? AND deleted = FALSE`, inspectionID, ownerInspection, orgID, legacyKey(elem))
	if err != nil || have > 0 {
		return err
	}
	return p.createFromInline(ctx, tx, cause, orgID, ownerInspection, inspectionID, elem, added)
}

// firstAdd is the time of the earliest add tag of a set element.
func firstAdd(tags []string, fallback time.Time) time.Time {
	first := ""
	for _, t := range tags {
		if first == "" || wsync.Compare(t, first) < 0 {
			first = t
		}
	}
	return stampTime(first, fallback)
}

// systemCause is the scope and author of the changes a conversion derives
// for a record of the given hive.
func systemCause(ctx context.Context, q queryer, hiveID string) (*wv1.Change, error) {
	rows, err := queryMaps(ctx, q, `SELECT apiary_id FROM hive WHERE id = ?`, hiveID)
	if err != nil {
		return nil, err
	}
	cause := &wv1.Change{AuthorId: systemAuthor}
	if len(rows) > 0 {
		cause.ScopeId = rs(rows[0]["apiary_id"])
	}
	return cause, nil
}

// convertHive converts the inline picture of one hive.
func (p *Photos) convertHive(ctx context.Context, tx *sqlx.Tx, id string) error {
	rows, err := queryMaps(ctx, tx, `SELECT organization_id, deleted, photo, field_hlc FROM hive WHERE id = ?`, id)
	if err != nil || len(rows) == 0 {
		return err
	}
	if rb(rows[0]["deleted"]) {
		// No device shows a deleted hive: its picture is dropped.
		_, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE hive SET photo = '' WHERE id = ?`), id)
		return err
	}
	orgID, inline := rs(rows[0]["organization_id"]), rs(rows[0]["photo"])
	pictures, err := livePhotosOf(ctx, tx, orgID, ownerHive, id)
	if err != nil || len(pictures) > 0 || !isDataURL(inline) {
		return err // converted before, or replaced by an updated app since
	}
	cause, err := systemCause(ctx, tx, id)
	if err != nil {
		return err
	}
	setAt := stampTime(wsync.ParseFieldClock(rs(rows[0]["field_hlc"]))["photo"], p.now())
	return p.createFromInline(ctx, tx, cause, orgID, ownerHive, id, inline, setAt)
}

// eraseOldHivePhotos empties, in the logged changes of every hive, the
// inline pictures that are not the hive's current one.
func (p *Photos) eraseOldHivePhotos(ctx context.Context) error {
	hives, err := queryMaps(ctx, p.db, `SELECT DISTINCT org_id, entity_id FROM change_log
		WHERE entity = 'hive' AND payload LIKE '%"photo":"data:%'`)
	if err != nil {
		return err
	}
	for _, h := range hives {
		orgID, id := rs(h["org_id"]), rs(h["entity_id"])
		err := p.inTx(ctx, func(tx *sqlx.Tx) error {
			current := ""
			rows, err := queryMaps(ctx, tx, `SELECT photo FROM hive WHERE id = ? AND deleted = FALSE`, id)
			if err != nil {
				return err
			}
			if len(rows) > 0 && isDataURL(rs(rows[0]["photo"])) {
				current = legacyKey(rs(rows[0]["photo"]))
			}
			return editPayloads(ctx, tx, orgID, "hive", id, func(fields map[string]json.RawMessage, _ string) bool {
				return blankInlinePhoto(fields, func(key string) bool { return key != current })
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}
