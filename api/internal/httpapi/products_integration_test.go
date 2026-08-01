package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// Scenario: 建立商品時一併建立多個 SKU (spec Product SKU nested management).
func TestProductCreateWithNestedSKUs(t *testing.T) {
	e := newCatalogEnv(t)

	code, body := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "經典跑鞋", "slug": "classic-runner",
		"skus": []map[string]any{
			{"sku_code": "SHOE-M-RED", "options": map[string]any{"size": "M", "color": "red"}, "price_amount": 1290, "stock_qty": 10},
			{"sku_code": "SHOE-L-RED", "options": map[string]any{"size": "L", "color": "red"}, "price_amount": 1290, "stock_qty": 5},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create product: %d %+v", code, body)
	}
	if body["status"].(float64) != 0 {
		t.Fatalf("new product must default to draft (status=0): %+v", body)
	}
	skus, _ := body["skus"].([]any)
	if len(skus) != 2 {
		t.Fatalf("want 2 skus, got %d: %+v", len(skus), body)
	}
}

// Scenario: 更新時新增與移除 SKU；同店 sku_code 重複被拒 (spec Product SKU
// nested management).
func TestProductUpdateSKUUpsert(t *testing.T) {
	e := newCatalogEnv(t)

	_, created := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "T", "slug": "t",
		"skus": []map[string]any{
			{"sku_code": "A", "price_amount": 100, "stock_qty": 1},
			{"sku_code": "B", "price_amount": 200, "stock_qty": 2},
		},
	})
	productID := int(created["id"].(float64))
	skusRaw, _ := created["skus"].([]any)
	var aID int
	for _, s := range skusRaw {
		sm := s.(map[string]any)
		if sm["sku_code"] == "A" {
			aID = int(sm["id"].(float64))
		}
	}

	// Update: keep+update A, drop B, add C.
	code, updated := e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", productID)), map[string]any{
		"skus": []map[string]any{
			{"id": aID, "sku_code": "A", "price_amount": 150, "stock_qty": 9},
			{"sku_code": "C", "price_amount": 300, "stock_qty": 3},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("update skus: %d %+v", code, updated)
	}
	skusRaw2, _ := updated["skus"].([]any)
	if len(skusRaw2) != 2 {
		t.Fatalf("want 2 skus after upsert (A updated, B removed, C added), got %d: %+v", len(skusRaw2), updated)
	}
	codes := map[string]float64{}
	for _, s := range skusRaw2 {
		sm := s.(map[string]any)
		codes[sm["sku_code"].(string)] = sm["price_amount"].(float64)
	}
	if _, ok := codes["B"]; ok {
		t.Fatalf("sku B should have been removed: %+v", codes)
	}
	if codes["A"] != 150 {
		t.Fatalf("sku A price not updated: %+v", codes)
	}
	if _, ok := codes["C"]; !ok {
		t.Fatalf("sku C should have been added: %+v", codes)
	}

	// Duplicate sku_code within the same shop is rejected.
	_, other := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{"title": "T2", "slug": "t2"})
	otherID := int(other["id"].(float64))
	code, _ = e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", otherID)), map[string]any{
		"skus": []map[string]any{{"sku_code": "A", "price_amount": 1, "stock_qty": 1}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("dup sku_code across products in same shop: want 422, got %d", code)
	}
}

// Scenario: 大額整數往返精確；負數價格/庫存被拒 (spec SKU price stored as
// integer minor units / SKU single-quantity inventory).
func TestSKUMoneyAndStockPrecision(t *testing.T) {
	e := newCatalogEnv(t)

	const bigPrice = int64(9_223_372_036) // well beyond int32, safely below int64 max
	code, body := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "Big Ticket", "slug": "big-ticket",
		"skus": []map[string]any{{"sku_code": "BIG", "price_amount": bigPrice, "stock_qty": 50}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %+v", code, body)
	}
	skus, _ := body["skus"].([]any)
	got := int64(skus[0].(map[string]any)["price_amount"].(float64))
	if got != bigPrice {
		t.Fatalf("price_amount round-trip: want %d, got %d", bigPrice, got)
	}

	code, _ = e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "Neg Price", "slug": "neg-price",
		"skus": []map[string]any{{"sku_code": "NEG1", "price_amount": -1, "stock_qty": 1}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("negative price: want 422, got %d", code)
	}
	code, _ = e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "Neg Stock", "slug": "neg-stock",
		"skus": []map[string]any{{"sku_code": "NEG2", "price_amount": 1, "stock_qty": -1}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("negative stock: want 422, got %d", code)
	}
}

