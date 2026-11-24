package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/mediaupload"
	"ksdevworks/ecommerce/api/internal/ent/productimage"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/media"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// mediaEnv exercises change product-images-r2 against media.FakeStore (design
// D2): the full upload → attach → public read flow with no MinIO needed.
type mediaEnv struct {
	t      *testing.T
	client *ent.Client
	router http.Handler
	issuer *auth.TokenIssuer
	store  *media.FakeStore
	svc    *media.Service

	shopA, shopB int
	ownerA       int // product.view/edit + page.edit in shop A
	viewerA      int // product.view only in shop A
	ownerB       int
	productA     int
	domainA      string
	baseURL      string
}

func mediaCfg(base string) config.MediaConfig {
	return config.MediaConfig{
		AccountID: "acct", AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b",
		PublicBaseURL: base, Endpoint: "http://unused.test",
		MaxBytes: 15 << 20, MaxPixels: 40_000_000, ProcessConcurrency: 2,
		WebPQuality: 80, UploadURLTTL: 10 * time.Minute,
	}
}

func newMediaEnv(t *testing.T, enabled bool) *mediaEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &mediaEnv{t: t, client: client, domainA: "media-a.test", baseURL: "https://img.example.test"}

	perms := []string{"product.view", "product.create", "product.edit", "product.delete", "page.edit"}
	permIDs := map[string]int{}
	for _, p := range perms {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	owner := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range perms {
		client.RolePermission.Create().SetRoleID(owner.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	viewer := client.Role.Create().SetName("viewer").SetScope("merchant").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(viewer.ID).SetPermissionID(permIDs["product.view"]).SaveX(ctx)

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID
	site := client.Site.Create().SetDomain(e.domainA).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(site.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	mk := func(email string, shop int, role *ent.Role) int {
		u := client.User.Create().SetEmail(email).SetPasswordHash(hash).SaveX(ctx).ID
		client.ShopUser.Create().SetShopID(shop).SetUserID(u).SaveX(ctx)
		client.RoleUser.Create().SetUserID(u).SetRoleID(role.ID).SetShopID(shop).SaveX(ctx)
		return u
	}
	e.ownerA = mk("owner-a@media.dev", e.shopA, owner)
	e.viewerA = mk("viewer-a@media.dev", e.shopA, viewer)
	e.ownerB = mk("owner-b@media.dev", e.shopB, owner)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	resolver := tenant.NewResolver(client, rdb, log)

	e.store = media.NewFakeStore()
	t.Cleanup(e.store.Close)
	cfg := config.MediaConfig{}
	var store media.ObjectStore
	if enabled {
		cfg = mediaCfg(e.baseURL)
		store = e.store
	}
	e.svc = media.NewService(client, cfg, store, log)

	catalogService := &catalog.Service{Client: client}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:        &config.Config{},
		Log:        log,
		AdminMW:    httpapi.NewAdminMiddleware(issuer),
		TenantMW:   httpapi.NewTenantMiddleware(resolver),
		Roles:      &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: log},
		Products:   &httpapi.ProductsHandler{Client: client, Service: catalogService, Media: e.svc, Authz: authz, Log: log},
		Media:      &httpapi.MediaHandler{Client: client, Service: e.svc, Authz: authz, Log: log},
		Categories: &httpapi.CategoriesHandler{Client: client, Service: catalogService, Authz: authz, Log: log},
	})

	p := client.Product.Create().SetShopID(e.shopA).SetTitle("Roses").SetSlug("roses").SetStatus(1).SaveX(ctx)
	client.ProductSKU.Create().SetShopID(e.shopA).SetProductID(p.ID).SetSkuCode("R-1").SetPriceAmount(100).SaveX(ctx)
	e.productA = p.ID
	return e
}

