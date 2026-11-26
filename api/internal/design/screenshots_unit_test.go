package design

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 7, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func chunk(kind string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, kind...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(kind), data...)))
}

func errCode(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}

func TestReencodeValidatesAndStripsMetadata(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	src := testPNG(t, 390, 25)

	// Metadata chunks (text, EXIF, ICC profile name) must not survive.
	withMeta := append([]byte{}, src[:33]...)
	withMeta = append(withMeta, chunk("tEXt", []byte("Author\x00someone@example.com"))...)
	withMeta = append(withMeta, chunk("eXIf", []byte("GPS-DATA"))...)
	withMeta = append(withMeta, src[33:]...)
	out, w, h, err := s.reencode(ctx, withMeta, 390)
	if err != nil || w != 390 || h != 25 {
		t.Fatalf("reencode: %v %d %d", err, w, h)
	}
	for _, leak := range []string{"tEXt", "eXIf", "someone@example.com", "GPS-DATA"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Fatalf("%q survived the re-encode", leak)
		}
	}
	// Deterministic: the same input always stores the same bytes (replay relies on it).
	again, _, _, _ := s.reencode(ctx, withMeta, 390)
	if !bytes.Equal(out, again) {
		t.Fatal("re-encoding is not deterministic")
	}

	for name, tc := range map[string]struct {
		body     []byte
		viewport int
	}{
		"wrong width":      {src, 1280},
		"not an image":     {[]byte("hello"), 390},
		"empty":            {nil, 390},
		"too tall":         {testPNG(t, 390, MaxScreenshotHeight+1), 390},
		"truncated pixels": {src[:len(src)-20], 390},
	} {
		if _, _, _, err := s.reencode(ctx, tc.body, tc.viewport); errCode(err) != "invalid_screenshot" {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A decompression bomb: a tiny file declaring a huge image is refused from
	// the header alone, before any pixel is allocated.
	bomb := append([]byte{}, src[:16]...)
	bomb = binary.BigEndian.AppendUint32(bomb, 390)
	bomb = binary.BigEndian.AppendUint32(bomb, 1_000_000)
	bomb = append(bomb, src[24:29]...)
	bomb = binary.BigEndian.AppendUint32(bomb, crc32.ChecksumIEEE(bomb[12:29]))
	bomb = append(bomb, chunk("IEND", nil)...)
	if _, _, _, err := s.reencode(ctx, bomb, 390); errCode(err) != "invalid_screenshot" {
		t.Errorf("bomb: %v", err)
	}
}

func manifest(t *testing.T, json string) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(json))
}

const okManifest = `{"tool":"cap","tool_version":"1","dataset":"sandbox-v1","api_origin_class":"sandbox","captured_at":"2026-11-26T00:00:00Z","set_version":1}`

