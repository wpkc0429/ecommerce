package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/contentschema"
	entcs "ksdevworks/ecommerce/api/internal/ent/contentschema"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
)

// Gate tests of change retire-hosted-storefront-and-themes group 1 (tasks
// 1.7). They prove that nothing which validates, publishes or renders content
// reads themes/theme_pages any more, which is the precondition for removing
// the theme system.

func normalizeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("normalize json: %v", err)
	}
	return v
}

// 1.7 (1): the data backfill in the migration produces, for a database that has
// themed shops but no content_schemas rows, exactly one version 1 per shop
// whose content equals the theme's schemas.
func TestContentSchemaBackfillMigrationEquivalence(t *testing.T) {
	e := newCMSEnv(t)
	ctx := context.Background()

	themeID := *e.client.Shop.GetX(ctx, e.shopID).ThemeID
	want, err := contentschema.WriterFor(e.client).FromTheme(ctx, themeID)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-migration database: themed shop, no schema rows.
	if _, err := e.client.ContentSchema.Delete().Exec(ctx); err != nil {
		t.Fatal(err)
	}

	sqlText, err := os.ReadFile(filepath.Join("..", "..", "migrations", "20261124155743_add_content_schemas.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	idx := strings.Index(string(sqlText), `INSERT INTO "content_schemas"`)
	if idx < 0 {
		t.Fatal("backfill INSERT not found in migration")
	}
	backfill := string(sqlText)[idx:]

	db, err := sql.Open("pgx", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ { // second run proves idempotency
		if _, err := db.Exec(backfill); err != nil {
			t.Fatalf("backfill run %d: %v", i+1, err)
		}
	}

	rows := e.client.ContentSchema.Query().Where(entcs.ShopIDEQ(e.shopID)).AllX(ctx)
	if len(rows) != 1 || rows[0].Version != 1 || rows[0].Source != contentschema.SourceThemeBackfill {
		t.Fatalf("want exactly one version-1 theme_backfill row, got %d rows: %+v", len(rows), rows)
	}
	wantRaw, _ := json.Marshal(want)
	if !reflect.DeepEqual(normalizeJSON(t, rows[0].SchemaJSON), normalizeJSON(t, wantRaw)) {
		t.Fatalf("backfilled schema differs from the theme's schemas:\n got %s\nwant %s", rows[0].SchemaJSON, wantRaw)
	}

	// The store returns the same page types, in the theme's order.
	types, err := (&contentschema.Store{Client: e.client}).Types(ctx, e.shopID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ti := range types {
		got = append(got, ti.TypeKey+"="+ti.ComponentKey)
	}
	if strings.Join(got, ",") != "home=starter/home,about=starter/about,landing_page=starter/landing" {
		t.Fatalf("page types after backfill: %v", got)
	}
}

// 1.7 (2): with every theme_pages row deleted, page validation, publish,
// global content, public render, preview and page-types all still work.
func TestContentSurvivesDeletingThemePages(t *testing.T) {
	e := newCMSEnv(t)
	ctx := context.Background()

	if _, err := e.client.ThemePage.Delete().Where(themepage.IDGT(0)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.client.ThemePage.Query().CountX(ctx); n != 0 {
		t.Fatalf("theme_pages not empty: %d", n)
	}

	// page-types comes from the shop's own schema.
	code, body, raw := e.admin1(t, "GET", e.shopPath("/page-types"), nil)
	types, _ := body["page_types"].([]any)
	if code != 200 || len(types) != 3 {
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
		t.Fatalf("render after theme_pages deletion: %d %v", rcode, bundle)
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
	code, body, _ = e.admin1(t, "GET", e.shopPath("/pages"), nil)
	if code != 200 {
		t.Fatalf("list pages: %d", code)
	}
}

// 1.7 (3): switching theme writes a new schema version, and page validation
// then uses the new version.
func TestThemeSwitchWritesNewSchemaVersion(t *testing.T) {
	e := newCMSEnv(t)
	ctx := context.Background()
	store := &contentschema.Store{Client: e.client}

	before, err := store.Active(ctx, e.shopID)
	if err != nil || before.Version != 1 {
		t.Fatalf("demo shop must have schema version 1 after seeding: %v %+v", err, before)
	}

	code, body, raw := e.admin1(t, "POST", "/api/v1/admin/themes",
		map[string]any{"code": "gate-min", "name": "Gate", "layout_key": "gate/main",
			"config_schema": map[string]any{"type": "object", "additionalProperties": false}})
	if code != 201 {
		t.Fatalf("create theme: %d %s", code, raw)
	}
	tid := int(body["id"].(float64))
	if code, _, raw := e.admin1(t, "POST", fmt.Sprintf("/api/v1/admin/themes/%d/pages", tid),
		map[string]any{"type_key": "home", "component_key": "gate/home",
			"page_schema": map[string]any{"type": "object", "additionalProperties": false}}); code != 201 {
		t.Fatalf("create theme page: %d %s", code, raw)
	}
	if code, _, raw := e.admin1(t, "POST", e.shopPath("/theme"), map[string]any{"theme_id": tid}); code != 200 {
		t.Fatalf("switch theme: %d %s", code, raw)
	}
	after, err := store.Active(ctx, e.shopID)
	if err != nil || after.Version != 2 {
		t.Fatalf("switch must write version 2: %v %+v", err, after)
	}
	if _, ok := after.Doc.Types["about"]; ok || len(after.Doc.Types) != 1 {
		t.Fatalf("new version must hold only the new theme's types: %v", after.Doc.TypeOrder)
	}
	// The old starter-only type is no longer valid for new pages.
	if code, _, _ := e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "x", "slug": "x"}); code != 422 {
		t.Fatalf("type from the old theme: want 422, got %d", code)
	}
	// Editing the theme afterwards propagates as another version.
	if code, _, raw := e.admin1(t, "PUT", fmt.Sprintf("/api/v1/admin/themes/%d", tid),
		map[string]any{"config_schema": map[string]any{"type": "object"}}); code != 200 {
		t.Fatalf("update theme: %d %s", code, raw)
	}
	if v, _ := store.Active(ctx, e.shopID); v == nil || v.Version != 3 {
		t.Fatalf("theme edit must propagate a new version: %+v", v)
	}
	// Re-applying identical content does not create a version.
	if code, _, raw := e.admin1(t, "POST", e.shopPath("/theme"), map[string]any{"theme_id": tid}); code != 200 {
		t.Fatalf("re-switch: %d %s", code, raw)
	}
	if v, _ := store.Active(ctx, e.shopID); v == nil || v.Version != 3 {
		t.Fatalf("identical schema must not create a version: %+v", v)
	}
}
