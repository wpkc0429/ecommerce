// Package content implements per-shop content schema registration (change
// headless-content-store, first slice): the vocabulary allowlist and the
// eight-step validation pipeline of design D3, the compatibility diff of D4,
// and the registration service.
//
// It lives beside, not inside, internal/contentschema (the store) because the
// validation pipeline needs the cms engine (CompileSchema, Hydrate) and cms
// already imports the store.
package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/contentschema"
)

// Limits of design D3. They are variables only so tests can name them; nothing
// but tests changes them.
const (
	MaxBodyBytes       = 128 << 10 // step 1
	MaxRawJSONDepth    = 40        // step 1 (see the deviation note in Validate)
	MaxSchemaDepth     = 8         // step 2: properties/items/oneOf nesting
	MaxNodes           = 2000      // step 2: written nodes in the whole document
	MaxExpandedNodes   = 5000      // step 4: nodes after $ref expansion, per type
	MaxExpandedDepth   = 16        // step 4: depth after $ref expansion, per type
	MaxProperties      = 50
	MaxEnumValues      = 100
	MaxEnumValueLen    = 200
	MaxTypes           = 50
	MaxDocuments       = 100
	MaxCollections     = 20
	MaxDefs            = 50
	MaxUnionBranches   = 30
	MaxTitleLen        = 200
	MaxDescriptionLen  = 500
	MaxHelpLen         = 200
	MaxPatternLen      = 200
	MaxStringLen       = 2000
	MaxLinkImageLen    = 2048
	MaxRichtextLen     = 50000
	MaxArrayItems      = 200
	MaxCollectionItems = 100
	maxErrors          = 50
	// ValidationBudget is the wall-clock budget of steps 5-6 (compile and the
	// defaults self-consistency check).
	ValidationBudget = 2 * time.Second
)

// DraftSchemaURI is the only value of "$schema" a content schema may carry.
const DraftSchemaURI = "https://json-schema.org/draft/2020-12/schema"

var (
	typeKeyRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,49}$`)
	docKeyRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	prefixRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}-$`)
	propNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
	defNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,49}$`)
	refRe       = regexp.MustCompile(`^#/\$defs/([A-Za-z_][A-Za-z0-9_-]{0,49})$`)
	reservedKey = map[string]bool{"__proto__": true, "constructor": true, "prototype": true}
)

// Parsed is a schema document that passed every validation step.
type Parsed struct {
	// Normalized is the canonical form (sorted keys, compact) that is stored
	// and hashed.
	Normalized []byte
	// Digest is the hex SHA-256 of Normalized.
	Digest      string
	Types       map[string]TypeDef
	Documents   map[string]DocumentDecl
	Collections map[string]CollectionDecl
}

// TypeDef is one named shape of the document.
type TypeDef struct {
	Title  string
	Schema json.RawMessage
}

// DocumentDecl declares a fixed document key.
type DocumentDecl struct{ Type string }

// CollectionDecl declares a dynamic key prefix.
type CollectionDecl struct {
	Type string
	Max  int
}

// newValidationError wraps Validate's problems as the 422 the HTTP layer sends.
func newValidationError(details []cms.Detail) *cms.ValidationError {
	return &cms.ValidationError{Message: "content schema is not acceptable", Details: details}
}

type collector struct{ details []cms.Detail }

func (c *collector) add(ptr, format string, args ...any) {
	if len(c.details) >= maxErrors {
		return
	}
	c.details = append(c.details, cms.Detail{Pointer: ptr, Message: fmt.Sprintf(format, args...)})
}

func (c *collector) full() bool { return len(c.details) >= maxErrors }

func esc(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }

