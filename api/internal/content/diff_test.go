package content_test

import (
	"encoding/json"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/content"
)

func obj(props string, required string) json.RawMessage {
	if required != "" {
		required = `,"required":[` + required + `]`
	}
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + props + `}` + required + `}`)
}

const (
	strP = `{"type":"string","maxLength":50,"default":""}`
)

func one(d content.DocInput, schema json.RawMessage) content.DocReport {
	r := content.Diff([]content.DocInput{d}, map[string]json.RawMessage{"t": schema}, json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"a":`+strP+`}}`))
	return r.Documents[0]
}

func page(draft, published string) content.DocInput {
	d := content.DocInput{Kind: content.KindPage, Key: "k", TypeKey: "t", Draft: json.RawMessage(draft)}
	if published != "" {
		d.Published = json.RawMessage(published)
	}
	return d
}

func TestDiffUnchangedIsOK(t *testing.T) {
	r := one(page(`{"a":"x"}`, `{"a":"y"}`), obj(`"a":`+strP, ""))
	if r.Status != content.StatusOK || len(r.RemovedPaths) != 0 || len(r.Invalid) != 0 || r.PublishedInvalid {
		t.Errorf("%+v", r)
	}
}

func TestDiffFieldRemoved(t *testing.T) {
	r := one(page(`{"a":"x","gone":"keep me"}`, ""), obj(`"a":`+strP, ""))
	if len(r.RemovedPaths) != 1 || r.RemovedPaths[0] != "/gone" {
		t.Fatalf("removed = %v", r.RemovedPaths)
	}
	// Dropping a key also makes the draft invalid (additionalProperties:false).
	if r.Status != content.StatusNeedsAttention || !r.AtRisk() {
		t.Errorf("status = %s", r.Status)
	}
}

func TestDiffFieldRenamed(t *testing.T) {
	r := one(page(`{"title":"x"}`, ""), obj(`"heading":`+strP, ""))
	if len(r.RemovedPaths) != 1 || r.RemovedPaths[0] != "/title" {
		t.Errorf("removed = %v", r.RemovedPaths)
	}
}

func TestDiffEmptyValuesAreNotLoss(t *testing.T) {
	// The auto-created home placeholder carries only an empty array.
	r := one(page(`{"sections":[],"x":"","y":null,"z":{}}`, ""), obj(`"a":`+strP, ""))
	if len(r.RemovedPaths) != 0 {
		t.Errorf("empty values must not count as removals: %v", r.RemovedPaths)
	}
	if r.Status != content.StatusNeedsAttention {
		t.Errorf("but the draft no longer validates: %s", r.Status)
	}
}

func TestDiffTypeChange(t *testing.T) {
	r := one(page(`{"n":"12"}`, ""), obj(`"n":{"type":"integer","default":0}`, ""))
	if r.Status != content.StatusNeedsAttention || len(r.Invalid) == 0 || r.Invalid[0].Pointer != "/n" {
		t.Errorf("%+v", r)
	}
	if len(r.RemovedPaths) != 0 {
		t.Errorf("a type change drops no key: %v", r.RemovedPaths)
	}
}

func TestDiffEnumNarrowed(t *testing.T) {
	r := one(page(`{"c":"red"}`, ""), obj(`"c":{"type":"string","enum":["blue","green"],"default":"blue"}`, ""))
	if r.Status != content.StatusNeedsAttention {
		t.Errorf("enum narrowing must flag: %+v", r)
	}
}

func TestDiffNewRequiredField(t *testing.T) {
	// Without a default in the stored content the draft is invalid.
	r := one(page(`{}`, ""), obj(`"a":`+strP, `"a"`))
	if r.Status != content.StatusNeedsAttention {
		t.Errorf("missing required must flag: %+v", r)
	}
	// Present value: fine.
	r = one(page(`{"a":"x"}`, ""), obj(`"a":`+strP, `"a"`))
	if r.Status != content.StatusOK {
		t.Errorf("%+v", r)
	}
}

func TestDiffOrphanedType(t *testing.T) {
	types := map[string]json.RawMessage{"t": obj("", "")}
	r := content.Diff([]content.DocInput{{Kind: content.KindPage, Key: "k", TypeKey: "gone", Draft: json.RawMessage(`{"a":"real"}`)}}, types, nil)
	if r.Documents[0].Status != content.StatusOrphaned || !r.HasRemovals() || !r.Documents[0].AtRisk() {
		t.Errorf("an orphaned page with content: %+v", r.Documents[0])
	}
	// The auto-created empty placeholder is orphaned but needs no confirmation.
	r = content.Diff([]content.DocInput{{Kind: content.KindPage, Key: "home", TypeKey: "home", Draft: json.RawMessage(`{"sections":[]}`)}}, types, nil)
	if r.Documents[0].Status != content.StatusOrphaned || r.HasRemovals() || r.Documents[0].AtRisk() {
		t.Errorf("an orphaned empty placeholder: %+v", r.Documents[0])
	}
	// Published content alone counts too.
	r = content.Diff([]content.DocInput{{Kind: content.KindPage, Key: "p", TypeKey: "gone", Draft: json.RawMessage(`{}`), Published: json.RawMessage(`{"x":[1]}`)}}, types, nil)
	if !r.HasRemovals() {
		t.Errorf("published content of an orphan must count")
	}
}

func TestDiffPublishedInvalidKeepsServing(t *testing.T) {
	r := one(page(`{"a":"x"}`, `{"old":"v"}`), obj(`"a":`+strP, ""))
	if !r.PublishedInvalid || r.Status != content.StatusNeedsAttention {
		t.Errorf("%+v", r)
	}
	if len(r.RemovedPaths) != 1 || r.RemovedPaths[0] != "/old" {
		t.Errorf("removed = %v", r.RemovedPaths)
	}
}

func TestDiffGlobalUsesGlobalSchema(t *testing.T) {
	r := content.Diff([]content.DocInput{{Kind: content.KindGlobal, Key: "global", TypeKey: "global", Draft: json.RawMessage(`{"a":"ok","b":"gone"}`)}},
		map[string]json.RawMessage{}, obj(`"a":`+strP, ""))
	d := r.Documents[0]
	if len(d.RemovedPaths) != 1 || d.RemovedPaths[0] != "/b" {
		t.Errorf("%+v", d)
	}
}

func TestRemovedPathsNestedAndSections(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{
	  "hero":{"type":"object","additionalProperties":false,"properties":{"title":` + strP + `}},
	  "list":{"type":"array","maxItems":5,"items":{"type":"object","additionalProperties":false,"properties":{"q":` + strP + `}}},
	  "sections":{"type":"array","maxItems":5,"items":{"oneOf":[{"$ref":"#/$defs/a"}]}}},
	  "$defs":{"a":{"type":"object","additionalProperties":false,"properties":{"type":{"type":"string","const":"a"},"t":` + strP + `}}}}`)
	payload := json.RawMessage(`{"hero":{"title":"x","sub":"lost"},"list":[{"q":"1"},{"q":"2","z":"lost"}],
	  "sections":[{"type":"a","t":"1","extra":"lost"},{"type":"removed","t":"2"},{"type":"a"}]}`)
	got := content.RemovedPaths(schema, payload)
	want := []string{"/hero/sub", "/list/1/z", "/sections/0/extra", "/sections/1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestRemovedPathsToleratesGarbage(t *testing.T) {
	if got := content.RemovedPaths(json.RawMessage(`not json`), json.RawMessage(`{}`)); got != nil {
		t.Errorf("%v", got)
	}
	if got := content.RemovedPaths(obj("", ""), json.RawMessage(`[1,2]`)); len(got) != 0 {
		t.Errorf("%v", got)
	}
	// Self-referencing $ref (only possible in an unvalidated stored schema)
	// must terminate.
	cyc := json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}}}}}`)
	deep := json.RawMessage(`{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"b":1}}}}}}}}}}}`)
	_ = content.RemovedPaths(cyc, deep)
}