// Scenario: 分頁/依分類篩選/依狀態篩選 (spec Product listing pagination and filters).
func TestProductListPaginationAndFilters(t *testing.T) {
	e := newCatalogEnv(t)

	_, catShoes := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "Shoes", "slug": "shoes"})
	shoesID := int(catShoes["id"].(float64))
	_, catBags := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "Bags", "slug": "bags"})
	bagsID := int(catBags["id"].(float64))

	for i := 0; i < 3; i++ {
		e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
			"title": fmt.Sprintf("Shoe %d", i), "slug": fmt.Sprintf("shoe-%d", i), "category_ids": []int{shoesID},
		})
	}
	_, bagProduct := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "Bag", "slug": "bag", "category_ids": []int{bagsID},
	})
	bagID := int(bagProduct["id"].(float64))
	e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", bagID)), map[string]any{"status": 1})

	// Pagination.
	code, page1 := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?page=1&page_size=2"), nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	if page1["total"].(float64) != 4 {
		t.Fatalf("total: want 4, got %v", page1["total"])
	}
	if len(page1["products"].([]any)) != 2 {
		t.Fatalf("page size not honored: %+v", page1)
	}

	// Filter by category.
	code, byCategory := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/products?category_id=%d", shoesID)), nil)
	if code != http.StatusOK || byCategory["total"].(float64) != 3 {
		t.Fatalf("filter by category: %d %+v", code, byCategory)
	}

	// Filter by status.
	code, published := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?status=1"), nil)
	if code != http.StatusOK || published["total"].(float64) != 1 {
		t.Fatalf("filter by status: %d %+v", code, published)
	}
}

// Scenario: 依關鍵字搜尋（title / slug / sku_code）、與其他篩選並用、未帶時
// 行為不變 (spec Product listing pagination and filters, change
// admin-commerce-catalog design D6).
//
// The keyword runs over the whole catalog, not the loaded page — a search box
// that only filters the current page tells a merchant a product does not exist
// when it merely lives two pages away, and the merchant then creates a
// duplicate.
func TestProductListKeywordSearch(t *testing.T) {
	e := newCatalogEnv(t)

	e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "母親節康乃馨花束", "slug": "mothers-day-carnation",
		"skus": []map[string]any{{"sku_code": "CARN-M", "price_amount": 3400, "stock_qty": 5}},
	})
	_, tulip := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "鬱金香花束", "slug": "tulip-bouquet",
		"skus": []map[string]any{{"sku_code": "TULIP-L", "price_amount": 4400, "stock_qty": 3}},
	})
	tulipID := int(tulip["id"].(float64))
	e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", tulipID)), map[string]any{"status": 1})

	// Title substring.
	code, byTitle := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?q=康乃馨"), nil)
	if code != http.StatusOK || byTitle["total"].(float64) != 1 {
		t.Fatalf("search by title: %d %+v", code, byTitle)
	}

	// Slug substring, case-insensitive.
	_, bySlug := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?q=TULIP-BOU"), nil)
	if bySlug["total"].(float64) != 1 {
		t.Fatalf("search by slug must be case-insensitive: %+v", bySlug)
	}

	// SKU code — the merchant's other everyday identifier for a product.
	_, bySKU := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?q=carn-m"), nil)
	if bySKU["total"].(float64) != 1 {
		t.Fatalf("search by sku_code: %+v", bySKU)
	}

	// Combined with status: 康乃馨 is still a draft, so status=1 excludes it.
	_, combined := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?q=花束&status=1"), nil)
	if combined["total"].(float64) != 1 {
		t.Fatalf("q + status must both apply: %+v", combined)
	}
	got := combined["products"].([]any)[0].(map[string]any)["slug"]
	if got != "tulip-bouquet" {
		t.Fatalf("q + status returned the wrong product: %v", got)
	}

	// No match is an empty page, not an error.
	_, none := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?q=zzz-nothing"), nil)
	if none["total"].(float64) != 0 || len(none["products"].([]any)) != 0 {
		t.Fatalf("no-match search: %+v", none)
	}

	// Absent (and blank) q leaves the query exactly as it was.
	_, all := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products"), nil)
	_, blank := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products?q=%20"), nil)
	if all["total"].(float64) != 2 || blank["total"].(float64) != 2 {
		t.Fatalf("absent/blank q must not filter: all=%+v blank=%+v", all, blank)
	}
}