// Validate runs the eight steps of design D3 on a submitted content.schema.json.
// It returns either a Parsed document or a non-empty list of pointer details
// (the caller answers 422 and writes nothing).
//
// Deviation from the design text, recorded here and in the change's tasks: the
// raw-JSON nesting limit before parsing is 40, not 12. The structural limit
// (8 levels of properties/items/oneOf) costs two JSON levels per schema level
// plus the wrapper levels, so a limit of 12 would reject schemas the structural
// rule allows. 40 still bounds the parser far below any stack concern; the
// parser here is token-based and never recurses deeper than that.
func Validate(raw []byte) (*Parsed, []cms.Detail) {
	// Step 1.
	if len(raw) > MaxBodyBytes {
		return nil, []cms.Detail{{Pointer: "", Message: fmt.Sprintf("schema is larger than %d bytes", MaxBodyBytes)}}
	}
	doc, perr := parseStrict(raw)
	if perr != nil {
		return nil, []cms.Detail{*perr}
	}
	top, ok := doc.(map[string]any)
	if !ok {
		return nil, []cms.Detail{{Pointer: "", Message: "schema must be a JSON object"}}
	}
	c := &collector{}

	// Top level shape.
	for _, k := range sortedKeys(top) {
		switch k {
		case "format", "types", "documents", "collections":
		default:
			c.add("/"+esc(k), "unknown top-level key %q", k)
		}
	}
	if f, ok := top["format"].(json.Number); !ok || f.String() != "2" {
		c.add("/format", "format must be 2")
	}
	typesRaw, _ := top["types"].(map[string]any)
	if typesRaw == nil {
		c.add("/types", "types must be an object")
		typesRaw = map[string]any{}
	}
	if len(typesRaw) == 0 {
		c.add("/types", "at least one type is required")
	}
	if len(typesRaw) > MaxTypes {
		c.add("/types", "at most %d types are allowed", MaxTypes)
	}

	// Steps 2-4 per type: structure, vocabulary, $ref graph.
	st := &docState{c: c}
	parsedTypes := map[string]TypeDef{}
	for _, tk := range sortedKeys(typesRaw) {
		base := "/types/" + esc(tk)
		if !typeKeyRe.MatchString(tk) {
			c.add(base, "type key must match %s", typeKeyRe.String())
			continue
		}
		tm, ok := typesRaw[tk].(map[string]any)
		if !ok {
			c.add(base, "a type must be an object with title and schema")
			continue
		}
		for _, k := range sortedKeys(tm) {
			if k != "title" && k != "schema" {
				c.add(base+"/"+esc(k), "unknown key %q (a type has only title and schema)", k)
			}
		}
		title := ""
		if t, present := tm["title"]; present {
			s, isStr := t.(string)
			if !isStr {
				c.add(base+"/title", "title must be a string")
			} else {
				title = s
				checkPlainText(c, base+"/title", "title", s, MaxTitleLen)
			}
		}
		sm, ok := tm["schema"].(map[string]any)
		if !ok {
			c.add(base+"/schema", "schema must be a JSON Schema object")
			continue
		}
		st.checkType(tk, base+"/schema", sm)
		schemaRaw, err := json.Marshal(sm)
		if err != nil {
			c.add(base+"/schema", "schema cannot be serialized")
			continue
		}
		parsedTypes[tk] = TypeDef{Title: title, Schema: schemaRaw}
	}
	if st.nodes > MaxNodes {
		c.add("/types", "the document has more than %d schema nodes", MaxNodes)
	}

	// Step 7 (cheap, so it runs before the expensive steps): declarations.
	docs, colls := checkDeclarations(c, top, typesRaw)

	if len(c.details) > 0 {
		return nil, c.details
	}

	// Steps 5-6 under a wall-clock budget.
	if details := runBudgeted(func() []cms.Detail { return compileAndSelfCheck(parsedTypes) }); len(details) > 0 {
		return nil, details
	}

	norm, err := normalize(doc)
	if err != nil {
		return nil, []cms.Detail{{Pointer: "", Message: "schema cannot be normalized"}}
	}
	sum := sha256.Sum256(norm)
	return &Parsed{
		Normalized:  norm,
		Digest:      hex.EncodeToString(sum[:]),
		Types:       parsedTypes,
		Documents:   docs,
		Collections: colls,
	}, nil
}

// ── step 1: strict JSON ───────────────────────────────────────────────

// parseStrict parses raw as one JSON value, rejecting duplicate object keys,
// trailing data and nesting beyond MaxRawJSONDepth (checked before descending,
// so a hostile "[[[[…" never gets far).
func parseStrict(raw []byte) (any, *cms.Detail) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := parseValue(dec, 0, "")
	if err != nil {
		return nil, err
	}
	if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
		return nil, &cms.Detail{Pointer: "", Message: "unexpected data after the JSON document"}
	}
	return v, nil
}

