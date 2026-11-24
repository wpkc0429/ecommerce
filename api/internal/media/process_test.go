package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	gwebp "github.com/gen2brain/webp"
	xwebp "golang.org/x/image/webp"
)

var testOpt = ProcessOptions{MaxPixels: 40_000_000, Quality: 80}

func variantMap(t *testing.T, vs []Variant) map[string]Variant {
	t.Helper()
	m := map[string]Variant{}
	for _, v := range vs {
		m[v.Name] = v
	}
	if len(m) != 2 {
		t.Fatalf("want thumb+large, got %d variants", len(vs))
	}
	return m
}

func TestProcessDownsizesKeepingAspect(t *testing.T) {
	vs, err := Process(jpegBytes(gradient(3000, 2000)), testOpt)
	if err != nil {
		t.Fatal(err)
	}
	m := variantMap(t, vs)
	if m["large"].Width != 1600 || m["large"].Height != 1067 {
		t.Errorf("large = %dx%d, want 1600x1067", m["large"].Width, m["large"].Height)
	}
	if m["thumb"].Width != 480 || m["thumb"].Height != 320 {
		t.Errorf("thumb = %dx%d, want 480x320", m["thumb"].Width, m["thumb"].Height)
	}
}

func TestProcessNeverUpscales(t *testing.T) {
	vs, err := Process(pngBytes(gradient(300, 200)), testOpt)
	if err != nil {
		t.Fatal(err)
	}
	m := variantMap(t, vs)
	for _, n := range []string{"thumb", "large"} {
		if m[n].Width != 300 || m[n].Height != 200 {
			t.Errorf("%s = %dx%d, want 300x200", n, m[n].Width, m[n].Height)
		}
	}
}

func TestProcessOutputDecodesAsWebPWithReportedSize(t *testing.T) {
	vs, err := Process(jpegBytes(gradient(1000, 500)), testOpt)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		img, err := xwebp.Decode(bytes.NewReader(v.Data))
		if err != nil {
			t.Fatalf("%s is not decodable WebP: %v", v.Name, err)
		}
		if b := img.Bounds(); b.Dx() != v.Width || b.Dy() != v.Height {
			t.Errorf("%s: decoded %dx%d, reported %dx%d", v.Name, b.Dx(), b.Dy(), v.Width, v.Height)
		}
	}
}

func TestProcessAppliesExifOrientationAndDropsExif(t *testing.T) {
	// 400x200 landscape, red left / blue right, tagged "rotate 90 CW" (6):
	// upright result is 200x400 portrait with red on TOP, blue at the BOTTOM.
	src := withOrientation(jpegBytes(gradient(400, 200)), 6)
	vs, err := Process(src, testOpt)
	if err != nil {
		t.Fatal(err)
	}
	m := variantMap(t, vs)
	l := m["large"]
	if l.Width != 200 || l.Height != 400 {
		t.Fatalf("oriented large = %dx%d, want 200x400", l.Width, l.Height)
	}
	img, err := xwebp.Decode(bytes.NewReader(l.Data))
	if err != nil {
		t.Fatal(err)
	}
	r1, _, b1, _ := img.At(100, 20).RGBA()
	r2, _, b2, _ := img.At(100, 380).RGBA()
	if r1 <= b1 || b2 <= r2 {
		t.Errorf("orientation not applied: top (r=%d,b=%d) bottom (r=%d,b=%d)", r1>>8, b1>>8, r2>>8, b2>>8)
	}
	for _, v := range vs {
		if bytes.Contains(v.Data, []byte("EXIF")) || bytes.Contains(v.Data, []byte("Exif")) {
			t.Errorf("%s carries an EXIF chunk", v.Name)
		}
	}
}

func TestJPEGOrientationParsing(t *testing.T) {
	base := jpegBytes(gradient(8, 8))
	if got := jpegOrientation(base); got != 1 {
		t.Errorf("no EXIF → %d, want 1", got)
	}
	for o := uint16(1); o <= 8; o++ {
		if got := jpegOrientation(withOrientation(base, o)); got != int(o) {
			t.Errorf("orientation %d parsed as %d", o, got)
		}
	}
}