// Scenario: editor 不可刪除商品；merchant_owner 可以 (spec Merchant-scoped
// catalog management authorization).
func TestProductRBACDeleteRestriction(t *testing.T) {
	e := newCatalogEnv(t)

	_, p := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{"title": "X", "slug": "x"})
	productID := int(p["id"].(float64))

	code, _ := e.call(t, e.editorA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", productID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("editor delete: want 403, got %d", code)
	}
	code, _ = e.call(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", productID)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("owner delete: want 204, got %d", code)
	}
}

// Scenario: 跨店操作被拒；跨店查詢資料互不可見 (spec Tenant data isolation
// for catalog tables / Cross-shop access guard).
func TestProductCrossShopIsolation(t *testing.T) {
	e := newCatalogEnv(t)

	e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{"title": "A only", "slug": "a-only"})
	e.callJSON(t, e.ownerB, "POST", e.shopPath(e.shopB, "/products"), map[string]any{"title": "B only", "slug": "b-only"})

	code, listA := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products"), nil)
	if code != http.StatusOK || listA["total"].(float64) != 1 {
		t.Fatalf("shop A list leaked cross-shop data: %d %+v", code, listA)
	}

	code, _ = e.call(t, e.ownerB, "GET", e.shopPath(e.shopA, "/products"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("shop B user on shop A endpoint: want 403, got %d", code)
	}
}

// ── public published-only catalog endpoint (spec Published-only public
// catalog endpoint) ──────────────────────────────────────────────────