func (e *mediaEnv) call(userID int, method, path string, body any) (int, map[string]any, string) {
	e.t.Helper()
	tok, err := e.issuer.IssueAdmin(userID, nil)
	if err != nil {
		e.t.Fatal(err)
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

func (e *mediaEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

func (e *mediaEnv) imagesPath(parts string) string {
	return e.shopPath(e.shopA, fmt.Sprintf("/products/%d/images%s", e.productA, parts))
}

func testJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 120, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 85}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// presign asks for an upload intent and PUTs data to the returned URL exactly
// as a browser would (the returned headers verbatim). It returns the upload id.
func (e *mediaEnv) upload(userID, shopID int, purpose, contentType string, data []byte) string {
	e.t.Helper()
	code, body, raw := e.call(userID, "POST", e.shopPath(shopID, "/media/uploads"), map[string]any{
		"content_type": contentType, "size_bytes": len(data), "purpose": purpose,
	})
	if code != http.StatusCreated {
		e.t.Fatalf("create upload: %d %s", code, raw)
	}
	req, _ := http.NewRequest(http.MethodPut, body["upload_url"].(string), bytes.NewReader(data))
	for k, v := range body["headers"].(map[string]any) {
		req.Header.Set(k, v.(string))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		e.t.Fatalf("PUT to presigned URL: %d %s", resp.StatusCode, b)
	}
	return body["upload_id"].(string)
}

func (e *mediaEnv) attach(uploadID, alt string) (int, map[string]any, string) {
	e.t.Helper()
	return e.call(e.ownerA, "POST", e.imagesPath(""), map[string]any{"upload_id": uploadID, "alt": alt})
}

func (e *mediaEnv) addImage(w, h int, alt string) map[string]any {
	e.t.Helper()
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(w, h))
	code, body, raw := e.attach(up, alt)
	if code != http.StatusCreated {
		e.t.Fatalf("attach: %d %s", code, raw)
	}
	return body
}

func (e *mediaEnv) imageCount() int {
	return e.client.ProductImage.Query().CountX(context.Background())
}

// ── presign ──────────────────────────────────────────────────────────

func TestMediaCreateUpload(t *testing.T) {
	e := newMediaEnv(t, true)
	code, body, raw := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/uploads"), map[string]any{
		"content_type": "image/jpeg", "size_bytes": 204800, "purpose": "product_image",
	})
	if code != http.StatusCreated {
		t.Fatalf("want 201: %d %s", code, raw)
	}
	for _, k := range []string{"upload_id", "upload_url", "headers", "expires_at"} {
		if body[k] == nil {
			t.Errorf("response missing %s: %s", k, raw)
		}
	}
	if body["headers"].(map[string]any)["Content-Type"] != "image/jpeg" {
		t.Errorf("headers = %v", body["headers"])
	}
	// The intent is keyed under incoming/{shop}/ by the API itself.
	up := e.client.MediaUpload.Query().OnlyX(context.Background())
	if !strings.HasPrefix(up.ObjectKey, fmt.Sprintf("incoming/%d/", e.shopA)) || up.ConsumedAt != nil {
		t.Errorf("intent = %+v", up)
	}
}

func TestMediaPresignedPutEnforcesSignedHeaders(t *testing.T) {
	e := newMediaEnv(t, true)
	data := testJPEG(40, 40)
	_, body, _ := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/uploads"), map[string]any{
		"content_type": "image/jpeg", "size_bytes": len(data), "purpose": "product_image",
	})
	// A different Content-Type must be refused by the "signature".
	req, _ := http.NewRequest(http.MethodPut, body["upload_url"].(string), bytes.NewReader(data))
	req.Header.Set("Content-Type", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT with other Content-Type: %d, want 403", resp.StatusCode)
	}
}

func TestMediaCreateUploadRejections(t *testing.T) {
	e := newMediaEnv(t, true)
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"svg", map[string]any{"content_type": "image/svg+xml", "size_bytes": 10, "purpose": "product_image"}, "content_type"},
		{"gif", map[string]any{"content_type": "image/gif", "size_bytes": 10, "purpose": "product_image"}, "content_type"},
		{"heic", map[string]any{"content_type": "image/heic", "size_bytes": 10, "purpose": "product_image"}, "convert"},
		{"heif", map[string]any{"content_type": "image/heif", "size_bytes": 10, "purpose": "product_image"}, "convert"},
		{"too big", map[string]any{"content_type": "image/jpeg", "size_bytes": 15<<20 + 1, "purpose": "product_image"}, "limit"},
		{"zero", map[string]any{"content_type": "image/jpeg", "size_bytes": 0, "purpose": "product_image"}, "positive"},
		{"purpose", map[string]any{"content_type": "image/jpeg", "size_bytes": 10, "purpose": "avatar"}, "purpose"},
	}
	for _, c := range cases {
		code, _, raw := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/uploads"), c.body)
		if code != http.StatusUnprocessableEntity || !strings.Contains(raw, c.want) {
			t.Errorf("%s: %d %s (want 422 mentioning %q)", c.name, code, raw, c.want)
		}
	}
	if n := e.client.MediaUpload.Query().CountX(context.Background()); n != 0 {
		t.Errorf("rejected requests created %d upload intents", n)
	}
}

