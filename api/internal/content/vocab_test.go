package content_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/content"
)

// wrap puts one type schema into a minimal format-2 document.
func wrap(typeSchema string) []byte {
	return []byte(`{"format":2,"types":{"home":{"title":"Home","schema":` + typeSchema + `}}}`)
}

const okRoot = `{"type":"object","additionalProperties":false,"properties":{
  "title":{"type":"string","maxLength":100,"default":"hi"},
  "items":{"type":"array","maxItems":5,"default":[],"items":{"type":"string","maxLength":10,"default":""}}}}`

func detailsText(ds []cms.Detail) string {
	var sb strings.Builder
	for _, d := range ds {
		sb.WriteString(d.Pointer + ": " + d.Message + "\n")
	}
	return sb.String()
}

// mustFail asserts that raw is rejected and that some detail names the pointer
// suffix and message fragment.
func mustFail(t *testing.T, raw []byte, pointerHas, msgHas string) {
	t.Helper()
	p, ds := content.Validate(raw)
	if p != nil || len(ds) == 0 {
		t.Fatalf("expected rejection (%s / %s), got accepted", pointerHas, msgHas)
	}
	for _, d := range ds {
		if strings.Contains(d.Pointer, pointerHas) && strings.Contains(d.Message, msgHas) {
			return
		}
	}
	t.Fatalf("no detail matches pointer~%q message~%q; got:\n%s", pointerHas, msgHas, detailsText(ds))
}

func TestValidateAcceptsMinimalSchema(t *testing.T) {
	p, ds := content.Validate(wrap(okRoot))
	if p == nil {
		t.Fatalf("rejected: %s", detailsText(ds))
	}
	if p.Digest == "" || len(p.Normalized) == 0 || p.Types["home"].Title != "Home" {
		t.Errorf("parsed document incomplete: %+v", p)
	}
}

func TestValidateDigestIsStableAcrossKeyOrderAndWhitespace(t *testing.T) {
	a := []byte(`{"format":2,"types":{"home":{"title":"Home","schema":` + okRoot + `}}}`)
	b := []byte(`{
	  "types": {"home": {"schema": ` + okRoot + `, "title": "Home"}},
	  "format": 2 }`)
	pa, _ := content.Validate(a)
	pb, ds := content.Validate(b)
	if pa == nil || pb == nil {
		t.Fatalf("rejected: %s", detailsText(ds))
	}
	if pa.Digest != pb.Digest {
		t.Errorf("same schema, different digest")
	}
}

func TestValidateStarterSchemaFromDocs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "content-schema", "starter.content.schema.json"))
	if err != nil {
		t.Fatalf("read starter: %v", err)
	}
	p, ds := content.Validate(raw)
	if p == nil {
		t.Fatalf("the documented starter schema is rejected:\n%s", detailsText(ds))
	}
	for _, k := range []string{"global", "home", "story", "faq", "product_info"} {
		if _, ok := p.Types[k]; !ok {
			t.Errorf("starter lacks type %s", k)
		}
	}
	if len(p.Documents) != 5 {
		t.Errorf("starter documents = %v", p.Documents)
	}
}

// ── step 1: body, JSON ────────────────────────────────────────────────

func TestValidateRejectsOversizeBody(t *testing.T) {
	big := make([]byte, content.MaxBodyBytes+1)
	for i := range big {
		big[i] = ' '
	}
	mustFail(t, big, "", "larger than")
}

func TestValidateRejectsMalformedAndTrailing(t *testing.T) {
	mustFail(t, []byte(`{"format":2,`), "", "not valid JSON")
	mustFail(t, []byte(`not json`), "", "not valid JSON")
	mustFail(t, append(wrap(okRoot), []byte(` {}`)...), "", "after the JSON document")
	mustFail(t, []byte(`[]`), "", "must be a JSON object")
}

func TestValidateRejectsDuplicateKeys(t *testing.T) {
	dup := `{"format":2,"types":{"home":{"title":"a","title":"b","schema":` + okRoot + `}}}`
	mustFail(t, []byte(dup), "/types/home/title", "duplicate object key")
	nested := `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string","maxLength":5,"default":"","maxLength":6}}}`
	mustFail(t, wrap(nested), "maxLength", "duplicate object key")
}

