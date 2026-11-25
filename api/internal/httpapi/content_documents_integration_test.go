package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/contentstore"
	entdoc "ksdevworks/ecommerce/api/internal/ent/contentdocument"
	entrev "ksdevworks/ecommerce/api/internal/ent/contentrevision"
	entaudit "ksdevworks/ecommerce/api/internal/ent/contentauditlog"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Integration tests of the second slice of headless-content-store: the
// document API, revisions, the public read API and the compatibility layer.

const (
	noteSchema   = `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string","maxLength":100,"default":""},"body":{"type":"string","maxLength":5000,"default":"","x-editor":"richtext"},"link":{"type":"string","maxLength":200,"default":"","x-editor":"link"}}}`
	globalSchema = `{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string","maxLength":50,"default":""}}}`
)

func docsSchema() []byte {
	return []byte(`{"format":2,"types":{"note":{"title":"Note","schema":` + noteSchema + `},"global":{"title":"Global","schema":` + globalSchema + `}},` +
		`"documents":{"banner":{"type":"note"},"global":{"type":"global"}},"collections":{"promo-":{"type":"note","max":2}}}`)
}

// docEnv is a schemaEnv whose shop A has the documents schema registered.
func docEnv(t *testing.T) *schemaEnv {
	t.Helper()
	e := newSchemaEnv(t)
	code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema?confirm_removals=true"), docsSchema())
	if code != 200 {
		t.Fatalf("register docs schema: %d %s", code, raw)
	}
	return e
}