func parseValue(dec *json.Decoder, depth int, ptr string) (any, *cms.Detail) {
	tok, err := dec.Token()
	if err != nil {
		return nil, &cms.Detail{Pointer: ptr, Message: "schema is not valid JSON: " + err.Error()}
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	if depth+1 > MaxRawJSONDepth {
		return nil, &cms.Detail{Pointer: ptr, Message: fmt.Sprintf("JSON is nested deeper than %d levels", MaxRawJSONDepth)}
	}
	switch d {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, &cms.Detail{Pointer: ptr, Message: "schema is not valid JSON: " + err.Error()}
			}
			key, _ := kt.(string)
			if _, dup := obj[key]; dup {
				return nil, &cms.Detail{Pointer: ptr + "/" + esc(key), Message: fmt.Sprintf("duplicate object key %q", key)}
			}
			v, derr := parseValue(dec, depth+1, ptr+"/"+esc(key))
			if derr != nil {
				return nil, derr
			}
			obj[key] = v
		}
		if _, err := dec.Token(); err != nil {
			return nil, &cms.Detail{Pointer: ptr, Message: "schema is not valid JSON: " + err.Error()}
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			v, derr := parseValue(dec, depth+1, fmt.Sprintf("%s/%d", ptr, len(arr)))
			if derr != nil {
				return nil, derr
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, &cms.Detail{Pointer: ptr, Message: "schema is not valid JSON: " + err.Error()}
		}
		return arr, nil
	}
	return nil, &cms.Detail{Pointer: ptr, Message: "schema is not valid JSON"}
}