func TestValidateRejectsHostileRawDepth(t *testing.T) {
	deep := strings.Repeat("[", 60000) + strings.Repeat("]", 60000)
	start := time.Now()
	mustFail(t, []byte(deep), "", "nested deeper")
	if time.Since(start) > time.Second {
		t.Errorf("deep JSON took %s to reject", time.Since(start))
	}
	// A deep, valid-looking object nest.
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString(`{"a":`)
	}
	sb.WriteString(`1`)
	sb.WriteString(strings.Repeat("}", 60))
	mustFail(t, []byte(sb.String()), "", "nested deeper")
}

func TestValidateTopLevelShape(t *testing.T) {
	mustFail(t, []byte(`{"format":1,"types":{"home":{"schema":`+okRoot+`}}}`), "/format", "must be 2")
	mustFail(t, []byte(`{"types":{}}`), "/format", "must be 2")
	mustFail(t, []byte(`{"format":2,"types":{}}`), "/types", "at least one type")
	mustFail(t, []byte(`{"format":2,"types":{"home":{"schema":`+okRoot+`}},"extra":1}`), "/extra", "unknown top-level key")
	mustFail(t, []byte(`{"format":2,"types":{"Bad Key":{"schema":`+okRoot+`}}}`), "/types/Bad Key", "type key must match")
	mustFail(t, []byte(`{"format":2,"types":{"home":{"schema":`+okRoot+`,"x":1}}}`), "/types/home/x", "unknown key")
}

// ── step 2: structural limits ─────────────────────────────────────────

func nestedObjects(levels int) string {
	inner := `{"type":"string","maxLength":10,"default":""}`
	for i := 0; i < levels; i++ {
		inner = `{"type":"object","additionalProperties":false,"properties":{"c":` + inner + `}}`
	}
	return inner
}

func TestValidateDepthLimit(t *testing.T) {
	// 8 nested levels: the innermost string sits at depth 8: allowed.
	if p, ds := content.Validate(wrap(nestedObjects(8))); p == nil {
		t.Fatalf("depth 8 must pass: %s", detailsText(ds))
	}
	mustFail(t, wrap(nestedObjects(9)), "/properties", "nested deeper than 8")
	mustFail(t, wrap(nestedObjects(30)), "/properties", "nested deeper than")
}

func TestValidateNodeCountLimit(t *testing.T) {
	var props []string
	// 41 objects x 50 string properties = 2091 nodes > 2000.
	for i := 0; i < 41; i++ {
		var sub []string
		for j := 0; j < 50; j++ {
			sub = append(sub, fmt.Sprintf(`"f%d":{"type":"string","maxLength":5,"default":""}`, j))
		}
		props = append(props, fmt.Sprintf(`"o%d":{"type":"object","additionalProperties":false,"properties":{%s}}`, i, strings.Join(sub, ",")))
	}
	schema := `{"type":"object","additionalProperties":false,"properties":{` + strings.Join(props[:41], ",") + `}}`
	// 41 > 50? no: the root has 41 properties (ok), the document has 2092 nodes.
	mustFail(t, wrap(schema), "/types", "more than 2000 schema nodes")
}

