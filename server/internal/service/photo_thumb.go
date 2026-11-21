package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // PNG input
	"log"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "golang.org/x/image/webp" // WebP input (lossy, lossless and with alpha)

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

const (
	// thumbEdge is the longer edge of a thumbnail the server makes.
	thumbEdge = 200
	// maxThumbSourcePixels bounds what the server decodes to make one,
	// read from the image header before anything is decoded. 12 MP covers
	// what phones take; decoding costs about 4 bytes of memory per pixel.
	maxThumbSourcePixels = 12_000_000
	// maxPixelsPerByte bounds how far a large image may compress. Photos
	// stay well below 50 pixels per byte; a huge, uniform PNG of a few KB (a
	// decoding bomb) is far above. Such an image keeps its blob, it only
	// gets no thumbnail; devices then show the full image. Images of up to
	// freePixels are cheap to decode whatever their size in bytes.
	maxPixelsPerByte = 128
	freePixels       = 2_000_000
	// maxThumbsPerPush bounds the thumbnails one Push makes. Inline photos
	// beyond it are stored without one.
	maxThumbsPerPush = 16
)

// makeThumb is thumbnail; tests count its calls.
var makeThumb = thumbnail

// thumbnail returns a small JPEG data URL of the image, or "" when the
// image cannot be decoded here (damaged or oversized input). Devices
// make their own thumbnails; this is for images that reach the server
// without one: inline photos of 0.4.x apps and photos added through the API.
func thumbnail(data []byte) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return ""
	}
	if px := int64(cfg.Width) * int64(cfg.Height); px > maxThumbSourcePixels || (px > freePixels && px > int64(len(data))*maxPixelsPerByte) {
		return ""
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, shrink(src, thumbEdge), &jpeg.Options{Quality: 72}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// shrink scales the image down so that its longer edge is at most edge
// pixels, averaging the source pixels each target pixel covers. Transparent
// areas become white.
func shrink(src image.Image, edge int) *image.RGBA {
	b := src.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), src, b.Min, draw.Over)

	w, h := b.Dx(), b.Dy()
	if w <= edge && h <= edge {
		return flat
	}
	tw, th := edge, edge
	if w >= h {
		th = max(1, h*edge/w)
	} else {
		tw = max(1, w*edge/h)
	}
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	for ty := range th {
		y0, y1 := ty*h/th, max((ty+1)*h/th, ty*h/th+1)
		for tx := range tw {
			x0, x1 := tx*w/tw, max((tx+1)*w/tw, tx*w/tw+1)
			var r, g, bl, n int
			for y := y0; y < y1; y++ {
				row := flat.Pix[y*flat.Stride+x0*4 : y*flat.Stride+x1*4]
				for i := 0; i < len(row); i += 4 {
					r, g, bl, n = r+int(row[i]), g+int(row[i+1]), bl+int(row[i+2]), n+1
				}
			}
			o := out.Pix[ty*out.Stride+tx*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), 0xff //nolint:gosec // G115: an average of bytes is a byte
		}
	}
	return out
}

// Inline photos are converted inside the transaction that stores the change
// carrying them, which holds the global write lock. Decoding an image and
// writing its blob are slow, so both happen before: prepareInline does the
// work for every inline image of a request first, and createFromInline only
// records what was prepared.
//
// A prepared blob is stored under the key of the photo record it is meant
// for (blob.PhotoKey of a fresh record id), like every other photo blob.
// That keeps one blob per record, keeps workspaces apart, and lets the
// sweep give the blob back to its record when a restored database lost the
// row and a device sends the record again. When the request is over, the
// release function deletes every prepared blob that no record uses: the
// change carrying it was refused or was a replay, or the request failed.
// A retried Push therefore leaves nothing behind from earlier attempts.
//
// An image its record already shows as a live photo (a replay) is not
// prepared at all. An image that has a photo record elsewhere in the
// workspace (the same image added to another visit) is not decoded again:
// its prepared copy takes the existing record's thumbnail.

// preparedImage is an inline image whose blob is already stored.
type preparedImage struct {
	id, key, typ, thumb string
	size                int
}

type preparedKey struct{}

// preparedImages holds, per legacyKey, prepared copies not yet used.
type preparedImages map[string][]preparedImage

func preparedFrom(ctx context.Context) preparedImages {
	m, _ := ctx.Value(preparedKey{}).(preparedImages)
	return m
}

// take hands out one prepared copy of the inline image, if there is one.
func (m preparedImages) take(inline string) (preparedImage, bool) {
	k := legacyKey(inline)
	list := m[k]
	if len(list) == 0 {
		return preparedImage{}, false
	}
	m[k] = list[1:]
	return list[0], true
}

// inlineImage is an inline image and the record (hive or inspection) whose
// change carries it.
type inlineImage struct{ owner, ownerID, data string }

