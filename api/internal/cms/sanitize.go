package cms

import (
	"bytes"
	"encoding/json"
	"fmt"

	"ksdevworks/ecommerce/api/internal/cms/richtext"
)

// editorRichText is the schema annotation that marks a string field as
// merchant-authored HTML (change cms-rich-text-sanitization, design D3). The
// trigger is the annotation, never the field name: any theme — and any future
// content-field schema driving auto-generated admin forms — is protected the
// moment it declares a field rich text, with no Go change.
const editorRichText = "richtext"

// SanitizeRichText walks payload alongside schema and rewrites every string
// whose schema node carries `"x-editor": "richtext"` with its allowlisted
// form (design D1/D2: write side is the authority; rewrite, never reject).
//
// It reuses the hydrator's $ref and section-branch (oneOf discriminated by
// properties.type.const) resolution so both walkers agree on which schema
// node a payload value belongs to. Values the schema does not describe are
// left untouched — ValidatePayload has already rejected them for the
// additionalProperties:false schemas themes are required to use.
//
// When nothing changes the original bytes are returned as-is (changed=false),
// so a save that needs no sanitization is byte-for-byte what the caller sent.
// Numbers are decoded with UseNumber so a rewrite never alters them.
func SanitizeRichText(schemaRaw, payloadRaw json.RawMessage) (json.RawMessage, bool, error) {
	if len(payloadRaw) == 0 || len(schemaRaw) == 0 {
		return payloadRaw, false, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, false, fmt.Errorf("cms: sanitize: schema unmarshal: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(payloadRaw))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, false, fmt.Errorf("cms: sanitize: payload unmarshal: %w", err)
	}
	s := &sanitizer{hydrator: hydrator{root: schema}}
	out := s.walk(schema, payload, 0)
	if !s.changed {
		return payloadRaw, false, nil
	}
	// SetEscapeHTML(false): json.Marshal would escape every < > & as <
	// etc. — equivalent JSON, but unreadable in the DB and admin responses.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, false, fmt.Errorf("cms: sanitize: marshal: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true, nil
}

type sanitizer struct {
	hydrator
	changed bool
}

func (s *sanitizer) walk(node map[string]any, value any, depth int) any {
	if depth > maxHydrateDepth || node == nil || value == nil {
		return value
	}
	node = s.resolve(node, depth)

	switch v := value.(type) {
	case string:
		if ed, _ := node["x-editor"].(string); ed == editorRichText {
			clean := richtext.Sanitize(v)
			if clean != v {
				s.changed = true
			}
			return clean
		}
		return v

	case map[string]any:
		props, _ := node["properties"].(map[string]any)
		if props == nil {
			return v
		}
		for key, child := range v {
			ps, ok := props[key].(map[string]any)
			if !ok {
				continue
			}
			v[key] = s.walk(ps, child, depth+1)
		}
		return v

	case []any:
		items, _ := node["items"].(map[string]any)
		if items == nil {
			return v
		}
		if branches := s.sectionBranches(items, depth); branches != nil {
			for i, item := range v {
				if branch := matchSectionBranch(branches, item); branch != nil {
					v[i] = s.walk(branch, item, depth+1)
				}
			}
			return v
		}
		for i, item := range v {
			v[i] = s.walk(items, item, depth+1)
		}
		return v
	}
	return value
}

// validateAndSanitize is the single write-side gate for CMS payloads: schema
// validation first (so malformed input is still a 422 with a pointer), then
// rich text sanitization (a rewrite, never a rejection — design D2). The
// sanitized payload is validated once more only when it changed: sanitizing
// can lengthen a string (entity escaping, an added rel attribute), and a
// schema maxLength must hold for what is actually stored.
func validateAndSanitize(schema, payload json.RawMessage) (json.RawMessage, error) {
	if err := ValidatePayload(schema, payload); err != nil {
		return nil, err
	}
	clean, changed, err := SanitizeRichText(schema, payload)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := ValidatePayload(schema, clean); err != nil {
			return nil, err
		}
	}
	return clean, nil
}
