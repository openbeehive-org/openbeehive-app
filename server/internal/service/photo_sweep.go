package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"time"

	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Background upkeep of photos. One loop per server process; the server runs
// as a single instance.
//
//   - Right after a photo was deleted: remove its blob. The delete itself
//     only marks the row inside its transaction; a row that is deleted and
//     still names a blob is the to-do list, so nothing is lost when the
//     process stops in between.
//   - Every tidyInterval: delete the photos of records that were deleted a
//     while ago (a deleted inspection can be restored for a moment, so its
//     photos are not deleted along with it).
//   - At start and every sweepInterval: give blobs back to photos that lost
//     them (a restored database), remove blobs of deleted photos, and the
//     inline data of logged removes that are past their retention. Blobs no
//     photo row knows at all (uploads whose record never arrived, a
//     workspace purge that stopped, or images of photos newer than a
//     restored backup whose devices have not sent them again yet) are
//     removed only after OrphanGrace, and never by the sweep at start.
//     Only keys of the form the server builds for photos are touched.

const (
	tidyInterval  = 15 * time.Minute
	sweepInterval = 6 * time.Hour
	// ownerGrace is how long a deleted record keeps its photos.
	ownerGrace = time.Hour
	// blobGrace protects blobs that are being written right now: an upload
	// stores the blob first and names it on the photo row afterwards.
	blobGrace = time.Hour
	// DefaultOrphanGrace is the shortest time a blob that no photo row
	// knows is kept. After a database restore, devices send the records of
	// newer photos again when they next sync, which may be weeks later.
	DefaultOrphanGrace = 30 * 24 * time.Hour
)

// systemAuthor is recorded as the author of changes the upkeep makes.
const systemAuthor = "system"

func (p *Photos) wakeWorker() {
	select {
	case p.wake <- struct{}{}:
	default: // already pending
	}
}

// Run does the upkeep until ctx ends.
func (p *Photos) Run(ctx context.Context) {
	if p.blobs == nil {
		return
	}
	tidy := time.NewTicker(tidyInterval)
	defer tidy.Stop()
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()
	p.Tidy(ctx)
	// The first sweep only adopts: right after a start, possibly on a
	// restored database, no device has had a chance to send its records.
	p.sweep(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.wakeDelay):
			}
			p.logErr("remove blobs of deleted photos", p.removeDeletedBlobs(ctx))
		case <-tidy.C:
			p.Tidy(ctx)
		case <-sweep.C:
			p.Sweep(ctx)
		}
	}
}

func (p *Photos) logErr(what string, err error) {
	if err != nil {
		log.Printf("photos: %s: %v", what, err)
	}
}

// Tidy runs the frequent upkeep once.
func (p *Photos) Tidy(ctx context.Context) {
	p.logErr("delete photos of deleted records", p.deleteOrphanedPhotos(ctx))
	p.logErr("remove blobs of deleted photos", p.removeDeletedBlobs(ctx))
}

// Sweep runs the occasional upkeep once.
func (p *Photos) Sweep(ctx context.Context) { p.sweep(ctx, true) }

func (p *Photos) sweep(ctx context.Context, removeUnknown bool) {
	n, err := p.sweepBlobs(ctx, removeUnknown)
	p.logErr("sweep blobs", err)
	if n > 0 {
		log.Printf("photos: removed %d blobs of deleted or unknown photos", n)
	}
	p.logErr("erase old inline removes", p.eraseOldLegacyRemoves(ctx))
}

// removeDeletedBlobs deletes the blob of every deleted photo that still
// names one, then clears the name.
func (p *Photos) removeDeletedBlobs(ctx context.Context) error {
	rows, err := queryMaps(ctx, p.db, `SELECT id, blob_key FROM photo WHERE deleted = TRUE AND blob_key <> ''`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := p.blobs.Delete(ctx, rs(r["blob_key"])); err != nil {
			return err
		}
		if _, err := p.db.ExecContext(ctx, p.db.Rebind(
			`UPDATE photo SET blob_key = '', blob_type = '', blob_size = 0 WHERE id = ? AND deleted = TRUE`),
			rs(r["id"])); err != nil {
			return err
		}
	}
	return nil
}

