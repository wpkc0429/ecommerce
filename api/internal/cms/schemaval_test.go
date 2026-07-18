package cms

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Scenario (theme-system/Theme schema validity): 不合法 schema 被拒.
func TestMetaschemaRejectsInvalidSchema(t *testing.T) {
	bad := json.RawMessage(`{"type": "strng"}`)
	err := ValidateSchemaDoc(bad)
	if err == nil {
		t.Fatal("invalid schema must be rejected")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(ve.Details) == 0 {
		t.Fatal("validation error must carry location details")
	}
}

func TestMetaschemaAcceptsValidSchema(t *testing.T) {
	good := json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"title": {"type": "string", "default": "hi", "x-editor": "text"}
		}
	}`)
	if err := ValidateSchemaDoc(good); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}
}

// Scenario (page-management/Payload schema validation): 型別錯誤附定位 —
// banner.images defined as array, payload sends a string → details point at
// /banner/images.
func TestValidatePayloadPointerDetails(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"banner": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"images": {"type": "array", "items": {"type": "string"}, "default": []}
				}
			}
		}
	}`)
	payload := json.RawMessage(`{"banner": {"images": "not-an-array"}}`)

	err := ValidatePayload(schema, payload)
	if err == nil {
		t.Fatal("payload must fail validation")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	found := false
	for _, d := range ve.Details {
		if d.Pointer == "/banner/images" {
			found = true
		}
	}
	if !found {
		t.Fatalf("details must include pointer /banner/images, got %+v", ve.Details)
	}

	if err := ValidatePayload(schema, json.RawMessage(`{"banner": {"images": ["a.png"]}}`)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

// additionalProperties: false rejects undeclared keys at write time.
func TestValidatePayloadRejectsUnknownKeys(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {"a": {"type": "string", "default": ""}}
	}`)
	err := ValidatePayload(schema, json.RawMessage(`{"a": "x", "sneaky": 1}`))
	if err == nil {
		t.Fatal("unknown key must be rejected on write")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatal("wrong error type")
	}
	joined := ""
	for _, d := range ve.Details {
		joined += d.Pointer + " " + d.Message + ";"
	}
	if !strings.Contains(joined, "sneaky") {
		t.Fatalf("details should mention the offending key: %s", joined)
	}
}
