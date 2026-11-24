package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/ent/publishablekey"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

type tenancyEnv struct {
	client   *ent.Client
	resolver *tenant.Resolver

	active, disabled, review, brandA int // shop ids
}

func newTenancyEnv(t *testing.T) *tenancyEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &tenancyEnv{client: client}

	e.active = client.Shop.Create().SetName("Active").SetStatus(1).SaveX(ctx).ID
	e.disabled = client.Shop.Create().SetName("Disabled").SetStatus(0).SaveX(ctx).ID
	e.review = client.Shop.Create().SetName("Review").SetStatus(2).SaveX(ctx).ID
	e.brandA = client.Shop.Create().SetName("Brand A").SetStatus(1).SaveX(ctx).ID

	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	e.resolver = tenant.NewResolver(client, rdb, log)
	return e
}

// The tenant middleware end-to-end: the publishable key is the only channel.
// Host and X-Site-Domain / X-Site-Path name nothing any more (change
// retire-hosted-storefront-and-themes).
func TestTenantMiddlewareKeyOnly(t *testing.T) {
	e := newTenancyEnv(t)
	ctx := context.Background()
	mint := func(shopID int) string {
		v, err := tenant.NewPublishableKey()
		if err != nil {
			t.Fatal(err)
		}
		e.client.PublishableKey.Create().SetShopID(shopID).SetKey(v).SaveX(ctx)
		return v
	}
	activeKey, disabledKey, reviewKey := mint(e.active), mint(e.disabled), mint(e.review)

	var gotShop int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotShop, _ = tenant.ShopID(r.Context())
		w.WriteHeader(200)
	})
	mw := httpapi.NewTenantMiddleware(e.resolver)(inner)

	cases := []struct {
		name     string
		key      string
		host     string
		headers  map[string]string
		want     int
		wantShop int
	}{
		{"a live key resolves its shop", activeKey, "anything.example", nil, 200, e.active},
		{"no key is a 404 even with a Host", "", "shop1.test", nil, 404, 0},
		{"X-Site-Domain / X-Site-Path name nothing", "", "shop1.test",
			map[string]string{"X-Site-Domain": "shop1.test", "X-Site-Path": "/brand-a"}, 404, 0},
		{"the key wins and the headers are ignored", activeKey, "x.example",
			map[string]string{"X-Site-Domain": "other.test"}, 200, e.active},
		{"disabled shop key 503", disabledKey, "x.example", nil, 503, 0},
		{"review shop key 404", reviewKey, "x.example", nil, 404, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotShop = 0
			req := httptest.NewRequest("POST", "/api/v1/shop/auth/login", nil)
			req.Host = tc.host
			if tc.key != "" {
				req.Header.Set(httpapi.PublishableKeyHeader, tc.key)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.wantShop != 0 && gotShop != tc.wantShop {
				t.Fatalf("shop ctx %d, want %d", gotShop, tc.wantShop)
			}
			if tc.key == "" && !strings.Contains(rec.Body.String(), "unknown_publishable_key") {
				t.Fatalf("a missing key answers like an unknown key: %s", rec.Body.String())
			}
		})
	}
}