func TestValidateTooManyPropertiesEnumTypes(t *testing.T) {
	var sub []string
	for j := 0; j < 51; j++ {
		sub = append(sub, fmt.Sprintf(`"f%d":{"type":"string","maxLength":5,"default":""}`, j))
	}
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"properties":{`+strings.Join(sub, ",")+`}}`),
		"/properties", "at most 50 properties")

	var vals []string
	for j := 0; j < 101; j++ {
		vals = append(vals, fmt.Sprintf(`"v%d"`, j))
	}
	enum := `{"type":"object","additionalProperties":false,"properties":{"e":{"type":"string","maxLength":10,"default":"v0","x-editor":"select","enum":[` + strings.Join(vals, ",") + `]}}}`
	mustFail(t, wrap(enum), "/enum", "more than 100 values")

	long := `{"type":"object","additionalProperties":false,"properties":{"e":{"type":"string","maxLength":300,"default":"a","enum":["` + strings.Repeat("x", 201) + `"]}}}`
	mustFail(t, wrap(long), "/enum/0", "longer than 200")

	var types []string
	for i := 0; i < 51; i++ {
		types = append(types, fmt.Sprintf(`"t%d":{"schema":%s}`, i, `{"type":"object","additionalProperties":false,"properties":{}}`))
	}
	mustFail(t, []byte(`{"format":2,"types":{`+strings.Join(types, ",")+`}}`), "/types", "at most 50 types")
}

// ── step 3: vocabulary ────────────────────────────────────────────────

func prop(body string) []byte {
	return wrap(`{"type":"object","additionalProperties":false,"properties":{"p":` + body + `}}`)
}

func TestValidateForbiddenKeywords(t *testing.T) {
	for _, kw := range []string{"$id", "$anchor", "$dynamicRef", "$dynamicAnchor", "patternProperties", "propertyNames",
		"unevaluatedProperties", "unevaluatedItems", "dependentSchemas", "dependentRequired", "if", "then", "else",
		"allOf", "anyOf", "not", "contentEncoding", "contentMediaType", "uniqueItems", "multipleOf"} {
		body := fmt.Sprintf(`{"type":"string","maxLength":5,"default":"","%s":{}}`, kw)
		mustFail(t, prop(body), "/"+kw, "not allowed")
	}
	// Forbidden at the root too.
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"properties":{},"allOf":[]}`), "/allOf", "not allowed")
	mustFail(t, wrap(`{"$id":"x","type":"object","additionalProperties":false,"properties":{}}`), "/$id", "not allowed")
}

func TestValidateUnknownExtensionsAndKeywords(t *testing.T) {
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-foo":1}`), "/x-foo", "unknown extension")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","frobnicate":1}`), "/frobnicate", "not allowed")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","minimum":1}`), "/minimum", "not allowed on a string")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","format":"regex"}`), "/format", "date or date-time")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","format":"email"}`), "/format", "date or date-time")
	mustFail(t, prop(`{"type":["string","null"],"maxLength":5}`), "/type", "type must be one of")
	mustFail(t, prop(`{"maxLength":5}`), "/type", "type must be one of")
	mustFail(t, prop(`{"type":"null"}`), "/type", "type must be one of")
}

func TestValidateRootMustBeStrictObject(t *testing.T) {
	mustFail(t, wrap(`{"type":"string","maxLength":5,"default":""}`), "/type", "root of a type must be an object")
	mustFail(t, wrap(`{"type":"object","properties":{}}`), "/additionalProperties", "additionalProperties: false")
	mustFail(t, wrap(`{"type":"object","additionalProperties":true,"properties":{}}`), "/additionalProperties", "additionalProperties: false")
	mustFail(t, wrap(`{"type":"object","additionalProperties":{"type":"string"},"properties":{}}`), "/additionalProperties", "additionalProperties: false")
	mustFail(t, wrap(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","additionalProperties":false,"properties":{}}`),
		"/$schema", "$schema may only be")
}

func TestValidateStringsNeedMaxLength(t *testing.T) {
	mustFail(t, prop(`{"type":"string","default":""}`), "/p/maxLength", "must declare maxLength")
	mustFail(t, prop(`{"type":"string","maxLength":2001,"default":""}`), "/p/maxLength", "between 1 and 2000")
	mustFail(t, prop(`{"type":"string","maxLength":0,"default":""}`), "/p/maxLength", "between 1 and 2000")
	mustFail(t, prop(`{"type":"string","maxLength":1.5,"default":""}`), "/p/maxLength", "must be an integer")
	mustFail(t, prop(`{"type":"string","minLength":10,"maxLength":5,"default":""}`), "/p/minLength", "larger than maxLength")
	// richtext may be longer, link and image up to 2048.
	if p, ds := content.Validate(prop(`{"type":"string","maxLength":50000,"default":"","x-editor":"richtext"}`)); p == nil {
		t.Errorf("richtext 50000 must pass: %s", detailsText(ds))
	}
	mustFail(t, prop(`{"type":"string","maxLength":50001,"default":"","x-editor":"richtext"}`), "/p/maxLength", "between 1 and 50000")
	if p, ds := content.Validate(prop(`{"type":"string","maxLength":2048,"default":"","x-editor":"link"}`)); p == nil {
		t.Errorf("link 2048 must pass: %s", detailsText(ds))
	}
	mustFail(t, prop(`{"type":"string","maxLength":2049,"default":"","x-editor":"link"}`), "/p/maxLength", "between 1 and 2048")
	mustFail(t, prop(`{"type":"string","maxLength":3000,"default":"","x-editor":"text"}`), "/p/maxLength", "between 1 and 2000")
}

