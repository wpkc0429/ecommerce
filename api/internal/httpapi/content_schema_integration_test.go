package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/ent"
	entcs "ksdevworks/ecommerce/api/internal/ent/contentschema"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entpermission "ksdevworks/ecommerce/api/internal/ent/permission"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
	entrole "ksdevworks/ecommerce/api/internal/ent/role"
	entuserpermission "ksdevworks/ecommerce/api/internal/ent/userpermission"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Integration tests of the first slice of headless-content-store: content
// schema registration, versioning, dry run, needs_attention, the content.*
// permission nodes with their page.* aliases, and page creation on a new shop.

type schemaEnv struct {
	client *ent.Client
	router http.Handler
	issuer *auth.TokenIssuer
	cms    *cms.Service
	buf    *bytes.Buffer
	rdb    *redis.Client

	platform                          int
	shopA, shopB                      int
	ownerA, editorA, staffA, ownerB   int
	contentOnly, pageOnly, contentMgr int // custom roles on shop A
}

func newSchemaEnv(t *testing.T) *schemaEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: "root@test.dev", AdminPassword: "root-password-123"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &schemaEnv{client: client, buf: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(e.buf, nil))
	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	dispatcher := events.NewDispatcher(log)
	renderCache := render.NewCache(rdb, log)
	dispatcher.Subscribe((&render.Invalidator{Cache: renderCache, Log: log}).Handle)
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	e.cms = &cms.Service{Client: client, Dispatcher: dispatcher}
	resolver := tenant.NewResolver(client, rdb, log)
	cfg := &config.Config{PreviewTokenTTL: 10 * time.Minute}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:      cfg,
		Log:      log,
		AdminMW:  httpapi.NewAdminMiddleware(issuer),
		TenantMW: httpapi.NewTenantMiddleware(resolver),
		Pages:    &httpapi.PagesHandler{Client: client, Service: e.cms, Issuer: issuer, Cfg: cfg, Authz: authz, Log: log},
		ContentSchema: &httpapi.ContentSchemaHandler{
			Service: &content.SchemaService{Client: client, Dispatcher: dispatcher, Log: log},
			Authz:   authz, Limiter: ratelimit.New(rdb, log), Log: log,
		},
		Render: &httpapi.RenderHandler{
			Resolver: resolver, Assembler: &render.Assembler{Client: client},
			Cache: renderCache, Issuer: issuer, Engine: engine, Log: log,
		},
		ContentDocuments: &httpapi.ContentDocumentsHandler{
			Service: &content.Service{Client: client, Dispatcher: dispatcher, Gate: e.cms, Log: log},
			Issuer:  issuer, Authz: authz, Limiter: ratelimit.New(rdb, log), PreviewTTL: 10 * time.Minute, Log: log,
		},
		ContentPublic: &httpapi.ContentPublicHandler{
			Reader: &content.Reader{Client: client}, Cache: renderCache, Issuer: issuer, Engine: engine,
			MaxAge: 60, Log: log,
		},
		ContentPublicRateLimit: httpapi.NewContentPublicRateLimit(ratelimit.New(rdb, log), 10000, time.Minute),
	})
	e.rdb = rdb

	e.platform = client.User.Query().OnlyX(ctx).ID
	shopA, err := e.cms.CreateShop(ctx, cms.ShopInput{Name: "Shop A"})
	if err != nil {
		t.Fatal(err)
	}
	shopB, err := e.cms.CreateShop(ctx, cms.ShopInput{Name: "Shop B"})
	if err != nil {
		t.Fatal(err)
	}
	e.shopA, e.shopB = shopA.ID, shopB.ID
	testutil.BindTenant(t, client, e.shopA, "schema-a")
	testutil.BindTenant(t, client, e.shopB, "schema-b")

	role := func(name string) int {
		return client.Role.Query().Where(entrole.NameEQ(name), entrole.ScopeEQ("merchant")).OnlyX(ctx).ID
	}
	hash, _ := auth.HashPassword("password-123")
	member := func(email, roleName string, shop int) int {
		u := client.User.Create().SetEmail(email).SetPasswordHash(hash).SaveX(ctx).ID
		client.ShopUser.Create().SetShopID(shop).SetUserID(u).SaveX(ctx)
		client.RoleUser.Create().SetUserID(u).SetRoleID(role(roleName)).SetShopID(shop).SaveX(ctx)
		return u
	}
	e.ownerA = member("owner-a@t.dev", "merchant_owner", e.shopA)
	e.editorA = member("editor-a@t.dev", "editor", e.shopA)
	e.staffA = member("staff-a@t.dev", "staff", e.shopA)
	e.ownerB = member("owner-b@t.dev", "merchant_owner", e.shopB)

	custom := func(email, roleName string, nodes ...string) int {
		r := client.Role.Create().SetName(roleName).SetScope("merchant").SaveX(ctx)
		for _, n := range nodes {
			p := client.Permission.Query().Where(entpermissionName(n)).OnlyX(ctx)
			client.RolePermission.Create().SetRoleID(r.ID).SetPermissionID(p.ID).SaveX(ctx)
		}
		u := client.User.Create().SetEmail(email).SetPasswordHash(hash).SaveX(ctx).ID
		client.ShopUser.Create().SetShopID(e.shopA).SetUserID(u).SaveX(ctx)
		client.RoleUser.Create().SetUserID(u).SetRoleID(r.ID).SetShopID(e.shopA).SaveX(ctx)
		return u
	}
	e.contentOnly = custom("content-only@t.dev", "content_only", "content.view", "content.edit", "content.publish")
	e.pageOnly = custom("page-only@t.dev", "page_only", "page.view", "page.create", "page.edit", "page.publish")
	e.contentMgr = custom("content-mgr@t.dev", "content_mgr", "content.view", "content.manage")
	return e
}

