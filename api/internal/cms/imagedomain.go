package cms

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// editorImage marks a string field as an image URL (change product-images-r2,
// design D7). Same rule as editorRichText: the trigger is the schema
// annotation, never the field name.
const editorImage = "image"

// CheckImageFields walks payload alongside schema and reports every string
// whose schema node carries "x-editor": "image" and whose value is neither ""
// nor prefixed by one of prefixes. It returns nil when prefixes is empty
// (media not configured: nothing could satisfy the rule, so it is not applied).
//
// Prefixes MUST end in "/": the comparison is a plain prefix match, and
// without the slash "https://img.example.test.evil.test/a.webp" would pass.
func CheckImageFields(schemaRaw, payloadRaw json.RawMessage, prefixes []string) ([]Detail, error) {
	if len(prefixes) == 0 || len(schemaRaw) == 0 || len(payloadRaw) == 0 {
		return nil, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, fmt.Errorf("cms: image check: schema unmarshal: %w", err)
	}
	var payload any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return nil, fmt.Errorf("cms: image check: payload unmarshal: %w", err)
	}
	c := &imageChecker{hydrator: hydrator{root: schema}, prefixes: prefixes}
	c.walk(schema, payload, "", 0)
	return c.bad, nil
}

type imageChecker struct {
	hydrator
	prefixes []string
	bad      []Detail
}

func (c *imageChecker) allowed(v string) bool {
	if v == "" {
		return true
	}
	for _, p := range c.prefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func (c *imageChecker) walk(node map[string]any, value any, ptr string, depth int) {
	if depth > maxHydrateDepth || node == nil || value == nil {
		return
	}
	node = c.resolve(node, depth)
	switch v := value.(type) {
	case string:
		if ed, _ := node["x-editor"].(string); ed == editorImage && !c.allowed(v) {
			c.bad = append(c.bad, Detail{Pointer: ptr, Message: "image URL must be empty or hosted on the configured image domain"})
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
