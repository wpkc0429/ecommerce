package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	"github.com/gen2brain/webp"
	"golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"
)

// Variant sizes (design D4): long edge in pixels, never upscaled.
const (
	ThumbEdge = 480
	LargeEdge = 1600
)

// Variant is one encoded output of Process.
type Variant struct {
	Name          string // "thumb" | "large"
	Data          []byte // WebP
	Width, Height int
}

// Errors Process can return; all map to HTTP 422.
var (
	ErrUnsupportedImage = errors.New("media: content is not a JPEG, PNG or WebP image")
	ErrTooManyPixels    = errors.New("media: image exceeds the pixel limit")
	ErrCorruptImage     = errors.New("media: image could not be decoded")
)

// ProcessOptions bounds one Process call.
type ProcessOptions struct {
	MaxPixels int
	Quality   int
}

// Process validates raw bytes by their actual content (the declared type is
// never trusted), decodes, applies EXIF orientation, downsizes (never up) to
// the large and thumb variants and encodes both as lossy WebP. Re-encoding
// drops EXIF/GPS, which is a privacy requirement (the bucket is public).
//
// DecodeConfig runs before the full decode so a decompression bomb is rejected
// from its header, before any pixel buffer is allocated (design D1).
func Process(data []byte, opt ProcessOptions) ([]Variant, error) {
	kind := sniff(data)
	if kind == "" {
		return nil, ErrUnsupportedImage
	}
	cfg, err := decodeConfig(kind, data)
	if err != nil {
		return nil, ErrCorruptImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrCorruptImage
	}
	if opt.MaxPixels > 0 && int64(cfg.Width)*int64(cfg.Height) > int64(opt.MaxPixels) {
		return nil, ErrTooManyPixels
	}
	src, err := decode(kind, data)
	if err != nil {
		return nil, ErrCorruptImage
	}

	orientation := 1
	if kind == "jpeg" {
		orientation = jpegOrientation(data)
	}

	// Scale first (cheap, bounded to 1600px), then orient the small result:
	// rotation/flip commute with uniform scaling, and orienting a 40MP source
	// through image.Image.At would be needlessly slow.
	large := orient(fitWithin(src, LargeEdge), orientation)
	thumb := fitWithin(large, ThumbEdge)

	var out []Variant
	for _, v := range []struct {
		name string
		img  image.Image
	}{{"thumb", thumb}, {"large", large}} {
		var buf bytes.Buffer
		if err := webp.Encode(&buf, v.img, webp.Options{Quality: opt.Quality}); err != nil {
			return nil, fmt.Errorf("media: encode %s: %w", v.name, err)
		}
		b := v.img.Bounds()
		out = append(out, Variant{Name: v.name, Data: buf.Bytes(), Width: b.Dx(), Height: b.Dy()})
	}
	return out, nil
}

// sniff identifies the format from content, not from the declared type.
func sniff(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "jpeg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	}
	return ""
}

func decodeConfig(kind string, data []byte) (image.Config, error) {
	r := bytes.NewReader(data)
	switch kind {
	case "jpeg":
		return jpeg.DecodeConfig(r)
	case "png":
		return png.DecodeConfig(r)
	default:
		return xwebp.DecodeConfig(r)
	}
}

func decode(kind string, data []byte) (image.Image, error) {
	r := bytes.NewReader(data)
	switch kind {
	case "jpeg":
		return jpeg.Decode(r)
	case "png":
		return png.Decode(r)
	default:
		return xwebp.Decode(r)
	}
}

// fitWithin returns src scaled so its long edge is at most max (never
// upscaled, aspect ratio kept, no cropping). When no scaling is needed the
// source is returned as is.
func fitWithin(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= max {
		return src
	}
	nw := (w*max + long/2) / long
	nh := (h*max + long/2) / long
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// orient applies an EXIF orientation (1-8) so the pixels are upright.
func orient(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if o >= 5 {
		nw, nh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 270 CW
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// jpegOrientation reads the EXIF Orientation tag from a JPEG's APP1 segment;
// 1 when absent or malformed.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan / end: no more metadata
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+segLen]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + segLen
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 && bo.Uint16(t[e+2:e+4]) == 3 {
			if v := int(bo.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// WarmUp runs one tiny encode so the WASM encoder's one-time initialisation
// happens at boot rather than inside the first merchant upload (design Risks).
func WarmUp() error {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	return webp.Encode(&buf, img, webp.Options{Quality: 50})
}
