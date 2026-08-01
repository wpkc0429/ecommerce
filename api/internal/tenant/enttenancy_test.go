package tenant_test

import (
	"context"
	"encoding/json"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshipmentitem "ksdevworks/ecommerce/api/internal/ent/shipmentitem"
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

// TestTenantScopeOnShipmentItems covers change partial-shipment: shipment_items
// is tenant data — it says what a named customer's parcel contained — so it must
// be scoped exactly like order_items.
//
// The **eager-load** assertion is the one worth having. Registration in
// tenantOwned is checked structurally next door, but every production read of
// this table goes through `WithItems(...)` on a shipment, which ent runs as a
// separate query for the edge. If that query did not pass through the
// interceptor, a shop could reach another shop's parcel contents while its own
// direct queries looked perfectly scoped — a leak whose symptom is invisible
// from the calling code.
func TestTenantScopeOnShipmentItems(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	type fixture struct {
		shopID, shipmentID, itemID int
	}
	build := func(name, email string, qty int32) fixture {
		shop := client.Shop.Create().SetName(name).SaveX(ctx)
		member := client.Member.Create().SetShopID(shop.ID).SetEmail(email).SetStatus(1).SaveX(ctx)
		ord := client.Order.Create().
			SetShopID(shop.ID).SetMemberID(member.ID).
			SetStatus(0).SetPaymentStatus(0).SetFulfillmentStatus(0).
			SetCurrency("TWD").SetSubtotalAmount(1000).SetTotalAmount(1000).
			SetShippingAddress(json.RawMessage(`{}`)).
			SaveX(ctx)
		item := client.OrderItem.Create().
			SetShopID(shop.ID).SetOrderID(ord.ID).
			SetProductTitle("花").SetSkuCode("SKU-1").
			SetQuantity(qty).SetPriceAmount(1000).SetCurrency("TWD").
			SaveX(ctx)
		sh := client.Shipment.Create().
			SetShopID(shop.ID).SetOrderID(ord.ID).SetCarrier("黑貓宅急便").SetStatus(1).
			SaveX(ctx)
		si := client.ShipmentItem.Create().
			SetShopID(shop.ID).SetShipmentID(sh.ID).SetOrderItemID(item.ID).SetQuantity(qty).
			SaveX(ctx)
		return fixture{shopID: shop.ID, shipmentID: sh.ID, itemID: si.ID}
	}

	a := build("Shop A", "a@t.dev", 1)
	b := build("Shop B", "b@t.dev", 2)

	ctxA := tenant.WithShopID(ctx, a.shopID)

	// Direct query: only A's parcel line is visible.
	rows := client.ShipmentItem.Query().AllX(ctxA)
	if len(rows) != 1 || rows[0].ID != a.itemID {
		t.Fatalf("shipment_items scoped query returned %d rows (%+v), want only shop A's", len(rows), rows)
	}

	// B's row is unreachable by primary key from inside A.
	if _, err := client.ShipmentItem.Query().
		Where(entshipmentitem.IDEQ(b.itemID)).Only(ctxA); !ent.IsNotFound(err) {
		t.Fatalf("cross-tenant shipment_item lookup: want NotFound, got %v", err)
	}

	// Eager loading through the shipment edge is scoped too — this is the path
	// every handler actually uses.
	shipments := client.Shipment.Query().
		WithItems().
		AllX(ctxA)
	if len(shipments) != 1 {
		t.Fatalf("shipments scoped query returned %d rows, want 1", len(shipments))
	}
	if len(shipments[0].Edges.Items) != 1 || shipments[0].Edges.Items[0].ID != a.itemID {
		t.Fatalf("eager-loaded parcel contents leaked across tenants: %+v", shipments[0].Edges.Items)
	}

	// Creation in tenant context is stamped with the context shop even when the
	// caller passes another shop's id (same guarantee as every other entity).
	// A second line on A's order, because UNIQUE(shipment_id, order_item_id)
	// rightly refuses to put the same line in the same parcel twice.
	orderA := client.OrderItem.Query().Where(entorderitem.ShopIDEQ(a.shopID)).OnlyX(ctx)
	secondLine := client.OrderItem.Create().
		SetShopID(a.shopID).SetOrderID(orderA.OrderID).
		SetProductTitle("花瓶").SetSkuCode("SKU-2").
		SetQuantity(1).SetPriceAmount(500).SetCurrency("TWD").
		SaveX(ctx)
	stamped := client.ShipmentItem.Create().
		SetShopID(b.shopID).SetShipmentID(a.shipmentID).
		SetOrderItemID(secondLine.ID).
		SetQuantity(1).
		SaveX(ctxA)
	if stamped.ShopID != a.shopID {
		t.Fatalf("create in ctx A landed in shop %d, want %d", stamped.ShopID, a.shopID)
	}
	client.ShipmentItem.DeleteOneID(stamped.ID).ExecX(ctx)

	// Outside tenant context both rows remain reachable (back-office/CLI path).
	if n := client.ShipmentItem.Query().CountX(ctx); n != 2 {
		t.Fatalf("unscoped shipment_item count: got %d, want 2", n)
	}
}
