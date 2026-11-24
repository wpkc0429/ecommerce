package httpapi_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/contentschema"
)

// Gate tests of change retire-hosted-storefront-and-themes group 1 (tasks
// 1.7), kept after the removal: they prove that nothing which validates,
// publishes or renders content depends on themes. (The test of the one-time
// backfill SQL of the add_content_schemas migration lived here until the theme
// tables were dropped; the migration test in internal/testutil covers it.)

// 1.7 (2): with no theme anywhere, page validation, publish, global content,
// public render, preview and page-types all work from the shop's own schema.
func TestContentWorksWithoutThemes(t *testing.T) {
	e := newCMSEnv(t)

	// The seeded demo shop has no theme at all: its schema is its own version.
	// page-types comes from the shop's own schema.
	code, body, raw := e.admin1(t, "GET", e.shopPath("/page-types"), nil)
	types, _ := body["page_types"].([]any)
	if code != 200 || len(types) != 3 || strings.Contains(raw, "component_key") {
		t.Fatalf("page-types: %d %s", code, raw)
	}

	// Create + update + publish a page (validation reads the shop schema).
	code, body, raw = e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "關於", "slug": "about-us"})
	if code != 201 {
		t.Fatalf("create page: %d %s", code, raw)
	}
	pid := int(body["id"].(float64))
	if code, _, raw := e.admin1(t, "PUT", e.shopPath(fmt.Sprintf("/pages/%d", pid)),
		map[string]any{"content": map[string]any{"sections": []any{
			map[string]any{"type": "hero", "title": "Hi"},
		}}}); code != 200 {
		t.Fatalf("update page: %d %s", code, raw)
	}
	if code, _, raw := e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", pid)), nil); code != 200 {
		t.Fatalf("publish page: %d %s", code, raw)
	}
	// A type that is not in the schema is still refused.
	if code, _, _ := e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "nope", "title": "x", "slug": "x"}); code != 422 {
		t.Fatalf("unknown type: want 422, got %d", code)
	}
	// Invalid payload still refused.
	if code, _, _ := e.admin1(t, "PUT", e.shopPath(fmt.Sprintf("/pages/%d", pid)),
		map[string]any{"content": map[string]any{"sections": "x"}}); code != 422 {
		t.Fatalf("invalid payload: want 422, got %d", code)
	}

	// Global content read and write.
	if code, _, raw := e.admin1(t, "GET", e.shopPath("/content"), nil); code != 200 || !strings.Contains(raw, "schema") {
		t.Fatalf("get content: %d %s", code, raw)
	}
	if code, _, raw := e.admin1(t, "PUT", e.shopPath("/content"),
		map[string]any{"content": map[string]any{"header": map[string]any{"site_title": "新招牌"}}}); code != 200 {
		t.Fatalf("put content: %d %s", code, raw)
	}
	if code, _, _ := e.admin1(t, "PUT", e.shopPath("/content"),
		map[string]any{"content": map[string]any{"hacker_field": 1}}); code != 422 {
		t.Fatalf("invalid global content: want 422, got %d", code)
	}

	// Public render of the published page, with hydration from the schema.
	rcode, bundle, _ := e.renderPage(t, "/about-us")
	if rcode != 200 || dig(bundle, "shop", "content", "header", "site_title") != "新招牌" {
		t.Fatalf("render without themes: %d %v", rcode, bundle)
	}
	sections, _ := dig(bundle, "page", "content", "sections").([]any)
	if len(sections) == 0 || sections[0].(map[string]any)["cta_text"] != "了解更多" {
		t.Fatalf("hydration lost: %v", sections)
	}

	// Preview of the working copy.
	code, body, raw = e.admin1(t, "GET", e.shopPath(fmt.Sprintf("/pages/%d/preview-token", pid)), nil)
	if code != 200 {
		t.Fatalf("preview token: %d %s", code, raw)
	}
	req := httptest.NewRequest("GET", "/api/v1/render/preview?token="+body["preview_token"].(string), nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}

	// Listing flags nothing as incompatible while the types are in the schema.
	if code, _, _ := e.admin1(t, "GET", e.shopPath("/pages"), nil); code != 200 {
		t.Fatalf("list pages: %d", code)
	}
}

// A page whose type_key is not in the shop's schema is flagged incompatible
// in the list and cannot be validated, but it is not deleted.
func TestPageIncompatibleWhenTypeLeavesSchema(t *testing.T) {
	e := newCMSEnv(t)
	ctx := context.Background()
	store := &contentschema.Store{Client: e.client}

	code, body, raw := e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "landing_page", "title": "促銷", "slug": "promo"})
	if code != 201 {
		t.Fatalf("create landing page: %d %s", code, raw)
	}
	promoID := int(body["id"].(float64))

	// Register a new schema version without landing_page.
	active, err := store.Active(ctx, e.shopID)
	if err != nil {
		t.Fatal(err)
	}
	doc := active.Doc
	delete(doc.Types, "landing_page")
	doc.TypeOrder = []string{"home", "about"}
	if _, err := contentschema.WriterFor(e.client).Write(ctx, e.shopID, doc, contentschema.SourceRegistered); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Active(ctx, e.shopID); v == nil || v.Version != 2 {
		t.Fatalf("new schema must be version 2: %+v", v)
	}
	// Writing identical content again does not create a version.
	if written, err := contentschema.WriterFor(e.client).Write(ctx, e.shopID, doc, contentschema.SourceRegistered); err != nil || written {
		t.Fatalf("identical schema must not create a version: written=%v err=%v", written, err)
	}

	code, body, raw = e.admin1(t, "GET", e.shopPath("/pages"), nil)
	if code != 200 {
		t.Fatalf("list pages: %d %s", code, raw)
	}
	flagged := map[string]bool{}
	for _, p := range body["pages"].([]any) {
		pm := p.(map[string]any)
		flagged[pm["slug"].(string)], _ = pm["incompatible"].(bool)
	}
	if !flagged["promo"] || flagged["home"] {
		t.Fatalf("incompatible flags: %v", flagged)
	}
	if code, _, _ := e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", promoID)), nil); code != 422 {
		t.Fatalf("publishing a page whose type left the schema: want 422, got %d", code)
	}
}