// normalize re-serializes a parsed document with sorted keys and numbers kept
// exactly as written (json.Number).
func normalize(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// NormalizeRaw returns the canonical form and digest of any stored JSON, so a
// stored version can be compared with a freshly validated one.
func NormalizeRaw(raw []byte) ([]byte, string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, "", err
	}
	norm, err := normalize(v)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(norm)
	return norm, hex.EncodeToString(sum[:]), nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkPlainText(c *collector, ptr, what, s string, max int) {
	if len([]rune(s)) > max {
		c.add(ptr, "%s is longer than %d characters", what, max)
	}
	if strings.ContainsAny(s, "<>") {
		c.add(ptr, "%s must be plain text (no < or >)", what)
	}
}

// ── steps 2-4: structure, vocabulary, $ref graph ─────────────────────

type container struct {
	own      int      // written nodes directly inside (not through $ref)
	maxDepth int      // deepest written node
	refs     []refUse // $ref occurrences
}

type refUse struct {
	target string
	depth  int
	ptr    string
}

type docState struct {
	c     *collector
	nodes int // across the whole document
}

type typeState struct {
	*docState
	typeKey string
	defs    map[string]any
	// containers: "" is the root schema, otherwise a $defs name.
	containers map[string]*container
	cur        *container
	stopped    bool
}

func (d *docState) checkType(typeKey, base string, root map[string]any) {
	ts := &typeState{docState: d, typeKey: typeKey, containers: map[string]*container{}}

	// Root.
	for _, k := range sortedKeys(root) {
		if k == "$schema" {
			if s, _ := root[k].(string); s != DraftSchemaURI {
				d.c.add(base+"/$schema", "$schema may only be %q", DraftSchemaURI)
			}
		}
	}
	defs := map[string]any{}
	if dv, present := root["$defs"]; present {
		dm, ok := dv.(map[string]any)
		if !ok {
			d.c.add(base+"/$defs", "$defs must be an object")
		} else {
			defs = dm
			if len(dm) > MaxDefs {
				d.c.add(base+"/$defs", "at most %d definitions are allowed", MaxDefs)
			}
		}
	}
	ts.defs = defs

	if t, _ := root["type"].(string); t != "object" {
		d.c.add(base+"/type", "the root of a type must be an object schema")
	}

	ts.cur = &container{}
	ts.containers[""] = ts.cur
	ts.node(root, base, 0, roleRoot)

	for _, name := range sortedKeys(defs) {
		if !defNameRe.MatchString(name) {
			d.c.add(base+"/$defs/"+esc(name), "definition name must match %s", defNameRe.String())
			continue
		}
		ts.cur = &container{}
		ts.containers[name] = ts.cur
		ts.node(defs[name], base+"/$defs/"+esc(name), 0, roleDef)
	}

	ts.checkRefGraph(base)
}

type role int

const (
	roleRoot role = iota
	roleDef
	roleProperty
	roleItems
	roleBranch
)

// allowed keywords per node type (beyond the common ones).
var common = map[string]bool{
	"type": true, "title": true, "description": true, "default": true,
	"enum": true, "const": true, "x-help": true, "x-editor-order": true,
}

var perType = map[string]map[string]bool{
	"object":  {"properties": true, "required": true, "additionalProperties": true},
	"array":   {"items": true, "minItems": true, "maxItems": true, "x-editor": true},
	"string":  {"minLength": true, "maxLength": true, "pattern": true, "format": true, "x-editor": true},
	"integer": {"minimum": true, "maximum": true},
	"number":  {"minimum": true, "maximum": true},
	"boolean": {},
}

var stringEditors = map[string]bool{
	"text": true, "textarea": true, "richtext": true, "image": true, "link": true, "color": true, "select": true,
}

func (ts *typeState) node(n any, ptr string, depth int, rl role) {
	if ts.stopped || ts.c.full() {
		return
	}
	m, ok := n.(map[string]any)
	if !ok {
		ts.c.add(ptr, "a schema node must be an object")
		return
	}
	ts.nodes++
	ts.cur.own++
	if depth > ts.cur.maxDepth {
		ts.cur.maxDepth = depth
	}
	if ts.nodes > MaxNodes {
		ts.stopped = true
		return
	}
	if depth > MaxSchemaDepth {
		ts.c.add(ptr, "schema is nested deeper than %d levels", MaxSchemaDepth)
		ts.stopped = true
		return
	}

	// Always-forbidden keywords get a specific message.
	for _, k := range sortedKeys(m) {
		if forbidden[k] {
			ts.c.add(ptr+"/"+esc(k), "keyword %q is not allowed", k)
		}
	}

	// Description and title are plain text wherever they appear.
	ts.checkTexts(m, ptr)

	if refV, has := m["$ref"]; has {
		for _, k := range sortedKeys(m) {
			switch k {
			case "$ref", "title", "description", "x-help", "x-editor-order":
			default:
				if !forbidden[k] {
					ts.c.add(ptr+"/"+esc(k), "keyword %q cannot sit next to $ref", k)
				}
			}
		}
		ref, _ := refV.(string)
		mm := refRe.FindStringSubmatch(ref)
		if mm == nil {
			ts.c.add(ptr+"/$ref", "only local references of the form #/$defs/<name> are allowed")
			return
		}
		if _, exists := ts.defs[mm[1]]; !exists {
			ts.c.add(ptr+"/$ref", "%q does not exist in $defs", ref)
			return
		}
		ts.cur.refs = append(ts.cur.refs, refUse{target: mm[1], depth: depth, ptr: ptr + "/$ref"})
		return
	}

	// Block union (the sections convention): items: {oneOf:[object branches]}.
	if oneOf, has := m["oneOf"]; has {
		ts.union(m, oneOf, ptr, depth, rl)
		return
	}

	typ, _ := m["type"].(string)
	allowedForType, known := perType[typ]
	if !known {
		ts.c.add(ptr+"/type", "type must be one of object, array, string, integer, number, boolean")
		return
	}
	for _, k := range sortedKeys(m) {
		switch {
		case forbidden[k]:
		case common[k], allowedForType[k]:
		case k == "$schema" && rl == roleRoot:
		case k == "$defs" && rl == roleRoot:
		case strings.HasPrefix(k, "x-"):
			ts.c.add(ptr+"/"+esc(k), "unknown extension %q (allowed: x-editor, x-editor-order, x-help)", k)
		default:
			ts.c.add(ptr+"/"+esc(k), "keyword %q is not allowed on a %s schema", k, typ)
		}
	}
	if v, present := m["x-editor-order"]; present {
		if num, isNum := v.(json.Number); !isNum || toInt(num) < 0 || strings.ContainsAny(num.String(), ".eE") {
			ts.c.add(ptr+"/x-editor-order", "x-editor-order must be a non-negative integer")
		}
	}
	ts.checkEnumConst(m, ptr)

	switch typ {
	case "object":
		ts.object(m, ptr, depth)
	case "array":
		ts.array(m, ptr, depth)
	case "string":
		ts.str(m, ptr)
	case "integer", "number":
		for _, k := range []string{"minimum", "maximum"} {
			if v, present := m[k]; present {
				if _, isNum := v.(json.Number); !isNum {
					ts.c.add(ptr+"/"+k, "%s must be a number", k)
				}
			}
		}
	}
}

var forbidden = map[string]bool{
	"$id": true, "$anchor": true, "$dynamicRef": true, "$dynamicAnchor": true, "$vocabulary": true, "$comment": true,
	"patternProperties": true, "propertyNames": true, "unevaluatedProperties": true, "unevaluatedItems": true,
	"dependentSchemas": true, "dependentRequired": true, "dependencies": true,
	"if": true, "then": true, "else": true, "allOf": true, "anyOf": true, "not": true,
	"contentEncoding": true, "contentMediaType": true, "contentSchema": true,
	"minProperties": true, "maxProperties": true, "uniqueItems": true, "contains": true, "prefixItems": true,
	"additionalItems": true, "multipleOf": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"minContains": true, "maxContains": true,
}

func (ts *typeState) checkTexts(m map[string]any, ptr string) {
	if v, present := m["title"]; present {
		if s, ok := v.(string); ok {
			checkPlainText(ts.c, ptr+"/title", "title", s, MaxTitleLen)
		} else {
			ts.c.add(ptr+"/title", "title must be a string")
		}
	}
	if v, present := m["description"]; present {
		if s, ok := v.(string); ok {
			checkPlainText(ts.c, ptr+"/description", "description", s, MaxDescriptionLen)
		} else {
			ts.c.add(ptr+"/description", "description must be a string")
		}
	}
	if v, present := m["x-help"]; present {
		if s, ok := v.(string); ok {
			checkPlainText(ts.c, ptr+"/x-help", "x-help", s, MaxHelpLen)
		} else {
			ts.c.add(ptr+"/x-help", "x-help must be a string")
		}
	}
}

func (ts *typeState) checkEnumConst(m map[string]any, ptr string) {
	if v, present := m["enum"]; present {
		arr, ok := v.([]any)
		switch {
		case !ok || len(arr) == 0:
			ts.c.add(ptr+"/enum", "enum must be a non-empty array")
		case len(arr) > MaxEnumValues:
			ts.c.add(ptr+"/enum", "enum has more than %d values", MaxEnumValues)
		default:
			for i, e := range arr {
				ts.scalarValue(e, fmt.Sprintf("%s/enum/%d", ptr, i))
			}
		}
	}
	if v, present := m["const"]; present {
		ts.scalarValue(v, ptr+"/const")
	}
}

func (ts *typeState) scalarValue(v any, ptr string) {
	switch t := v.(type) {
	case string:
		if len([]rune(t)) > MaxEnumValueLen {
			ts.c.add(ptr, "value is longer than %d characters", MaxEnumValueLen)
		}
	case json.Number, bool:
	default:
		ts.c.add(ptr, "value must be a string, number or boolean")
	}
}

func (ts *typeState) object(m map[string]any, ptr string, depth int) {
	if ap, present := m["additionalProperties"]; !present || ap != false {
		ts.c.add(ptr+"/additionalProperties", "every object must declare additionalProperties: false")
	}
	props := map[string]any{}
	if pv, present := m["properties"]; present {
		pm, ok := pv.(map[string]any)
		if !ok {
			ts.c.add(ptr+"/properties", "properties must be an object")
		} else {
			props = pm
		}
	}
	if len(props) > MaxProperties {
		ts.c.add(ptr+"/properties", "an object may have at most %d properties", MaxProperties)
	}
	for _, name := range sortedKeys(props) {
		if !propNameRe.MatchString(name) || reservedKey[name] {
			ts.c.add(ptr+"/properties/"+esc(name), "property name must match %s and not be a reserved word", propNameRe.String())
			continue
		}
		ts.node(props[name], ptr+"/properties/"+esc(name), depth+1, roleProperty)
	}
	if rv, present := m["required"]; present {
		arr, ok := rv.([]any)
		if !ok {
			ts.c.add(ptr+"/required", "required must be an array of property names")
			return
		}
		if len(arr) > MaxProperties {
			ts.c.add(ptr+"/required", "required lists more than %d names", MaxProperties)
			return
		}
		for i, e := range arr {
			s, isStr := e.(string)
			if !isStr {
				ts.c.add(fmt.Sprintf("%s/required/%d", ptr, i), "required entries must be strings")
				continue
			}
			if _, exists := props[s]; !exists {
				ts.c.add(fmt.Sprintf("%s/required/%d", ptr, i), "%q is not a declared property", s)
			}
		}
	}
}

func (ts *typeState) array(m map[string]any, ptr string, depth int) {
	mi, present := m["maxItems"]
	n, isNum := mi.(json.Number)
	switch {
	case !present:
		ts.c.add(ptr+"/maxItems", "every array must declare maxItems")
	case !isNum || strings.ContainsAny(n.String(), ".eE") || toInt(n) < 1 || toInt(n) > MaxArrayItems:
		ts.c.add(ptr+"/maxItems", "maxItems must be an integer between 1 and %d", MaxArrayItems)
	}
	if v, has := m["minItems"]; has {
		nn, ok := v.(json.Number)
		if !ok || strings.ContainsAny(nn.String(), ".eE") || toInt(nn) < 0 {
			ts.c.add(ptr+"/minItems", "minItems must be a non-negative integer")
		} else if isNum && toInt(nn) > toInt(n) {
			ts.c.add(ptr+"/minItems", "minItems is larger than maxItems")
		}
	}
	items, has := m["items"]
	if !has {
		ts.c.add(ptr+"/items", "every array must declare items")
		return
	}
	switch ed, _ := m["x-editor"].(string); {
	case m["x-editor"] == nil, ed == "list":
	case ed == "sections":
		im, _ := items.(map[string]any)
		if _, hasUnion := im["oneOf"]; !hasUnion {
			ts.c.add(ptr+"/x-editor", "x-editor sections needs items.oneOf (a block union)")
		}
	default:
		ts.c.add(ptr+"/x-editor", "on an array x-editor must be list or sections")
	}
	ts.node(items, ptr+"/items", depth+1, roleItems)
}

func (ts *typeState) str(m map[string]any, ptr string) {
	ed, _ := m["x-editor"].(string)
	if v, present := m["x-editor"]; present {
		if _, isStr := v.(string); !isStr || !stringEditors[ed] {
			ts.c.add(ptr+"/x-editor", "x-editor on a string must be one of text, textarea, richtext, image, link, color, select")
			ed = ""
		}
	}
	limit := MaxStringLen
	switch ed {
	case "richtext":
		limit = MaxRichtextLen
	case "link", "image":
		limit = MaxLinkImageLen
	}
	mlv, present := m["maxLength"]
	n, isNum := mlv.(json.Number)
	_, hasEnum := m["enum"]
	_, hasConst := m["const"]
	switch {
	case !present && (hasEnum || hasConst):
		// A string fixed by const or enum is bounded by its (<=200 character)
		// values; it needs no maxLength of its own.
	case !present:
		ts.c.add(ptr+"/maxLength", "every string must declare maxLength (not needed when const or enum fixes the value)")
	case !isNum || strings.ContainsAny(n.String(), ".eE") || toInt(n) < 1 || toInt(n) > limit:
		ts.c.add(ptr+"/maxLength", "maxLength must be an integer between 1 and %d", limit)
	}
	if v, has := m["minLength"]; has {
		nn, ok := v.(json.Number)
		if !ok || strings.ContainsAny(nn.String(), ".eE") || toInt(nn) < 0 {
			ts.c.add(ptr+"/minLength", "minLength must be a non-negative integer")
		} else if isNum && toInt(nn) > toInt(n) {
			ts.c.add(ptr+"/minLength", "minLength is larger than maxLength")
		}
	}
	if v, has := m["pattern"]; has {
		p, ok := v.(string)
		switch {
		case !ok:
			ts.c.add(ptr+"/pattern", "pattern must be a string")
		case len(p) > MaxPatternLen:
			ts.c.add(ptr+"/pattern", "pattern is longer than %d characters", MaxPatternLen)
		default:
			// Go regexp is RE2: linear time, no backtracking. It still has to
			// compile, which also rejects lookaheads and backreferences.
			if _, err := regexp.Compile(p); err != nil {
				ts.c.add(ptr+"/pattern", "pattern is not a valid RE2 expression")
			}
		}
	}
	if v, has := m["format"]; has {
		if f, _ := v.(string); f != "date" && f != "date-time" {
			ts.c.add(ptr+"/format", "format may only be date or date-time")
		}
	}
	if ed == "select" {
		if _, hasEnum := m["enum"]; !hasEnum {
			ts.c.add(ptr+"/x-editor", "x-editor select needs enum")
		}
	}
}

func (ts *typeState) union(m map[string]any, oneOf any, ptr string, depth int, rl role) {
	for _, k := range sortedKeys(m) {
		switch k {
		case "oneOf", "title", "description":
		default:
			if !forbidden[k] {
				ts.c.add(ptr+"/"+esc(k), "keyword %q cannot sit next to oneOf", k)
			}
		}
	}
	if rl != roleItems {
		ts.c.add(ptr+"/oneOf", "oneOf is only allowed as the items of an array (a block union)")
		return
	}
	branches, ok := oneOf.([]any)
	if !ok || len(branches) == 0 {
		ts.c.add(ptr+"/oneOf", "oneOf must be a non-empty array of branches")
		return
	}
	if len(branches) > MaxUnionBranches {
		ts.c.add(ptr+"/oneOf", "oneOf has more than %d branches", MaxUnionBranches)
		return
	}
	seen := map[string]bool{}
	for i, b := range branches {
		bptr := fmt.Sprintf("%s/oneOf/%d", ptr, i)
		ts.node(b, bptr, depth+1, roleBranch)
		tc, ok := ts.branchTypeConst(b)
		if !ok {
			ts.c.add(bptr, "every branch must be an object whose properties.type.const is a string")
			continue
		}
		if seen[tc] {
			ts.c.add(bptr, "duplicate block type %q", tc)
		}
		seen[tc] = true
	}
}

// branchTypeConst resolves one local $ref hop and returns properties.type.const.
func (ts *typeState) branchTypeConst(b any) (string, bool) {
	m, ok := b.(map[string]any)
	if !ok {
		return "", false
	}
	if ref, has := m["$ref"].(string); has {
		mm := refRe.FindStringSubmatch(ref)
		if mm == nil {
			return "", false
		}
		m, ok = ts.defs[mm[1]].(map[string]any)
		if !ok {
			return "", false
		}
	}
	if t, _ := m["type"].(string); t != "object" {
		return "", false
	}
	props, _ := m["properties"].(map[string]any)
	tp, _ := props["type"].(map[string]any)
	c, ok := tp["const"].(string)
	return c, ok && c != ""
}

func toInt(n json.Number) int {
	i, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil {
			return -1
		}
		if f > 1e9 {
			return 1 << 30
		}
		return int(f)
	}
	if i > 1<<30 {
		return 1 << 30
	}
	if i < -(1 << 30) {
		return -(1 << 30)
	}
	return int(i)
}