func (e *schemaEnv) path(shop int, rest string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shop, rest)
}

// do sends a request as user (raw body) and returns status and decoded body.
func (e *schemaEnv) do(t *testing.T, user int, method, path string, body []byte) (int, map[string]any, string) {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(user, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed, rec.Body.String()
}

func (e *schemaEnv) doJSON(t *testing.T, user int, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	return e.do(t, user, method, path, raw)
}

func (e *schemaEnv) publicPage(t *testing.T, label, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/render/page?path="+path, nil)
	testutil.SetTenant(req, label)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func (e *schemaEnv) versions(t *testing.T, shop int) int {
	t.Helper()
	return e.client.ContentSchema.Query().Where(entcs.ShopIDEQ(shop)).CountX(context.Background())
}

func starterSchema(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "content-schema", "starter.content.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A tiny schema family for the versioning tests.
func twoTypeSchema(fields ...string) []byte {
	props := make([]string, 0, len(fields))
	for _, f := range fields {
		props = append(props, fmt.Sprintf(`"%s":{"type":"string","maxLength":100,"default":""}`, f))
	}
	obj := `{"type":"object","additionalProperties":false,"properties":{` + strings.Join(props, ",") + `}}`
	return []byte(`{"format":2,"types":{"note":{"title":"Note","schema":` + obj + `},"global":{"title":"Global","schema":{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string","maxLength":50,"default":""}}}}}}`)
}

func TestSchemaRegistrationOnANewShopEndToEnd(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()
	shop := e.shopA
	v := httpapi.NewContractValidator(t)

	// A new shop cannot create a page, and the error says how to fix it.
	code, body, raw := e.doJSON(t, e.ownerA, "POST", e.path(shop, "/pages"), map[string]any{"type_key": "story", "title": "S", "slug": "story"})
	if code != 422 {
		t.Fatalf("create before registration: %d %s", code, raw)
	}
	msg := fmt.Sprint(dig(body, "error", "message"))
	for _, want := range []string{"shop has no content schema", fmt.Sprintf("PUT /api/v1/admin/shops/%d/content/schema", shop), "dry_run=true", "docs/content-schema.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("422 message lacks %q: %s", want, msg)
		}
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "GET", e.path(shop, "/content/schema"), nil); code != 404 || !strings.Contains(raw, "content_schema_not_found") {
		t.Fatalf("get before registration: %d %s", code, raw)
	} else {
		v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/content/schema", "GET", 404, []byte(raw))
	}

	// Dry run first: it reports and writes nothing.
	code, body, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema?dry_run=true"), starterSchema(t))
	if code != 200 || body["dry_run"] != true || body["changed"] != true || body["would_register_version"] != float64(1) {
		t.Fatalf("dry run: %d %s", code, raw)
	}
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/content/schema", "PUT", 200, []byte(raw))
	if n := e.versions(t, shop); n != 0 {
		t.Fatalf("dry run wrote %d versions", n)
	}
	// The auto-created home placeholder does not satisfy the starter's home type.
	docs, _ := dig(body, "report", "documents").([]any)
	if len(docs) == 0 || docs[0].(map[string]any)["key"] != "home" || docs[0].(map[string]any)["status"] != "needs_attention" {
		t.Fatalf("dry-run report: %s", raw)
	}

	// Register.
	code, body, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema"), starterSchema(t))
	if code != 200 || body["version"] != float64(1) || body["changed"] != true || body["dry_run"] != false {
		t.Fatalf("register: %d %s", code, raw)
	}
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/content/schema", "PUT", 200, []byte(raw))
	row := e.client.ContentSchema.Query().Where(entcs.ShopIDEQ(shop)).OnlyX(ctx)
	if row.CreatedByUserID == nil || *row.CreatedByUserID != e.ownerA || row.Source != "registered" {
		t.Errorf("audit columns: created_by=%v source=%s", row.CreatedByUserID, row.Source)
	}
	// The audit log line carries ids and counts, never the schema text.
	if out := e.buf.String(); !strings.Contains(out, "content.schema.register") || strings.Contains(out, "product_info") || strings.Contains(out, "你的商店") {
		t.Errorf("audit log: %s", out)
	}

	// Registering the same schema again is a no-op, even reformatted.
	var reformatted bytes.Buffer
	_ = json.Indent(&reformatted, starterSchema(t), "", "\t")
	code, body, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema"), reformatted.Bytes())
	if code != 200 || body["changed"] != false || body["version"] != float64(1) || e.versions(t, shop) != 1 {
		t.Fatalf("re-register: %d %s", code, raw)
	}

	// Read and list.
	code, body, raw = e.doJSON(t, e.editorA, "GET", e.path(shop, "/content/schema"), nil)
	if code != 200 || body["version"] != float64(1) || body["format"] != float64(2) || dig(body, "schema", "format") != float64(2) {
		t.Fatalf("get: %d %s", code, raw)
	}
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/content/schema", "GET", 200, []byte(raw))
	code, body, raw = e.doJSON(t, e.ownerA, "GET", e.path(shop, "/content/schema/versions"), nil)
	if vs, _ := body["versions"].([]any); code != 200 || len(vs) != 1 {
		t.Fatalf("versions: %d %s", code, raw)
	}
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/content/schema/versions", "GET", 200, []byte(raw))
	if code, _, _ := e.doJSON(t, e.ownerA, "GET", e.path(shop, "/content/schema?version=9"), nil); code != 404 {
		t.Fatalf("missing version: %d", code)
	}
	if code, _, _ := e.doJSON(t, e.ownerA, "GET", e.path(shop, "/content/schema?version=x"), nil); code != 400 {
		t.Fatalf("bad version: %d", code)
	}

	// A format-1 writer (the demo seed) must not replace a registered format 2.
	written, err := contentschema.WriterFor(e.client).Write(ctx, shop, contentschema.Doc{Format: 1, Types: map[string]json.RawMessage{"x": json.RawMessage(`{"type":"object"}`)}, Global: json.RawMessage(`{"type":"object"}`)}, contentschema.SourceRegistered)
	if err != nil || written {
		t.Fatalf("format-1 writer over format 2: written=%v err=%v", written, err)
	}

	// Page types come from the registered schema (global is not a page type).
	code, body, raw = e.doJSON(t, e.ownerA, "GET", e.path(shop, "/page-types"), nil)
	types, _ := body["page_types"].([]any)
	if code != 200 || len(types) != 4 || strings.Contains(raw, `"global"`) {
		t.Fatalf("page-types: %d %s", code, raw)
	}

	// Create, edit, publish a page; the public API serves it hydrated.
	// Registration (slice 2) already created the documents the schema declares,
	// so the declared "story" page exists as an empty draft; creating a second
	// page of that type exercises the create contract.
	code, _, raw = e.doJSON(t, e.ownerA, "POST", e.path(shop, "/pages"), map[string]any{"type_key": "story", "title": "故事", "slug": "story-extra"})
	if code != 201 {
		t.Fatalf("create page: %d %s", code, raw)
	}
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/pages", "POST", 201, []byte(raw))
	pid := e.client.Page.Query().Where(entpage.ShopIDEQ(shop), entpage.SlugEQ("story")).OnlyX(ctx).ID
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", e.path(shop, fmt.Sprintf("/pages/%d", pid)),
		map[string]any{"content": map[string]any{"eyebrow": "ABOUT", "title": "我們的故事"}}); code != 200 {
		t.Fatalf("update page: %d %s", code, raw)
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", e.path(shop, fmt.Sprintf("/pages/%d", pid)),
		map[string]any{"content": map[string]any{"eyebrow": strings.Repeat("x", 101)}}); code != 422 {
		t.Fatalf("over-long field must be 422: %d %s", code, raw)
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "POST", e.path(shop, fmt.Sprintf("/pages/%d/publish", pid)), nil); code != 200 {
		t.Fatalf("publish: %d %s", code, raw)
	}
	rcode, bundle := e.publicPage(t, "schema-a", "/story")
	if rcode != 200 || dig(bundle, "page", "content", "title") != "我們的故事" {
		t.Fatalf("public render: %d %v", rcode, bundle)
	}
	paragraphs, _ := dig(bundle, "page", "content", "paragraphs").([]any)
	if len(paragraphs) != 2 {
		t.Errorf("default paragraphs must be hydrated: %v", paragraphs)
	}
	// An unknown type is still refused.
	if code, _, _ := e.doJSON(t, e.ownerA, "POST", e.path(shop, "/pages"), map[string]any{"type_key": "global", "title": "g", "slug": "g"}); code != 422 {
		t.Fatalf("global is not a page type: %d", code)
	}

	// The home placeholder was flagged at registration and is fixed by saving it.
	home := e.client.Page.Query().Where(entpage.ShopIDEQ(shop), entpage.SlugEQ("home")).OnlyX(ctx)
	if !home.NeedsAttention {
		t.Fatalf("home placeholder must need attention")
	}
	code, body, _ = e.doJSON(t, e.ownerA, "GET", e.path(shop, "/pages"), nil)
	flagged := false
	for _, p := range body["pages"].([]any) {
		if pm := p.(map[string]any); pm["slug"] == "home" && pm["needs_attention"] == true {
			flagged = true
		}
	}
	if code != 200 || !flagged {
		t.Errorf("list must expose needs_attention")
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "POST", e.path(shop, fmt.Sprintf("/pages/%d/publish", home.ID)), nil); code != 422 {
		t.Fatalf("publishing a page that needs attention must be 422: %d %s", code, raw)
	}
	if code, body, raw := e.doJSON(t, e.ownerA, "PUT", e.path(shop, fmt.Sprintf("/pages/%d", home.ID)),
		map[string]any{"content": map[string]any{"hero": map[string]any{"title": "歡迎"}}}); code != 200 || body["needs_attention"] != false {
		t.Fatalf("fixing home: %d %s", code, raw)
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "POST", e.path(shop, fmt.Sprintf("/pages/%d/publish", home.ID)), nil); code != 200 {
		t.Fatalf("publish home: %d %s", code, raw)
	}

	// Another shop is untouched.
	if code, _, _ := e.doJSON(t, e.ownerB, "GET", e.path(e.shopB, "/content/schema"), nil); code != 404 {
		t.Fatalf("shop B must still have no schema: %d", code)
	}
	if code, _, _ := e.doJSON(t, e.ownerB, "POST", e.path(e.shopB, "/pages"), map[string]any{"type_key": "story", "title": "S", "slug": "story"}); code != 422 {
		t.Fatalf("shop B page creation: %d", code)
	}
}

func TestSchemaRegistrationRejectionsAre422AndWriteNothing(t *testing.T) {
	e := newSchemaEnv(t)
	cases := map[string]string{
		"remote ref":    `{"format":2,"types":{"a":{"schema":{"type":"object","additionalProperties":false,"properties":{"p":{"$ref":"https://example.com/x.json"}}}}}}`,
		"no maxLength":  `{"format":2,"types":{"a":{"schema":{"type":"object","additionalProperties":false,"properties":{"p":{"type":"string","default":""}}}}}}`,
		"open object":   `{"format":2,"types":{"a":{"schema":{"type":"object","properties":{}}}}}`,
		"not json":      `{{{`,
		"format 1":      `{"format":1,"types":{"a":{"type":"object"}},"global":{}}`,
		"duplicate key": `{"format":2,"format":2,"types":{}}`,
	}
	for name, body := range cases {
		code, parsed, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema"), []byte(body))
		if code != 422 {
			t.Errorf("%s: %d %s", name, code, raw)
			continue
		}
		details, _ := dig(parsed, "error", "details").([]any)
		if len(details) == 0 || details[0].(map[string]any)["pointer"] == nil {
			t.Errorf("%s: 422 without pointer details: %s", name, raw)
		}
	}
	// Oversize body.
	big := bytes.Repeat([]byte(" "), content.MaxBodyBytes+10)
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema"), big); code != 422 {
		t.Errorf("oversize: %d %s", code, raw)
	}
	if n := e.versions(t, e.shopA); n != 0 {
		t.Errorf("rejected registrations wrote %d versions", n)
	}
	// Dry run validates too.
	if code, _, _ := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema?dry_run=true"), []byte(cases["remote ref"])); code != 422 {
		t.Errorf("dry run of a bad schema: %d", code)
	}
}