func TestOrientAllEightKeepPixelCountAndSwapDims(t *testing.T) {
	src := gradient(6, 4)
	for o := 1; o <= 8; o++ {
		got := orient(src, o).Bounds()
		w, h := 6, 4
		if o >= 5 {
			w, h = 4, 6
		}
		if got.Dx() != w || got.Dy() != h {
			t.Errorf("orientation %d: %dx%d, want %dx%d", o, got.Dx(), got.Dy(), w, h)
		}
	}
}

func TestProcessRejectsTooManyPixelsFromHeader(t *testing.T) {
	// 4000x3000 = 12MP with a 10MP limit: rejected after DecodeConfig.
	_, err := Process(pngBytes(image.NewGray(image.Rect(0, 0, 4000, 3000))), ProcessOptions{MaxPixels: 10_000_000, Quality: 80})
	if !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("err = %v, want ErrTooManyPixels", err)
	}
}

func TestProcessRejectsNonImages(t *testing.T) {
	for name, data := range map[string][]byte{
		"html":  []byte("<html><script>alert(1)</script></html>"),
		"svg":   []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"),
		"gif":   []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"empty": {},
	} {
		if _, err := Process(data, testOpt); !errors.Is(err, ErrUnsupportedImage) {
			t.Errorf("%s: err = %v, want ErrUnsupportedImage", name, err)
		}
	}
}

func TestProcessRejectsTruncatedImage(t *testing.T) {
	data := jpegBytes(gradient(200, 200))
	if _, err := Process(data[:len(data)/2], testOpt); err == nil {
		t.Fatal("truncated JPEG must not produce variants")
	}
}

func TestProcessAcceptsWebPInput(t *testing.T) {
	first, err := Process(pngBytes(gradient(500, 300)), testOpt)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Process(variantMap(t, first)["large"].Data, testOpt)
	if err != nil {
		t.Fatalf("WebP input: %v", err)
	}
	if variantMap(t, again)["large"].Width != 500 {
		t.Error("WebP round trip changed size")
	}
}

// Regression: x/image/webp returns raw studio-swing VP8 planes in a Go
// (full-range) YCbCr, so decoding a WebP and re-encoding it used to squeeze the
// tonal range (black 0 -> 16, white 255 -> ~235): every uploaded WebP came out
// visibly grayer. The check compares the raw planes of input and output, which
// is independent of how a decoder converts to RGB: a faithful re-encode keeps
// the Y/Cb/Cr statistics, the bug scaled Y by 219/255.
func TestProcessWebPInputKeepsTonalRange(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			var c color.NRGBA
			switch {
			case x < 64:
				c = color.NRGBA{0, 0, 0, 255}
			case x < 128:
				c = color.NRGBA{255, 255, 255, 255}
			case x < 192:
				c = color.NRGBA{200, 60, 60, 255}
			default:
				c = color.NRGBA{uint8(y), uint8(y), uint8(y), 255}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var in bytes.Buffer
	if err := gwebp.Encode(&in, src, gwebp.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	vs, err := Process(in.Bytes(), testOpt)
	if err != nil {
		t.Fatal(err)
	}
	rawStats := func(b []byte) (y, cb, cr float64, ymax uint8) {
		img, err := xwebp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		p, ok := img.(*image.YCbCr)
		if !ok {
			t.Fatalf("decoded %T, want *image.YCbCr", img)
		}
		for _, v := range p.Y {
			y += float64(v)
			if v > ymax {
				ymax = v
			}
		}
		for i := range p.Cb {
			cb += float64(p.Cb[i])
			cr += float64(p.Cr[i])
		}
		return y / float64(len(p.Y)), cb / float64(len(p.Cb)), cr / float64(len(p.Cr)), ymax
	}
	iy, icb, icr, imax := rawStats(in.Bytes())
	for _, v := range vs {
		oy, ocb, ocr, omax := rawStats(v.Data)
		if math.Abs(oy-iy) > 3 || math.Abs(ocb-icb) > 3 || math.Abs(ocr-icr) > 3 {
			t.Errorf("%s: mean Y/Cb/Cr %.1f/%.1f/%.1f, input %.1f/%.1f/%.1f (tonal range shifted)", v.Name, oy, ocb, ocr, iy, icb, icr)
		}
		if int(imax)-int(omax) > 4 {
			t.Errorf("%s: max Y %d, input %d (highlights clipped down)", v.Name, omax, imax)
		}
	}
}