// prepareInline stores the blobs of the given inline images and makes their
// thumbnails, decoding at most maxThumbs images, and returns a context that
// hands them to createFromInline. Values that are not JPEG, PNG or WebP
// images are skipped; createFromInline leaves those alone anyway. The
// caller must call release once the transaction that records the photos
// has committed or rolled back.
func (p *Photos) prepareInline(ctx context.Context, orgID string, inlines []inlineImage, maxThumbs int) (_ context.Context, release func(), _ error) {
	release = func() {}
	if p == nil || p.blobs == nil {
		return ctx, release, nil
	}
	out := preparedImages{}
	ctx = context.WithValue(ctx, preparedKey{}, out)
	if len(inlines) == 0 {
		return ctx, release, nil
	}
	thumbs, shown, err := p.knownImages(ctx, orgID, inlines)
	if err != nil {
		return ctx, release, err
	}
	decoded := 0
	var keys []string
	release = func() { p.releasePrepared(context.WithoutCancel(ctx), orgID, keys) }
	for _, in := range inlines {
		k := legacyKey(in.data)
		if shown[in.owner+"/"+in.ownerID+"/"+k] {
			continue // a replay: nothing to convert
		}
		data := decodeDataURL(in.data, MaxPhotoBytes)
		typ := sniffPhotoType(data)
		if typ == "" {
			continue
		}
		thumb, have := thumbs[k]
		if !have && decoded < maxThumbs {
			decoded++
			thumb = makeThumb(data)
			thumbs[k] = thumb
		}
		img := preparedImage{id: uuid.NewString(), typ: typ, thumb: thumb, size: len(data)}
		img.key = blob.PhotoKey(orgID, img.id)
		keys = append(keys, img.key)
		if err := p.blobs.Put(ctx, img.key, bytes.NewReader(data), int64(len(data)), typ); err != nil {
			release()
			return ctx, func() {}, err
		}
		out[k] = append(out[k], img)
	}
	return ctx, release, nil
}

// releasePrepared deletes the prepared blobs that no photo record uses.
// Their keys are fresh record ids, so no other request can use them.
func (p *Photos) releasePrepared(ctx context.Context, orgID string, keys []string) {
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	q, args, err := sqlx.In(`SELECT blob_key FROM photo WHERE organization_id = ? AND blob_key IN (?)`, orgID, keys)
	var used []string
	if err == nil {
		err = p.db.SelectContext(ctx, &used, p.db.Rebind(q), args...)
	}
	if err != nil {
		log.Printf("photos: unused prepared blobs are left to the sweep: %v", err)
		return
	}
	for _, k := range keys {
		if slices.Contains(used, k) {
			continue
		}
		if err := p.blobs.Delete(ctx, k); err != nil {
			log.Printf("photos: unused prepared blob is left to the sweep: %v", err)
		}
	}
}

// knownImages looks up the live photo records in the workspace made from
// the given images. It returns, per legacyKey, a thumbnail one of them
// has, and the set of "owner/ownerID/legacyKey" they belong to.
func (p *Photos) knownImages(ctx context.Context, orgID string, inlines []inlineImage) (thumbs map[string]string, shown map[string]bool, _ error) {
	keys := make([]string, 0, len(inlines))
	for _, in := range inlines {
		keys = append(keys, legacyKey(in.data))
	}
	q, args, err := sqlx.In(`SELECT owner_entity, owner_id, legacy_key, thumb FROM photo
		WHERE organization_id = ? AND deleted = FALSE AND legacy_key IN (?)`, orgID, keys)
	if err != nil {
		return nil, nil, err
	}
	var found []struct {
		Owner   string `db:"owner_entity"`
		OwnerID string `db:"owner_id"`
		Key     string `db:"legacy_key"`
		Thumb   string `db:"thumb"`
	}
	if err := p.db.SelectContext(ctx, &found, p.db.Rebind(q), args...); err != nil {
		return nil, nil, err
	}
	thumbs, shown = map[string]string{}, map[string]bool{}
	for _, f := range found {
		shown[f.Owner+"/"+f.OwnerID+"/"+f.Key] = true
		if f.Thumb != "" {
			thumbs[f.Key] = f.Thumb
		}
	}
	return thumbs, shown, nil
}

// inlineImages lists the inline images the changes of a Push carry: the
// added elements of inspection.photo_keys and the values of hive.photo.
func inlineImages(changes []*wv1.Change) []inlineImage {
	var out []inlineImage
	for _, ch := range changes {
		if ch.Op != wv1.ChangeOp_CHANGE_OP_UPSERT || !bytes.Contains([]byte(ch.PayloadJson), []byte(`"data:`)) {
			continue
		}
		var p struct {
			Photo     *string `json:"photo"`
			PhotoKeys *struct {
				Add []string `json:"add"`
			} `json:"photo_keys"`
		}
		if json.Unmarshal([]byte(ch.PayloadJson), &p) != nil {
			continue // refused by the payload validation
		}
		switch {
		case ch.Entity == "hive" && p.Photo != nil && isDataURL(*p.Photo):
			out = append(out, inlineImage{ownerHive, ch.EntityId, *p.Photo})
		case ch.Entity == "inspection" && p.PhotoKeys != nil:
			for _, e := range p.PhotoKeys.Add {
				if isDataURL(e) {
					out = append(out, inlineImage{ownerInspection, ch.EntityId, e})
				}
			}
		}
	}
	return out
}