func TestMediaUploadPermissions(t *testing.T) {
	e := newMediaEnv(t, true)
	ok := map[string]any{"content_type": "image/jpeg", "size_bytes": 10, "purpose": "product_image"}
	if code, _, _ := e.call(e.viewerA, "POST", e.shopPath(e.shopA, "/media/uploads"), ok); code != http.StatusForbidden {
		t.Errorf("viewer product_image: %d, want 403", code)
	}
	// Cross-shop: owner of shop B on shop A's path.
	if code, _, _ := e.call(e.ownerB, "POST", e.shopPath(e.shopA, "/media/uploads"), ok); code != http.StatusForbidden {
		t.Errorf("cross-shop: %d, want 403", code)
	}
	// content_image needs page.edit: the viewer has neither, the owner has both.
	ci := map[string]any{"content_type": "image/jpeg", "size_bytes": 10, "purpose": "content_image"}
	if code, _, _ := e.call(e.viewerA, "POST", e.shopPath(e.shopA, "/media/uploads"), ci); code != http.StatusForbidden {
		t.Errorf("viewer content_image: %d, want 403", code)
	}
	if code, _, raw := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/uploads"), ci); code != http.StatusCreated {
		t.Errorf("owner content_image: %d %s", code, raw)
	}
	// Unauthenticated.
	req := httptest.NewRequest("POST", e.shopPath(e.shopA, "/media/uploads"), strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want 401", rec.Code)
	}
}

// ── attach ───────────────────────────────────────────────────────────

func TestMediaAttachConvertsToTwoWebPVariants(t *testing.T) {
	e := newMediaEnv(t, true)
	img := e.addImage(3000, 2000, "A rose")

	if img["width"].(float64) != 1600 || img["height"].(float64) != 1067 {
		t.Errorf("size = %vx%v, want 1600x1067", img["width"], img["height"])
	}
	if img["alt"] != "A rose" || img["position"].(float64) != 0 {
		t.Errorf("alt/position: %v", img)
	}
	urls := img["urls"].(map[string]any)
	for _, v := range []string{"thumb", "large"} {
		u := urls[v].(string)
		if !strings.HasPrefix(u, e.baseURL+"/shops/") || !strings.HasSuffix(u, "/"+v+".webp") {
			t.Errorf("%s url = %s", v, u)
		}
		key := strings.TrimPrefix(u, e.baseURL+"/")
		body, ct, cc, ok := e.store.Object(key)
		if !ok || ct != "image/webp" || len(body) < 12 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WEBP" {
			t.Errorf("%s object missing or not WebP (ok=%v ct=%q)", v, ok, ct)
		}
		if !strings.Contains(cc, "immutable") {
			t.Errorf("%s Cache-Control = %q, want immutable", v, cc)
		}
	}
	if left := e.store.Keys("incoming/"); len(left) != 0 {
		t.Errorf("original not deleted: %v", left)
	}
	// The database keeps a KEY, never a URL (design D5).
	row := e.client.ProductImage.Query().OnlyX(context.Background())
	if strings.Contains(row.ImageKey, "://") || !strings.HasPrefix(row.ImageKey, fmt.Sprintf("shops/%d/products/%d/", e.shopA, e.productA)) {
		t.Errorf("image_key = %q", row.ImageKey)
	}
}

func TestMediaAttachSmallImageNotUpscaled(t *testing.T) {
	e := newMediaEnv(t, true)
	img := e.addImage(300, 200, "")
	if img["width"].(float64) != 300 || img["height"].(float64) != 200 {
		t.Errorf("size = %vx%v, want 300x200", img["width"], img["height"])
	}
}

func TestMediaAttachReuseConflict(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(100, 100))
	if code, _, raw := e.attach(up, ""); code != http.StatusCreated {
		t.Fatalf("first: %d %s", code, raw)
	}
	if code, _, _ := e.attach(up, ""); code != http.StatusConflict {
		t.Errorf("second attach: %d, want 409", code)
	}
	if e.imageCount() != 1 {
		t.Errorf("images = %d, want 1", e.imageCount())
	}
}

