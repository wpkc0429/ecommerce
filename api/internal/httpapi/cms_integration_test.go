package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/page"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// cmsEnv wires the full application graph (mirroring app.wire) on the test
// infra, seeded through the real seed command — the starter theme flows
// through the entire pipeline.
type cmsEnv struct {
	client *ent.Client
	router http.Handler
	issuer *auth.TokenIssuer

	admin  int // platform super admin (from seed)
	shopID int // demo shop bound to demo.localhost
	homeID int // auto-created draft home page
}

func newCMSEnv(t *testing.T) *cmsEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()

	if err := seed.Run(ctx, client, seed.Options{
		AdminEmail:    "root@test.dev",
		AdminPassword: "root-password-123",
		Demo:          true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := &cmsEnv{client: client}
	e.admin = client.User.Query().Where(user.EmailEQ("root@test.dev")).OnlyX(ctx).ID
	home := client.Page.Query().Where(page.SlugEQ("home")).OnlyX(ctx)
	e.shopID = home.ShopID
	e.homeID = home.ID

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	cfg := &config.Config{PreviewTokenTTL: 10 * time.Minute}

	dispatcher := events.NewDispatcher(log)
	resolver := tenant.NewResolver(client, rdb, log)
	dispatcher.Subscribe(func(ctx context.Context, ev events.Event) {
		if m, ok := ev.(events.SiteMappingChanged); ok {
			resolver.InvalidateHosts(ctx, m.Hosts...)
		}
	})
	renderCache := render.NewCache(rdb, log)
	invalidator := &render.Invalidator{Cache: renderCache, Client: client, Log: log}
	dispatcher.Subscribe(invalidator.Handle)

	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	cmsService := &cms.Service{Client: client, Dispatcher: dispatcher}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:     cfg,
		Log:     log,
		AdminMW: httpapi.NewAdminMiddleware(issuer),
		Themes:  &httpapi.ThemesHandler{Client: client, Service: cmsService, Authz: authz, Log: log},
		Pages:   &httpapi.PagesHandler{Client: client, Service: cmsService, Issuer: issuer, Cfg: cfg, Authz: authz, Log: log},
		Render: &httpapi.RenderHandler{
			Resolver:  resolver,
			Assembler: &render.Assembler{Client: client},
			Cache:     renderCache,
			Issuer:    issuer,
			Engine:    engine,
			Log:       log,
		},
	})
	return e
}