func (e *schemaEnv) doH(t *testing.T, user int, method, path string, body any, h map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(user, nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *schemaEnv) pub(t *testing.T, label, path string, h map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if label != "" {
		testutil.SetTenant(req, label)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func jsonOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

func (e *schemaEnv) docPath(rest string) string { return e.path(e.shopA, "/content/documents"+rest) }

func (e *schemaEnv) saveDoc(t *testing.T, user int, key, ifMatch string, body any) *httptest.ResponseRecorder {
	t.Helper()
	h := map[string]string{"If-Match": ifMatch}
	if ifMatch == "*" {
		h = map[string]string{"If-None-Match": "*"}
	}
	return e.doH(t, user, "PUT", e.docPath("/"+key), body, h)
}

func note(title string) map[string]any {
	return map[string]any{"content": map[string]any{"title": title, "body": "<p>hi</p>", "link": "/about"}}
}

func TestDocumentLifecycleAndPublicRead(t *testing.T) {
	e := docEnv(t)
	ctx := context.Background()

	// Registration created the declared fixed document as an empty draft.
	rec := e.doH(t, e.ownerA, "GET", e.docPath(""), nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"key":"banner"`) || !strings.Contains(rec.Body.String(), `"key":"global"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.doH(t, e.ownerA, "GET", e.docPath("/banner"), nil, nil)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get banner: %d etag=%s %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if m := jsonOf(t, rec); m["schema_node"] == nil || m["type_key"] != "note" || m["declared"] != true {
		t.Fatalf("detail lacks the schema node: %v", m)
	}

	// Optimistic lock: a save needs a precondition.
	if rec := e.doH(t, e.ownerA, "PUT", e.docPath("/banner"), note("x"), nil); rec.Code != 428 {
		t.Fatalf("no precondition: want 428, got %d", rec.Code)
	}
	rec = e.saveDoc(t, e.editorA, "banner", `"1"`, note("First"))
	if rec.Code != 200 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("save: %d etag=%s %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	rec = e.saveDoc(t, e.editorA, "banner", `"1"`, note("Stale"))
	if rec.Code != 412 || !strings.Contains(rec.Body.String(), `"current_rev":2`) {
		t.Fatalf("stale If-Match: want 412 with the current rev, got %d %s", rec.Code, rec.Body.String())
	}
	// A create on a taken key and an update of a missing key both fail.
	if rec := e.saveDoc(t, e.editorA, "banner", "*", note("dup")); rec.Code != 412 {
		t.Fatalf("create on a taken key: want 412, got %d", rec.Code)
	}
	if rec := e.saveDoc(t, e.editorA, "promo-spring", `"1"`, note("x")); rec.Code != 404 {
		t.Fatalf("update of a missing key: want 404, got %d", rec.Code)
	}
	// Keys the schema does not declare cannot be created; collections can, up to max.
	if rec := e.saveDoc(t, e.editorA, "random-key", "*", note("x")); rec.Code != 422 {
		t.Fatalf("undeclared key: want 422, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.saveDoc(t, e.editorA, "promo-spring", "*", note("Spring")); rec.Code != 201 || jsonOf(t, rec)["type_key"] != "note" {
		t.Fatalf("collection create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.saveDoc(t, e.editorA, "promo-summer", "*", note("Summer")); rec.Code != 201 {
		t.Fatalf("collection create 2: %d", rec.Code)
	}
	if rec := e.saveDoc(t, e.editorA, "promo-autumn", "*", note("Autumn")); rec.Code != 422 {
		t.Fatalf("collection max 2: want 422, got %d %s", rec.Code, rec.Body.String())
	}
	// Validation: unknown field, bad link, rich text rewritten, not rejected.
	if rec := e.saveDoc(t, e.editorA, "banner", `"2"`, map[string]any{"content": map[string]any{"nope": 1}}); rec.Code != 422 {
		t.Fatalf("unknown field: want 422, got %d", rec.Code)
	}
	if rec := e.saveDoc(t, e.editorA, "banner", `"2"`, map[string]any{"content": map[string]any{"link": "javascript:alert(1)"}}); rec.Code != 422 {
		t.Fatalf("javascript: link: want 422, got %d %s", rec.Code, rec.Body.String())
	}
	rec = e.saveDoc(t, e.editorA, "banner", `"2"`, map[string]any{"content": map[string]any{"title": "Sanitized", "body": "<p>ok</p><script>alert(1)</script>"}})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "script") {
		t.Fatalf("rich text must be rewritten: %d %s", rec.Code, rec.Body.String())
	}

	// Not published yet: the public API answers 404, byte for byte the same as
	// for a key that does not exist at all.
	nopub := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil)
	nonexist := e.pub(t, "schema-a", "/api/v1/shop/content/no-such-key", nil)
	badKey := e.pub(t, "schema-a", "/api/v1/shop/content/Bad_Key", nil)
	if nopub.Code != 404 || nopub.Body.String() != nonexist.Body.String() || fmt.Sprint(nopub.Header()) != fmt.Sprint(nonexist.Header()) ||
		nopub.Body.String() != badKey.Body.String() {
		t.Fatalf("draft-only and missing must be indistinguishable:\n%d %v %s\n%d %v %s", nopub.Code, nopub.Header(), nopub.Body, nonexist.Code, nonexist.Header(), nonexist.Body)
	}

	// Permissions: an editor edits but cannot publish.
	if rec := e.doH(t, e.editorA, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 403 {
		t.Fatalf("editor publish: want 403, got %d", rec.Code)
	}
	if rec := e.doH(t, e.staffA, "GET", e.docPath(""), nil, nil); rec.Code != 403 {
		t.Fatalf("staff list: want 403, got %d", rec.Code)
	}
	if rec := e.doH(t, e.ownerB, "GET", e.docPath("/banner"), nil, nil); rec.Code != 403 {
		t.Fatalf("other shop's owner: want 403, got %d", rec.Code)
	}
	rec = e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), map[string]any{"expected_rev": 1}, nil)
	if rec.Code != 412 {
		t.Fatalf("publish with a stale expected_rev: want 412, got %d %s", rec.Code, rec.Body.String())
	}
	rec = e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil)
	if rec.Code != 200 || jsonOf(t, rec)["status"] != float64(1) {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	// Public read: hydrated body, validators, cache headers.
	rec = e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil)
	if rec.Code != 200 {
		t.Fatalf("public get: %d %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") != "public, max-age=60, stale-while-revalidate=300" ||
		!strings.Contains(rec.Header().Get("Vary"), "X-Publishable-Key") {
		t.Fatalf("headers: %v", rec.Header())
	}
	m := jsonOf(t, rec)
	c := m["content"].(map[string]any)
	if m["key"] != "banner" || m["type_key"] != "note" || c["title"] != "Sanitized" || c["link"] != "" || m["schema_version"] != float64(1) {
		t.Fatalf("body: %v", m)
	}
	// Conditional requests.
	rec = e.pub(t, "schema-a", "/api/v1/shop/content/banner", map[string]string{"If-None-Match": etag})
	if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
		t.Fatalf("304: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", map[string]string{"If-None-Match": `"stale"`}); rec.Code != 200 {
		t.Fatalf("stale etag: want 200, got %d", rec.Code)
	}

	// A new draft is not visible until it is published; publishing changes the ETag at once.
	rec = e.saveDoc(t, e.editorA, "banner", `"3"`, note("Second"))
	if rec.Code != 200 {
		t.Fatalf("save 2: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil); rec.Header().Get("ETag") != etag {
		t.Fatal("a draft save must not change the public document")
	}
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 200 {
		t.Fatalf("publish 2: %d", rec.Code)
	}
	rec = e.pub(t, "schema-a", "/api/v1/shop/content/banner", map[string]string{"If-None-Match": etag})
	if rec.Code != 200 || rec.Header().Get("ETag") == etag || jsonOf(t, rec)["content"].(map[string]any)["title"] != "Second" {
		t.Fatalf("publish must invalidate the cache at once: %d %v", rec.Code, rec.Header())
	}

	// The list: published documents only, each with the ETag of its own body.
	rec = e.pub(t, "schema-a", "/api/v1/shop/content", nil)
	lm := jsonOf(t, rec)
	docs := lm["documents"].([]any)
	var keys []string
	for _, d := range docs {
		keys = append(keys, d.(map[string]any)["key"].(string))
	}
	// banner and the global document (live since it existed) are published.
	if rec.Code != 200 || fmt.Sprint(keys) != "[banner global]" || lm["schema_version"] != float64(1) {
		t.Fatalf("list: %d %v %s", rec.Code, keys, rec.Body.String())
	}
	if docs[0].(map[string]any)["key"] != "banner" || docs[0].(map[string]any)["etag"] != e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil).Header().Get("ETag") {
		t.Fatal("list etag must equal the document's ETag")
	}
	if _, has := docs[0].(map[string]any)["content"]; has {
		t.Fatal("the plain list must not carry content")
	}
	rec = e.pub(t, "schema-a", "/api/v1/shop/content?include=content", nil)
	if c := jsonOf(t, rec)["documents"].([]any)[0].(map[string]any)["content"]; c == nil {
		t.Fatalf("include=content: %s", rec.Body.String())
	}

	// Cross-shop: shop B has no schema and no documents; A's key is not there.
	if rec := e.pub(t, "schema-b", "/api/v1/shop/content/banner", nil); rec.Code != 404 || rec.Body.String() != nonexist.Body.String() {
		t.Fatalf("cross-shop read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.pub(t, "schema-b", "/api/v1/shop/content", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "banner") {
		t.Fatalf("cross-shop list leaked: %s", rec.Body.String())
	}
	// No key at all: the tenant layer's own 404.
	if rec := e.pub(t, "", "/api/v1/shop/content/banner", nil); rec.Code != 404 {
		t.Fatalf("no publishable key: %d", rec.Code)
	}

	// Unpublish takes it offline again, indistinguishably.
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/banner/unpublish"), nil, nil); rec.Code != 200 {
		t.Fatalf("unpublish: %d", rec.Code)
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil); rec.Code != 404 || rec.Body.String() != nonexist.Body.String() {
		t.Fatalf("after unpublish: %d %s", rec.Code, rec.Body.String())
	}

	// Delete: fixed declared documents cannot be deleted, collection members can.
	if rec := e.doH(t, e.ownerA, "DELETE", e.docPath("/banner"), nil, nil); rec.Code != 409 {
		t.Fatalf("delete declared: want 409, got %d", rec.Code)
	}
	if rec := e.doH(t, e.ownerA, "DELETE", e.docPath("/promo-spring"), nil, nil); rec.Code != 204 {
		t.Fatalf("delete collection doc: %d", rec.Code)
	}
	if n := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("promo-spring")).CountX(ctx); n != 0 {
		t.Fatal("document still there")
	}
	if n := e.client.Page.Query().Where(entpage.ShopIDEQ(e.shopA), entpage.SlugEQ("promo-spring")).CountX(ctx); n != 0 {
		t.Fatal("legacy page row of a deleted document still there")
	}
	// The global document cannot be unpublished or deleted.
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/global/unpublish"), nil, nil); rec.Code != 409 {
		t.Fatalf("unpublish global: %d", rec.Code)
	}
	if rec := e.doH(t, e.ownerA, "DELETE", e.docPath("/global"), nil, nil); rec.Code != 409 {
		t.Fatalf("delete global: %d", rec.Code)
	}
}

func TestDocumentRevisionsRestoreAndRetention(t *testing.T) {
	e := docEnv(t)
	ctx := context.Background()
	rev := 1
	for i := 1; i <= 22; i++ {
		rec := e.saveDoc(t, e.ownerA, "banner", fmt.Sprintf(`"%d"`, rev), note(fmt.Sprintf("v%d", i)))
		if rec.Code != 200 {
			t.Fatalf("save %d: %d %s", i, rec.Code, rec.Body.String())
		}
		rev++
		// The publish limit is 20 a minute per caller: alternate two publishers.
		publisher := []int{e.ownerA, e.contentOnly}[i%2]
		if rec := e.doH(t, publisher, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 200 {
			t.Fatalf("publish %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	doc := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("banner")).OnlyX(ctx)
	if n := e.client.ContentRevision.Query().Where(entrev.DocumentIDEQ(doc.ID), entrev.KindEQ("publish")).CountX(ctx); n != 20 {
		t.Fatalf("retained publish revisions = %d, want 20", n)
	}
	rec := e.doH(t, e.ownerA, "GET", e.docPath("/banner/revisions"), nil, nil)
	revs := jsonOf(t, rec)["revisions"].([]any)
	if rec.Code != 200 || len(revs) != 20 {
		t.Fatalf("revisions list: %d len=%d", rec.Code, len(revs))
	}
	if strings.Contains(rec.Body.String(), "v22") {
		t.Fatal("the list must not carry content")
	}
	newest := int(revs[0].(map[string]any)["rev_no"].(float64))
	oldest := int(revs[len(revs)-1].(map[string]any)["rev_no"].(float64))
	if newest != 22 || oldest != 3 {
		t.Fatalf("rev_no range = %d..%d, want 3..22", oldest, newest)
	}
	if rec := e.doH(t, e.ownerA, "GET", e.docPath("/banner/revisions/2"), nil, nil); rec.Code != 404 {
		t.Fatalf("a pruned revision: want 404, got %d", rec.Code)
	}
	rec = e.doH(t, e.ownerA, "GET", e.docPath("/banner/revisions/5"), nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"v5"`) {
		t.Fatalf("revision 5: %d %s", rec.Code, rec.Body.String())
	}

	// Restore: an editor may (content.edit); it only changes the DRAFT.
	before := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil)
	rec = e.doH(t, e.editorA, "POST", e.docPath("/banner/revisions/5/restore"), nil, map[string]string{"If-Match": `"23"`})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"v5"`) || rec.Header().Get("ETag") != `"24"` {
		t.Fatalf("restore: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	after := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil)
	if after.Body.String() != before.Body.String() || after.Header().Get("ETag") != before.Header().Get("ETag") {
		t.Fatal("restore must not change the published document")
	}
	var published map[string]any
	_ = json.Unmarshal(after.Body.Bytes(), &published)
	if published["content"].(map[string]any)["title"] != "v22" {
		t.Fatalf("published changed: %v", published)
	}
	// Stale If-Match on restore, and an editor still cannot publish the result.
	if rec := e.doH(t, e.editorA, "POST", e.docPath("/banner/revisions/5/restore"), nil, map[string]string{"If-Match": `"23"`}); rec.Code != 412 {
		t.Fatalf("restore with a stale If-Match: %d", rec.Code)
	}
	if rec := e.doH(t, e.editorA, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 403 {
		t.Fatalf("editor publish: %d", rec.Code)
	}
}

func TestDocumentOptimisticLockAllowsExactlyOneWriter(t *testing.T) {
	e := docEnv(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.saveDoc(t, e.ownerA, "banner", `"1"`, note(fmt.Sprintf("writer-%d", i))).Code
		}()
	}
	wg.Wait()
	ok, stale := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 412:
			stale++
		}
	}
	if ok != 1 || stale != 7 {
		t.Fatalf("codes = %v: want exactly one 200 and seven 412", codes)
	}
}

func TestContentPreviewTokens(t *testing.T) {
	e := docEnv(t)
	if rec := e.saveDoc(t, e.ownerA, "banner", `"1"`, note("Draft only")); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	tokOf := func(user int, scope string) string {
		rec := e.doH(t, user, "POST", e.docPath("/banner/preview-token"), map[string]any{"scope": scope}, nil)
		if rec.Code != 200 {
			t.Fatalf("preview-token: %d %s", rec.Code, rec.Body.String())
		}
		return jsonOf(t, rec)["preview_token"].(string)
	}
	tok := tokOf(e.editorA, "document")

	rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": tok})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("preview: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if m := jsonOf(t, rec); m["preview"] != true || m["content"].(map[string]any)["title"] != "Draft only" {
		t.Fatalf("preview body: %v", m)
	}
	// The same draft is NOT public.
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil); rec.Code != 404 {
		t.Fatalf("draft leaked: %d", rec.Code)
	}
	// The token is only read from the header; a query parameter is ignored.
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview?token="+tok, nil); rec.Code != 401 {
		t.Fatalf("query token: want 401, got %d", rec.Code)
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", nil); rec.Code != 401 {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": "garbage"}); rec.Code != 401 {
		t.Fatalf("garbage token: %d", rec.Code)
	}
	// A document-scoped token does not open another document; a shop-scoped one does.
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/global/preview", map[string]string{"X-Preview-Token": tok}); rec.Code != 401 {
		t.Fatalf("token for another key: %d", rec.Code)
	}
	shopTok := tokOf(e.ownerA, "shop")
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/global/preview", map[string]string{"X-Preview-Token": shopTok}); rec.Code != 200 {
		t.Fatalf("shop-scoped token: %d %s", rec.Code, rec.Body.String())
	}
	// Another shop's storefront refuses it.
	if rec := e.pub(t, "schema-b", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": tok}); rec.Code != 401 {
		t.Fatalf("cross-shop token: %d", rec.Code)
	}
	// Other token families cannot be used as preview tokens, and vice versa.
	adminTok, _ := e.issuer.IssueAdmin(e.ownerA, nil)
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": adminTok}); rec.Code != 401 {
		t.Fatalf("admin token as preview token: %d", rec.Code)
	}
	pageTok, _ := e.issuer.IssuePreview(e.ownerA, e.shopA, "home", time.Minute)
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": pageTok}); rec.Code != 401 {
		t.Fatalf("page preview token as content preview token: %d", rec.Code)
	}
	if _, _, err := e.issuer.VerifyContentPreview(adminTok); err == nil {
		t.Fatal("an admin token must not verify as a content preview token")
	}
	// Expired.
	old, _ := e.issuer.IssueContentPreview(e.ownerA, e.shopA, auth.PreviewScopeShop, "", -time.Minute)
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": old}); rec.Code != 401 {
		t.Fatalf("expired token: %d", rec.Code)
	}
	// Staff cannot even get a token; a holder who lost access is refused at use.
	if rec := e.doH(t, e.staffA, "POST", e.docPath("/banner/preview-token"), nil, nil); rec.Code != 403 {
		t.Fatalf("staff token: %d", rec.Code)
	}
	staffTok, _ := e.issuer.IssueContentPreview(e.staffA, e.shopA, auth.PreviewScopeShop, "", time.Minute)
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": staffTok}); rec.Code != 403 {
		t.Fatalf("holder without content.view: want 403, got %d", rec.Code)
	}
}

