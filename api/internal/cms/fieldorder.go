package cms

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// editorOrderKeyword is the schema extension keyword (design
// cms-editor-field-order, same family as "x-editor") that lets a theme
// author pin the presentation order of admin form fields. It is necessary
// because Postgres jsonb does not preserve object key order (see
// openspec/project.md Important Constraints) — without an explicit
// annotation, the order schema.properties keys arrive in at the admin is
// undefined.
const editorOrderKeyword = "x-editor-order"

// FieldOrder returns the keys of a schema object node's "properties" map
// ordered for admin form rendering: entries annotated with
// "x-editor-order" (an integer; smaller sorts first) come first in
// ascending order, ties broken alphabetically by key; entries without the
// annotation are appended after, sorted alphabetically. The alphabetical
// fallback is deterministic on purpose — it is never worse than the
// undefined jsonb/Go-map iteration order this keyword exists to fix, and it
// lets theme authors annotate incrementally without every field needing a
// value.
func FieldOrder(props map[string]any) []string {
	type entry struct {
		key       string
		order     float64
		annotated bool
	}
	entries := make([]entry, 0, len(props))
	for key, raw := range props {
		e := entry{key: key}
		if ps, ok := raw.(map[string]any); ok {
			if v, ok := ps[editorOrderKeyword]; ok {
				if f, ok := v.(float64); ok {
					e.order = f
					e.annotated = true
				}
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.annotated != b.annotated {
			return a.annotated // annotated entries sort before unannotated ones
		}
		if a.annotated && a.order != b.order {
			return a.order < b.order
		}
		return a.key < b.key
	})
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.key
	}
	return out
}

// validateEditorOrder walks a parsed schema document (any nesting level —
// top-level properties, nested object properties, $defs section-block
// definitions, array items, ...) and rejects "x-editor-order" values that
// are not integers. It is intentionally structure-agnostic: rather than
// assuming the keyword only ever appears directly under "properties", it
// inspects every map in the document for the keyword's presence, which is
// robust to however deeply theme schemas nest their field definitions.
func validateEditorOrder(raw json.RawMessage) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// CompileSchema (called earlier in ValidateSchemaDoc) already
		// rejects unparsable JSON; unreachable in practice.
		return nil
	}
	var details []Detail
	var walk func(v any, ptr string)
	walk = func(v any, ptr string) {
		switch t := v.(type) {
		case map[string]any:
			if raw, ok := t[editorOrderKeyword]; ok && !isJSONInteger(raw) {
				details = append(details, Detail{
					Pointer: ptr + "/" + editorOrderKeyword,
					Message: fmt.Sprintf("%s must be an integer", editorOrderKeyword),
				})
			}
			for k, vv := range t {
				walk(vv, ptr+"/"+escapePointerSegment(k))
			}
		case []any:
			for i, vv := range t {
				walk(vv, fmt.Sprintf("%s/%d", ptr, i))
			}
		}
	}
	walk(doc, "")
	if len(details) > 0 {
		return &ValidationError{
			Message: "schema contains invalid x-editor-order annotations",
			Details: details,
		}
	}
	return nil
}

// isJSONInteger reports whether v (a value decoded from JSON) is a number
// with no fractional part. encoding/json decodes all JSON numbers as
// float64 when unmarshaled into `any`.
func isJSONInteger(v any) bool {
	f, ok := v.(float64)
	if !ok {
		return false
	}
	return f == math.Trunc(f)
}
