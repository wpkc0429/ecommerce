package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Tests for change product-subtitle-public-categories: product subtitle, the
// public category tree, category filtering (with subcategories) and manual
// product order inside a category.

func (e *catalogEnv) ownerFor(shopID int) int {
	if shopID == e.shopB {
		return e.ownerB
	}
	return e.ownerA
}

func (e *catalogEnv) mkCategory(t *testing.T, shopID int, name, slug string, parentID *int) int {
	t.Helper()
	body := map[string]any{"name": name, "slug": slug}
	if parentID != nil {
		body["parent_id"] = *parentID
	}
	code, c := e.callJSON(t, e.ownerFor(shopID), "POST", e.shopPath(shopID, "/categories"), body)
	if code != http.StatusCreated {
		t.Fatalf("create category %s: %d %+v", slug, code, c)
	}
	return int(c["id"].(float64))
}

func (e *catalogEnv) mkProduct(t *testing.T, shopID int, slug string, status int, catIDs ...int) int {
	t.Helper()
	body := map[string]any{"title": "P " + slug, "slug": slug, "status": status}
	if catIDs != nil {
		body["category_ids"] = catIDs
	}
	code, p := e.callJSON(t, e.ownerFor(shopID), "POST", e.shopPath(shopID, "/products"), body)
	if code != http.StatusCreated {
		t.Fatalf("create product %s: %d %+v", slug, code, p)
	}
	return int(p["id"].(float64))
}

func (e *catalogEnv) publicProducts(t *testing.T, query string) (total int, ids []int) {
	t.Helper()
	code, body := e.public(t, "GET", "/api/v1/shop/products"+query, e.shopADomain)
	if code != http.StatusOK {
		t.Fatalf("public list %q: %d %s", query, code, body)
	}
	var out struct {
		Products []struct {
			ID int `json:"id"`
		} `json:"products"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	for _, p := range out.Products {
		ids = append(ids, p.ID)
	}
	return out.Total, ids
}

func (e *catalogEnv) putOrder(t *testing.T, userID, shopID, catID int, ids []int) (int, map[string]any) {
	t.Helper()
	return e.callJSON(t, userID, "PUT", e.shopPath(shopID, fmt.Sprintf("/categories/%d/product-order", catID)), map[string]any{"product_ids": ids})
}

func (e *catalogEnv) getOrder(t *testing.T, shopID, catID int) []int {
	t.Helper()
	code, body := e.callJSON(t, e.ownerA, "GET", e.shopPath(shopID, fmt.Sprintf("/categories/%d/product-order", catID)), nil)
	if code != http.StatusOK {
		t.Fatalf("get order: %d %+v", code, body)
	}
	var out []int
	for _, p := range body["products"].([]any) {
		out = append(out, int(p.(map[string]any)["product_id"].(float64)))
	}
	return out
}

// Scenario: 建立含副標的商品 / 更新時未帶副標不覆蓋 / 副標以字元數而非位元組限制.
func TestProductSubtitle(t *testing.T) {
	e := newCatalogEnv(t)

	code, p := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"),
		map[string]any{"title": "花束", "slug": "bq", "status": 1, "subtitle": "Seasonal Bouquet"})
	if code != http.StatusCreated || p["subtitle"] != "Seasonal Bouquet" {
		t.Fatalf("create: %d %+v", code, p)
	}
	id := int(p["id"].(float64))

	// Update without subtitle keeps it.
	code, p = e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", id)), map[string]any{"title": "花束二"})
	if code != http.StatusOK || p["subtitle"] != "Seasonal Bouquet" {
		t.Fatalf("update w/o subtitle: %d %+v", code, p)
	}
	// Public detail carries it.
	pubCode, body := e.public(t, "GET", "/api/v1/shop/products/bq", e.shopADomain)
	if pubCode != http.StatusOK || !strings.Contains(body, `"subtitle":"Seasonal Bouquet"`) {
		t.Fatalf("public detail: %d %s", pubCode, body)
	}
	// Explicit empty string clears it.
	code, p = e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", id)), map[string]any{"subtitle": ""})
	if code != http.StatusOK || p["subtitle"] != "" {
		t.Fatalf("clear subtitle: %d %+v", code, p)
	}

	// 200 CJK characters is fine (600 bytes); 201 is rejected.
	ok := strings.Repeat("花", 200)
	code, p = e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", id)), map[string]any{"subtitle": ok})
	if code != http.StatusOK || p["subtitle"] != ok {
		t.Fatalf("200 runes: %d", code)
	}
	code, _ = e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", id)), map[string]any{"subtitle": ok + "花"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("201 runes on update: want 422, got %d", code)
	}
	code, _ = e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"),
		map[string]any{"title": "x", "slug": "x", "subtitle": ok + "花"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("201 runes on create: want 422, got %d", code)
	}
}

// Scenario: 公開商品回應含分類摘要 (category_ids 保留且同集合).
func TestPublicProductCategorySummary(t *testing.T) {
	e := newCatalogEnv(t)
	na := e.mkCategory(t, e.shopA, "本季新品", "new-arrivals", nil)
	e.mkProduct(t, e.shopA, "rose", 1, na)

	code, body := e.public(t, "GET", "/api/v1/shop/products/rose", e.shopADomain)
	if code != http.StatusOK {
		t.Fatalf("detail: %d %s", code, body)
	}
	var p struct {
		CategoryIDs []int `json:"category_ids"`
		Categories  []struct {
			ID   int    `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"categories"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Categories) != 1 || p.Categories[0].Slug != "new-arrivals" || p.Categories[0].Name != "本季新品" || p.Categories[0].ID != na {
		t.Fatalf("categories: %+v", p.Categories)
	}
	if len(p.CategoryIDs) != 1 || p.CategoryIDs[0] != na {
		t.Fatalf("category_ids: %+v", p.CategoryIDs)
	}
}