func TestSchemaVersioningRemovalsConfirmAndNeedsAttention(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()
	shop := e.shopA

	// v1: note has title + body, global has name.
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema"), twoTypeSchema("title", "body")); code != 200 {
		t.Fatalf("v1: %d %s", code, raw)
	}
	code, body, raw := e.doJSON(t, e.ownerA, "POST", e.path(shop, "/pages"), map[string]any{"type_key": "note", "title": "N", "slug": "n"})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	pid := int(body["id"].(float64))
	pagePath := e.path(shop, fmt.Sprintf("/pages/%d", pid))
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", pagePath, map[string]any{"content": map[string]any{"title": "Hello", "body": "Precious words"}}); code != 200 {
		t.Fatalf("save: %d %s", code, raw)
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "POST", pagePath+"/publish", nil); code != 200 {
		t.Fatalf("publish: %d %s", code, raw)
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", pagePath, map[string]any{"content": map[string]any{"title": "Draft 2", "body": "Draft words"}}); code != 200 {
		t.Fatalf("save draft 2: %d %s", code, raw)
	}

	// v2 drops "body": a dry run reports it, nothing is written.
	v2 := twoTypeSchema("title")
	code, body, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema?dry_run=true"), v2)
	if code != 200 || body["would_register_version"] != float64(2) {
		t.Fatalf("dry run v2: %d %s", code, raw)
	}
	var noteDoc map[string]any
	for _, d := range dig(body, "report", "documents").([]any) {
		if d.(map[string]any)["key"] == "n" {
			noteDoc = d.(map[string]any)
		}
	}
	if noteDoc == nil || fmt.Sprint(noteDoc["removed_paths"]) != "[/body]" || noteDoc["status"] != "needs_attention" || noteDoc["published_invalid"] != true {
		t.Fatalf("dry-run report for the page: %v", noteDoc)
	}
	if e.versions(t, shop) != 1 {
		t.Fatalf("dry run must not write")
	}

	// Without confirm_removals: 409 with the report, nothing written.
	code, body, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema"), v2)
	if code != 409 || dig(body, "error", "code") != "removals_require_confirmation" || !strings.Contains(raw, "/body") {
		t.Fatalf("unconfirmed removal: %d %s", code, raw)
	}
	httpapi.NewContractValidator(t).AssertResponse(t, "/api/v1/admin/shops/{shopID}/content/schema", "PUT", 409, []byte(raw))
	if e.versions(t, shop) != 1 {
		t.Fatalf("a 409 must not write")
	}

	// With confirm: version 2, snapshot keeps the old values, page flagged.
	code, body, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema?confirm_removals=true"), v2)
	if code != 200 || body["version"] != float64(2) {
		t.Fatalf("confirmed: %d %s", code, raw)
	}
	code, body, raw = e.doJSON(t, e.ownerA, "GET", e.path(shop, "/content/schema?version=2&include=snapshot"), nil)
	snap := fmt.Sprint(body["snapshot"])
	if code != 200 || !strings.Contains(snap, "Draft words") || !strings.Contains(snap, "Precious words") || body["has_snapshot"] != true {
		t.Fatalf("snapshot lost the old values: %d %s", code, raw)
	}
	if code, body, _ := e.doJSON(t, e.ownerA, "GET", e.path(shop, "/content/schema?version=2"), nil); code != 200 || body["snapshot"] != nil {
		t.Errorf("the snapshot must only come with include=snapshot")
	}
	// Registration never rewrote the document.
	pg := e.client.Page.GetX(ctx, pid)
	if !strings.Contains(string(pg.ContentJSON), "Draft words") || !pg.NeedsAttention {
		t.Errorf("page after registration: needs_attention=%v content=%s", pg.NeedsAttention, pg.ContentJSON)
	}

	// The published content keeps being served (hydrated under the new schema).
	rcode, bundle := e.publicPage(t, "schema-a", "/n")
	if rcode != 200 || dig(bundle, "page", "content", "title") != "Hello" {
		t.Fatalf("published content must keep being served: %d %v", rcode, bundle)
	}
	if _, has := dig(bundle, "page", "content").(map[string]any)["body"]; has {
		t.Errorf("the removed field must not be served")
	}

	// The draft must be fixed before it can be saved or published.
	if code, _, raw := e.doJSON(t, e.ownerA, "POST", pagePath+"/publish", nil); code != 422 {
		t.Fatalf("publish while invalid: %d %s", code, raw)
	}
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", pagePath, map[string]any{"content": map[string]any{"title": "x", "body": "still old"}}); code != 422 {
		t.Fatalf("save of an invalid draft: %d %s", code, raw)
	}
	// A title-only update does not clear the flag.
	if code, body, _ := e.doJSON(t, e.ownerA, "PUT", pagePath, map[string]any{"title": "Renamed"}); code != 200 || body["needs_attention"] != true {
		t.Fatalf("title update: %d %v", code, body)
	}
	// A valid draft clears it unless the published snapshot is still invalid.
	if code, body, raw := e.doJSON(t, e.ownerA, "PUT", pagePath, map[string]any{"content": map[string]any{"title": "Fixed"}}); code != 200 || body["needs_attention"] != true {
		t.Fatalf("valid draft but invalid published must keep the flag: %d %s", code, raw)
	}
	if code, body, raw := e.doJSON(t, e.ownerA, "POST", pagePath+"/publish", nil); code != 200 || body["needs_attention"] != false {
		t.Fatalf("publish the fixed draft: %d %s", code, raw)
	}

	// v3 removes the whole note type: the page is orphaned, needs confirmation.
	v3 := []byte(`{"format":2,"types":{"other":{"schema":{"type":"object","additionalProperties":false,"properties":{}}}}}`)
	code, _, raw = e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema"), v3)
	if code != 409 || !strings.Contains(raw, "orphaned") {
		t.Fatalf("type removal: %d %s", code, raw)
	}
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(shop, "/content/schema?confirm_removals=true"), v3); code != 200 {
		t.Fatalf("confirmed type removal: %d %s", code, raw)
	}
	_, body, _ = e.doJSON(t, e.ownerA, "GET", e.path(shop, "/pages"), nil)
	incompatible := false
	for _, p := range body["pages"].([]any) {
		if pm := p.(map[string]any); pm["slug"] == "n" && pm["incompatible"] == true {
			incompatible = true
		}
	}
	if !incompatible {
		t.Errorf("an orphaned page must be listed as incompatible")
	}
	if e.versions(t, shop) != 3 {
		t.Errorf("versions = %d, want 3", e.versions(t, shop))
	}
}