func TestValidateArraysNeedMaxItems(t *testing.T) {
	item := `{"type":"string","maxLength":5,"default":""}`
	mustFail(t, prop(`{"type":"array","items":`+item+`}`), "/p/maxItems", "must declare maxItems")
	mustFail(t, prop(`{"type":"array","maxItems":201,"items":`+item+`}`), "/p/maxItems", "between 1 and 200")
	mustFail(t, prop(`{"type":"array","maxItems":5}`), "/p/items", "must declare items")
	mustFail(t, prop(`{"type":"array","maxItems":5,"minItems":6,"items":`+item+`}`), "/p/minItems", "larger than maxItems")
	mustFail(t, prop(`{"type":"array","maxItems":5,"items":`+item+`,"x-editor":"table"}`), "/p/x-editor", "list or sections")
	mustFail(t, prop(`{"type":"array","maxItems":5,"items":`+item+`,"x-editor":"sections"}`), "/p/x-editor", "needs items.oneOf")
}

func TestValidatePatternLimits(t *testing.T) {
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","pattern":"`+strings.Repeat("a", 201)+`"}`), "/p/pattern", "longer than 200")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","pattern":"(?=a)b"}`), "/p/pattern", "RE2")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","pattern":"(a"}`), "/p/pattern", "RE2")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","pattern":"(a)\\1"}`), "/p/pattern", "RE2")
}

// Regex-DoS attempts: a catastrophic-backtracking pattern is legal RE2 (linear
// time), and validating a long hostile value against it must stay fast.
func TestValidateCatastrophicPatternIsLinear(t *testing.T) {
	schema := `{"type":"object","additionalProperties":false,"properties":{"p":{"type":"string","maxLength":2000,"default":"a","pattern":"^(a+)+$"}}}`
	p, ds := content.Validate(wrap(schema))
	if p == nil {
		t.Fatalf("an RE2 pattern must be accepted: %s", detailsText(ds))
	}
	hostile, _ := json.Marshal(map[string]string{"p": strings.Repeat("a", 1999) + "!"})
	start := time.Now()
	err := cms.ValidatePayload(p.Types["home"].Schema, hostile)
	if err == nil {
		t.Errorf("value must not match")
	}
	if time.Since(start) > time.Second {
		t.Errorf("hostile value took %s", time.Since(start))
	}
}

func TestValidateTextsArePlain(t *testing.T) {
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","title":"<script>alert(1)</script>"}`), "/p/title", "plain text")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","description":"a<b"}`), "/p/description", "plain text")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-help":"<img src=x onerror=1>"}`), "/p/x-help", "plain text")
	mustFail(t, []byte(`{"format":2,"types":{"home":{"title":"<b>x</b>","schema":`+okRoot+`}}}`), "/types/home/title", "plain text")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","title":"`+strings.Repeat("a", 201)+`"}`), "/p/title", "longer than 200")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","description":"`+strings.Repeat("a", 501)+`"}`), "/p/description", "longer than 500")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-help":"`+strings.Repeat("a", 201)+`"}`), "/p/x-help", "longer than 200")
}

func TestValidateXEditorRules(t *testing.T) {
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-editor":"wysiwyg"}`), "/p/x-editor", "must be one of")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-editor":"select"}`), "/p/x-editor", "needs enum")
	mustFail(t, prop(`{"type":"integer","x-editor":"text"}`), "/p/x-editor", "unknown extension")
	mustFail(t, prop(`{"type":"object","additionalProperties":false,"properties":{},"x-editor":"text"}`), "/p/x-editor", "unknown extension")
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-editor-order":-1}`), "/p/x-editor-order", "non-negative")
	for _, ed := range []string{"text", "textarea", "richtext", "image", "link", "color"} {
		if p, ds := content.Validate(prop(fmt.Sprintf(`{"type":"string","maxLength":100,"default":"","x-editor":%q}`, ed))); p == nil {
			t.Errorf("x-editor %s must pass: %s", ed, detailsText(ds))
		}
	}
	if p, ds := content.Validate(prop(`{"type":"string","maxLength":10,"default":"a","x-editor":"select","enum":["a","b"]}`)); p == nil {
		t.Errorf("select with enum must pass: %s", detailsText(ds))
	}
}

