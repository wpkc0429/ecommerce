package tenant_test

import (
	"context"
	"encoding/json"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Covers spec multi-tenancy/Tenant data isolation enforcement:
// queries in tenant context are automatically scoped to shop_id, and the
// storefront cannot see other shops' pages.
func TestTenantScopeOnQueries(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shopA := client.Shop.Create().SetName("Shop A").SetContentJSON(json.RawMessage("{}")).SaveX(ctx)
	shopB := client.Shop.Create().SetName("Shop B").SetContentJSON(json.RawMessage("{}")).SaveX(ctx)

	client.Page.Create().SetShopID(shopA.ID).SetTypeKey("home").SetSlug("home").SetTitle("A home").SaveX(ctx)
	client.Page.Create().SetShopID(shopA.ID).SetTypeKey("about").SetSlug("about").SetTitle("A about").SaveX(ctx)
	client.Page.Create().SetShopID(shopB.ID).SetTypeKey("home").SetSlug("home").SetTitle("B home").SaveX(ctx)
	client.Page.Create().SetShopID(shopB.ID).SetTypeKey("about").SetSlug("only-in-b").SetTitle("B only").SaveX(ctx)

	// Scenario: 資料層強制範圍 — query without any explicit shop condition.
	ctxA := tenant.WithShopID(ctx, shopA.ID)
	pages := client.Page.Query().AllX(ctxA)
	if len(pages) != 2 {
		t.Fatalf("tenant ctx A: got %d pages, want 2", len(pages))
	}
	for _, p := range pages {
		if p.ShopID != shopA.ID {
			t.Fatalf("leaked page %q from shop %d into tenant A", p.Slug, p.ShopID)
		}
	}

	// Scenario: 前台跨租戶讀取被隔離 — shop B's slug is invisible in A's context.
	_, err := client.Page.Query().Where(entpage.SlugEQ("only-in-b")).Only(ctxA)
	if !ent.IsNotFound(err) {
		t.Fatalf("cross-tenant slug lookup: want NotFound, got %v", err)
	}

	// Same slug resolves per-tenant.
	got := client.Page.Query().Where(entpage.SlugEQ("home")).OnlyX(ctxA)
	if got.Title != "A home" {
		t.Fatalf("slug home in ctx A resolved to %q", got.Title)
	}

	// Without tenant context (admin/CLI path) all rows remain reachable.
	if n := client.Page.Query().CountX(ctx); n != 4 {
		t.Fatalf("unscoped count: got %d, want 4", n)
	}
}

func TestTenantScopeOnMutations(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shopA := client.Shop.Create().SetName("Shop A").SaveX(ctx)
	shopB := client.Shop.Create().SetName("Shop B").SaveX(ctx)
	ctxA := tenant.WithShopID(ctx, shopA.ID)

	// Creation in tenant context is stamped with the context shop even when
	// the caller supplies a different shop id (client input is not trusted).
	p := client.Page.Create().SetShopID(shopB.ID).SetTypeKey("home").SetSlug("sneaky").SaveX(ctxA)
	if p.ShopID != shopA.ID {
		t.Fatalf("create in ctx A landed in shop %d, want %d", p.ShopID, shopA.ID)
	}

	bPage := client.Page.Create().SetShopID(shopB.ID).SetTypeKey("home").SetSlug("b-page").SaveX(ctx)

	// Updating another tenant's row through tenant context must not match.
	err := client.Page.UpdateOneID(bPage.ID).SetTitle("hijacked").Exec(ctxA)
	if !ent.IsNotFound(err) {
		t.Fatalf("cross-tenant update: want NotFound, got %v", err)
	}

	// Bulk update in tenant context only touches tenant rows.
	n := client.Page.Update().SetTitle("scoped").SaveX(ctxA)
	if n != 1 {
		t.Fatalf("bulk update touched %d rows, want 1", n)
	}
	if got := client.Page.GetX(ctx, bPage.ID); got.Title == "scoped" {
		t.Fatal("bulk update leaked into shop B")
	}

	// Cross-tenant delete must not match either.
	if err := client.Page.DeleteOneID(bPage.ID).Exec(ctxA); !ent.IsNotFound(err) {
		t.Fatalf("cross-tenant delete: want NotFound, got %v", err)
	}
}

// TestTenantScopeOnCatalogEntities covers change product-catalog (design D7):
// Category/Product/ProductSKU/ProductCategory must all be registered in
// tenantOwned and honor the same interceptor/hook behavior as Page.
func TestTenantScopeOnCatalogEntities(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shopA := client.Shop.Create().SetName("Shop A").SaveX(ctx)
	shopB := client.Shop.Create().SetName("Shop B").SaveX(ctx)

	catA := client.Category.Create().SetShopID(shopA.ID).SetName("A cat").SetSlug("a-cat").SaveX(ctx)
	catB := client.Category.Create().SetShopID(shopB.ID).SetName("B cat").SetSlug("b-cat").SaveX(ctx)
	prodA := client.Product.Create().SetShopID(shopA.ID).SetTitle("A prod").SetSlug("a-prod").SaveX(ctx)
	prodB := client.Product.Create().SetShopID(shopB.ID).SetTitle("B prod").SetSlug("b-prod").SaveX(ctx)
	client.ProductSKU.Create().SetShopID(shopA.ID).SetProductID(prodA.ID).SetSkuCode("A-SKU").SetPriceAmount(100).SaveX(ctx)
	client.ProductSKU.Create().SetShopID(shopB.ID).SetProductID(prodB.ID).SetSkuCode("B-SKU").SetPriceAmount(200).SaveX(ctx)
	client.ProductCategory.Create().SetShopID(shopA.ID).SetProductID(prodA.ID).SetCategoryID(catA.ID).SaveX(ctx)
	client.ProductCategory.Create().SetShopID(shopB.ID).SetProductID(prodB.ID).SetCategoryID(catB.ID).SaveX(ctx)

	ctxA := tenant.WithShopID(ctx, shopA.ID)

	if n := client.Category.Query().CountX(ctxA); n != 1 {
		t.Fatalf("category scoped count: got %d, want 1", n)
	}
	if n := client.Product.Query().CountX(ctxA); n != 1 {
		t.Fatalf("product scoped count: got %d, want 1", n)
	}
	if n := client.ProductSKU.Query().CountX(ctxA); n != 1 {
		t.Fatalf("sku scoped count: got %d, want 1", n)
	}
	if n := client.ProductCategory.Query().CountX(ctxA); n != 1 {
		t.Fatalf("product_category scoped count: got %d, want 1", n)
	}

	// Creation-time stamping: a client-supplied shop_id pointing at shop B is
	// overridden by the tenant context (same guarantee as Page).
	c := client.Category.Create().SetShopID(shopB.ID).SetName("sneaky").SetSlug("sneaky").SaveX(ctxA)
	if c.ShopID != shopA.ID {
		t.Fatalf("category create in ctx A landed in shop %d, want %d", c.ShopID, shopA.ID)
	}

	// Without tenant context, all rows remain reachable (admin/CLI path).
	if n := client.Category.Query().CountX(ctx); n != 3 { // catA, catB, sneaky
		t.Fatalf("unscoped category count: got %d, want 3", n)
	}
}
