package content

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"ksdevworks/ecommerce/api/internal/cms"
)

// Statuses of a document in a registration report (design D3/D4).
const (
	StatusOK             = "ok"
	StatusNeedsAttention = "needs_attention"
	StatusOrphaned       = "orphaned"
)

// Document kinds in a report.
const (
	KindPage   = "page"
	KindGlobal = "global"
)

// Limits that keep a report small however many documents a shop has.
const (
	maxReportPaths   = 50
	maxReportInvalid = 10
	maxWalkDepth     = 64
)

// DocInput is one stored document handed to the compatibility check.
type DocInput struct {
	Kind      string // KindPage or KindGlobal
	Key       string // page slug, or "global"
	TypeKey   string // page type; "global" for the global content
	Draft     json.RawMessage
	Published json.RawMessage // nil when never published
}

// DocReport is the compatibility verdict of one document under a new schema.
type DocReport struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Status string `json:"status"`
	// RemovedPaths are the JSON Pointers of values that the new schema would
	// drop on hydrate (a removed or renamed field that holds a value), taken
	// from the draft and the published snapshot. Empty values do not count.
	RemovedPaths []string `json:"removed_paths"`
	// Invalid are the first problems of the draft under the new schema.
	Invalid []cms.Detail `json:"invalid"`
	// PublishedInvalid is true when the published snapshot no longer validates;
	// it keeps being served (hydrated) regardless.
	PublishedInvalid bool `json:"published_invalid"`

	// hasContent: an orphaned document that holds real content (not just the
	// auto-created empty placeholder). Only such a document needs the caller's
	// confirmation and a snapshot.
	hasContent bool
}

// Report is the registration compatibility report.
type Report struct {
	Documents []DocReport `json:"documents"`
}

// HasRemovals reports whether registering would drop values or orphan a
// document, which is what "confirm_removals" has to acknowledge.
func (r Report) HasRemovals() bool {
	for _, d := range r.Documents {
		if len(d.RemovedPaths) > 0 || (d.Status == StatusOrphaned && d.hasContent) {
			return true
		}
	}
	return false
}

// AtRisk reports whether the document needs a snapshot of its content.
func (d DocReport) AtRisk() bool {
	return len(d.RemovedPaths) > 0 || d.Status == StatusNeedsAttention || (d.Status == StatusOrphaned && d.hasContent)
}

// Diff checks every document against the new schema's types and global schema.
// It never writes anything.
func Diff(docs []DocInput, types map[string]json.RawMessage, global json.RawMessage) Report {
	out := Report{Documents: make([]DocReport, 0, len(docs))}
	for _, d := range docs {
		rep := DocReport{Kind: d.Kind, Key: d.Key, Status: StatusOK, RemovedPaths: []string{}, Invalid: []cms.Detail{}}
		var schema json.RawMessage
		if d.Kind == KindGlobal {
			schema = global
		} else {
			s, ok := types[d.TypeKey]
			if !ok {
				rep.Status = StatusOrphaned
				rep.hasContent = hasContent(d.Draft) || hasContent(d.Published)
				out.Documents = append(out.Documents, rep)
				continue
			}
			schema = s
		}
		paths := map[string]bool{}
		collect := func(payload json.RawMessage) {
			for _, p := range RemovedPaths(schema, payload) {
				paths[p] = true
			}
		}
		collect(d.Draft)
		if len(d.Published) > 0 {
			collect(d.Published)
		}
		for p := range paths {
			rep.RemovedPaths = append(rep.RemovedPaths, p)
		}
		sort.Strings(rep.RemovedPaths)
		if len(rep.RemovedPaths) > maxReportPaths {
			rep.RemovedPaths = rep.RemovedPaths[:maxReportPaths]
		}

		if err := cms.ValidatePayload(schema, d.Draft); err != nil {
			rep.Status = StatusNeedsAttention
			if ve, ok := err.(*cms.ValidationError); ok {
				rep.Invalid = ve.Details
				if len(rep.Invalid) > maxReportInvalid {
					rep.Invalid = rep.Invalid[:maxReportInvalid]
				}
			}
		}
		if len(d.Published) > 0 && cms.ValidatePayload(schema, d.Published) != nil {
			rep.PublishedInvalid = true
			rep.Status = StatusNeedsAttention
		}
		out.Documents = append(out.Documents, rep)
	}
	return out
}