func TestValidatePropertyNamesAndRequired(t *testing.T) {
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"properties":{"__proto__":{"type":"string","maxLength":5,"default":""}}}`),
		"/properties/__proto__", "reserved")
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"properties":{"a b":{"type":"string","maxLength":5,"default":""}}}`),
		"/properties/a b", "property name must match")
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"properties":{},"required":["nope"]}`), "/required/0", "not a declared property")
}

// ── step 4: $ref graph ────────────────────────────────────────────────

func TestValidateRefsMustBeLocalDefs(t *testing.T) {
	for _, ref := range []string{
		"https://example.com/x.json", "http://evil/x", "x.json", "#", "#/properties/p", "#/$defs/",
		"file:///etc/passwd", "../x.json", "#/$defs/a/b", "#/$defs/missing",
	} {
		schema := fmt.Sprintf(`{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":%q}},"$defs":{"a":{"type":"string","maxLength":5,"default":""}}}`, ref)
		mustFail(t, wrap(schema), "$ref", "")
	}
	// A good local reference.
	good := `{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/a"}},"$defs":{"a":{"type":"string","maxLength":5,"default":""}}}`
	if p, ds := content.Validate(wrap(good)); p == nil {
		t.Fatalf("local $ref must pass: %s", detailsText(ds))
	}
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/a","maxLength":3}},"$defs":{"a":{"type":"string","maxLength":5,"default":""}}}`),
		"/maxLength", "cannot sit next to $ref")
}

func TestValidateRefCycles(t *testing.T) {
	self := `{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/a"}},"$defs":{"a":{"type":"object","additionalProperties":false,"properties":{"x":{"$ref":"#/$defs/a"}}}}}`
	mustFail(t, wrap(self), "$ref", "cycle")
	mutual := `{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/a"}},"$defs":{
	  "a":{"type":"object","additionalProperties":false,"properties":{"x":{"$ref":"#/$defs/b"}}},
	  "b":{"type":"object","additionalProperties":false,"properties":{"y":{"$ref":"#/$defs/a"}}}}}`
	mustFail(t, wrap(mutual), "$ref", "cycle")
	refToRef := `{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/a"}},"$defs":{
	  "a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}}}`
	mustFail(t, wrap(refToRef), "$ref", "cycle")
	// An unreachable (unused) cycle is still a cycle.
	unused := `{"type":"object","additionalProperties":false,"properties":{},"$defs":{"a":{"type":"object","additionalProperties":false,"properties":{"x":{"$ref":"#/$defs/a"}}}}}`
	mustFail(t, wrap(unused), "$ref", "cycle")
}

// A billion-laughs style schema: 12 definitions each referencing the previous
// one 6 times. It is tiny as written and astronomically large when expanded.
func TestValidateRefAmplificationIsRejectedFast(t *testing.T) {
	var defs []string
	defs = append(defs, `"d0":{"type":"string","maxLength":5,"default":""}`)
	for i := 1; i <= 12; i++ {
		var refs []string
		for j := 0; j < 6; j++ {
			refs = append(refs, fmt.Sprintf(`"r%d":{"$ref":"#/$defs/d%d"}`, j, i-1))
		}
		defs = append(defs, fmt.Sprintf(`"d%d":{"type":"object","additionalProperties":false,"properties":{%s}}`, i, strings.Join(refs, ",")))
	}
	schema := `{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/d12"}},"$defs":{` + strings.Join(defs, ",") + `}}`
	start := time.Now()
	mustFail(t, wrap(schema), "", "after $ref expansion")
	if time.Since(start) > time.Second {
		t.Errorf("amplification check took %s", time.Since(start))
	}
}

func TestValidateExpandedDepthLimit(t *testing.T) {
	var defs []string
	defs = append(defs, `"d0":{"type":"string","maxLength":5,"default":""}`)
	for i := 1; i <= 20; i++ {
		defs = append(defs, fmt.Sprintf(`"d%d":{"type":"object","additionalProperties":false,"properties":{"c":{"$ref":"#/$defs/d%d"}}}`, i, i-1))
	}
	schema := `{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"#/$defs/d20"}},"$defs":{` + strings.Join(defs, ",") + `}}`
	mustFail(t, wrap(schema), "", "after $ref expansion the schema is nested deeper")
}