func TestMediaAttachOtherShopsUploadIs422(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerB, e.shopB, "product_image", "image/jpeg", testJPEG(100, 100))
	code, _, _ := e.attach(up, "")
	if code != http.StatusUnprocessableEntity {
		t.Errorf("foreign upload_id: %d, want 422", code)
	}
	// The other shop's original must not even have been read, let alone deleted.
	if len(e.store.Keys("incoming/")) != 1 {
		t.Errorf("foreign original touched: %v", e.store.Keys(""))
	}
}

func TestMediaAttachExpiredIs422(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(100, 100))
	id, _ := uuid.Parse(up)
	e.client.MediaUpload.UpdateOneID(id).SetExpiresAt(time.Now().Add(-time.Minute)).ExecX(context.Background())
	if code, _, _ := e.attach(up, ""); code != http.StatusUnprocessableEntity {
		t.Errorf("expired: %d, want 422", code)
	}
}

func TestMediaAttachWrongPurposeIs422(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerA, e.shopA, "content_image", "image/jpeg", testJPEG(100, 100))
	if code, _, _ := e.attach(up, ""); code != http.StatusUnprocessableEntity {
		t.Errorf("content_image used as product image: %d, want 422", code)
	}
}

func TestMediaAttachNotYetUploadedIs422(t *testing.T) {
	e := newMediaEnv(t, true)
	_, body, _ := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/uploads"), map[string]any{
		"content_type": "image/jpeg", "size_bytes": 100, "purpose": "product_image",
	})
	if code, _, _ := e.attach(body["upload_id"].(string), ""); code != http.StatusUnprocessableEntity {
		t.Errorf("no object PUT yet: %d, want 422", code)
	}
}

func TestMediaAttachContentMismatchIs422AndRetryable(t *testing.T) {
	e := newMediaEnv(t, true)
	html := []byte("<html><script>alert(1)</script></html>")
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", html) // claims JPEG
	code, _, raw := e.attach(up, "")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("html as jpeg: %d %s", code, raw)
	}
	if e.imageCount() != 0 {
		t.Error("an image row was created for non-image content")
	}
	// Failure releases the intent (it can be retried without a new PUT).
	id, _ := uuid.Parse(up)
	if e.client.MediaUpload.Query().Where(mediaupload.ID(id), mediaupload.ConsumedAtIsNil()).CountX(context.Background()) != 1 {
		t.Error("failed attach consumed the upload intent")
	}
	if len(e.store.Keys("shops/")) != 0 {
		t.Errorf("variants written for invalid content: %v", e.store.Keys("shops/"))
	}
}

func TestMediaAttachTooManyPixelsIs422(t *testing.T) {
	e := newMediaEnv(t, true)
	cfg := mediaCfg(e.baseURL)
	cfg.MaxPixels = 10_000
	e.svc.Cfg = cfg
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(200, 200)) // 40k px
	if code, _, _ := e.attach(up, ""); code != http.StatusUnprocessableEntity {
		t.Errorf("over pixel limit: %d, want 422", code)
	}
}

func TestMediaAltTooLongIs422(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(50, 50))
	if code, _, _ := e.attach(up, strings.Repeat("花", 301)); code != http.StatusUnprocessableEntity {
		t.Errorf("301-rune alt: %d, want 422", code)
	}
	if code, _, raw := e.attach(up, strings.Repeat("花", 300)); code != http.StatusCreated {
		t.Errorf("300-rune alt: %d %s", code, raw)
	}
}

func TestMediaPerProductLimit(t *testing.T) {
	e := newMediaEnv(t, true)
	for i := 0; i < media.MaxImagesPerProduct; i++ {
		e.addImage(40, 40, "")
	}
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(40, 40))
	if code, _, _ := e.attach(up, ""); code != http.StatusConflict {
		t.Errorf("21st image: %d, want 409", code)
	}
	if e.imageCount() != media.MaxImagesPerProduct {
		t.Errorf("images = %d", e.imageCount())
	}
}

func TestMediaAttachToOtherShopsProductIs404(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerB, e.shopB, "product_image", "image/jpeg", testJPEG(40, 40))
	// ownerB on shop B's path, but product belongs to shop A.
	code, _, _ := e.call(e.ownerB, "POST", e.shopPath(e.shopB, fmt.Sprintf("/products/%d/images", e.productA)),
		map[string]any{"upload_id": up})
	if code != http.StatusNotFound {
		t.Errorf("foreign product: %d, want 404", code)
	}
}