// The compatibility layer: the old endpoints and the new documents stay equal.
func TestLegacyEndpointsAndDocumentsStayConsistent(t *testing.T) {
	e := docEnv(t)
	ctx := context.Background()

	// A page created through the old endpoint is a document with the same data.
	code, body, raw := e.doJSON(t, e.ownerA, "POST", e.path(e.shopA, "/pages"), map[string]any{
		"type_key": "note", "title": "Legacy", "slug": "legacy-note", "content": map[string]any{"title": "from legacy"},
	})
	if code != 201 {
		t.Fatalf("legacy create: %d %s", code, raw)
	}
	pageID := int(body["id"].(float64))
	d := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("legacy-note")).OnlyX(ctx)
	if d.TypeKey != "note" || d.Title != "Legacy" || d.Status != 0 || d.DraftRev != 1 || !strings.Contains(string(d.DraftJSON), "from legacy") {
		t.Fatalf("document of the legacy page: %+v", d)
	}
	// An edit through the legacy endpoint bumps draft_rev; the new API sees it.
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", e.path(e.shopA, fmt.Sprintf("/pages/%d", pageID)), map[string]any{
		"content": map[string]any{"title": "edited legacy"},
	}); code != 200 {
		t.Fatalf("legacy update: %d %s", code, raw)
	}
	rec := e.doH(t, e.ownerA, "GET", e.docPath("/legacy-note"), nil, nil)
	if rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), "edited legacy") {
		t.Fatalf("legacy edit not visible through the document API: %v %s", rec.Header(), rec.Body.String())
	}
	// Legacy publish -> public document API serves it; revision written.
	if code, _, raw := e.doJSON(t, e.ownerA, "POST", e.path(e.shopA, fmt.Sprintf("/pages/%d/publish", pageID)), nil); code != 200 {
		t.Fatalf("legacy publish: %d %s", code, raw)
	}
	rec = e.pub(t, "schema-a", "/api/v1/shop/content/legacy-note", nil)
	if rec.Code != 200 || jsonOf(t, rec)["content"].(map[string]any)["title"] != "edited legacy" {
		t.Fatalf("public read of a legacy-published page: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.client.ContentRevision.Query().Where(entrev.DocumentIDEQ(d.ID), entrev.KindEQ("publish")).CountX(ctx); n != 1 {
		t.Fatalf("publish revisions = %d, want 1", n)
	}
	// The other direction: publishing through the new API shows on the old
	// render endpoint, byte-identical page content.
	if rec := e.saveDoc(t, e.ownerA, "banner", `"1"`, note("via documents")); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if code, page := e.publicPage(t, "schema-a", "/banner"); code != 200 || dig(page, "page", "content", "title") != "via documents" {
		t.Fatalf("legacy render of a document published through the new API: %d %v", code, page)
	}
	pg := e.client.Page.Query().Where(entpage.ShopIDEQ(e.shopA), entpage.SlugEQ("banner")).OnlyX(ctx)
	bd := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("banner")).OnlyX(ctx)
	if pg.Status != bd.Status || string(pg.ContentJSON) != string(bd.DraftJSON) || string(pg.PublishedJSON) != string(bd.PublishedJSON) {
		t.Fatalf("pages row and document differ:\n%s\n%s", pg.PublishedJSON, bd.PublishedJSON)
	}
	// Unpublish through the old endpoint takes the document offline.
	if code, _, _ := e.doJSON(t, e.ownerA, "POST", e.path(e.shopA, fmt.Sprintf("/pages/%d/unpublish", pageID)), nil); code != 200 {
		t.Fatal("legacy unpublish")
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/legacy-note", nil); rec.Code != 404 {
		t.Fatalf("after legacy unpublish: %d", rec.Code)
	}
	// Legacy delete removes the document and its history.
	if code, _, _ := e.doJSON(t, e.ownerA, "DELETE", e.path(e.shopA, fmt.Sprintf("/pages/%d", pageID)), nil); code != 204 && code != 200 {
		t.Fatalf("legacy delete: %d", code)
	}
	if n := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("legacy-note")).CountX(ctx); n != 0 {
		t.Fatal("document of a deleted page remains")
	}
	if n := e.client.ContentRevision.Query().Where(entrev.DocumentIDEQ(d.ID)).CountX(ctx); n != 0 {
		t.Fatal("revisions of a deleted page remain")
	}
	// Slugs that cannot be content keys, and the reserved key "global".
	for _, slug := range []string{"-bad", "global", strings.Repeat("a", 64)} {
		if code, _, _ := e.doJSON(t, e.ownerA, "POST", e.path(e.shopA, "/pages"), map[string]any{"type_key": "note", "title": "x", "slug": slug}); code != 422 {
			t.Fatalf("slug %q: want 422, got %d", slug, code)
		}
	}

	// Global content: the old PUT /content writes draft + published at once and
	// shows up in the global document; publishing the document writes shops.content_json.
	if code, _, raw := e.doJSON(t, e.ownerA, "PUT", e.path(e.shopA, "/content"), map[string]any{"content": map[string]any{"name": "Legacy Name"}}); code != 200 {
		t.Fatalf("legacy PUT /content: %d %s", code, raw)
	}
	g := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("global")).OnlyX(ctx)
	shop := e.client.Shop.Query().Where(entshop.IDEQ(e.shopA)).OnlyX(ctx)
	if g.Status != 1 || !strings.Contains(string(g.PublishedJSON), "Legacy Name") || string(g.PublishedJSON) != string(shop.ContentJSON) {
		t.Fatalf("global after the legacy PUT: status=%d published=%s shop=%s", g.Status, g.PublishedJSON, shop.ContentJSON)
	}
	rec = e.saveDoc(t, e.ownerA, "global", fmt.Sprintf(`"%d"`, g.DraftRev), map[string]any{"content": map[string]any{"name": "New Name"}})
	if rec.Code != 200 {
		t.Fatalf("save global: %d %s", rec.Code, rec.Body.String())
	}
	shop = e.client.Shop.Query().Where(entshop.IDEQ(e.shopA)).OnlyX(ctx)
	if !strings.Contains(string(shop.ContentJSON), "Legacy Name") {
		t.Fatal("a draft of global must not change shops.content_json")
	}
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/global/publish"), nil, nil); rec.Code != 200 {
		t.Fatalf("publish global: %d %s", rec.Code, rec.Body.String())
	}
	shop = e.client.Shop.Query().Where(entshop.IDEQ(e.shopA)).OnlyX(ctx)
	if !strings.Contains(string(shop.ContentJSON), "New Name") {
		t.Fatalf("shops.content_json must be the projection of the global document: %s", shop.ContentJSON)
	}
}

