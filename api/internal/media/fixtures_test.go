package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// gradient makes a w x h image whose left half is red and right half blue, so
// orientation changes are detectable from pixel colours.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, color.RGBA{R: 220, A: 255})
			} else {
				img.Set(x, y, color.RGBA{B: 220, A: 255})
			}
		}
	}
	return img
}

func jpegBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// withOrientation inserts an APP1/EXIF segment (little-endian TIFF) carrying
// the Orientation tag plus a GPS-looking marker string right after SOI.
func withOrientation(jpg []byte, o uint16) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("II")
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(42))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(8))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(1)) // one entry
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x0112))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(3))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, o)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0)) // next IFD
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}