func TestParseManifest(t *testing.T) {
	if m, err := ParseManifest(manifest(t, okManifest)); err != nil || m.Dataset != DatasetSandbox || m.SetVersion != 1 {
		t.Fatalf("valid: %+v %v", m, err)
	}
	for name, in := range map[string]string{
		"empty":       "",
		"not base64":  "***",
		"not json":    manifest(t, "nope"),
		"no tool":     manifest(t, strings.Replace(okManifest, `"tool":"cap",`, "", 1)),
		"no version":  manifest(t, strings.Replace(okManifest, `"set_version":1`, `"set_version":0`, 1)),
		"bad origin":  manifest(t, strings.Replace(okManifest, `"api_origin_class":"sandbox"`, `"api_origin_class":"elsewhere"`, 1)),
		"bad time":    manifest(t, strings.Replace(okManifest, "2026-11-26T00:00:00Z", "yesterday", 1)),
		"over 2 KiB":  manifest(t, okManifest[:len(okManifest)-1]+`,"pad":"`+strings.Repeat("x", 2100)+`"}`),
		"long tool":   manifest(t, strings.Replace(okManifest, `"cap"`, `"`+strings.Repeat("a", 61)+`"`, 1)),
		"set ver big": manifest(t, strings.Replace(okManifest, `"set_version":1`, `"set_version":100000`, 1)),
	} {
		if _, err := ParseManifest(in); errCode(err) != "invalid_screenshot" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCaptureRulesAdminSandboxOnly(t *testing.T) {
	sandbox := Manifest{Dataset: DatasetSandbox, APIOrigin: OriginClassSandbox}
	live := Manifest{Dataset: DatasetPublicLive, APIOrigin: OriginClassPublic}
	mixed := Manifest{Dataset: DatasetSandbox, APIOrigin: OriginClassPublic}
	cases := []struct {
		site, role string
		m          Manifest
		refused    bool
	}{
		{SiteAdmin, "owner", sandbox, false},
		{SiteAdmin, "staff", sandbox, false},
		{SiteAdmin, "owner", live, true},
		{SiteAdmin, "owner", mixed, true},
		{SiteAdmin, "owner", Manifest{}, true},
		{SiteStorefront, "anon", sandbox, false},
		{SiteStorefront, "anon", live, false},
		{SiteStorefront, "anon", mixed, true},
		{SiteStorefront, "owner", sandbox, true},
		{SiteStorefront, "staff", live, true},
	}
	for _, c := range cases {
		err := checkCaptureRules(c.site, c.role, c.m, sandboxPolicy{})
		if c.refused != (errCode(err) == "live_data_capture_refused") || (err != nil && !c.refused) {
			t.Errorf("%s/%s %+v: refused=%v err=%v", c.site, c.role, c.m, c.refused, err)
		}
	}
}

func TestCaptureRulesSandboxOrigins(t *testing.T) {
	m := func(origin string) Manifest {
		return Manifest{Dataset: DatasetSandbox, APIOrigin: OriginClassSandbox, Origin: origin}
	}
	list := sandboxPolicy{origins: []string{"http://127.0.0.1:13001", "https://sandbox.example.com"}}
	required := sandboxPolicy{required: true}
	cases := []struct {
		name    string
		site    string
		m       Manifest
		p       sandboxPolicy
		refused bool
	}{
		{"listed", SiteAdmin, m("http://127.0.0.1:13001"), list, false},
		{"listed https", SiteAdmin, m("https://sandbox.example.com"), list, false},
		{"unlisted", SiteAdmin, m("https://merchant.example.com"), list, true},
		{"port differs", SiteAdmin, m("http://127.0.0.1:13002"), list, true},
		{"scheme differs", SiteAdmin, m("https://127.0.0.1:13001"), list, true},
		{"no origin with list", SiteAdmin, m(""), list, true},
		{"empty list in production", SiteAdmin, m("http://127.0.0.1:13001"), required, true},
		{"empty list in dev", SiteAdmin, m(""), sandboxPolicy{}, false},
		{"storefront ignores list", SiteStorefront, m("https://merchant.example.com"), list, false},
		{"storefront ignores production", SiteStorefront, m(""), required, false},
	}
	for _, c := range cases {
		err := checkCaptureRules(c.site, "anon", c.m, c.p)
		if c.site == SiteAdmin {
			err = checkCaptureRules(c.site, "owner", c.m, c.p)
		}
		if c.refused != (errCode(err) == "live_data_capture_refused") || (err != nil && !c.refused) {
			t.Errorf("%s: refused=%v err=%v", c.name, c.refused, err)
		}
	}
}

func TestParseManifestOrigin(t *testing.T) {
	with := func(o string) string {
		return manifest(t, strings.Replace(okManifest, `"set_version":1`, `"set_version":1,"origin":"`+o+`"`, 1))
	}
	got, err := ParseManifest(with("HTTP://127.0.0.1:13001"))
	if err != nil || got.Origin != "http://127.0.0.1:13001" {
		t.Fatalf("normalized origin: %v %q", err, got.Origin)
	}
	for _, bad := range []string{"127.0.0.1:13001", "http://127.0.0.1:13001/path", "ftp://x", "http://u:p@x"} {
		if _, err := ParseManifest(with(bad)); errCode(err) != "invalid_screenshot" {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestScreenshotKeyShape(t *testing.T) {
	var req, id [16]byte
	req[0], id[0] = 1, 2
	k := ScreenshotKey(7, req, id)
	if !strings.HasPrefix(k, "shops/7/design/01000000-") || !strings.HasSuffix(k, ".png") || strings.Count(k, "/") != 4 {
		t.Fatalf("key %q", k)
	}
}
