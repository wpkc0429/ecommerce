package cms

import (
	"encoding/json"
	"strings"
	"testing"
)

// A page schema shaped like the starter's: sections oneOf by type const via
// $defs, a richtext field inside a section, a richtext field inside an array
// of objects, and a plain text field that must never be touched.
const sanitizeTestSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "intro": { "type": "string", "x-editor": "richtext" },
    "headline": { "type": "string", "x-editor": "text" },
    "count": { "type": "integer" },
    "sections": {
      "type": "array",
      "items": { "oneOf": [ { "$ref": "#/$defs/rich_text" }, { "$ref": "#/$defs/faq" } ] }
    }
  },
  "$defs": {
    "rich_text": {
      "type": "object",
      "properties": {
        "type": { "const": "rich_text" },
        "html": { "type": "string", "x-editor": "richtext" }
      }
    },
    "faq": {
      "type": "object",
      "properties": {
        "type": { "const": "faq" },
        "title": { "type": "string", "x-editor": "text" },
        "items": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "q": { "type": "string", "x-editor": "text" },
              "a": { "type": "string", "x-editor": "richtext" }
            }
          }
        }
      }
    }
  }
}`

func sanitizeFixture(t *testing.T, payload string) (map[string]any, bool, string) {
	t.Helper()
	out, changed, err := SanitizeRichText(json.RawMessage(sanitizeTestSchema), json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m, changed, string(out)
}

func TestSanitizeRichTextWalksSectionsAndNestedArrays(t *testing.T) {
	m, changed, raw := sanitizeFixture(t, `{
	  "intro": "<p>hi</p><script>alert(1)</script>",
	  "headline": "<b>標題</b><script>x</script>",
	  "count": 12345678901234,
	  "sections": [
	    { "type": "rich_text", "html": "<img src=\"https://x.test/a.png\" onerror=\"alert(2)\">" },
	    { "type": "faq", "title": "<i>t</i>", "items": [ { "q": "<u>q</u>", "a": "<a href=\"javascript:alert(3)\">a</a>" } ] }
	  ]
	}`)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if strings.Contains(raw, "alert(1)") || strings.Contains(raw, "alert(2)") || strings.Contains(raw, "alert(3)") || strings.Contains(raw, "onerror") {
		t.Fatalf("dangerous content survived: %s", raw)
	}
	// Plain-text fields are untouched (they are rendered as text, not HTML).
	if m["headline"] != "<b>標題</b><script>x</script>" {
		t.Fatalf("x-editor:text field must be untouched: %v", m["headline"])
	}
	faq := m["sections"].([]any)[1].(map[string]any)
	if faq["title"] != "<i>t</i>" {
		t.Fatalf("faq title (text) changed: %v", faq["title"])
	}
	if faq["items"].([]any)[0].(map[string]any)["q"] != "<u>q</u>" {
		t.Fatal("faq q (text) changed")
	}
	// Large integers survive the rewrite exactly (UseNumber).
	if !strings.Contains(raw, "12345678901234") {
		t.Fatalf("number altered: %s", raw)
	}
	if m["intro"] != "<p>hi</p>" {
		t.Fatalf("intro = %q", m["intro"])
	}
}

func TestSanitizeRichTextUnchangedReturnsOriginalBytes(t *testing.T) {
	in := `{"intro":"<p>ok</p>","count":1.50,"sections":[{"type":"rich_text","html":"<h2>x</h2>"}]}`
	out, changed, err := SanitizeRichText(json.RawMessage(sanitizeTestSchema), json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(out) != in {
		t.Fatalf("clean payload must be returned byte-for-byte: changed=%v out=%s", changed, out)
	}
}

func TestSanitizeRichTextIgnoresUnknownShapes(t *testing.T) {
	// Values the schema does not describe (or describes differently) are left
	// alone; validation is a separate step.
	in := `{"intro": 5, "sections": "not-an-array", "extra": "<script>x</script>"}`
	out, changed, err := SanitizeRichText(json.RawMessage(sanitizeTestSchema), json.RawMessage(in))
	if err != nil || changed || string(out) != in {
		t.Fatalf("unexpected rewrite: changed=%v err=%v out=%s", changed, err, out)
	}
}

func TestSanitizeRichTextStarterSchema(t *testing.T) {
	// The real starter rich_text section, assembled the way seed does it.
	defs := map[string]any{}
	if err := json.Unmarshal([]byte(`{
	  "rich_text": {"type":"object","additionalProperties":false,"required":["type"],
	    "properties":{"type":{"const":"rich_text"},"html":{"type":"string","default":"","x-editor":"richtext"}}},
	  "hero": {"type":"object","additionalProperties":false,"required":["type"],
	    "properties":{"type":{"const":"hero"},"title":{"type":"string","x-editor":"text"}}}
	}`), &defs); err != nil {
		t.Fatal(err)
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sections": map[string]any{"type": "array", "items": map[string]any{"oneOf": []any{
				map[string]any{"$ref": "#/$defs/hero"}, map[string]any{"$ref": "#/$defs/rich_text"},
			}}},
		},
		"$defs": defs,
	})
	out, changed, err := SanitizeRichText(schema, json.RawMessage(
		`{"sections":[{"type":"hero","title":"<script>t</script>"},{"type":"rich_text","html":"<h1>大標</h1><table><tr><td colspan=\"2\">x</td></tr></table>"}]}`))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	s := string(out)
	if strings.Contains(s, "<h1>") || !strings.Contains(s, "大標") || !strings.Contains(s, `colspan=\"2\"`) {
		t.Fatalf("h1 must be unwrapped, table kept: %s", s)
	}
	if !strings.Contains(s, "<script>t</script>") {
		t.Fatalf("hero title (text) must be untouched: %s", s)
	}
}
