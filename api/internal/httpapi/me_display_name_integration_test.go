package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// PATCH /admin/me sets the caller's own display name (design-history follow-up).
func TestMeDisplayName(t *testing.T) {
	e := newAcctEnv(t)
	tok := e.tokenFor(t, e.adminID)
	code, me, raw := e.do(t, "GET", "/api/v1/admin/me", tok, nil)
	if code != 200 || me["display_name"] != nil {
		t.Fatalf("initial me: %d %s", code, raw)
	}
	code, out, raw := e.do(t, "PATCH", "/api/v1/admin/me", tok, map[string]any{"display_name": "  Support Sam "})
	if code != http.StatusOK || out["display_name"] != "Support Sam" {
		t.Fatalf("set: %d %s", code, raw)
	}
	if _, me, _ = e.do(t, "GET", "/api/v1/admin/me", tok, nil); me["display_name"] != "Support Sam" {
		t.Fatalf("me after set: %v", me)
	}
	for _, bad := range []string{"sam@example.com", "call 0912345678", strings.Repeat("x", 81), "two\nlines"} {
		if code, _, raw := e.do(t, "PATCH", "/api/v1/admin/me", tok, map[string]any{"display_name": bad}); code != http.StatusUnprocessableEntity {
			t.Fatalf("%q accepted: %d %s", bad, code, raw)
		}
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/admin/me", tok, map[string]any{}); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing field: %d", code)
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/admin/me", "", map[string]any{"display_name": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	code, out, raw = e.do(t, "PATCH", "/api/v1/admin/me", tok, map[string]any{"display_name": ""})
	if code != http.StatusOK || out["display_name"] != nil {
		t.Fatalf("clear: %d %s", code, raw)
	}
}
