package service

import (
	"context"
	"encoding/json"
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
//   - Every sweepInterval: remove blobs no live photo points to (uploads
//     whose record never arrived, conversions that were rolled back, deleted
//     workspaces), and the inline data of logged removes that are past their
//     retention.

const (
	tidyInterval  = 15 * time.Minute
	sweepInterval = 6 * time.Hour
	// ownerGrace is how long a deleted record keeps its photos.
	ownerGrace = time.Hour
	// blobGrace protects blobs that are being written right now: an upload
	// stores the blob first and names it on the photo row afterwards.
	blobGrace = time.Hour
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
func (p *Photos) Sweep(ctx context.Context) {
	n, err := p.sweepBlobs(ctx)
	p.logErr("sweep blobs", err)
	if n > 0 {
		log.Printf("photos: removed %d blobs without a photo record", n)
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

// sweepBlobs removes every blob that no live photo names and that is older
// than blobGrace, and returns how many it removed.
func (p *Photos) sweepBlobs(ctx context.Context) (int, error) {
	var stale []string
	err := p.blobs.List(ctx, "", func(o blob.Object) error {
		if p.now().Sub(o.ModTime) < blobGrace {
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