// Scenario: 回傳本店分類樹 / 不跨店 / 未知 Host 404.
func TestPublicCategoryTree(t *testing.T) {
	e := newCatalogEnv(t)
	bouquets := e.mkCategory(t, e.shopA, "花束", "bouquets", nil)
	e.mkCategory(t, e.shopA, "玫瑰", "roses", &bouquets)
	e.mkCategory(t, e.shopB, "蛋糕", "cakes", nil)

	code, body := e.public(t, "GET", "/api/v1/shop/categories", e.shopADomain)
	if code != http.StatusOK {
		t.Fatalf("public categories: %d %s", code, body)
	}
	var out struct {
		Categories []struct {
			Slug     string `json:"slug"`
			Name     string `json:"name"`
			ParentID *int   `json:"parent_id"`
			Children []struct {
				Slug     string `json:"slug"`
				ParentID *int   `json:"parent_id"`
			} `json:"children"`
		} `json:"categories"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Categories) != 1 || out.Categories[0].Slug != "bouquets" {
		t.Fatalf("roots: %s", body)
	}
	ch := out.Categories[0].Children
	if len(ch) != 1 || ch[0].Slug != "roses" || ch[0].ParentID == nil || *ch[0].ParentID != bouquets {
		t.Fatalf("children: %s", body)
	}
	if strings.Contains(body, "cakes") {
		t.Fatalf("leaked another shop category: %s", body)
	}

	if code, _ := e.public(t, "GET", "/api/v1/shop/categories", "unbound-domain.test"); code != http.StatusNotFound {
		t.Fatalf("unbound host: want 404, got %d", code)
	}
}

// Scenario: 依分類篩選 / 含子分類且不重複 / 直接成員在前 / 未知與他店 slug 回空.
func TestPublicProductCategoryFilter(t *testing.T) {
	e := newCatalogEnv(t)
	bouquets := e.mkCategory(t, e.shopA, "花束", "bouquets", nil)
	roses := e.mkCategory(t, e.shopA, "玫瑰", "roses", &bouquets)
	e.mkCategory(t, e.shopB, "蛋糕", "cakes", nil)

	// p2 (lowest id) is only in the child; p1 and p3 are direct members.
	// A plain id sort would put p2 first; direct members must still lead.
	p2 := e.mkProduct(t, e.shopA, "p2", 1, roses)
	p1 := e.mkProduct(t, e.shopA, "p1", 1, bouquets)
	p3 := e.mkProduct(t, e.shopA, "p3", 1, bouquets, roses)
	e.mkProduct(t, e.shopA, "draft", 0, bouquets)
	other := e.mkProduct(t, e.shopA, "other", 1)

	total, ids := e.publicProducts(t, "?category=bouquets")
	if total != 3 || fmt.Sprint(ids) != fmt.Sprint([]int{p1, p3, p2}) {
		t.Fatalf("bouquets: total=%d ids=%v want [%d %d %d]", total, ids, p1, p3, p2)
	}
	// A child category does not include its parent products.
	total, ids = e.publicProducts(t, "?category=roses")
	if total != 2 || fmt.Sprint(ids) != fmt.Sprint([]int{p2, p3}) {
		t.Fatalf("roses: total=%d ids=%v", total, ids)
	}
	// Pagination counts the filtered set.
	total, ids = e.publicProducts(t, "?category=bouquets&page_size=2&page=2")
	if total != 3 || fmt.Sprint(ids) != fmt.Sprint([]int{p2}) {
		t.Fatalf("page 2: total=%d ids=%v", total, ids)
	}
	for _, q := range []string{"?category=no-such-slug", "?category=cakes"} {
		total, ids = e.publicProducts(t, q)
		if total != 0 || len(ids) != 0 {
			t.Fatalf("%s: want empty, got total=%d ids=%v", q, total, ids)
		}
	}
	// No filter: unchanged, all published products in id order.
	total, ids = e.publicProducts(t, "")
	if total != 4 || fmt.Sprint(ids) != fmt.Sprint([]int{p2, p1, p3, other}) {
		t.Fatalf("unfiltered: total=%d ids=%v", total, ids)
	}
}

// Scenario: 手動排序反映於公開列表 / 不完整排列被拒 / 權限 / 更新商品不重設 / 新增關聯排最後.
func TestCategoryProductOrder(t *testing.T) {
	e := newCatalogEnv(t)
	na := e.mkCategory(t, e.shopA, "本季新品", "new-arrivals", nil)
	child := e.mkCategory(t, e.shopA, "子", "child", &na)
	p1 := e.mkProduct(t, e.shopA, "o1", 1, na)
	p2 := e.mkProduct(t, e.shopA, "o2", 1, na)
	p3 := e.mkProduct(t, e.shopA, "o3", 1, na)
	pc := e.mkProduct(t, e.shopA, "oc", 1, child)

	// New associations land last, in creation order.
	if got := e.getOrder(t, e.shopA, na); fmt.Sprint(got) != fmt.Sprint([]int{p1, p2, p3}) {
		t.Fatalf("initial order: %v", got)
	}

	code, body := e.putOrder(t, e.ownerA, e.shopA, na, []int{p3, p1, p2})
	if code != http.StatusOK {
		t.Fatalf("put order: %d %+v", code, body)
	}
	if _, ids := e.publicProducts(t, "?category=new-arrivals"); fmt.Sprint(ids) != fmt.Sprint([]int{p3, p1, p2, pc}) {
		t.Fatalf("public order: %v", ids)
	}

	// Updating the product with the SAME category_ids must not reset order.
	if code, b := e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", p3)),
		map[string]any{"title": "renamed", "category_ids": []int{na}}); code != http.StatusOK {
		t.Fatalf("update: %d %+v", code, b)
	}
	if got := e.getOrder(t, e.shopA, na); fmt.Sprint(got) != fmt.Sprint([]int{p3, p1, p2}) {
		t.Fatalf("order reset by product update: %v", got)
	}
	// Adding a second category to p1 keeps p1 position in the first.
	e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", p1)), map[string]any{"category_ids": []int{na, child}})
	if got := e.getOrder(t, e.shopA, na); fmt.Sprint(got) != fmt.Sprint([]int{p3, p1, p2}) {
		t.Fatalf("order changed by adding another category: %v", got)
	}
	if got := e.getOrder(t, e.shopA, child); fmt.Sprint(got) != fmt.Sprint([]int{pc, p1}) {
		t.Fatalf("child order (new association last): %v", got)
	}
	// Full replacement still works: removing p2 from na.
	e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", p2)), map[string]any{"category_ids": []int{}})
	if got := e.getOrder(t, e.shopA, na); fmt.Sprint(got) != fmt.Sprint([]int{p3, p1}) {
		t.Fatalf("after removal: %v", got)
	}
	// Re-adding p2 puts it last.
	e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/products/%d", p2)), map[string]any{"category_ids": []int{na}})
	if got := e.getOrder(t, e.shopA, na); fmt.Sprint(got) != fmt.Sprint([]int{p3, p1, p2}) {
		t.Fatalf("re-added: %v", got)
	}

	// Incomplete / foreign / duplicated permutations are rejected, order kept.
	for name, ids := range map[string][]int{
		"missing":   {p3, p1},
		"extra":     {p3, p1, p2, pc},
		"duplicate": {p3, p3, p1},
		"empty":     {},
	} {
		if code, _ := e.putOrder(t, e.ownerA, e.shopA, na, ids); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: want 422, got %d", name, code)
		}
	}
	if got := e.getOrder(t, e.shopA, na); fmt.Sprint(got) != fmt.Sprint([]int{p3, p1, p2}) {
		t.Fatalf("order changed by rejected PUTs: %v", got)
	}

	// Permissions: editor may edit; another shop owner is forbidden; unknown category 404.
	if code, _ := e.putOrder(t, e.editorA, e.shopA, na, []int{p3, p1, p2}); code != http.StatusOK {
		t.Fatalf("editor put: %d", code)
	}
	if code, _ := e.putOrder(t, e.ownerB, e.shopA, na, []int{p3, p1, p2}); code != http.StatusForbidden {
		t.Fatalf("other shop owner: want 403, got %d", code)
	}
	if code, _ := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/categories/999999/product-order"), nil); code != http.StatusNotFound {
		t.Fatalf("unknown category: want 404, got %d", code)
	}
}