// ── block unions ──────────────────────────────────────────────────────

func TestValidateBlockUnion(t *testing.T) {
	good := `{"type":"object","additionalProperties":false,"properties":{"sections":{"type":"array","maxItems":10,"default":[],"x-editor":"sections","items":{"oneOf":[
	  {"$ref":"#/$defs/hero"},{"$ref":"#/$defs/cta"}]}}},
	  "$defs":{
	   "hero":{"type":"object","additionalProperties":false,"properties":{"type":{"type":"string","const":"hero"},"title":{"type":"string","maxLength":50,"default":""}}},
	   "cta":{"type":"object","additionalProperties":false,"properties":{"type":{"type":"string","const":"cta"},"text":{"type":"string","maxLength":50,"default":""}}}}}`
	if p, ds := content.Validate(wrap(good)); p == nil {
		t.Fatalf("block union must pass: %s", detailsText(ds))
	}
	noConst := strings.Replace(good, `"const":"cta"`, `"default":"cta"`, 1)
	mustFail(t, wrap(noConst), "/oneOf/1", "properties.type.const")
	dup := strings.Replace(good, `"const":"cta"`, `"const":"hero"`, 1)
	mustFail(t, wrap(dup), "/oneOf/1", "duplicate block type")
	// oneOf outside an array's items.
	mustFail(t, prop(`{"oneOf":[{"type":"string","maxLength":5,"default":""}]}`), "/p/oneOf", "only allowed as the items")
	mustFail(t, prop(`{"type":"array","maxItems":3,"items":{"oneOf":[]}}`), "/oneOf", "non-empty")
	mustFail(t, prop(`{"type":"array","maxItems":3,"items":{"oneOf":[{"type":"string","maxLength":5}],"minItems":1}}`), "/minItems", "cannot sit next to oneOf")
}

// ── step 6: defaults self-consistency ─────────────────────────────────

func TestValidateRejectsDefaultsThatViolateTheSchema(t *testing.T) {
	// default longer than maxLength.
	mustFail(t, prop(`{"type":"string","maxLength":3,"default":"toolong"}`), "/types/home/schema", "rejects its own default")
	// default not in enum.
	mustFail(t, prop(`{"type":"string","maxLength":10,"default":"z","enum":["a","b"]}`), "/types/home/schema", "rejects its own default")
	// wrong type.
	mustFail(t, prop(`{"type":"integer","default":"x"}`), "/types/home/schema", "rejects its own default")
	// no default at all: hydrates to null, which is not a string.
	mustFail(t, prop(`{"type":"string","maxLength":3}`), "/types/home/schema", "rejects its own default")
	// default violates pattern; minLength.
	mustFail(t, prop(`{"type":"string","maxLength":10,"default":"abc","pattern":"^[0-9]+$"}`), "/types/home/schema", "rejects its own default")
	mustFail(t, prop(`{"type":"string","maxLength":10,"minLength":5,"default":"abc"}`), "/types/home/schema", "rejects its own default")
	// array default exceeding maxItems.
	mustFail(t, prop(`{"type":"array","maxItems":1,"default":["a","b"],"items":{"type":"string","maxLength":5,"default":""}}`), "/types/home/schema", "rejects its own default")
	// required, no default.
	mustFail(t, wrap(`{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"integer"}}}`), "/types/home/schema", "rejects its own default")
	// A consistent schema passes.
	if p, ds := content.Validate(prop(`{"type":"integer","default":3,"minimum":1,"maximum":9}`)); p == nil {
		t.Errorf("consistent integer default must pass: %s", detailsText(ds))
	}
}

func TestValidateBadEditorOrderAndCompileErrors(t *testing.T) {
	// min greater than max compiles (no semantic check), a broken pattern does not
	// reach the compiler thanks to the vocab pass. x-editor-order shape is
	// checked by the existing validator.
	mustFail(t, prop(`{"type":"string","maxLength":5,"default":"","x-editor-order":1.5}`), "/p/x-editor-order", "non-negative integer")
}