func (e *cmsEnv) admin1(t *testing.T, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(e.admin, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed, rec.Body.String()
}

// renderPage requests the public render bundle for demo.localhost.
func (e *cmsEnv) renderPage(t *testing.T, path string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/render/page?path="+path, nil)
	req.Header.Set("X-Site-Domain", "demo.localhost")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed, rec.Header().Get("X-Cache")
}

func (e *cmsEnv) shopPath(parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", e.shopID, parts)
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// Covers 6.6 + 7.1 + 7.2 + spec draft/publish scenarios.
func TestDraftPublishRenderFlow(t *testing.T) {
	e := newCMSEnv(t)

	// 草稿不可公開存取 — auto-created home is a draft.
	if code, _, _ := e.renderPage(t, "/"); code != 404 {
		t.Fatalf("draft home must 404 publicly, got %d", code)
	}

	// Publish home.
	if code, _, raw := e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", e.homeID)), nil); code != 200 {
		t.Fatalf("publish: %d %s", code, raw)
	}

	// Render bundle (design D8 structure) with hydration.
	code, bundle, cacheState := e.renderPage(t, "/")
	if code != 200 || cacheState != "MISS" {
		t.Fatalf("first render: code=%d cache=%s", code, cacheState)
	}
	if dig(bundle, "shop", "theme", "code") != "starter" {
		t.Fatalf("bundle.shop.theme.code wrong: %v", dig(bundle, "shop", "theme", "code"))
	}
	if dig(bundle, "page", "component_key") != "starter/home" {
		t.Fatalf("bundle.page.component_key wrong: %v", dig(bundle, "page", "component_key"))
	}
	// Shop content hydrated: seeded value + schema default both present.
	if dig(bundle, "shop", "content", "header", "site_title") != "Demo Shop" {
		t.Fatal("seeded site_title missing")
	}
	if dig(bundle, "shop", "content", "tokens", "color_primary") != "#2563eb" {
		t.Fatal("token default not hydrated")
	}
	// Section hydration: hero got its cta_text default.
	sections, _ := dig(bundle, "page", "content", "sections").([]any)
	if len(sections) == 0 {
		t.Fatal("sections missing")
	}
	hero := sections[0].(map[string]any)
	if hero["type"] != "hero" || hero["cta_text"] != "了解更多" {
		t.Fatalf("hero not hydrated: %v", hero)
	}

	// Second request: cache HIT.
	if _, _, cacheState := e.renderPage(t, "/"); cacheState != "HIT" {
		t.Fatalf("second render should HIT, got %s", cacheState)
	}

	// 編輯不影響線上 — edit the draft; storefront still serves the snapshot.
	newContent := map[string]any{"sections": []any{
		map[string]any{"type": "hero", "title": "全新標題"},
	}}
	if code, _, raw := e.admin1(t, "PUT", e.shopPath(fmt.Sprintf("/pages/%d", e.homeID)),
		map[string]any{"content": newContent}); code != 200 {
		t.Fatalf("edit draft: %d %s", code, raw)
	}
	_, bundle, _ = e.renderPage(t, "/")
	sections, _ = dig(bundle, "page", "content", "sections").([]any)
	if sections[0].(map[string]any)["title"] == "全新標題" {
		t.Fatal("unpublished edit leaked to the storefront")
	}

	// 發佈生效 — publish deletes the page key; next render is fresh.
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", e.homeID)), nil)
	code, bundle, cacheState = e.renderPage(t, "/")
	if code != 200 || cacheState != "MISS" {
		t.Fatalf("post-publish render: code=%d cache=%s", code, cacheState)
	}
	sections, _ = dig(bundle, "page", "content", "sections").([]any)
	if sections[0].(map[string]any)["title"] != "全新標題" {
		t.Fatal("published content not served")
	}

	// 下架 — unpublish → 404.
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/unpublish", e.homeID)), nil)
	if code, _, _ := e.renderPage(t, "/"); code != 404 {
		t.Fatalf("unpublished page must 404, got %d", code)
	}
}

// Covers 6.5 page CRUD validation rules.
func TestPageCRUDValidation(t *testing.T) {
	e := newCMSEnv(t)

	// 不支援的 type_key → 422.
	code, _, _ := e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "nope", "title": "x", "slug": "x"})
	if code != 422 {
		t.Fatalf("unknown type_key: want 422, got %d", code)
	}
	// 保留字 slug → 422 (api + home).
	for _, slug := range []string{"api", "home", "admin", "_next"} {
		code, _, _ = e.admin1(t, "POST", e.shopPath("/pages"),
			map[string]any{"type_key": "about", "title": "x", "slug": slug})
		if code != 422 {
			t.Fatalf("reserved slug %q: want 422, got %d", slug, code)
		}
	}
	// Invalid charset → 422; uppercase input is normalized instead.
	code, _, _ = e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "x", "slug": "Bad Slug!"})
	if code != 422 {
		t.Fatalf("invalid slug: want 422, got %d", code)
	}
	code, body, _ := e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "關於", "slug": "about-us"})
	if code != 201 || body["slug"] != "about-us" || body["status"].(float64) != 0 {
		t.Fatalf("create page: %d %v", code, body)
	}
	// 同店 slug 重複 → 422.
	code, _, _ = e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "dup", "slug": "about-us"})
	if code != 422 {
		t.Fatalf("dup slug: want 422, got %d", code)
	}
	// Payload violating the page schema → 422 with JSON Pointer details.
	code, body, _ = e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "x", "slug": "broken",
			"content": map[string]any{"sections": "not-an-array"}})
	if code != 422 {
		t.Fatalf("invalid payload: want 422, got %d", code)
	}
	details := dig(body, "error", "details")
	rawDetails, _ := json.Marshal(details)
	if !bytes.Contains(rawDetails, []byte("/sections")) {
		t.Fatalf("422 details must locate /sections: %s", rawDetails)
	}

	// SEO meta round-trips into the render bundle (spec SEO 輸出).
	code, body, _ = e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "about", "title": "SEO頁", "slug": "seo-page",
			"meta": map[string]any{"seo_title": "SEO 標題", "seo_description": "描述"}})
	if code != 201 {
		t.Fatalf("create seo page: %d", code)
	}
	pid := int(body["id"].(float64))
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", pid)), nil)
	rcode, bundle, _ := e.renderPage(t, "/seo-page")
	if rcode != 200 || dig(bundle, "page", "seo", "seo_title") != "SEO 標題" {
		t.Fatalf("seo output: code=%d seo=%v", rcode, dig(bundle, "page", "seo"))
	}
}

