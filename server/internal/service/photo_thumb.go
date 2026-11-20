package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // PNG input
	// WebP has no decoder in the standard library: such an image is stored
	// as it is and gets no server-made thumbnail.
)

const (
	// thumbEdge is the longer edge of a thumbnail the server makes.
	thumbEdge = 200
	// maxThumbSourcePixels bounds what the server decodes to make one.
	maxThumbSourcePixels = 50_000_000
)

// makeThumb returns a small JPEG data URL of the image, or "" when the
// image cannot be decoded here (WebP, damaged or oversized input). Devices
// make their own thumbnails; this is for images that reach the server
// without one: inline photos of 0.4.x apps and photos added through the API.
func makeThumb(data []byte) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxThumbSourcePixels {
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