// ── ordering, alt, delete ────────────────────────────────────────────

func TestMediaReorder(t *testing.T) {
	e := newMediaEnv(t, true)
	a := int(e.addImage(40, 40, "a")["id"].(float64))
	b := int(e.addImage(40, 40, "b")["id"].(float64))
	c := int(e.addImage(40, 40, "c")["id"].(float64))

	// Partial / duplicated / foreign ids are rejected and change nothing.
	for name, ids := range map[string][]int{
		"partial": {c, a}, "duplicate": {c, c, a}, "unknown": {c, a, b + 999}, "empty": {},
	} {
		if code, _, _ := e.call(e.ownerA, "PUT", e.imagesPath("/order"), map[string]any{"image_ids": ids}); code != http.StatusUnprocessableEntity {
			t.Errorf("%s order: %d, want 422", name, code)
		}
	}
	_, list, _ := e.call(e.ownerA, "GET", e.imagesPath(""), nil)
	if got := imageIDs(list); fmt.Sprint(got) != fmt.Sprint([]int{a, b, c}) {
		t.Fatalf("order changed by rejected requests: %v", got)
	}

	code, body, raw := e.call(e.ownerA, "PUT", e.imagesPath("/order"), map[string]any{"image_ids": []int{c, a, b}})
	if code != http.StatusOK {
		t.Fatalf("reorder: %d %s", code, raw)
	}
	if got := imageIDs(body); fmt.Sprint(got) != fmt.Sprint([]int{c, a, b}) {
		t.Errorf("reorder response: %v", got)
	}
	_, list, _ = e.call(e.ownerA, "GET", e.imagesPath(""), nil)
	if got := imageIDs(list); fmt.Sprint(got) != fmt.Sprint([]int{c, a, b}) {
		t.Errorf("persisted order: %v", got)
	}
}

func imageIDs(body map[string]any) []int {
	var out []int
	for _, v := range body["images"].([]any) {
		out = append(out, int(v.(map[string]any)["id"].(float64)))
	}
	return out
}

func TestMediaUpdateAltAndDelete(t *testing.T) {
	e := newMediaEnv(t, true)
	img := e.addImage(80, 60, "old")
	id := int(img["id"].(float64))

	code, body, raw := e.call(e.ownerA, "PUT", e.imagesPath(fmt.Sprintf("/%d", id)), map[string]any{"alt": "new"})
	if code != http.StatusOK || body["alt"] != "new" {
		t.Fatalf("update alt: %d %s", code, raw)
	}
	if code, _, _ := e.call(e.viewerA, "PUT", e.imagesPath(fmt.Sprintf("/%d", id)), map[string]any{"alt": "x"}); code != http.StatusForbidden {
		t.Errorf("viewer edit: %d, want 403", code)
	}
	if code, _, _ := e.call(e.viewerA, "GET", e.imagesPath(""), nil); code != http.StatusOK {
		t.Errorf("viewer list: %d, want 200", code)
	}

	if code, _, _ := e.call(e.ownerA, "DELETE", e.imagesPath(fmt.Sprintf("/%d", id)), nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if e.imageCount() != 0 {
		t.Error("row not deleted")
	}
	if left := e.store.Keys("shops/"); len(left) != 0 {
		t.Errorf("objects left after delete: %v", left)
	}
	if code, _, _ := e.call(e.ownerA, "DELETE", e.imagesPath(fmt.Sprintf("/%d", id)), nil); code != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", code)
	}
}

func TestMediaDeleteSurvivesStorageFailure(t *testing.T) {
	e := newMediaEnv(t, true)
	id := int(e.addImage(80, 60, "")["id"].(float64))
	e.store.FailDelete = true // D8: object deletion is best-effort after commit
	if code, _, _ := e.call(e.ownerA, "DELETE", e.imagesPath(fmt.Sprintf("/%d", id)), nil); code != http.StatusNoContent {
		t.Errorf("delete with failing storage: %d, want 204", code)
	}
	if e.imageCount() != 0 {
		t.Error("row must be deleted even when the object delete fails")
	}
}

