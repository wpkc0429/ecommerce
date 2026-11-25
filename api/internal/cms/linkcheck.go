package cms

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// editorLink marks a string field as a link (change headless-content-store
// slice 2, design D3/D6). Like editorImage and editorRichText the trigger is
// the schema annotation, never the field name.
const editorLink = "link"

// MaxLinkLength is the longest accepted link value.
const MaxLinkLength = 2048

// LinkAllowed is the single link predicate: a value is empty, an absolute
// https:// or http:// URL, a mailto: or tel: link, or a site-relative path or
// fragment (starting with "/" or "#"). Everything else - javascript:, data:,
// vbscript:, protocol-relative "//host" paths, values with control characters
// or longer than MaxLinkLength - is refused. The check is on the stored value,
// so a consumer that renders it as href may rely on it (and should still check
// once more: docs/content-store.md).
func LinkAllowed(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > MaxLinkLength {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return len(v) > len("https://")
	case strings.HasPrefix(lower, "http://"):
		return len(v) > len("http://")
	case strings.HasPrefix(lower, "mailto:"), strings.HasPrefix(lower, "tel:"):
		return true
	case strings.HasPrefix(v, "//"), strings.HasPrefix(v, "/\\"):
		return false
	case strings.HasPrefix(v, "/"), strings.HasPrefix(v, "#"):
		return true
	}
	return false
}

// CheckLinkFields walks payload alongside schema and reports every string
// whose schema node carries "x-editor": "link" and whose value is not
// LinkAllowed. Same walk as CheckImageFields.
func CheckLinkFields(schemaRaw, payloadRaw json.RawMessage) ([]Detail, error) {
	if len(schemaRaw) == 0 || len(payloadRaw) == 0 {
		return nil, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, fmt.Errorf("cms: link check: schema unmarshal: %w", err)
	}
	var payload any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return nil, fmt.Errorf("cms: link check: payload unmarshal: %w", err)
	}
	c := &linkChecker{hydrator: hydrator{root: schema}}
	c.walk(schema, payload, "", 0)
	return c.bad, nil
}

type linkChecker struct {
	hydrator
	bad []Detail
}

func (c *linkChecker) walk(node map[string]any, value any, ptr string, depth int) {
	if depth > maxHydrateDepth || node == nil || value == nil {
		return
	}
	node = c.resolve(node, depth)
	switch v := value.(type) {
	case string:
		if ed, _ := node["x-editor"].(string); ed == editorLink && !LinkAllowed(v) {
			c.bad = append(c.bad, Detail{Pointer: ptr, Message: "link must be https://, http://, mailto:, tel:, or a path starting with / or #"})
		}
	case map[string]any:
		props, _ := node["properties"].(map[string]any)
		for key, child := range v {
			if ps, ok := props[key].(map[string]any); ok {
				c.walk(ps, child, ptr+"/"+escapePointer(key), depth+1)
			}
		}
	case []any:
		items, _ := node["items"].(map[string]any)
		if items == nil {
			return
		}
		branches := c.sectionBranches(items, depth)
		for i, item := range v {
			p := ptr + "/" + strconv.Itoa(i)
			if branches != nil {
				if branch := matchSectionBranch(branches, item); branch != nil {
					c.walk(branch, item, p, depth+1)
				}
				continue
			}
			c.walk(items, item, p, depth+1)
		}
	}
}