// TestPublishableKeyTenantResolution pins spec storefront-client-auth's
// resolution requirements at the middleware boundary: an unusable key never
// falls back, and unknown/malformed/revoked keys are indistinguishable.
func TestPublishableKeyTenantResolution(t *testing.T) {
	e := newTenancyEnv(t)
	ctx := context.Background()

	mint := func(shopID int) string {
		v, err := tenant.NewPublishableKey()
		if err != nil {
			t.Fatal(err)
		}
		e.client.PublishableKey.Create().SetShopID(shopID).SetKey(v).SaveX(ctx)
		return v
	}
	activeKey := mint(e.active)
	brandKey := mint(e.brandA)
	disabledKey := mint(e.disabled)
	reviewKey := mint(e.review)
	revokedKey := mint(e.active)
	e.client.PublishableKey.Update().
		Where(publishablekey.KeyEQ(revokedKey)).SetRevokedAt(time.Now()).ExecX(ctx)

	var gotShop int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotShop, _ = tenant.ShopID(r.Context())
		w.WriteHeader(200)
	})
	mw := httpapi.NewTenantMiddleware(e.resolver)(inner)

	call := func(key, host string) (int, string, int) {
		gotShop = 0
		req := httptest.NewRequest("POST", "/api/v1/shop/auth/login", nil)
		req.Host = host
		if key != "" {
			req.Header.Set(httpapi.PublishableKeyHeader, key)
		}
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), gotShop
	}

	// Scenario: 有效金鑰解析出商家.
	if code, body, shop := call(activeKey, "irrelevant.example"); code != 200 || shop != e.active {
		t.Fatalf("active key: code=%d shop=%d (%s)", code, shop, body)
	}
	if code, body, shop := call(brandKey, "irrelevant.example"); code != 200 || shop != e.brandA {
		t.Fatalf("second-shop key: code=%d shop=%d (%s)", code, shop, body)
	}

	// Scenario: 已撤銷金鑰被拒且不回退.
	revokedCode, revokedBody, shop := call(revokedKey, "shop1.test")
	if revokedCode != 404 || shop != 0 {
		t.Fatalf("revoked key must 404 without falling back: code=%d shop=%d (%s)", revokedCode, shop, revokedBody)
	}

	// Scenario: 未知金鑰與已撤銷金鑰的回應同構 (malformed too — every unusable
	// key looks the same from outside).
	unknownCode, unknownBody, _ := call("pk_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "shop1.test")
	malformedCode, malformedBody, _ := call("not-a-key", "shop1.test")
	if unknownCode != revokedCode || malformedCode != revokedCode {
		t.Fatalf("statuses must be identical: revoked=%d unknown=%d malformed=%d", revokedCode, unknownCode, malformedCode)
	}
	if unknownBody != revokedBody || malformedBody != revokedBody {
		t.Fatalf("bodies must be identical:\nrevoked=%s\nunknown=%s\nmalformed=%s", revokedBody, unknownBody, malformedBody)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(revokedBody), &parsed)
	if errObj, _ := parsed["error"].(map[string]any); errObj["code"] != "unknown_publishable_key" {
		t.Fatalf("want the dedicated error code, got %v", parsed)
	}

	// Scenario: 停用商家的金鑰 / 審核中商家的金鑰.
	if code, body, _ := call(disabledKey, "shop1.test"); code != 503 {
		t.Fatalf("disabled shop key: want 503, got %d (%s)", code, body)
	}
	if code, body, _ := call(reviewKey, "shop1.test"); code != 404 {
		t.Fatalf("under-review shop key: want 404, got %d (%s)", code, body)
	}

	// Scenario: 撤銷即時生效 — prime the cache with a live key, revoke it, then
	// invalidate through the same call the domain-event subscriber makes.
	rotating := mint(e.active)
	if code, _, _ := call(rotating, "irrelevant.example"); code != 200 {
		t.Fatalf("priming call: %d", code)
	}
	e.client.PublishableKey.Update().
		Where(publishablekey.KeyEQ(rotating)).SetRevokedAt(time.Now()).ExecX(ctx)
	e.resolver.InvalidateKeys(ctx, rotating)
	if code, body, _ := call(rotating, "irrelevant.example"); code != 404 {
		t.Fatalf("revocation must take effect without waiting for the TTL: %d (%s)", code, body)
	}

	// Scenario: 金鑰不接受查詢參數.
	req := httptest.NewRequest("POST", "/api/v1/shop/auth/login?publishable_key="+activeKey, nil)
	req.Host = "nope.test"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("query-parameter key must not resolve a tenant: %d (%s)", rec.Code, rec.Body.String())
	}
}