func TestMediaProductPutLeavesImagesAlone(t *testing.T) {
	e := newMediaEnv(t, true)
	e.addImage(40, 40, "one")
	e.addImage(40, 40, "two")
	skus := e.client.ProductSKU.Query().AllX(context.Background())
	code, _, raw := e.call(e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", e.productA)), map[string]any{
		"title": "Roses 2",
		"skus":  []map[string]any{{"id": skus[0].ID, "sku_code": "R-1", "price_amount": 100}},
	})
	if code != http.StatusOK {
		t.Fatalf("product PUT: %d %s", code, raw)
	}
	_, list, _ := e.call(e.ownerA, "GET", e.imagesPath(""), nil)
	if n := len(list["images"].([]any)); n != 2 {
		t.Errorf("images after product PUT = %d, want 2", n)
	}
}

func TestMediaProductDeleteCascadesAndRemovesObjects(t *testing.T) {
	e := newMediaEnv(t, true)
	e.addImage(40, 40, "")
	e.addImage(40, 40, "")
	if len(e.store.Keys("shops/")) != 4 {
		t.Fatalf("expected 4 variant objects, got %v", e.store.Keys("shops/"))
	}
	if code, _, raw := e.call(e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", e.productA)), nil); code != http.StatusNoContent {
		t.Fatalf("delete product: %d %s", code, raw)
	}
	if e.imageCount() != 0 {
		t.Error("image rows survived product deletion")
	}
	if left := e.store.Keys("shops/"); len(left) != 0 {
		t.Errorf("objects survived product deletion: %v", left)
	}
}

// ── public reads ─────────────────────────────────────────────────────

func (e *mediaEnv) public(path string) (int, map[string]any) {
	req := httptest.NewRequest("GET", path, nil)
	req.Host = e.domainA
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func TestMediaPublicProductImagesOrderedAndEmptyByDefault(t *testing.T) {
	e := newMediaEnv(t, true)

	code, p := e.public("/api/v1/shop/products/roses")
	if code != 200 {
		t.Fatalf("public get: %d", code)
	}
	imgs, ok := p["images"].([]any)
	if !ok || len(imgs) != 0 {
		t.Fatalf("no images must be [] (not missing/null): %v", p["images"])
	}

	a := int(e.addImage(40, 40, "a")["id"].(float64))
	b := int(e.addImage(40, 40, "b")["id"].(float64))
	e.call(e.ownerA, "PUT", e.imagesPath("/order"), map[string]any{"image_ids": []int{b, a}})

	_, p = e.public("/api/v1/shop/products/roses")
	imgs = p["images"].([]any)
	if int(imgs[0].(map[string]any)["id"].(float64)) != b || int(imgs[1].(map[string]any)["id"].(float64)) != a {
		t.Errorf("public order: %v", imgs)
	}
	first := imgs[0].(map[string]any)
	for _, k := range []string{"id", "alt", "position", "width", "height", "urls"} {
		if _, ok := first[k]; !ok {
			t.Errorf("image missing %s", k)
		}
	}

	// The list endpoint carries images too.
	_, list := e.public("/api/v1/shop/products")
	lp := list["products"].([]any)[0].(map[string]any)
	if len(lp["images"].([]any)) != 2 {
		t.Errorf("list images: %v", lp["images"])
	}

	// Changing the public domain changes URLs with no data migration (D5).
	before := first["urls"].(map[string]any)["large"].(string)
	cfg := mediaCfg("https://cdn.other.test")
	e.svc.Cfg = cfg
	_, p = e.public("/api/v1/shop/products/roses")
	after := p["images"].([]any)[0].(map[string]any)["urls"].(map[string]any)["large"].(string)
	if !strings.HasPrefix(before, "https://img.example.test/") || !strings.HasPrefix(after, "https://cdn.other.test/") ||
		strings.TrimPrefix(before, "https://img.example.test") != strings.TrimPrefix(after, "https://cdn.other.test") {
		t.Errorf("url did not follow the base URL: %s -> %s", before, after)
	}
}

func TestMediaAdminProductResponsesIncludeImages(t *testing.T) {
	e := newMediaEnv(t, true)
	e.addImage(40, 40, "x")
	_, p, _ := e.call(e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", e.productA)), nil)
	if len(p["images"].([]any)) != 1 {
		t.Errorf("admin get images: %v", p["images"])
	}
}

// ── concurrency ──────────────────────────────────────────────────────

func TestMediaConcurrentAttachOfSameUploadExactlyOneWins(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(200, 150))
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _, _ = e.attach(up, "")
		}(i)
	}
	wg.Wait()
	created, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	if created != 1 || conflict != len(codes)-1 {
		t.Fatalf("codes = %v, want exactly one 201 and the rest 409", codes)
	}
	if n := e.client.ProductImage.Query().Where(productimage.ProductID(e.productA)).CountX(context.Background()); n != 1 {
		t.Errorf("images = %d, want 1", n)
	}
}

