package cms

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLinkAllowed(t *testing.T) {
	good := []string{"", "https://example.com/a?b=c#d", "http://example.com", "HTTPS://EXAMPLE.COM", "mailto:a@example.com", "tel:+886912345678", "/about", "/", "#section", "/a/b?x=1"}
	bad := []string{
		"javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html;base64,AAAA", "vbscript:x", "//evil.example.com/x", "/\\evil.example.com",
		"about", "ftp://example.com", "https://", "http://", "https://a.com/\nx", "https://a.com/\x00", " https://a.com", "\thttps://a.com",
		"https://" + strings.Repeat("a", MaxLinkLength),
	}
	for _, v := range good {
		if !LinkAllowed(v) {
			t.Errorf("LinkAllowed(%q) = false, want true", v)
		}
	}
	for _, v := range bad {
		if LinkAllowed(v) {
			t.Errorf("LinkAllowed(%q) = true, want false", v)
		}
	}
}

func TestCheckLinkFieldsWalksNestedAndSections(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
	  "cta":{"type":"string","x-editor":"link"},
	  "plain":{"type":"string"},
	  "items":{"type":"array","items":{"type":"object","properties":{"href":{"type":"string","x-editor":"link"}}}}}}`)
	payload := json.RawMessage(`{"cta":"javascript:x","plain":"javascript:not-a-link","items":[{"href":"/ok"},{"href":"data:x"}]}`)
	bad, err := CheckLinkFields(schema, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 2 || bad[0].Pointer == bad[1].Pointer {
		t.Fatalf("want two findings (/cta and /items/1/href), got %+v", bad)
	}
	for _, d := range bad {
		if d.Pointer != "/cta" && d.Pointer != "/items/1/href" {
			t.Errorf("unexpected pointer %q", d.Pointer)
		}
	}
	if bad, _ := CheckLinkFields(schema, json.RawMessage(`{"cta":"/x","plain":"anything","items":[]}`)); len(bad) != 0 {
		t.Errorf("valid payload flagged: %+v", bad)
	}
}