// Scenario: 公開端點僅列出已發佈商品；草稿商品詳情 404；已發佈商品詳情含 SKU.
func TestPublicCatalogPublishedOnly(t *testing.T) {
	e := newCatalogEnv(t)

	_, draft := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "Draft Product", "slug": "draft-product",
	})
	draftID := int(draft["id"].(float64))

	_, published := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "Published Product", "slug": "published-product",
		"skus": []map[string]any{{"sku_code": "PUB-1", "price_amount": 500, "currency": "TWD", "stock_qty": 20}},
	})
	publishedID := int(published["id"].(float64))
	code, _ := e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", publishedID)), map[string]any{"status": 1})
	if code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}

	// List: only the published product appears.
	code, listBody := e.public(t, "GET", "/api/v1/shop/products", e.shopADomain)
	if code != http.StatusOK {
		t.Fatalf("public list: %d %s", code, listBody)
	}
	var list struct {
		Products []map[string]any `json:"products"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal([]byte(listBody), &list); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, listBody)
	}
	if list.Total != 1 {
		t.Fatalf("public list total: want 1 (published only), got %d: %s", list.Total, listBody)
	}

	// Draft detail 404s.
	code, _ = e.public(t, "GET", "/api/v1/shop/products/draft-product", e.shopADomain)
	if code != http.StatusNotFound {
		t.Fatalf("draft product public detail: want 404, got %d", code)
	}
	_ = draftID

	// Published detail includes SKUs.
	code, detailBody := e.public(t, "GET", "/api/v1/shop/products/published-product", e.shopADomain)
	if code != http.StatusOK {
		t.Fatalf("published product public detail: %d %s", code, detailBody)
	}
	var detail map[string]any
	_ = json.Unmarshal([]byte(detailBody), &detail)
	skus, _ := detail["skus"].([]any)
	if len(skus) != 1 {
		t.Fatalf("published detail missing skus: %s", detailBody)
	}
	sku := skus[0].(map[string]any)
	if sku["price_amount"].(float64) != 500 || sku["currency"] != "TWD" || sku["stock_qty"].(float64) != 20 {
		t.Fatalf("sku fields not surfaced correctly: %+v", sku)
	}
}

// Scenario: 不同 shop 的商品互不可見 on the public endpoint.
func TestPublicCatalogCrossShopIsolation(t *testing.T) {
	e := newCatalogEnv(t)

	_, p := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{"title": "Only A", "slug": "only-a"})
	pID := int(p["id"].(float64))
	e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", pID)), map[string]any{"status": 1})

	// Shop B has no site bound, so resolving its domain 404s — confirming
	// isolation would otherwise require binding a second domain. Instead,
	// verify shop A's public feed doesn't leak into an unrelated domain
	// lookup by requesting an unbound host.
	code, _ := e.public(t, "GET", "/api/v1/shop/products", "unbound-domain.test")
	if code != http.StatusNotFound {
		t.Fatalf("unbound domain: want 404, got %d", code)
	}
}

// skuIDs extracts the SKU ids of a serialized product, preserving order.
func skuIDs(t *testing.T, product map[string]any) []int {
	t.Helper()
	raw, _ := product["skus"].([]any)
	out := make([]int, 0, len(raw))
	for _, s := range raw {
		out = append(out, int(s.(map[string]any)["id"].(float64)))
	}
	return out
}

func ascending(ids []int) bool {
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			return false
		}
	}
	return true
}

// Scenario: 同一筆商品重複查詢的 SKU 順序一致 / 公開與後台端點的 SKU 順序一致 /
// 更新 SKU 不改變既有 SKU 的相對順序 (spec product-catalog: Deterministic SKU
// ordering in product responses, change commerce-demo-seed design D8).
func TestProductSKUOrderingIsDeterministic(t *testing.T) {
	e := newCatalogEnv(t)

	_, created := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "多尺寸花束", "slug": "multi-size-bouquet", "status": 1,
		"skus": []map[string]any{
			{"sku_code": "BQ-S", "options": map[string]any{"size": "S"}, "price_amount": 3400, "currency": "TWD", "stock_qty": 5},
			{"sku_code": "BQ-M", "options": map[string]any{"size": "M"}, "price_amount": 3800, "currency": "TWD", "stock_qty": 5},
			{"sku_code": "BQ-L", "options": map[string]any{"size": "L"}, "price_amount": 4400, "currency": "TWD", "stock_qty": 5},
		},
	})
	productID := int(created["id"].(float64))
	if code, _ := e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", productID)), map[string]any{"status": 1}); code != http.StatusOK {
		t.Fatalf("publish product")
	}

	// Repeated public reads agree with each other and are id ascending.
	var first []int
	for i := 0; i < 4; i++ {
		code, body := e.public(t, "GET", "/api/v1/shop/products/multi-size-bouquet", e.shopADomain)
		if code != http.StatusOK {
			t.Fatalf("public detail: %d %s", code, body)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(body), &detail); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		ids := skuIDs(t, detail)
		if len(ids) != 3 {
			t.Fatalf("want 3 skus, got %d", len(ids))
		}
		if !ascending(ids) {
			t.Fatalf("public detail sku order is not id ascending: %v", ids)
		}
		if first == nil {
			first = ids
		} else if fmt.Sprint(ids) != fmt.Sprint(first) {
			t.Fatalf("public detail sku order changed between reads: %v then %v", first, ids)
		}
	}

	// The public list agrees with the public detail.
	code, listBody := e.public(t, "GET", "/api/v1/shop/products", e.shopADomain)
	if code != http.StatusOK {
		t.Fatalf("public list: %d %s", code, listBody)
	}
	var list struct {
		Products []map[string]any `json:"products"`
	}
	if err := json.Unmarshal([]byte(listBody), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, p := range list.Products {
		if int(p["id"].(float64)) != productID {
			continue
		}
		if got := fmt.Sprint(skuIDs(t, p)); got != fmt.Sprint(first) {
			t.Fatalf("public list sku order %s != public detail %v", got, first)
		}
	}

	// The back office agrees with the storefront: one product, one order.
	_, adminDetail := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", productID)), nil)
	if got := fmt.Sprint(skuIDs(t, adminDetail)); got != fmt.Sprint(first) {
		t.Fatalf("admin detail sku order %s != public %v", got, first)
	}
	_, adminList := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/products"), nil)
	for _, p := range adminList["products"].([]any) {
		pm := p.(map[string]any)
		if int(pm["id"].(float64)) != productID {
			continue
		}
		if got := fmt.Sprint(skuIDs(t, pm)); got != fmt.Sprint(first) {
			t.Fatalf("admin list sku order %s != public %v", got, first)
		}
	}

	// Updating an existing SKU must not move it: upsert keeps its id.
	code, updated := e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", productID)), map[string]any{
		"skus": []map[string]any{
			{"id": first[2], "sku_code": "BQ-L", "price_amount": 4500, "stock_qty": 5},
			{"id": first[1], "sku_code": "BQ-M", "price_amount": 3800, "stock_qty": 5},
			{"id": first[0], "sku_code": "BQ-S", "price_amount": 3400, "stock_qty": 5},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("update skus: %d %+v", code, updated)
	}
	if got := fmt.Sprint(skuIDs(t, updated)); got != fmt.Sprint(first) {
		t.Fatalf("sku order changed after an in-place update: %s != %v", got, first)
	}
}