func TestSchemaGlobalContentRemovalIsReported(t *testing.T) {
	e := newSchemaEnv(t)
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema"), twoTypeSchema("title")); code != 200 {
		t.Fatalf("v1: %d %s", code, raw)
	}
	e.client.Shop.UpdateOneID(e.shopA).SetContentJSON(json.RawMessage(`{"name":"Shop","tagline":"will be lost"}`)).ExecX(context.Background())
	// The global schema has no "tagline": registering anything now would drop it.
	code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema"), twoTypeSchema("title", "body"))
	if code != 409 {
		t.Fatalf("an unconfirmed global removal must be 409: %d %s", code, raw)
	}
	code, body, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema?dry_run=true"), twoTypeSchema("title", "body", "extra"))
	found := false
	for _, d := range dig(body, "report", "documents").([]any) {
		dm := d.(map[string]any)
		if dm["kind"] == "global" && fmt.Sprint(dm["removed_paths"]) == "[/tagline]" {
			found = true
		}
	}
	if code != 200 || !found {
		t.Fatalf("global content removal must be reported: %d %s", code, raw)
	}
}

func TestContentPermissionsAndAliases(t *testing.T) {
	e := newSchemaEnv(t)
	shop := e.shopA
	schema := twoTypeSchema("title")
	put := func(user int, query string) int {
		code, _, _ := e.do(t, user, "PUT", e.path(shop, "/content/schema"+query), schema)
		return code
	}

	// Registration: owner and platform super_admin only.
	if code := put(e.staffA, "?dry_run=true"); code != 403 {
		t.Errorf("staff dry run: %d", code)
	}
	if code := put(e.editorA, "?dry_run=true"); code != 403 {
		t.Errorf("editor dry run: %d", code)
	}
	if code := put(e.contentOnly, "?dry_run=true"); code != 403 {
		t.Errorf("content.view/edit/publish without manage: %d", code)
	}
	if code := put(e.pageOnly, "?dry_run=true"); code != 403 {
		t.Errorf("page.* roles cannot register: %d", code)
	}
	if code := put(e.ownerB, "?dry_run=true"); code != 403 {
		t.Errorf("another shop's owner: %d", code)
	}
	if code := put(e.contentMgr, "?dry_run=true"); code != 200 {
		t.Errorf("content.manage holder: %d", code)
	}
	if code := put(e.platform, ""); code != 200 {
		t.Errorf("platform super_admin: %d", code)
	}
	if n := e.versions(t, shop); n != 1 {
		t.Fatalf("versions = %d", n)
	}

	// Reads: view roles only.
	for user, want := range map[int]int{e.staffA: 403, e.editorA: 200, e.ownerA: 200, e.contentOnly: 200, e.pageOnly: 403, e.ownerB: 403} {
		if code, _, _ := e.doJSON(t, user, "GET", e.path(shop, "/content/schema"), nil); code != want {
			t.Errorf("GET schema as %d: got %d want %d", user, code, want)
		}
	}
	// Unauthenticated.
	req := httptest.NewRequest("PUT", e.path(shop, "/content/schema"), bytes.NewReader(schema))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}

	// page.* and content.* are equivalent aliases on the compatibility endpoints.
	create := func(user int, slug string) int {
		code, _, _ := e.doJSON(t, user, "POST", e.path(shop, "/pages"), map[string]any{"type_key": "note", "title": slug, "slug": slug})
		return code
	}
	if code := create(e.contentOnly, "by-content"); code != 201 {
		t.Errorf("content.edit alias on POST /pages: %d", code)
	}
	if code := create(e.pageOnly, "by-page"); code != 201 {
		t.Errorf("page.create on POST /pages: %d", code)
	}
	if code := create(e.staffA, "by-staff"); code != 403 {
		t.Errorf("staff on POST /pages: %d", code)
	}
	id := int(e.client.Page.Query().Where(entpage.ShopIDEQ(shop), entpage.SlugEQ("by-content")).OnlyX(context.Background()).ID)
	pp := e.path(shop, fmt.Sprintf("/pages/%d", id))
	if code, _, _ := e.doJSON(t, e.pageOnly, "POST", pp+"/publish", nil); code != 200 {
		t.Errorf("page.publish on publish: %d", code)
	}
	if code, _, _ := e.doJSON(t, e.contentOnly, "POST", pp+"/unpublish", nil); code != 200 {
		t.Errorf("content.publish on unpublish: %d", code)
	}
	// editor: view and edit but not publish (seed copies page.* to content.*).
	if code, _, _ := e.doJSON(t, e.editorA, "POST", pp+"/publish", nil); code != 403 {
		t.Errorf("editor must not publish: %d", code)
	}
	if code, _, _ := e.doJSON(t, e.editorA, "PUT", pp, map[string]any{"title": "edited"}); code != 200 {
		t.Errorf("editor edit: %d", code)
	}
	if code, _, _ := e.doJSON(t, e.staffA, "GET", e.path(shop, "/pages"), nil); code != 403 {
		t.Errorf("staff list pages: %d", code)
	}
}

