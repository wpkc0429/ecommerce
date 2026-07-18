package cms

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Hydrate walks the schema and fills the payload with defaults (design D6):
//
//   - objects: schema-driven recursion; missing properties get their default
//     (recursively hydrated); keys the schema does not define are DROPPED
//     (防舊主題殘留鍵與注入 — additionalProperties are never emitted).
//   - general arrays: the payload array is taken wholesale (never merged
//     element-wise with the schema default), but each element is still
//     hydrated against the items schema (default fill + unknown-key pruning).
//   - section arrays (items.oneOf where every branch discriminates on
//     properties.type.const): each element is hydrated against the branch
//     matching its "type"; elements with unknown types are DROPPED.
//   - scalars: payload value if present, otherwise the schema default
//     (null when the theme author omitted one, which 撰寫規範 forbids).
//
// Local $ref ("#/...") references are resolved against the schema root.
//
// The maximum nesting depth guards against pathological or cyclic schemas.
const maxHydrateDepth = 64

// Hydrate returns the hydrated payload for a schema. payload may be nil/empty.
func Hydrate(schemaRaw, payloadRaw json.RawMessage) (any, error) {
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, fmt.Errorf("cms: hydrate: schema unmarshal: %w", err)
	}
	var payload any
	if len(payloadRaw) > 0 {
		if err := json.Unmarshal(payloadRaw, &payload); err != nil {
			return nil, fmt.Errorf("cms: hydrate: payload unmarshal: %w", err)
		}
	}
	h := &hydrator{root: schema}
	return h.hydrate(schema, payload, 0), nil
}

// HydrateJSON is Hydrate returning marshaled bytes.
func HydrateJSON(schemaRaw, payloadRaw json.RawMessage) (json.RawMessage, error) {
	v, err := Hydrate(schemaRaw, payloadRaw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("cms: hydrate: marshal: %w", err)
	}
	return out, nil
}

type hydrator struct {
	root map[string]any
}

// resolve follows local $ref chains ("#/$defs/hero").
func (h *hydrator) resolve(node map[string]any, depth int) map[string]any {
	for i := 0; i < 8; i++ {
		ref, ok := node["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return node
		}
		target := h.lookupPointer(ref[2:])
		if target == nil {
			return node
		}
		node = target
	}
	return node
}

func (h *hydrator) lookupPointer(ptr string) map[string]any {
	var cur any = h.root
	for _, seg := range strings.Split(ptr, "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[seg]
		if !ok {
			return nil
		}
	}
	out, _ := cur.(map[string]any)
	return out
}

func (h *hydrator) hydrate(node map[string]any, value any, depth int) any {
	if depth > maxHydrateDepth || node == nil {
		return value
	}
	node = h.resolve(node, depth)
	typ, _ := node["type"].(string)
	props, hasProps := node["properties"].(map[string]any)

	switch {
	case typ == "object" || (typ == "" && hasProps):
		if !hasProps {
			// Object without declared properties: nothing is known to keep.
			return map[string]any{}
		}
		obj, isObj := value.(map[string]any)
		out := make(map[string]any, len(props))
		for key, raw := range props {
			ps, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			var childVal any
			present := false
			if isObj {
				childVal, present = obj[key]
			}
			// Explicit null is treated as missing → default applies.
			if present && childVal != nil {
				out[key] = h.hydrate(ps, childVal, depth+1)
			} else {
				out[key] = h.defaultFor(ps, depth+1)
			}
		}
		return out

	case typ == "array":
		items, _ := node["items"].(map[string]any)
		arr, isArr := value.([]any)
		if !isArr {
			return h.defaultFor(node, depth+1)
		}
		if branches := h.sectionBranches(items, depth); branches != nil {
			// Section array: per-item hydration by block type; unknown block
			// types are dropped (design D8: 渲染端容錯的伺服器側前哨).
			out := make([]any, 0, len(arr))
			for _, item := range arr {
				branch := matchSectionBranch(branches, item)
				if branch == nil {
					continue
				}
				out = append(out, h.hydrate(branch, item, depth+1))
			}
			return out
		}
		// General array: payload wholesale; per-element hydration only when
		// the items schema describes structured values.
		if items == nil {
			return arr
		}
		resolved := h.resolve(items, depth)
		itype, _ := resolved["type"].(string)
		_, itemsHaveProps := resolved["properties"].(map[string]any)
		if itype == "object" || itype == "array" || itemsHaveProps {
			out := make([]any, 0, len(arr))
			for _, item := range arr {
				out = append(out, h.hydrate(resolved, item, depth+1))
			}
			return out
		}
		return arr

	default: // scalar or untyped
		if value != nil {
			return value
		}
		return h.defaultFor(node, depth+1)
	}
}

// defaultFor produces the hydrated default value of a schema node.
func (h *hydrator) defaultFor(node map[string]any, depth int) any {
	if depth > maxHydrateDepth {
		return nil
	}
	node = h.resolve(node, depth)
	if d, ok := node["default"]; ok && d != nil {
		return h.hydrate(node, deepCopy(d), depth+1)
	}
	typ, _ := node["type"].(string)
	_, hasProps := node["properties"].(map[string]any)
	switch {
	case typ == "object" || (typ == "" && hasProps):
		return h.hydrate(node, map[string]any{}, depth+1)
	case typ == "array":
		return []any{}
	default:
		return nil
	}
}

// sectionBranches detects the section-array shape: items.oneOf where every
// branch has properties.type.const. Returns nil when not section-style.
func (h *hydrator) sectionBranches(items map[string]any, depth int) []map[string]any {
	if items == nil {
		return nil
	}
	items = h.resolve(items, depth)
	oneOf, ok := items["oneOf"].([]any)
	if !ok || len(oneOf) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(oneOf))
	for _, raw := range oneOf {
		bm, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		bm = h.resolve(bm, depth)
		if sectionTypeConst(bm) == "" {
			return nil
		}
		out = append(out, bm)
	}
	return out
}

func sectionTypeConst(branch map[string]any) string {
	props, _ := branch["properties"].(map[string]any)
	tp, _ := props["type"].(map[string]any)
	c, _ := tp["const"].(string)
	return c
}

func matchSectionBranch(branches []map[string]any, item any) map[string]any {
	obj, ok := item.(map[string]any)
	if !ok {
		return nil
	}
	itemType, _ := obj["type"].(string)
	if itemType == "" {
		return nil
	}
	for _, b := range branches {
		if sectionTypeConst(b) == itemType {
			return b
		}
	}
	return nil
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = deepCopy(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = deepCopy(vv)
		}
		return out
	default:
		return v
	}
}