// checkRefGraph detects $ref cycles and bounds the expanded size and depth
// (step 4): a small schema must not unfold into a huge one.
func (ts *typeState) checkRefGraph(base string) {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := map[string]int{}
	nodes := map[string]int{}
	depths := map[string]int{}
	var cyclic bool
	var visit func(name string)
	visit = func(name string) {
		if state[name] == black || cyclic {
			return
		}
		state[name] = grey
		ct := ts.containers[name]
		if ct == nil {
			state[name] = black
			return
		}
		total, maxD := ct.own, ct.maxDepth
		for _, r := range ct.refs {
			if state[r.target] == grey {
				ts.c.add(strings.TrimSuffix(r.ptr, "/$ref")+"/$ref", "$ref cycle through %q", r.target)
				cyclic = true
				return
			}
			visit(r.target)
			if cyclic {
				return
			}
			total += nodes[r.target]
			if total > MaxExpandedNodes+1 {
				total = MaxExpandedNodes + 1
			}
			if d := r.depth + 1 + depths[r.target]; d > maxD {
				maxD = d
			}
		}
		nodes[name], depths[name] = total, maxD
		state[name] = black
	}
	visit("")
	for name := range ts.containers {
		if name != "" {
			visit(name)
		}
	}
	if cyclic {
		return
	}
	if nodes[""] > MaxExpandedNodes {
		ts.c.add(base, "after $ref expansion the schema has more than %d nodes", MaxExpandedNodes)
	}
	if depths[""] > MaxExpandedDepth {
		ts.c.add(base, "after $ref expansion the schema is nested deeper than %d levels", MaxExpandedDepth)
	}
}

