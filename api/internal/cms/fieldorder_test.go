package cms

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func propsFromJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	return props
}

// Scenario (theme-system/Theme schema validity): 後台表單依標註順序渲染 —
// fields A, B, C annotated 2, 0, 1 must render in order B, C, A.
func TestFieldOrderSortsByAnnotation(t *testing.T) {
	props := propsFromJSON(t, `{
		"A": {"type": "string", "x-editor-order": 2},
		"B": {"type": "string", "x-editor-order": 0},
		"C": {"type": "string", "x-editor-order": 1}
	}`)
	got := FieldOrder(props)
	want := []string{"B", "C", "A"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FieldOrder() = %v, want %v", got, want)
	}
}

// Scenario (theme-system/Theme schema validity): 未標註欄位退回字母序排在最後 —
// A is annotated 0; D and B are unannotated and must sort alphabetically
// after A.
func TestFieldOrderFallbackAlphabetical(t *testing.T) {
	props := propsFromJSON(t, `{
		"A": {"type": "string", "x-editor-order": 0},
		"D": {"type": "string"},
		"B": {"type": "string"}
	}`)
	got := FieldOrder(props)
	want := []string{"A", "B", "D"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FieldOrder() = %v, want %v", got, want)
	}
}

// Equal x-editor-order values break ties alphabetically by key.
func TestFieldOrderTieBreakAlphabetical(t *testing.T) {
	props := propsFromJSON(t, `{
		"zebra": {"type": "string", "x-editor-order": 0},
		"alpha": {"type": "string", "x-editor-order": 0}
	}`)
	got := FieldOrder(props)
	want := []string{"alpha", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FieldOrder() = %v, want %v", got, want)
	}
}

// With no annotations at all, FieldOrder degrades to plain alphabetical
// order — deterministic, unlike the Go-map/jsonb iteration order it
// replaces.
func TestFieldOrderAllUnannotated(t *testing.T) {
	props := propsFromJSON(t, `{
		"charlie": {"type": "string"},
		"alpha": {"type": "string"},
		"bravo": {"type": "string"}
	}`)
	got := FieldOrder(props)
	want := []string{"alpha", "bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FieldOrder() = %v, want %v", got, want)
	}
}

// Empty properties map yields an empty (non-nil-panicking) slice.
func TestFieldOrderEmpty(t *testing.T) {
	got := FieldOrder(map[string]any{})
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %v", got)
	}
}

// validateEditorOrder must be reachable through the package's normal
// error-typing convention.
func TestValidateEditorOrderAcceptsValidIntegers(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "x-editor-order": 0},
			"subtitle": {"type": "string", "x-editor-order": -1}
		}
	}`)
	if err := validateEditorOrder(schema); err != nil {
		t.Fatalf("valid integer annotations rejected: %v", err)
	}
}

func TestValidateEditorOrderRejectsNonInteger(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "x-editor-order": "1"}
		}
	}`)
	err := validateEditorOrder(schema)
	if err == nil {
		t.Fatal("non-integer x-editor-order must be rejected")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	found := false
	for _, d := range ve.Details {
		if d.Pointer == "/properties/title/x-editor-order" {
			found = true
		}
	}
	if !found {
		t.Fatalf("details must point at /properties/title/x-editor-order, got %+v", ve.Details)
	}
}

func TestValidateEditorOrderRejectsFloat(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "x-editor-order": 1.5}
		}
	}`)
	if err := validateEditorOrder(schema); err == nil {
		t.Fatal("non-integer float x-editor-order must be rejected")
	}
}

// x-editor-order can appear at any nesting level — nested object
// properties and $defs section-block definitions — not just the top-level
// properties map.
func TestValidateEditorOrderFindsNestedAnnotations(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"banner": {
				"type": "object",
				"properties": {
					"title": {"type": "string", "x-editor-order": true}
				}
			}
		},
		"$defs": {
			"hero": {
				"type": "object",
				"properties": {
					"cta_text": {"type": "string", "x-editor-order": "bad"}
				}
			}
		}
	}`)
	err := validateEditorOrder(schema)
	if err == nil {
		t.Fatal("nested invalid annotations must be rejected")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(ve.Details) != 2 {
		t.Fatalf("want 2 details (one per nested invalid annotation), got %+v", ve.Details)
	}
}
