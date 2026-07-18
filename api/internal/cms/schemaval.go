// Package cms implements the schema/payload-decoupled CMS engine (design D6):
// JSON Schema (draft 2020-12) validation of theme schemas and content
// payloads, plus hydration of payloads with schema defaults.
package cms

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// errPrinter localizes validation messages (English).
var errPrinter = message.NewPrinter(language.English)

// Detail locates one validation problem via JSON Pointer.
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

// CompileSchema compiles raw bytes as a JSON Schema (draft 2020-12).
// A compilation failure means the schema itself is invalid (metaschema
// validation for theme import — spec theme-system/Theme schema validity).
func CompileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, &ValidationError{
			Message: "schema is not valid JSON",
			Details: []Detail{{Pointer: "", Message: err.Error()}},
		}
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("schema.json", doc); err != nil {
		return nil, &ValidationError{
			Message: "schema is not a valid JSON Schema (draft 2020-12)",
			Details: []Detail{{Pointer: "", Message: err.Error()}},
		}
	}
	sch, err := c.Compile("schema.json")
	if err != nil {
		return nil, &ValidationError{
			Message: "schema is not a valid JSON Schema (draft 2020-12)",
			Details: []Detail{{Pointer: "", Message: err.Error()}},
		}
	}
	return sch, nil
}

// ValidateSchemaDoc verifies that raw is a compilable JSON Schema, and that
// any "x-editor-order" admin form field-ordering annotations it carries are
// well-formed (design cms-editor-field-order — same 422 path as metaschema
// validation, so authoring mistakes are caught at theme create/update time
// rather than silently misordering the admin form later).
func ValidateSchemaDoc(raw json.RawMessage) error {
	if _, err := CompileSchema(raw); err != nil {
		return err
	}
	return validateEditorOrder(raw)
}

// ValidatePayload validates a JSONB payload against a theme schema and
// returns a *ValidationError with JSON Pointer details on failure.
func ValidatePayload(schemaRaw, payload json.RawMessage) error {
	sch, err := CompileSchema(schemaRaw)
	if err != nil {
		return fmt.Errorf("cms: stored schema invalid: %w", err)
	}
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
	if err != nil {
		return &ValidationError{
			Message: "payload is not valid JSON",
			Details: []Detail{{Pointer: "", Message: err.Error()}},
		}
	}
	verr := sch.Validate(inst)
	if verr == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return &ValidationError{Message: "payload validation failed", Details: []Detail{{Message: verr.Error()}}}
	}
	return &ValidationError{
		Message: "payload does not conform to the schema",
		Details: collectDetails(ve),
	}
}

// collectDetails flattens the leaf causes of a validation error into
// JSON Pointer + message pairs.
func collectDetails(ve *jsonschema.ValidationError) []Detail {
	var out []Detail
	var walk func(v *jsonschema.ValidationError)
	walk = func(v *jsonschema.ValidationError) {
		if len(v.Causes) == 0 {
			out = append(out, Detail{
				Pointer: pointerOf(v.InstanceLocation),
				Message: v.ErrorKind.LocalizedString(errPrinter),
			})
			return
		}
		for _, c := range v.Causes {
			walk(c)
		}
	}
	walk(ve)
	return out
}

func pointerOf(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	ptr := ""
	for _, s := range segments {
		s = escapePointerSegment(s)
		ptr += "/" + s
	}
	return ptr
}

func escapePointerSegment(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