func TestSchemaRegistrationRateLimits(t *testing.T) {
	e := newSchemaEnv(t)
	schema := twoTypeSchema("title")
	var last int
	for i := 0; i < 12; i++ {
		last, _, _ = e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema?dry_run=true"), schema)
		if i < 10 && last != 200 {
			t.Fatalf("dry run %d: %d", i, last)
		}
	}
	if last != 429 {
		t.Errorf("11th+ dry run in a minute must be 429, got %d", last)
	}
	// Dry runs and registrations use separate buckets; another caller is not
	// affected either.
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema"), schema); code != 200 {
		t.Errorf("registration bucket: %d %s", code, raw)
	}
	if code, _, _ := e.do(t, e.platform, "PUT", e.path(e.shopA, "/content/schema?dry_run=true"), schema); code != 200 {
		t.Errorf("another caller: %d", code)
	}
}

func TestSchemaPreviewTokenHolderWithContentViewOnly(t *testing.T) {
	e := newSchemaEnv(t)
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema"), twoTypeSchema("title")); code != 200 {
		t.Fatalf("register: %d %s", code, raw)
	}
	code, body, raw := e.doJSON(t, e.ownerA, "POST", e.path(e.shopA, "/pages"), map[string]any{"type_key": "note", "title": "N", "slug": "n"})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	pid := int(body["id"].(float64))
	// A content.view-only account (no page.*) may preview.
	viewer := e.client.User.Create().SetEmail("viewer@t.dev").SetPasswordHash("x").SaveX(context.Background()).ID
	r := e.client.Role.Create().SetName("viewer").SetScope("merchant").SaveX(context.Background())
	p := e.client.Permission.Query().Where(entpermissionName("content.view")).OnlyX(context.Background())
	e.client.RolePermission.Create().SetRoleID(r.ID).SetPermissionID(p.ID).SaveX(context.Background())
	e.client.ShopUser.Create().SetShopID(e.shopA).SetUserID(viewer).SaveX(context.Background())
	e.client.RoleUser.Create().SetUserID(viewer).SetRoleID(r.ID).SetShopID(e.shopA).SaveX(context.Background())
	code, body, raw = e.doJSON(t, viewer, "GET", e.path(e.shopA, fmt.Sprintf("/pages/%d/preview-token", pid)), nil)
	if code != 200 {
		t.Fatalf("preview-token with content.view: %d %s", code, raw)
	}
	req := httptest.NewRequest("GET", "/api/v1/render/preview?token="+body["preview_token"].(string), nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("preview with content.view: %d %s", rec.Code, rec.Body.String())
	}
}