func TestContentAuditHasNoPII(t *testing.T) {
	e := docEnv(t)
	ctx := context.Background()
	const marker = "PII-MARKER-8472"
	rec := e.saveDoc(t, e.ownerA, "banner", `"1"`, map[string]any{"content": map[string]any{"title": marker}, "title": marker, "meta": map[string]any{"k": marker}})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil)
	e.doH(t, e.ownerA, "POST", e.docPath("/banner/revisions/1/restore"), nil, nil)
	e.doH(t, e.ownerA, "POST", e.docPath("/banner/unpublish"), nil, nil)

	rows := e.client.ContentAuditLog.Query().Where(entaudit.ShopIDEQ(e.shopA)).AllX(ctx)
	actions := map[string]bool{}
	for _, r := range rows {
		actions[r.Action] = true
		if strings.Contains(string(r.Detail), marker) || strings.Contains(r.DocKey, marker) {
			t.Fatalf("audit row carries content: %+v", r)
		}
		if r.ActorUserID == nil && r.Action != "schema.dry_run" {
			t.Fatalf("audit row without an actor: %+v", r)
		}
	}
	for _, want := range []string{"schema.register", "doc.save", "doc.publish", "doc.restore", "doc.unpublish"} {
		if !actions[want] {
			t.Errorf("no audit row for %s (have %v)", want, actions)
		}
	}
	if strings.Contains(e.buf.String(), marker) {
		t.Fatal("the log carries document content")
	}
	if !strings.Contains(e.buf.String(), "doc_key=banner") {
		t.Fatalf("expected structured audit log lines, got:\n%s", e.buf.String())
	}
}