// deleteOrphanedPhotos deletes the live photos of inspections and hives that
// have been deleted for longer than ownerGrace. The deletes are ordinary
// changes, so every device follows.
func (p *Photos) deleteOrphanedPhotos(ctx context.Context) error {
	for _, owner := range []string{ownerInspection, ownerHive} {
		rows, err := queryMaps(ctx, p.db, `SELECT p.id, p.organization_id, o.field_hlc AS owner_clock
			FROM photo p JOIN `+owner+` o ON o.id = p.owner_id AND o.organization_id = p.organization_id
			WHERE p.deleted = FALSE AND p.owner_entity = ? AND o.deleted = TRUE`, owner)
		if err != nil {
			return err
		}
		for _, r := range rows {
			deletedAt := stampTime(wsync.ParseFieldClock(rs(r["owner_clock"]))["deleted"], time.Time{})
			if p.now().Sub(deletedAt) < ownerGrace {
				continue
			}
			if err := p.deleteAsSystem(ctx, rs(r["organization_id"]), rs(r["id"])); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteAsSystem deletes one photo in a transaction of its own.
func (p *Photos) deleteAsSystem(ctx context.Context, orgID, photoID string) error {
	return p.inTx(ctx, func(tx *sqlx.Tx) error {
		row, err := photoByID(ctx, tx, orgID, photoID)
		if err != nil || row == nil || row.Deleted {
			return err
		}
		scope, err := ownerScope(ctx, tx, row)
		if err != nil {
			return err
		}
		return p.deletePhoto(ctx, tx, &wv1.Change{ScopeId: scope, AuthorId: systemAuthor}, row, false)
	})
}

// inTx runs fn in a transaction that holds the write lock, like every other
// writer of synced rows.
func (p *Photos) inTx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := p.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockWrites(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ownerScope is the sync scope of the record a photo belongs to: the apiary
// of its hive.
func ownerScope(ctx context.Context, q queryer, row *photoRow) (string, error) {
	hiveID := row.OwnerID
	if row.OwnerEntity == ownerInspection {
		rows, err := queryMaps(ctx, q, `SELECT hive_id FROM inspection WHERE id = ?`, row.OwnerID)
		if err != nil || len(rows) == 0 {
			return "", err
		}
		hiveID = rs(rows[0]["hive_id"])
	}
	rows, err := queryMaps(ctx, q, `SELECT apiary_id FROM hive WHERE id = ?`, hiveID)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rs(rows[0]["apiary_id"]), nil
}

// sweepBlobs removes the photo blobs that no live photo names and returns
// how many it removed: those of deleted photos once they are older than
// blobGrace, those no photo row knows (only with removeUnknown) once they
// are older than OrphanGrace. Keys of any other form are left alone: the
// store may be shared with files that are not the server's.
//
// A blob that sits where a live photo without an image would store its own
// is not removed but given to that photo. That is what a database restored
// from a backup older than the blob store looks like once devices have sent
// their records again, and what an upload leaves whose last step failed.
func (p *Photos) sweepBlobs(ctx context.Context, removeUnknown bool) (int, error) {
	waiting, err := p.photosWithoutBlob(ctx)
	if err != nil {
		return 0, err
	}
	deleted, err := p.deletedPhotoKeys(ctx)
	if err != nil {
		return 0, err
	}
	var stale []string
	err = p.blobs.List(ctx, "", func(o blob.Object) error {
		if !blob.IsPhotoKey(o.Key) {
			return nil
		}
		if photo, ok := waiting[o.Key]; ok {
			if adopted, err := p.adoptBlob(ctx, photo, o.Key); adopted || err != nil {
				return err
			}
		}
		if !p.mayRemove(o, deleted[o.Key], removeUnknown) {
			return nil
		}
		n, err := countRows(ctx, p.db, `SELECT COUNT(*) FROM photo WHERE blob_key = ? AND deleted = FALSE`, o.Key)
		if err == nil && n == 0 {
			stale = append(stale, o.Key)
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	for i, key := range stale {
		if err := p.blobs.Delete(ctx, key); err != nil {
			return i, err
		}
	}
	return len(stale), nil
}

// photosWithoutBlob returns the live photos that name no blob, by the key
// their image would be stored under.
func (p *Photos) photosWithoutBlob(ctx context.Context) (map[string]photoRow, error) {
	rows, err := queryMaps(ctx, p.db, `SELECT id, organization_id FROM photo WHERE deleted = FALSE AND blob_key = ''`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]photoRow, len(rows))
	for _, r := range rows {
		row := photoRow{ID: rs(r["id"]), OrgID: rs(r["organization_id"])}
		out[blob.PhotoKey(row.OrgID, row.ID)] = row
	}
	return out, nil
}

// mayRemove reports whether a blob that no live photo names is old enough
// to be removed: a deleted photo's after blobGrace, an unknown one after
// OrphanGrace.
func (p *Photos) mayRemove(o blob.Object, ofDeletedPhoto, removeUnknown bool) bool {
	age := p.now().Sub(o.ModTime)
	if ofDeletedPhoto {
		return age >= blobGrace
	}
	return removeUnknown && age >= p.OrphanGrace
}

// deletedPhotoKeys returns the keys under which deleted photos stored, or
// would have stored, their image.
func (p *Photos) deletedPhotoKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := queryMaps(ctx, p.db, `SELECT id, organization_id, blob_key FROM photo WHERE deleted = TRUE`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[blob.PhotoKey(rs(r["organization_id"]), rs(r["id"]))] = true
		if key := rs(r["blob_key"]); key != "" {
			out[key] = true
		}
	}
	return out, nil
}

// adoptBlob records an existing blob as the image of the photo, if it is an
// image, and reports whether it did.
func (p *Photos) adoptBlob(ctx context.Context, photo photoRow, key string) (bool, error) {
	body, size, err := p.blobs.Get(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(body, head)
	_ = body.Close()
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	typ := sniffPhotoType(head[:n])
	if typ == "" {
		return false, nil
	}
	_, err = p.db.ExecContext(ctx, p.db.Rebind(`UPDATE photo SET blob_key = ?, blob_type = ?, blob_size = ?
		WHERE id = ? AND organization_id = ? AND deleted = FALSE AND blob_key = ''`),
		key, typ, size, photo.ID, photo.OrgID)
	return err == nil, err
}

// eraseOldLegacyRemoves ends the retention of logged removes (see
// photo_legacy.go): for photos converted from the 0.4.x format that were
// deleted more than legacyRemoveRetention ago, the logged removes of their
// inspection stop naming the data URL. The photo row then forgets the
// image's hash, which marks it as done.
func (p *Photos) eraseOldLegacyRemoves(ctx context.Context) error {
	rows, err := queryPhotos(ctx, p.db, `deleted = TRUE AND legacy_key <> ''`)
	if err != nil {
		return err
	}
	for i := range rows {
		row := &rows[i]
		if p.now().Sub(stampTime(row.Clock["deleted"], time.Time{})) < legacyRemoveRetention {
			continue
		}
		err := p.inTx(ctx, func(tx *sqlx.Tx) error {
			if row.OwnerEntity == ownerInspection {
				if err := eraseInlineRemoves(ctx, tx, row.OrgID, row.OwnerID, func(key, _ string) bool {
					return key == row.LegacyKey
				}); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE photo SET legacy_key = '' WHERE id = ?`), row.ID)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// eraseInlineRemoves takes inline elements out of the logged removes of an
// inspection. match decides per element (by legacy key) and stamp of the
// logged change.
func eraseInlineRemoves(ctx context.Context, tx *sqlx.Tx, orgID, inspectionID string, match func(key, stamp string) bool) error {
	return editPayloads(ctx, tx, orgID, "inspection", inspectionID, func(fields map[string]json.RawMessage, stamp string) bool {
		return editPhotoKeys(fields, func(d *wsync.SetDelta) bool {
			return dropRemoves(d, func(elem string) bool { return match(legacyKey(elem), stamp) })
		})
	})
}
