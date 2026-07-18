package cms

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustHydrate(t *testing.T, schema, payload string) map[string]any {
	t.Helper()
	out, err := Hydrate(json.RawMessage(schema), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("hydrated root is %T, want object", out)
	}
	return m
}

const bannerSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"banner": {
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"title":    {"type": "string", "default": "hello"},
				"subtitle": {"type": "string", "default": ""}
			}
		},
		"nav": {
			"type": "array",
			"default": [{"label": "首頁", "href": "/"}],
			"items": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"label": {"type": "string", "default": ""},
					"href":  {"type": "string", "default": "/"}
				}
			}
		}
	}
}`

// Scenario (content-rendering/Hydration): schema 新增欄位自動補預設值.
func TestHydrateFillsDefaults(t *testing.T) {
	out := mustHydrate(t, bannerSchema, `{"banner": {"title": "自訂"}}`)
	banner := out["banner"].(map[string]any)
	if banner["title"] != "自訂" {
		t.Fatalf("payload value lost: %v", banner["title"])
	}
	if banner["subtitle"] != "" {
		t.Fatalf("missing field must get default \"\", got %v (undefined would leak to clients)", banner["subtitle"])
	}
	// Entirely missing object: built from nested defaults.
	out2 := mustHydrate(t, bannerSchema, `{}`)
	banner2 := out2["banner"].(map[string]any)
	if banner2["title"] != "hello" {
		t.Fatalf("nested default not applied: %v", banner2["title"])
	}
	// Array default applies when payload omits it — and its items are
	// hydrated too (href default fills).
	nav := out2["nav"].([]any)
	if len(nav) != 1 || nav[0].(map[string]any)["label"] != "首頁" {
		t.Fatalf("array default not applied: %v", nav)
	}
}

// Scenario: 殘留鍵被剔除 — keys undefined in the current schema disappear.
func TestHydrateDropsResidualKeys(t *testing.T) {
	out := mustHydrate(t, bannerSchema,
		`{"banner": {"title": "t", "old_widget": {"x": 1}}, "legacy_root": true}`)
	if _, exists := out["legacy_root"]; exists {
		t.Fatal("root residual key must be dropped")
	}
	banner := out["banner"].(map[string]any)
	if _, exists := banner["old_widget"]; exists {
		t.Fatal("nested residual key must be dropped")
	}
}

// 一般陣列以 payload 整體為準：payload array replaces the default wholesale
// (no element-wise merge), while items are still pruned and default-filled.
func TestHydrateGeneralArrayWholesale(t *testing.T) {
	out := mustHydrate(t, bannerSchema,
		`{"nav": [{"label": "自訂", "junk": 1}]}`)
	nav := out["nav"].([]any)
	if len(nav) != 1 {
		t.Fatalf("payload array must replace default wholesale, got %d items", len(nav))
	}
	item := nav[0].(map[string]any)
	if item["label"] != "自訂" || item["href"] != "/" {
		t.Fatalf("item hydration wrong: %v", item)
	}
	if _, exists := item["junk"]; exists {
		t.Fatal("unknown key inside array item must be dropped")
	}
	// Empty payload array stays empty (not re-merged with default).
	out2 := mustHydrate(t, bannerSchema, `{"nav": []}`)
	if nav2 := out2["nav"].([]any); len(nav2) != 0 {
		t.Fatalf("explicit empty array must stay empty, got %v", nav2)
	}
}

const sectionsSchema = `{
	"type": "object",
	"additionalProperties": false,
	"$defs": {
		"hero": {
			"type": "object",
			"additionalProperties": false,
			"required": ["type"],
			"properties": {
				"type":     {"const": "hero"},
				"title":    {"type": "string", "default": "歡迎"},
				"cta_text": {"type": "string", "default": "了解更多"}
			}
		},
		"rich_text": {
			"type": "object",
			"additionalProperties": false,
			"required": ["type"],
			"properties": {
				"type": {"const": "rich_text"},
				"html": {"type": "string", "default": ""}
			}
		}
	},
	"properties": {
		"sections": {
			"type": "array",
			"default": [],
			"items": {"oneOf": [{"$ref": "#/$defs/hero"}, {"$ref": "#/$defs/rich_text"}]}
		}
	}
}`

// Scenario: 區塊項目補全預設值 — block-type schema gains cta_text with a
// default; existing section items get it filled per their type.
func TestHydrateSectionItemDefaults(t *testing.T) {
	out := mustHydrate(t, sectionsSchema, `{
		"sections": [
			{"type": "hero", "title": "客製標題"},
			{"type": "rich_text", "html": "<p>hi</p>"}
		]
	}`)
	sections := out["sections"].([]any)
	if len(sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(sections))
	}
	hero := sections[0].(map[string]any)
	if hero["title"] != "客製標題" || hero["cta_text"] != "了解更多" {
		t.Fatalf("hero not hydrated by its block schema: %v", hero)
	}
	rt := sections[1].(map[string]any)
	if rt["html"] != "<p>hi</p>" || rt["type"] != "rich_text" {
		t.Fatalf("rich_text wrong: %v", rt)
	}
}

// Unknown block types (old theme leftovers / newer server) are dropped, and
// unknown keys inside known blocks are pruned.
func TestHydrateDropsUnknownSectionTypes(t *testing.T) {
	out := mustHydrate(t, sectionsSchema, `{
		"sections": [
			{"type": "hero", "old_prop": true},
			{"type": "legacy_carousel", "images": []},
			{"no_type_at_all": 1}
		]
	}`)
	sections := out["sections"].([]any)
	if len(sections) != 1 {
		t.Fatalf("unknown block types must be dropped: %v", sections)
	}
	hero := sections[0].(map[string]any)
	if _, exists := hero["old_prop"]; exists {
		t.Fatal("residual key in known block must be dropped")
	}
	if hero["type"] != "hero" {
		t.Fatalf("type discriminator must survive: %v", hero)
	}
}

// Missing sections key hydrates to the schema default (empty array), never
// null — clients can iterate unconditionally.
func TestHydrateMissingSectionsToEmptyArray(t *testing.T) {
	out := mustHydrate(t, sectionsSchema, `{}`)
	sections, ok := out["sections"].([]any)
	if !ok || sections == nil {
		t.Fatalf("sections must hydrate to [], got %T %v", out["sections"], out["sections"])
	}
	if len(sections) != 0 {
		t.Fatalf("want empty, got %v", sections)
	}
}

// Round-trip stability: hydrating an already-hydrated payload is idempotent.
func TestHydrateIdempotent(t *testing.T) {
	first, err := HydrateJSON(json.RawMessage(sectionsSchema),
		json.RawMessage(`{"sections":[{"type":"hero"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := HydrateJSON(json.RawMessage(sectionsSchema), first)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(first, &a)
	_ = json.Unmarshal(second, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("hydration not idempotent:\n%s\n%s", first, second)
	}
}