// ── content images + config ──────────────────────────────────────────

func TestMediaContentImage(t *testing.T) {
	e := newMediaEnv(t, true)
	up := e.upload(e.ownerA, e.shopA, "content_image", "image/jpeg", testJPEG(2400, 1200))
	code, body, raw := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/content-images"), map[string]any{"upload_id": up})
	if code != http.StatusCreated {
		t.Fatalf("content image: %d %s", code, raw)
	}
	large := body["urls"].(map[string]any)["large"].(string)
	if !strings.HasPrefix(large, e.baseURL+"/shops/") || !strings.Contains(large, "/content/") || body["width"].(float64) != 1600 {
		t.Errorf("content image = %s", raw)
	}
	if e.imageCount() != 0 {
		t.Error("content images must not create product_images rows")
	}
	// A product_image intent cannot be consumed as a content image.
	up2 := e.upload(e.ownerA, e.shopA, "product_image", "image/jpeg", testJPEG(40, 40))
	if code, _, _ := e.call(e.ownerA, "POST", e.shopPath(e.shopA, "/media/content-images"), map[string]any{"upload_id": up2}); code != http.StatusUnprocessableEntity {
		t.Errorf("wrong purpose: %d, want 422", code)
	}
	// page.edit is required.
	if code, _, _ := e.call(e.viewerA, "POST", e.shopPath(e.shopA, "/media/content-images"), map[string]any{"upload_id": up}); code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", code)
	}
}

func TestMediaConfigEndpoint(t *testing.T) {
	e := newMediaEnv(t, true)
	code, body, raw := e.call(e.viewerA, "GET", "/api/v1/admin/media/config", nil)
	if code != 200 || body["enabled"] != true || body["public_base_url"] != e.baseURL || body["max_bytes"].(float64) != 15<<20 {
		t.Fatalf("config: %d %s", code, raw)
	}
	if len(body["accepted_types"].([]any)) != 3 {
		t.Errorf("accepted_types: %v", body["accepted_types"])
	}
}

// ── disabled ─────────────────────────────────────────────────────────

func TestMediaDisabledAnswers503ButRoutesStayMounted(t *testing.T) {
	e := newMediaEnv(t, false)
	check := func(name, method, path string, body any) {
		code, parsed, raw := e.call(e.ownerA, method, path, body)
		errObj, _ := parsed["error"].(map[string]any)
		if code != http.StatusServiceUnavailable || errObj["code"] != "media_not_configured" {
			t.Errorf("%s: %d %s, want 503 media_not_configured", name, code, raw)
		}
	}
	check("uploads", "POST", e.shopPath(e.shopA, "/media/uploads"), map[string]any{"content_type": "image/jpeg", "size_bytes": 10, "purpose": "product_image"})
	check("content-images", "POST", e.shopPath(e.shopA, "/media/content-images"), map[string]any{"upload_id": uuid.NewString()})
	check("list", "GET", e.imagesPath(""), nil)
	check("attach", "POST", e.imagesPath(""), map[string]any{"upload_id": uuid.NewString()})
	check("reorder", "PUT", e.imagesPath("/order"), map[string]any{"image_ids": []int{}})
	check("update", "PUT", e.imagesPath("/1"), map[string]any{"alt": "x"})
	check("delete", "DELETE", e.imagesPath("/1"), nil)

	// Permission still comes first: an unauthorised caller learns nothing.
	if code, _, _ := e.call(e.viewerA, "POST", e.imagesPath(""), map[string]any{"upload_id": uuid.NewString()}); code != http.StatusForbidden {
		t.Errorf("viewer on disabled media: %d, want 403", code)
	}

	code, body, _ := e.call(e.ownerA, "GET", "/api/v1/admin/media/config", nil)
	if code != 200 || body["enabled"] != false || body["public_base_url"] != "" {
		t.Errorf("config when disabled: %d %v", code, body)
	}
	_, p := e.public("/api/v1/shop/products/roses")
	if imgs, ok := p["images"].([]any); !ok || len(imgs) != 0 {
		t.Errorf("public images when disabled must be []: %v", p["images"])
	}
}