// Covers 6.7 shop global content + tenant-wide version bump.
func TestShopContentUpdateFlow(t *testing.T) {
	e := newCMSEnv(t)
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", e.homeID)), nil)
	e.renderPage(t, "/") // prime cache

	// Unknown key → 422 (additionalProperties: false).
	code, _, _ := e.admin1(t, "PUT", e.shopPath("/content"),
		map[string]any{"content": map[string]any{"hacker_field": 1}})
	if code != 422 {
		t.Fatalf("invalid shop content: want 422, got %d", code)
	}

	// Valid update → immediate effect on next render (version bump).
	code, _, raw := e.admin1(t, "PUT", e.shopPath("/content"),
		map[string]any{"content": map[string]any{
			"header": map[string]any{"site_title": "新招牌"},
		}})
	if code != 200 {
		t.Fatalf("update content: %d %s", code, raw)
	}
	rcode, bundle, cacheState := e.renderPage(t, "/")
	if rcode != 200 || cacheState != "MISS" {
		t.Fatalf("post-update render: %d %s", rcode, cacheState)
	}
	if dig(bundle, "shop", "content", "header", "site_title") != "新招牌" {
		t.Fatal("shop content update not reflected")
	}
	// Hydration still fills the untouched parts with defaults.
	if dig(bundle, "shop", "content", "tokens", "color_primary") != "#2563eb" {
		t.Fatal("defaults lost after content update")
	}
}

// Covers 6.3 + 6.4 + theme-system spec scenarios.
func TestThemeManagementAndSwitch(t *testing.T) {
	e := newCMSEnv(t)
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", e.homeID)), nil)

	// Invalid schema on theme creation → 422 (metaschema).
	code, _, _ := e.admin1(t, "POST", "/api/v1/admin/themes",
		map[string]any{"code": "bad", "name": "Bad", "layout_key": "bad/main",
			"config_schema": map[string]any{"type": "strng"}})
	if code != 422 {
		t.Fatalf("invalid config_schema: want 422, got %d", code)
	}

	// Create a minimal theme (home only, no landing_page/about).
	code, body, raw := e.admin1(t, "POST", "/api/v1/admin/themes",
		map[string]any{"code": "minimal", "name": "Minimal", "layout_key": "minimal/main",
			"config_schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"tokens": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"color_primary": map[string]any{"type": "string", "default": "#000000"},
						},
					},
				},
			}})
	if code != 201 {
		t.Fatalf("create theme: %d %s", code, raw)
	}
	minimalID := int(body["id"].(float64))
	code, _, raw = e.admin1(t, "POST", fmt.Sprintf("/api/v1/admin/themes/%d/pages", minimalID),
		map[string]any{"type_key": "home", "component_key": "minimal/home",
			"page_schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"sections": map[string]any{"type": "array", "default": []any{},
						"items": map[string]any{"oneOf": []any{map[string]any{
							"type": "object", "additionalProperties": false,
							"required": []any{"type"},
							"properties": map[string]any{
								"type":  map[string]any{"const": "hero"},
								"title": map[string]any{"type": "string", "default": "极简"},
							},
						}}}},
				},
			}})
	if code != 201 {
		t.Fatalf("create theme page: %d %s", code, raw)
	}
	// 重複 type_key → 422.
	code, _, _ = e.admin1(t, "POST", fmt.Sprintf("/api/v1/admin/themes/%d/pages", minimalID),
		map[string]any{"type_key": "home", "component_key": "minimal/home2"})
	if code != 422 {
		t.Fatalf("dup type_key: want 422, got %d", code)
	}

	// A landing page exists on the shop (published) → precheck must flag it.
	code, body, _ = e.admin1(t, "POST", e.shopPath("/pages"),
		map[string]any{"type_key": "landing_page", "title": "促銷", "slug": "promo"})
	if code != 201 {
		t.Fatalf("create landing page: %d", code)
	}
	promoID := int(body["id"].(float64))
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", promoID)), nil)
	if code, _, _ := e.renderPage(t, "/promo"); code != 200 {
		t.Fatal("promo must render under starter")
	}

	code, body, _ = e.admin1(t, "GET", e.shopPath("/theme/precheck?theme_id="+fmt.Sprint(minimalID)), nil)
	if code != 200 {
		t.Fatalf("precheck: %d", code)
	}
	incompatible, _ := body["incompatible_pages"].([]any)
	found := false
	for _, ip := range incompatible {
		if ip.(map[string]any)["slug"] == "promo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("precheck must list promo (landing_page): %v", incompatible)
	}

	// 套用未開放主題被拒 → 422.
	e.admin1(t, "PUT", fmt.Sprintf("/api/v1/admin/themes/%d", minimalID),
		map[string]any{"is_active": false})
	code, _, _ = e.admin1(t, "POST", e.shopPath("/theme"), map[string]any{"theme_id": minimalID})
	if code != 422 {
		t.Fatalf("inactive theme switch: want 422, got %d", code)
	}
	e.admin1(t, "PUT", fmt.Sprintf("/api/v1/admin/themes/%d", minimalID),
		map[string]any{"is_active": true})

	// Switch → incompatible list, version bump, immediate new component_key.
	code, body, raw = e.admin1(t, "POST", e.shopPath("/theme"), map[string]any{"theme_id": minimalID})
	if code != 200 {
		t.Fatalf("switch: %d %s", code, raw)
	}
	if inc, _ := body["incompatible_pages"].([]any); len(inc) == 0 {
		t.Fatal("switch response must carry the incompatibility list")
	}
	rcode, bundle, cacheState := e.renderPage(t, "/")
	if rcode != 200 || cacheState != "MISS" {
		t.Fatalf("post-switch render: %d %s", rcode, cacheState)
	}
	if dig(bundle, "page", "component_key") != "minimal/home" ||
		dig(bundle, "shop", "theme", "code") != "minimal" {
		t.Fatalf("bundle not on new theme: %v", dig(bundle, "page", "component_key"))
	}
	// 換版後不受支援頁面前台 404 + 後台標示不相容.
	if code, _, _ := e.renderPage(t, "/promo"); code != 404 {
		t.Fatalf("unsupported page must 404 after switch, got %d", code)
	}
	_, body, _ = e.admin1(t, "GET", e.shopPath("/pages"), nil)
	for _, p := range body["pages"].([]any) {
		pm := p.(map[string]any)
		if pm["slug"] == "promo" && pm["incompatible"] != true {
			t.Fatal("promo must be flagged incompatible in the admin list")
		}
	}

	// 主題升級後全商家失效: update minimal's home schema adding a defaulted
	// field → next render carries the new default without any manual action.
	_, themeBody, _ := e.admin1(t, "GET", fmt.Sprintf("/api/v1/admin/themes/%d", minimalID), nil)
	pages := themeBody["pages"].([]any)
	tpID := int(pages[0].(map[string]any)["id"].(float64))
	code, _, raw = e.admin1(t, "PUT", fmt.Sprintf("/api/v1/admin/themes/%d/pages/%d", minimalID, tpID),
		map[string]any{"component_key": "minimal/home",
			"page_schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"promo_banner": map[string]any{"type": "string", "default": "主題升級新欄位"},
					"sections":     map[string]any{"type": "array", "default": []any{}, "items": map[string]any{}},
				},
			}})
	if code != 200 {
		t.Fatalf("update theme page: %d %s", code, raw)
	}
	rcode, bundle, cacheState = e.renderPage(t, "/")
	if rcode != 200 || cacheState != "MISS" {
		t.Fatalf("post-theme-update render: %d %s", rcode, cacheState)
	}
	if dig(bundle, "page", "content", "promo_banner") != "主題升級新欄位" {
		t.Fatalf("theme update default not hydrated: %v", dig(bundle, "page", "content"))
	}
}