func TestContentSaveRateLimit(t *testing.T) {
	e := docEnv(t)
	last := 0
	for i := 0; i < 62; i++ {
		last = e.doH(t, e.ownerA, "PUT", e.docPath("/banner"), note("x"), nil).Code
	}
	if last != 429 {
		t.Fatalf("62 saves in a minute: want 429 at the end, got %d", last)
	}
	// The limit is per caller: another user is unaffected.
	if rec := e.doH(t, e.editorA, "PUT", e.docPath("/banner"), note("x"), nil); rec.Code != 428 {
		t.Fatalf("another caller: want the normal 428, got %d", rec.Code)
	}
}

func TestContentPublicRateLimitAndCORS(t *testing.T) {
	e := docEnv(t)
	// 429 on the public endpoints, per shop and IP.
	limited := httpapi.NewContentPublicRateLimit(ratelimit.New(e.rdb, nil), 3, time.Minute)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := httpapi.NewClientIPMiddleware(nil)(withShop(e.shopA, limited(ok)))
	var codes []int
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if fmt.Sprint(codes) != "[204 204 204 429 429]" {
		t.Fatalf("public rate limit: %v", codes)
	}

	// CORS: a storefront origin may send X-Preview-Token / If-None-Match and read ETag.
	const origin = "https://shop.example.test"
	cors := httpapi.NewCORSMiddleware([]string{origin}, nil)(ok)
	pre := httptest.NewRequest("OPTIONS", "/api/v1/shop/content/banner", nil)
	pre.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	cors.ServeHTTP(rec, pre)
	allow := rec.Header().Get("Access-Control-Allow-Headers")
	for _, want := range []string{"X-Preview-Token", "If-None-Match", "X-Publishable-Key"} {
		if !strings.Contains(allow, want) {
			t.Errorf("Allow-Headers lacks %s: %q", want, allow)
		}
	}
	get := httptest.NewRequest("GET", "/api/v1/shop/content/banner", nil)
	get.Header.Set("Origin", origin)
	rec = httptest.NewRecorder()
	cors.ServeHTTP(rec, get)
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "ETag") {
		t.Errorf("Expose-Headers lacks ETag: %q", rec.Header().Get("Access-Control-Expose-Headers"))
	}
}