// ── step 7: declarations ──────────────────────────────────────────────

func checkDeclarations(c *collector, top, types map[string]any) (map[string]DocumentDecl, map[string]CollectionDecl) {
	docs := map[string]DocumentDecl{}
	colls := map[string]CollectionDecl{}

	if dv, present := top["documents"]; present {
		dm, ok := dv.(map[string]any)
		if !ok {
			c.add("/documents", "documents must be an object")
		} else {
			if len(dm) > MaxDocuments {
				c.add("/documents", "at most %d documents may be declared", MaxDocuments)
			}
			for _, k := range sortedKeys(dm) {
				p := "/documents/" + esc(k)
				if !docKeyRe.MatchString(k) {
					c.add(p, "document key must match %s", docKeyRe.String())
					continue
				}
				e, ok := dm[k].(map[string]any)
				if !ok {
					c.add(p, "a document declaration must be an object {\"type\": ...}")
					continue
				}
				for _, ek := range sortedKeys(e) {
					if ek != "type" {
						c.add(p+"/"+esc(ek), "unknown key %q", ek)
					}
				}
				t, _ := e["type"].(string)
				if _, exists := types[t]; !exists {
					c.add(p+"/type", "type %q is not defined in types", t)
					continue
				}
				if k == "global" && t != "global" {
					c.add(p+"/type", "the global document must use the global type")
				}
				docs[k] = DocumentDecl{Type: t}
			}
		}
	}

	if cv, present := top["collections"]; present {
		cm, ok := cv.(map[string]any)
		if !ok {
			c.add("/collections", "collections must be an object")
		} else {
			if len(cm) > MaxCollections {
				c.add("/collections", "at most %d collections may be declared", MaxCollections)
			}
			for _, k := range sortedKeys(cm) {
				p := "/collections/" + esc(k)
				if !prefixRe.MatchString(k) {
					c.add(p, "collection prefix must match %s (it ends with a hyphen)", prefixRe.String())
					continue
				}
				e, ok := cm[k].(map[string]any)
				if !ok {
					c.add(p, "a collection declaration must be an object {\"type\": ..., \"max\": ...}")
					continue
				}
				for _, ek := range sortedKeys(e) {
					if ek != "type" && ek != "max" {
						c.add(p+"/"+esc(ek), "unknown key %q", ek)
					}
				}
				t, _ := e["type"].(string)
				if _, exists := types[t]; !exists || t == "global" {
					c.add(p+"/type", "type %q is not a defined, non-global type", t)
					continue
				}
				max := 0
				if mv, ok := e["max"].(json.Number); !ok || strings.ContainsAny(mv.String(), ".eE") {
					c.add(p+"/max", "max must be an integer between 1 and %d", MaxCollectionItems)
				} else if max = toInt(mv); max < 1 || max > MaxCollectionItems {
					c.add(p+"/max", "max must be an integer between 1 and %d", MaxCollectionItems)
				}
				colls[k] = CollectionDecl{Type: t, Max: max}
			}
		}
	}

	// Conflicts: a fixed key may not fall inside a collection prefix, and no
	// prefix may contain another.
	prefixes := make([]string, 0, len(colls))
	for p := range colls {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	for dk := range docs {
		for _, p := range prefixes {
			if strings.HasPrefix(dk, p) {
				c.add("/documents/"+esc(dk), "key falls inside the collection prefix %q", p)
			}
		}
	}
	for i, a := range prefixes {
		for _, b := range prefixes[i+1:] {
			if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
				c.add("/collections/"+esc(b), "collection prefixes %q and %q overlap", a, b)
			}
		}
	}
	return docs, colls
}

