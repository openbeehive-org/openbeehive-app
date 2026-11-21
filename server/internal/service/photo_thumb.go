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

// makeThumb returns a small JPEG data URL of the image, or "" when the
// image cannot be decoded here (damaged or oversized input). Devices
// make their own thumbnails; this is for images that reach the server
// without one: inline photos of 0.4.x apps and photos added through the API.
func makeThumb(data []byte) string {
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
// records what was prepared. A prepared blob whose change is then refused
// or turns out to be a replay has no record; the sweep removes it like any
// other blob without one.

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

// prepareInline stores the blobs of the given inline images and makes their
// thumbnails, at most maxThumbs of them, and returns a context that hands
// them to createFromInline. Values that are not JPEG, PNG or WebP images
// are skipped; createFromInline leaves those alone anyway.
func (p *Photos) prepareInline(ctx context.Context, orgID string, inlines []string, maxThumbs int) (context.Context, error) {
	if p == nil || p.blobs == nil || len(inlines) == 0 {
		return ctx, nil
	}
	known, err := p.knownImages(ctx, orgID, inlines)
	if err != nil {
		return ctx, err
	}
	out := preparedImages{}
	thumbs := map[string]string{}
	for _, inline := range inlines {
		if known[legacyKey(inline)] {
			continue // a replay, or seen before: converted under the lock if at all
		}
		data := decodeDataURL(inline, MaxPhotoBytes)
		typ := sniffPhotoType(data)
		if typ == "" {
			continue
		}
		k := legacyKey(inline)
		thumb, made := thumbs[k]
		if !made && len(thumbs) < maxThumbs {
			thumb = makeThumb(data)
			thumbs[k] = thumb
		}
		img := preparedImage{id: uuid.NewString(), typ: typ, thumb: thumb, size: len(data)}
		img.key = blob.PhotoKey(orgID, img.id)
		if err := p.blobs.Put(ctx, img.key, bytes.NewReader(data), int64(len(data)), typ); err != nil {
			return ctx, err
		}
		out[k] = append(out[k], img)
	}
	return context.WithValue(ctx, preparedKey{}, out), nil
}

// knownImages reports which of the inline images already have a photo
// record in the workspace, live or deleted. A 0.4.x app sends the same
// changes again whenever a Push of it failed; those need no new blob.
func (p *Photos) knownImages(ctx context.Context, orgID string, inlines []string) (map[string]bool, error) {
	keys := make([]string, 0, len(inlines))
	for _, inline := range inlines {
		keys = append(keys, legacyKey(inline))
	}
	q, args, err := sqlx.In(`SELECT legacy_key FROM photo WHERE organization_id = ? AND legacy_key IN (?)`, orgID, keys)
	if err != nil {
		return nil, err
	}
	var found []string
	if err := p.db.SelectContext(ctx, &found, p.db.Rebind(q), args...); err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(found))
	for _, k := range found {
		known[k] = true
	}
	return known, nil
}

// inlineImages lists the inline images the changes of a Push carry: the
// added elements of inspection.photo_keys and the values of hive.photo.
func inlineImages(changes []*wv1.Change) []string {
	var out []string
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
			out = append(out, *p.Photo)
		case ch.Entity == "inspection" && p.PhotoKeys != nil:
			for _, e := range p.PhotoKeys.Add {
				if isDataURL(e) {
					out = append(out, e)
				}
			}
		}
	}
	return out
}