// Covers 7.4: preview outputs the working copy without touching cache or the
// public output, and enforces authorization.
func TestPreviewFlow(t *testing.T) {
	e := newCMSEnv(t)
	e.admin1(t, "POST", e.shopPath(fmt.Sprintf("/pages/%d/publish", e.homeID)), nil)
	e.renderPage(t, "/") // prime cache

	// Edit the draft.
	e.admin1(t, "PUT", e.shopPath(fmt.Sprintf("/pages/%d", e.homeID)),
		map[string]any{"content": map[string]any{"sections": []any{
			map[string]any{"type": "hero", "title": "預覽中的草稿"},
		}}})

	// Get a preview token and call the preview endpoint.
	code, body, _ := e.admin1(t, "GET", e.shopPath(fmt.Sprintf("/pages/%d/preview-token", e.homeID)), nil)
	if code != 200 {
		t.Fatalf("preview token: %d", code)
	}
	tok := body["preview_token"].(string)

	req := httptest.NewRequest("GET", "/api/v1/render/preview?token="+tok, nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var bundle map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &bundle)
	sections := dig(bundle, "page", "content", "sections").([]any)
	if sections[0].(map[string]any)["title"] != "預覽中的草稿" {
		t.Fatal("preview must serve the working copy")
	}

	// Public output unaffected and still cached.
	_, pub, cacheState := e.renderPage(t, "/")
	if cacheState != "HIT" {
		t.Fatalf("preview must not touch the public cache, got %s", cacheState)
	}
	pubSections := dig(pub, "page", "content", "sections").([]any)
	if pubSections[0].(map[string]any)["title"] == "預覽中的草稿" {
		t.Fatal("draft leaked into public render")
	}

	// Garbage token → 401; unauthorized user's token → 403.
	req = httptest.NewRequest("GET", "/api/v1/render/preview?token=garbage", nil)
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("garbage preview token: want 401, got %d", rec.Code)
	}
	hash, _ := auth.HashPassword("password-123")
	nobody := e.client.User.Create().SetEmail("nobody@test.dev").SetPasswordHash(hash).SaveX(context.Background())
	tok2, _ := e.issuer.IssuePreview(nobody.ID, e.shopID, "home", time.Minute)
	req = httptest.NewRequest("GET", "/api/v1/render/preview?token="+tok2, nil)
	rec = httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("unauthorized preview: want 403, got %d", rec.Code)
	}
}