// ── steps 5-6: compile and defaults self-consistency ──────────────────

func compileAndSelfCheck(types map[string]TypeDef) []cms.Detail {
	keys := make([]string, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []cms.Detail
	for _, k := range keys {
		base := "/types/" + esc(k)
		def := types[k]
		// Step 5: compile (draft 2020-12) and check x-editor-order shape.
		if err := cms.ValidateSchemaDoc(def.Schema); err != nil {
			var ve *cms.ValidationError
			if errors.As(err, &ve) {
				for _, d := range ve.Details {
					out = append(out, cms.Detail{Pointer: base + "/schema" + d.Pointer, Message: d.Message})
				}
			} else {
				out = append(out, cms.Detail{Pointer: base + "/schema", Message: err.Error()})
			}
			continue
		}
		// Step 6: the schema must accept its own hydrated defaults.
		hydrated, err := cms.HydrateJSON(def.Schema, nil)
		if err != nil {
			out = append(out, cms.Detail{Pointer: base + "/schema", Message: "defaults cannot be computed: " + err.Error()})
			continue
		}
		if err := cms.ValidatePayload(def.Schema, hydrated); err != nil {
			var ve *cms.ValidationError
			if errors.As(err, &ve) {
				for _, d := range ve.Details {
					out = append(out, cms.Detail{
						Pointer: base + "/schema",
						Message: fmt.Sprintf("the schema rejects its own default value at %q: %s (give every field a default that satisfies its constraints)", d.Pointer, d.Message),
					})
				}
			} else {
				out = append(out, cms.Detail{Pointer: base + "/schema", Message: err.Error()})
			}
		}
		if len(out) >= maxErrors {
			return out[:maxErrors]
		}
	}
	return out
}

// runBudgeted runs fn with the wall-clock budget of steps 5-6. The libraries
// are not context aware, so on timeout the goroutine is abandoned: it is
// bounded by the structural limits already enforced and ends on its own.
func runBudgeted(fn func() []cms.Detail) []cms.Detail {
	ctx, cancel := context.WithTimeout(context.Background(), ValidationBudget)
	defer cancel()
	done := make(chan []cms.Detail, 1)
	go func() { done <- fn() }()
	select {
	case d := <-done:
		return d
	case <-ctx.Done():
		return []cms.Detail{{Pointer: "", Message: fmt.Sprintf("schema validation exceeded its %s budget", ValidationBudget)}}
	}
}

// ToDoc converts a validated format-2 document into the store's normalized
// view (types resolve to their JSON Schema; "global" is the global schema).
func (p *Parsed) ToDoc() contentschema.Doc {
	d := contentschema.Doc{Format: 2, Types: map[string]json.RawMessage{}}
	for k, t := range p.Types {
		d.Types[k] = t.Schema
	}
	return d
}
