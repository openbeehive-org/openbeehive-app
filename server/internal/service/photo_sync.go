package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jmoiron/sqlx"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// What the write path does for photos. Both entry points (Push and the API
// writer) apply a change through applyLogged, which calls beforeChange and
// afterChange around the merge.

// beforeChange checks a change to a photo before it is merged. skip is true
// for a change to a photo that is already deleted: deletion is final, the
// change is dropped without being logged.
func (p *Photos) beforeChange(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change, orgID string) (skip bool, err error) {
	if p == nil || ch.Entity != "photo" {
		return false, nil
	}
	if ch.Op == wv1.ChangeOp_CHANGE_OP_UPSERT {
		if cerr := checkPhotoPayload(ch.PayloadJson); cerr != nil {
			return false, cerr
		}
	}
	cur, err := photoByID(ctx, tx, orgID, ch.EntityId)
	if err != nil || cur == nil {
		return false, err
	}
	return cur.Deleted, nil
}

// checkPhotoPayload validates the photo-specific rules of an upsert; types
// and sizes of plain columns are checked by the generic payload validation.
func checkPhotoPayload(payload string) *changeError {
	var f struct {
		OwnerEntity *string  `json:"owner_entity"`
		Thumb       *string  `json:"thumb"`
		ContentType *string  `json:"content_type"`
		Size        *float64 `json:"size_bytes"`
	}
	if json.Unmarshal([]byte(payload), &f) != nil {
		return nil // not an object, or a mistyped value: refused by the generic validation
	}
	if f.OwnerEntity != nil && *f.OwnerEntity != ownerInspection && *f.OwnerEntity != ownerHive {
		return invalidChange("owner_entity must be %q or %q", ownerInspection, ownerHive)
	}
	if f.ContentType != nil && *f.ContentType != "" && !photoTypes[*f.ContentType] {
		return invalidChange("content_type must be image/jpeg, image/png or image/webp")
	}
	if f.Size != nil && (*f.Size < 0 || *f.Size > MaxPhotoBytes) {
		return invalidChange("size_bytes must be between 0 and %d", MaxPhotoBytes)
	}
	if f.Thumb != nil && *f.Thumb != "" {
		if len(*f.Thumb) > maxThumbBytes {
			return invalidChange("thumb has %d bytes, limit is %d", len(*f.Thumb), maxThumbBytes)
		}
		if sniffPhotoType(decodeDataURL(*f.Thumb, maxThumbBytes)) == "" {
			return invalidChange("thumb must be a base64 data URL of a JPEG, PNG or WebP image")
		}
	}
	return nil
}

// afterChange runs once a change has been merged and logged.
//
//   - A photo that is now deleted is erased (see erase).
//   - An inspection or hive change that carries inline photo data comes from
//     a 0.4.x app and is converted (photo_legacy.go).
func (p *Photos) afterChange(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change, orgID string) error {
	if p == nil {
		return nil
	}
	switch ch.Entity {
	case "photo":
		row, err := photoByID(ctx, tx, orgID, ch.EntityId)
		if err != nil || row == nil || !row.Deleted {
			return err
		}
		return p.erase(ctx, tx, row, ch, true)
	case "inspection":
		if ch.Op == wv1.ChangeOp_CHANGE_OP_UPSERT && strings.Contains(ch.PayloadJson, `"photo_keys"`) {
			return p.legacyVisitPhotos(ctx, tx, ch, orgID)
		}
	case "hive":
		if ch.Op == wv1.ChangeOp_CHANGE_OP_UPSERT && strings.Contains(ch.PayloadJson, `"photo"`) {
			return p.legacyHivePhoto(ctx, tx, ch, orgID)
		}
	}
	return nil
}

// derive applies and logs a change the server makes on its own as a
// consequence of cause (same scope, same author). It does not run the photo
// hooks again.
func (p *Photos) derive(ctx context.Context, tx *sqlx.Tx, cause *wv1.Change, orgID string, ch *wv1.Change) error {
	ch.ScopeId, ch.AuthorId = cause.ScopeId, cause.AuthorId
	if err := applyChange(ctx, tx, entityCols[ch.Entity], ch, orgID); err != nil {
		return err
	}
	return appendChangeLog(ctx, tx, ch, orgID)
}

// deletePhoto deletes a live photo on the server's own initiative and erases
// it. notifyLegacy is passed on to erase.
func (p *Photos) deletePhoto(ctx context.Context, tx *sqlx.Tx, cause *wv1.Change, row *photoRow, notifyLegacy bool) error {
	del := &wv1.Change{
		Entity: "photo", EntityId: row.ID, Op: wv1.ChangeOp_CHANGE_OP_DELETE,
		PayloadJson: `{"deleted":1}`, Hlc: p.stampAfter(row.Clock["deleted"]),
	}
	if err := p.derive(ctx, tx, cause, row.OrgID, del); err != nil {
		return err
	}
	return p.erase(ctx, tx, row, cause, notifyLegacy)
}

// erase removes the image data of a deleted photo inside the transaction
// that deleted it: the thumbnail in the row and in every logged change, and,
// for a photo converted from the 0.4.x format, the inline copy 0.4.x apps
// read (eraseLegacy). The blob is removed by the worker after the commit, so
// a rolled-back delete loses nothing; blob_key stays on the row until then.
func (p *Photos) erase(ctx context.Context, tx *sqlx.Tx, row *photoRow, cause *wv1.Change, notifyLegacy bool) error {
	if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE photo SET thumb = '' WHERE id = ?`), row.ID); err != nil {
		return err
	}
	err := editPayloads(ctx, tx, row.OrgID, "photo", row.ID, func(fields map[string]json.RawMessage, _ string) bool {
		if raw, ok := fields["thumb"]; !ok || string(raw) == `""` {
			return false
		}
		fields["thumb"] = json.RawMessage(`""`)
		return true
	})
	if err != nil {
		return err
	}
	if row.LegacyKey != "" {
		if err := p.eraseLegacy(ctx, tx, row, cause, notifyLegacy); err != nil {
			return err
		}
	}
	p.wakeWorker()
	return nil
}

// loadORSet reads one set column of a row.
func loadORSet(ctx context.Context, tx *sqlx.Tx, table, col, id, orgID string) (wsync.ORSet, bool, error) {
	rows, err := queryMaps(ctx, tx, `SELECT `+col+` FROM `+table+` WHERE id = ? AND organization_id = ?`, id, orgID)
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	return wsync.ParseORSet(rs(rows[0][col])), true, nil
}

// visible reports whether the element has an add tag that was not removed.
func visible(set wsync.ORSet, elem string) bool {
	e, ok := set[elem]
	if !ok {
		return false
	}
	removed := make(map[string]bool, len(e.R))
	for _, t := range e.R {
		removed[t] = true
	}
	for _, t := range e.A {
		if !removed[t] {
			return true
		}
	}
	return false
}