// RemovedPaths lists the JSON Pointers of non-empty values in payload that
// cms.Hydrate would drop under schema: keys the schema does not declare, and
// section elements of an unknown block type. Same walk rules as hydrate
// (local $ref resolution, section unions by properties.type.const).
func RemovedPaths(schemaRaw, payloadRaw json.RawMessage) []string {
	var schema map[string]any
	if json.Unmarshal(schemaRaw, &schema) != nil {
		return nil
	}
	var payload any
	if len(payloadRaw) == 0 || json.Unmarshal(payloadRaw, &payload) != nil {
		return nil
	}
	w := &pathWalker{root: schema}
	w.walk(schema, payload, "", 0)
	return w.out
}

type pathWalker struct {
	root map[string]any
	out  []string
}

func (w *pathWalker) resolve(node map[string]any) map[string]any {
	for i := 0; i < 8; i++ {
		ref, ok := node["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return node
		}
		var cur any = w.root
		for _, seg := range strings.Split(ref[2:], "/") {
			m, ok := cur.(map[string]any)
			if !ok {
				return node
			}
			cur = m[seg]
		}
		t, ok := cur.(map[string]any)
		if !ok {
			return node
		}
		node = t
	}
	return node
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func (w *pathWalker) walk(node map[string]any, value any, ptr string, depth int) {
	if node == nil || depth > maxWalkDepth || len(w.out) >= 4*maxReportPaths {
		return
	}
	node = w.resolve(node)
	props, hasProps := node["properties"].(map[string]any)
	typ, _ := node["type"].(string)
	switch {
	case typ == "object" || (typ == "" && hasProps):
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := ptr + "/" + esc(k)
			ps, declared := props[k].(map[string]any)
			if !declared {
				if !isEmptyValue(obj[k]) {
					w.out = append(w.out, child)
				}
				continue
			}
			w.walk(ps, obj[k], child, depth+1)
		}
	case typ == "array":
		arr, ok := value.([]any)
		if !ok {
			return
		}
		items, _ := node["items"].(map[string]any)
		if items == nil {
			return
		}
		items = w.resolve(items)
		if oneOf, ok := items["oneOf"].([]any); ok && len(oneOf) > 0 {
			branches := map[string]map[string]any{}
			for _, b := range oneOf {
				bm, _ := b.(map[string]any)
				if bm == nil {
					continue
				}
				bm = w.resolve(bm)
				bp, _ := bm["properties"].(map[string]any)
				tp, _ := bp["type"].(map[string]any)
				if c, _ := tp["const"].(string); c != "" {
					branches[c] = bm
				}
			}
			for i, el := range arr {
				child := ptr + "/" + strconv.Itoa(i)
				obj, _ := el.(map[string]any)
				t, _ := obj["type"].(string)
				br, known := branches[t]
				if !known {
					if !isEmptyValue(el) {
						w.out = append(w.out, child)
					}
					continue
				}
				w.walk(br, el, child, depth+1)
			}
			return
		}
		for i, el := range arr {
			w.walk(items, el, ptr+"/"+strconv.Itoa(i), depth+1)
		}
	}
}

// hasContent reports whether raw holds any non-empty value anywhere: an empty
// string, array or object, null and false do not count, so the placeholder
// {"sections":[]} of an auto-created home page is "no content".
func hasContent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return true // unreadable: assume it matters
	}
	var walk func(v any, depth int) bool
	walk = func(v any, depth int) bool {
		if depth > maxWalkDepth {
			return true
		}
		switch t := v.(type) {
		case nil:
			return false
		case string:
			return t != ""
		case bool:
			return t
		case []any:
			for _, e := range t {
				if walk(e, depth+1) {
					return true
				}
			}
			return false
		case map[string]any:
			for _, e := range t {
				if walk(e, depth+1) {
					return true
				}
			}
			return false
		}
		return true
	}
	return walk(v, 0)
}
