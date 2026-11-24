package cms

import (
	"encoding/json"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/cms/richtext"
)

const imgSchema = `{
  "type": "object",
  "properties": {
    "logo": {"type": "string", "x-editor": "image"},
    "title": {"type": "string"},
    "sections": {"type": "array", "items": {"oneOf": [
      {"type": "object", "properties": {"type": {"const": "hero"}, "image": {"type": "string", "x-editor": "image"}}},
      {"type": "object", "properties": {"type": {"const": "text"}, "body": {"type": "string", "x-editor": "richtext"}}}
    ]}}
  }
}`

var imgPrefixes = []string{"https://img.example.test/"}

func checkImg(t *testing.T, payload string, prefixes []string) []Detail {
	t.Helper()
	d, err := CheckImageFields(json.RawMessage(imgSchema), json.RawMessage(payload), prefixes)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCheckImageFields(t *testing.T) {
	ok := `{"logo":"https://img.example.test/shops/1/content/x/large.webp","title":"https://elsewhere.test/a","sections":[{"type":"hero","image":""}]}`
	if d := checkImg(t, ok, imgPrefixes); len(d) != 0 {
		t.Errorf("valid payload rejected: %v", d)
	}

	bad := `{"logo":"https://example.com/a.jpg","sections":[{"type":"text","body":"x"},{"type":"hero","image":"https://img.example.test.evil.test/a.webp"}]}`
	d := checkImg(t, bad, imgPrefixes)
	if len(d) != 2 {
		t.Fatalf("want 2 violations, got %v", d)
	}
	got := map[string]bool{d[0].Pointer: true, d[1].Pointer: true}
	if !got["/logo"] || !got["/sections/1/image"] {
		t.Errorf("pointers = %v", got)
	}
}

func TestCheckImageFieldsOffWithoutPrefixes(t *testing.T) {
	if d := checkImg(t, `{"logo":"https://example.com/a.jpg"}`, nil); d != nil {
		t.Errorf("rule must be off when media is not configured, got %v", d)
	}
}

func TestSanitizeWithImagePrefixes(t *testing.T) {
	in := `<p>a</p><img src="https://img.example.test/ok.webp" alt="x"><img src="https://evil.test/p.png"><img src="https://img.example.test.evil.test/p.png">`
	out := richtext.SanitizeWithImagePrefixes(in, imgPrefixes)
	if !strings.Contains(out, "img.example.test/ok.webp") {
		t.Errorf("allowed image dropped: %s", out)
	}
	if strings.Contains(out, "evil.test") {
		t.Errorf("foreign image kept: %s", out)
	}
	if got := richtext.SanitizeWithImagePrefixes(in, nil); !strings.Contains(got, "evil.test/p.png") {
		t.Errorf("no prefixes must leave images to the allowlist only: %s", got)
	}
}
