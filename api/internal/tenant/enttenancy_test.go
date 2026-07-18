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