// The ETag is a pure function of the response body.
func TestStoreRetentionOfSchemaSnapshotsAndFlagMirror(t *testing.T) {
	e := docEnv(t)
	ctx := context.Background()
	doc := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("banner")).OnlyX(ctx)
	for v := 10; v < 18; v++ {
		if err := contentstore.AddSchemaSnapshot(ctx, e.client, e.shopA, "banner", v, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.client.ContentRevision.Query().Where(entrev.DocumentIDEQ(doc.ID), entrev.KindEQ("schema_snapshot")).CountX(ctx); n != 5 {
		t.Fatalf("schema snapshots kept = %d, want 5", n)
	}
	// SetFlags mirrors needs_attention to the legacy page.
	if err := contentstore.SetFlags(ctx, e.client, e.shopA, "banner", true, true); err != nil {
		t.Fatal(err)
	}
	if !e.client.Page.Query().Where(entpage.ShopIDEQ(e.shopA), entpage.SlugEQ("banner")).OnlyX(ctx).NeedsAttention {
		t.Fatal("needs_attention not mirrored to pages")
	}
}

// withShop puts a resolved tenant on the request context, as TenantMW does.
func withShop(id int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(tenant.WithShopID(r.Context(), id)))
	})
}

// Real responses validate against the OpenAPI contract (check 3).
func TestOpenAPIContentDocumentResponsesMatchImplementation(t *testing.T) {
	e := docEnv(t)
	v := httpapi.NewContractValidator(t)
	const (
		docs = "/api/v1/admin/shops/{shopID}/content/documents"
		doc  = docs + "/{key}"
	)
	check := func(path, method string, status int, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("%s %s: want %d, got %d %s", method, path, status, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() > 0 {
			v.AssertResponse(t, path, method, status, rec.Body.Bytes())
		}
	}
	check(docs, "GET", 200, e.doH(t, e.ownerA, "GET", e.docPath(""), nil, nil))
	check(doc, "GET", 200, e.doH(t, e.ownerA, "GET", e.docPath("/banner"), nil, nil))
	check(doc, "PUT", 200, e.saveDoc(t, e.ownerA, "banner", `"1"`, note("c")))
	check(doc, "PUT", 201, e.saveDoc(t, e.ownerA, "promo-a", "*", note("p")))
	check(doc, "PUT", 412, e.saveDoc(t, e.ownerA, "banner", `"1"`, note("c")))
	check(doc, "PUT", 428, e.doH(t, e.ownerA, "PUT", e.docPath("/banner"), note("c"), nil))
	check(doc, "PUT", 422, e.saveDoc(t, e.ownerA, "banner", `"2"`, map[string]any{"content": map[string]any{"bad": 1}}))
	check(doc+"/publish", "POST", 200, e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil))
	check(doc+"/unpublish", "POST", 200, e.doH(t, e.ownerA, "POST", e.docPath("/promo-a/unpublish"), nil, nil))
	check(doc+"/revisions", "GET", 200, e.doH(t, e.ownerA, "GET", e.docPath("/banner/revisions"), nil, nil))
	check(doc+"/revisions/{rev}", "GET", 200, e.doH(t, e.ownerA, "GET", e.docPath("/banner/revisions/1"), nil, nil))
	check(doc+"/revisions/{rev}/restore", "POST", 200, e.doH(t, e.ownerA, "POST", e.docPath("/banner/revisions/1/restore"), nil, nil))
	rec := e.doH(t, e.ownerA, "POST", e.docPath("/banner/preview-token"), nil, nil)
	check(doc+"/preview-token", "POST", 200, rec)
	tok := jsonOf(t, rec)["preview_token"].(string)

	pub := func(path, method string, status int, rec *httptest.ResponseRecorder) {
		t.Helper()
		check(path, method, status, rec)
	}
	pub("/api/v1/shop/content", "GET", 200, e.pub(t, "schema-a", "/api/v1/shop/content", nil))
	pub("/api/v1/shop/content", "GET", 200, e.pub(t, "schema-a", "/api/v1/shop/content?include=content", nil))
	pub("/api/v1/shop/content/{key}", "GET", 200, e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil))
	pub("/api/v1/shop/content/{key}", "GET", 404, e.pub(t, "schema-a", "/api/v1/shop/content/promo-a", nil))
	pub("/api/v1/shop/content/{key}/preview", "GET", 200, e.pub(t, "schema-a", "/api/v1/shop/content/banner/preview", map[string]string{"X-Preview-Token": tok}))
}