// ── change member-identity-per-shop: members is tenant-owned data ──────

// TestMemberIdentityIsShopScoped pins spec member-identity at the data layer:
// identity is per-shop, NULL email/phone stay distinct within a shop (design
// D3), the tenant interceptor scopes members automatically, and a shop's
// customer set is fully delimited by shop_id.
func TestMemberIdentityIsShopScoped(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shopA := client.Shop.Create().SetName("Identity A").SetStatus(1).SaveX(ctx).ID
	shopB := client.Shop.Create().SetName("Identity B").SetStatus(1).SaveX(ctx).ID

	// Scenario: 同一 email 於兩家商家為獨立身分.
	a := client.Member.Create().SetShopID(shopA).SetEmail("same@t.dev").SetPasswordHash("hash-a").SetStatus(1).SaveX(ctx)
	b := client.Member.Create().SetShopID(shopB).SetEmail("same@t.dev").SetPasswordHash("hash-b").SetStatus(1).SaveX(ctx)
	if a.ID == b.ID || *a.PasswordHash == *b.PasswordHash {
		t.Fatalf("expected two independent identities, got %d/%d", a.ID, b.ID)
	}

	// Scenario: 同商家內 email 重複被拒.
	if _, err := client.Member.Create().SetShopID(shopA).SetEmail("same@t.dev").SetStatus(1).Save(ctx); err == nil {
		t.Fatal("expected UNIQUE(shop_id, email) to reject a duplicate within one shop")
	}

	// Scenario: 同商家內多筆 email 為 NULL 被允許 — Postgres's default NULLS
	// DISTINCT semantics, which design D3 requires this index to keep (a
	// NULLS NOT DISTINCT index would reject the second row here).
	client.Member.Create().SetShopID(shopA).SetPhone("0900000001").SetStatus(1).SaveX(ctx)
	client.Member.Create().SetShopID(shopA).SetPhone("0900000002").SetStatus(1).SaveX(ctx)
	// …and the same for phone: two rows with both email and phone NULL.
	client.Member.Create().SetShopID(shopA).SetStatus(1).SaveX(ctx)
	client.Member.Create().SetShopID(shopA).SetStatus(1).SaveX(ctx)
	if n := client.Member.Query().Where(member.ShopIDEQ(shopA), member.EmailIsNil()).CountX(ctx); n != 4 {
		t.Fatalf("expected 4 email-NULL identities in one shop, got %d", n)
	}

	// Scenario: 租戶上下文自動範圍化顧客查詢 — no explicit shop predicate here.
	tenantA := tenant.WithShopID(ctx, shopA)
	rows := client.Member.Query().AllX(tenantA)
	if len(rows) != 5 {
		t.Fatalf("tenant-scoped query returned %d rows, want shop A's 5", len(rows))
	}
	for _, m := range rows {
		if m.ShopID != shopA {
			t.Fatalf("tenant-scoped query leaked a shop %d row", m.ShopID)
		}
	}

	// Scenario: 刪除單一商家顧客資料不影響其他商家 (資料集自我完備). The delete
	// carries no predicate at all — the tenant hook confines it to shop A.
	if n := client.Member.Delete().ExecX(tenantA); n != 5 {
		t.Fatalf("tenant-scoped delete removed %d rows, want shop A's 5", n)
	}
	if n := client.Member.Query().Where(member.ShopIDEQ(shopA)).CountX(ctx); n != 0 {
		t.Fatalf("shop A still has %d customers", n)
	}
	survivor, err := client.Member.Query().Where(member.ShopIDEQ(shopB)).Only(ctx)
	if err != nil {
		t.Fatalf("shop B's customer must be untouched: %v", err)
	}
	if survivor.ID != b.ID || survivor.Email == nil || *survivor.Email != "same@t.dev" {
		t.Fatalf("shop B's customer changed: %+v", survivor)
	}
}