func entpermissionName(n string) predicate.Permission { return entpermission.NameEQ(n) }

// The migration copies every page.* grant (custom roles and per-user
// overrides, denials included) to the matching content.* node and creates the
// four catalog rows; content.manage is never copied. The SQL is read from the
// migration file itself (same discipline as the other backfill tests).
func TestMigrationCopiesPageGrantsToContent(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "20261124181226_add_content_schema_registration.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = `"needs_attention" boolean NOT NULL DEFAULT false;`
	idx := strings.Index(string(raw), marker)
	if idx < 0 {
		t.Fatalf("migration no longer contains %q; if the data part moved, this test must follow it", marker)
	}
	dataSQL := string(raw)[idx+len(marker):]

	perm := map[string]int{}
	for _, n := range []string{"page.view", "page.create", "page.edit", "page.delete", "page.publish", "product.view"} {
		perm[n] = client.Permission.Create().SetName(n).SaveX(ctx).ID
	}
	mkRole := func(name string, nodes ...string) int {
		r := client.Role.Create().SetName(name).SetScope("merchant").SaveX(ctx)
		for _, n := range nodes {
			client.RolePermission.Create().SetRoleID(r.ID).SetPermissionID(perm[n]).SaveX(ctx)
		}
		return r.ID
	}
	viewer := mkRole("custom_viewer", "page.view")
	editor := mkRole("custom_editor", "page.view", "page.create", "page.edit", "product.view")
	publisher := mkRole("custom_publisher", "page.publish", "page.delete")
	none := mkRole("custom_none", "product.view")

	shop := client.Shop.Create().SetName("S").SetStatus(1).SaveX(ctx).ID
	u := client.User.Create().SetEmail("u@t.dev").SetPasswordHash("x").SaveX(ctx).ID
	client.UserPermission.Create().SetUserID(u).SetPermissionID(perm["page.edit"]).SetShopID(shop).SetIsGranted(false).SaveX(ctx)
	client.UserPermission.Create().SetUserID(u).SetPermissionID(perm["page.view"]).SetShopID(shop).SetIsGranted(true).SaveX(ctx)

	for i := 0; i < 2; i++ { // idempotent
		if _, err := client.ExecContext(ctx, dataSQL); err != nil {
			t.Fatalf("data migration (run %d): %v", i+1, err)
		}
	}

	nodesOf := func(roleID int) []string {
		names, err := client.Role.GetX(ctx, roleID).QueryPermissions().Select(entpermission.FieldName).Strings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range names {
			if strings.HasPrefix(n, "content.") {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	for name, tc := range map[string]struct {
		role int
		want string
	}{
		"viewer":    {viewer, "[content.view]"},
		"editor":    {editor, "[content.edit content.view]"},
		"publisher": {publisher, "[content.publish]"},
		"none":      {none, "[]"},
	} {
		if got := fmt.Sprint(nodesOf(tc.role)); got != tc.want {
			t.Errorf("%s: content nodes = %s, want %s", name, got, tc.want)
		}
	}
	if n := client.Permission.Query().Where(entpermission.NameEQ("content.manage")).CountX(ctx); n != 1 {
		t.Errorf("content.manage must be in the catalog, count=%d", n)
	}
	for _, r := range []int{viewer, editor, publisher, none} {
		for _, n := range nodesOf(r) {
			if n == "content.manage" {
				t.Errorf("content.manage must never be copied (role %d)", r)
			}
		}
	}
	// Per-user overrides, forced denial included.
	ups := client.UserPermission.Query().Where(entuserpermission.UserIDEQ(u)).AllX(ctx)
	got := map[string]bool{}
	for _, up := range ups {
		name := client.Permission.GetX(ctx, up.PermissionID).Name
		got[fmt.Sprintf("%s=%v", name, up.IsGranted)] = true
	}
	for _, want := range []string{"page.edit=false", "page.view=true", "content.edit=false", "content.view=true"} {
		if !got[want] {
			t.Errorf("user overrides = %v, missing %s", got, want)
		}
	}
}