// ── step 7: declarations ──────────────────────────────────────────────

func withDecl(extra string) []byte {
	return []byte(`{"format":2,"types":{"home":{"title":"H","schema":` + okRoot + `},"global":{"schema":` + okRoot + `}},` + extra + `}`)
}

func TestValidateDeclarations(t *testing.T) {
	if p, ds := content.Validate(withDecl(`"documents":{"home":{"type":"home"},"global":{"type":"global"}},"collections":{"campaign-":{"type":"home","max":20}}`)); p == nil {
		t.Fatalf("good declarations must pass: %s", detailsText(ds))
	}
	mustFail(t, withDecl(`"documents":{"Home":{"type":"home"}}`), "/documents/Home", "document key must match")
	mustFail(t, withDecl(`"documents":{"-x":{"type":"home"}}`), "/documents/-x", "document key must match")
	mustFail(t, withDecl(`"documents":{"`+strings.Repeat("a", 64)+`":{"type":"home"}}`), "/documents/", "document key must match")
	mustFail(t, withDecl(`"documents":{"faq":{"type":"missing"}}`), "/documents/faq/type", "not defined in types")
	mustFail(t, withDecl(`"documents":{"global":{"type":"home"}}`), "/documents/global/type", "global type")
	mustFail(t, withDecl(`"documents":{"home":{"type":"home","x":1}}`), "/documents/home/x", "unknown key")
	mustFail(t, withDecl(`"collections":{"campaign":{"type":"home","max":5}}`), "/collections/campaign", "prefix must match")
	mustFail(t, withDecl(`"collections":{"campaign-":{"type":"home"}}`), "/collections/campaign-/max", "between 1 and 100")
	mustFail(t, withDecl(`"collections":{"campaign-":{"type":"home","max":101}}`), "/collections/campaign-/max", "between 1 and 100")
	mustFail(t, withDecl(`"collections":{"campaign-":{"type":"global","max":5}}`), "/collections/campaign-/type", "non-global")
	mustFail(t, withDecl(`"documents":{"campaign-spring":{"type":"home"}},"collections":{"campaign-":{"type":"home","max":5}}`),
		"/documents/campaign-spring", "inside the collection prefix")
	mustFail(t, withDecl(`"collections":{"a-":{"type":"home","max":5},"a-b-":{"type":"home","max":5}}`), "/collections/", "overlap")
}

// ── budget and misc ───────────────────────────────────────────────────

func TestValidateIsFastOnLargeButLegalSchema(t *testing.T) {
	var props []string
	for i := 0; i < 40; i++ {
		var sub []string
		for j := 0; j < 40; j++ {
			sub = append(sub, fmt.Sprintf(`"f%d":{"type":"string","maxLength":20,"default":"x","pattern":"^[a-z]+$"}`, j))
		}
		props = append(props, fmt.Sprintf(`"o%d":{"type":"object","additionalProperties":false,"properties":{%s}}`, i, strings.Join(sub, ",")))
	}
	schema := `{"type":"object","additionalProperties":false,"properties":{` + strings.Join(props, ",") + `}}`
	start := time.Now()
	if p, ds := content.Validate(wrap(schema)); p == nil {
		t.Fatalf("legal large schema rejected: %s", detailsText(ds))
	}
	if d := time.Since(start); d > content.ValidationBudget {
		t.Errorf("took %s, over the budget", d)
	}
}

func TestValidateReportsAtMostFiftyProblems(t *testing.T) {
	var props []string
	for i := 0; i < 50; i++ {
		props = append(props, fmt.Sprintf(`"f%d":{"type":"string"}`, i))
	}
	_, ds := content.Validate(wrap(`{"type":"object","additionalProperties":false,"properties":{` + strings.Join(props, ",") + `}}`))
	if len(ds) == 0 || len(ds) > 50 {
		t.Errorf("details = %d, want 1..50", len(ds))
	}
}

func TestNormalizeRawIsOrderIndependent(t *testing.T) {
	_, a, err := content.NormalizeRaw([]byte(`{"b":1,"a":[1.0,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, b, _ := content.NormalizeRaw([]byte(`{ "a": [1.0, 2], "b": 1 }`))
	if a != b {
		t.Errorf("digest differs for equal documents")
	}
}
