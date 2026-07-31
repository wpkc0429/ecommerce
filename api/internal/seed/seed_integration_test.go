package seed_test

import (
	"context"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	entmembertier "ksdevworks/ecommerce/api/internal/ent/membertier"
	entproduct "ksdevworks/ecommerce/api/internal/ent/product"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
	entpublishablekey "ksdevworks/ecommerce/api/internal/ent/publishablekey"
	entshippingmethod "ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	entshopallowedorigin "ksdevworks/ecommerce/api/internal/ent/shopallowedorigin"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// pointsPerMinorUnit mirrors points.Service's platform-wide award ratio. It is
// duplicated rather than imported so this test asserts the demo data against
// the *stated* rule; importing the constant would make the test agree with the
// implementation by construction.
const pointsPerMinorUnit = 100

func runDemoSeed(t *testing.T, client *ent.Client) {
	t.Helper()
	if err := seed.Run(context.Background(), client, seed.Options{
		AdminEmail:    "admin@example.com",
		AdminPassword: "admin-change-me",
		Demo:          true,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// Scenario: 公開運送方式清單在 demo 資料上非空且已過濾 / demo 顧客可直接登入 /
// publishable key 可解析商家上下文 (spec demo-seed: Demo seed provisions the
// full commerce chain's prerequisites).
func TestDemoSeedProvisionsCommercePrerequisites(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	methods, err := client.ShippingMethod.Query().All(ctx)
	if err != nil {
		t.Fatalf("query shipping methods: %v", err)
	}
	var active, inactive, free, paid int
	for _, m := range methods {
		if m.IsActive {
			active++
			switch {
			case m.FlatRate == 0:
				free++
			case m.FlatRate > 0:
				paid++
			}
		} else {
			inactive++
		}
	}
	if active < 3 {
		t.Errorf("seeded active shipping methods: got %d, want >= 3", active)
	}
	if inactive < 1 {
		t.Error("seed must provision a DEACTIVATED shipping method: without one, " +
			"'deactivated methods are hidden from the public list and refused at checkout' " +
			"can only ever be verified against a test fixture, never against demo data")
	}
	if free < 1 || paid < 1 {
		t.Errorf("seeded active methods must cover both free and charged shipping: free=%d paid=%d", free, paid)
	}

	tiers, err := client.MemberTier.Query().Order(entmembertier.ByMinPoints()).All(ctx)
	if err != nil {
		t.Fatalf("query member tiers: %v", err)
	}
	if len(tiers) < 3 {
		t.Fatalf("seeded member tiers: got %d, want >= 3", len(tiers))
	}
	for i := 1; i < len(tiers); i++ {
		if tiers[i].MinPoints <= tiers[i-1].MinPoints {
			t.Fatalf("seeded tiers must form a strict ladder: %v", tiers)
		}
	}

	key, err := client.PublishableKey.Query().
		Where(entpublishablekey.KeyEQ(seed.DemoPublishableKey)).Only(ctx)
	if err != nil {
		t.Fatalf("seeded publishable key missing: %v", err)
	}
	if key.RevokedAt != nil {
		t.Error("seeded publishable key must not be revoked")
	}
	if ok, err := client.ShopAllowedOrigin.Query().
		Where(entshopallowedorigin.OriginEQ(seed.DemoAllowedOrigin)).Exist(ctx); err != nil || !ok {
		t.Errorf("seeded allowed origin missing (err=%v)", err)
	}

	customer, err := client.Member.Query().Where(entmember.EmailEQ(seed.DemoCustomerEmail)).Only(ctx)
	if err != nil {
		t.Fatalf("seeded demo customer missing: %v", err)
	}
	if customer.PasswordHash == nil || *customer.PasswordHash == "" {
		t.Error("seeded demo customer must have a password so member checkout needs no register call")
	}
	if customer.Status != 1 {
		t.Errorf("seeded demo customer status: got %d, want 1", customer.Status)
	}
	// Scenario: 尚未消費的顧客沒有等級 — level_id stays NULL until the first
	// ledger entry, which is what keeps recomputeLevel's "no qualifying tier"
	// branch observable even though the ladder starts at 0.
	if customer.LevelID != nil {
		t.Errorf("a customer with no points history must have no tier, got level_id=%d", *customer.LevelID)
	}
}

// Scenario: 單筆訂單付款後升級 — the precondition for it, checked on the data
// itself (spec demo-seed: Seeded tier thresholds are reachable from the seeded
// catalog).
//
// This is the invariant most likely to be broken by a well-meaning edit: bump
// the tier thresholds to "nicer" round numbers, or drop the high-ticket
// products, and the loyalty ladder silently becomes unreachable — level
// evaluation still runs, but returns the same answer forever, so nothing
// verifies it.
func TestDemoSeedTierThresholdsAreReachableFromTheCatalog(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	tiers, err := client.MemberTier.Query().Order(entmembertier.ByMinPoints()).All(ctx)
	if err != nil {
		t.Fatalf("query member tiers: %v", err)
	}
	var firstThreshold int32
	for _, tr := range tiers {
		if tr.MinPoints > 0 {
			firstThreshold = tr.MinPoints
			break
		}
	}
	if firstThreshold == 0 {
		t.Fatal("seeded ladder has no tier above 0 points — nothing can ever be crossed")
	}

	// The best a single-item order of one unit can do.
	published, err := client.Product.Query().Where(entproduct.StatusEQ(1)).IDs(ctx)
	if err != nil {
		t.Fatalf("query published products: %v", err)
	}
	skus, err := client.ProductSKU.Query().Where(entproductsku.ProductIDIn(published...)).All(ctx)
	if err != nil {
		t.Fatalf("query skus: %v", err)
	}
	var best int64
	var bestCode string
	for _, s := range skus {
		if s.PriceAmount > best && s.StockQty > 0 {
			best, bestCode = s.PriceAmount, s.SkuCode
		}
	}
	award := int32(best / pointsPerMinorUnit)
	if award < firstThreshold {
		t.Fatalf("no single-unit order of a seeded product can cross the first tier threshold: "+
			"best SKU %s at %d awards %d points, first threshold is %d. "+
			"Tier thresholds and catalog pricing must be chosen together — otherwise level "+
			"evaluation is unobservable on demo data.", bestCode, best, award, firstThreshold)
	}
}

// Scenario: 連續兩次 demo 佈建 / 不覆寫被調整過的值 (spec demo-seed: Demo
// seeding is idempotent per provisioned resource).
func TestDemoSeedIsIdempotent(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	counts := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		for name, count := range map[string]func(context.Context) (int, error){
			"shops":                client.Shop.Query().Count,
			"sites":                client.Site.Query().Count,
			"categories":           client.Category.Query().Count,
			"products":             client.Product.Query().Count,
			"product_skus":         client.ProductSKU.Query().Count,
			"product_category":     client.ProductCategory.Query().Count,
			"shipping_methods":     client.ShippingMethod.Query().Count,
			"member_tiers":         client.MemberTier.Query().Count,
			"publishable_keys":     client.PublishableKey.Query().Count,
			"shop_allowed_origins": client.ShopAllowedOrigin.Query().Count,
			"members":              client.Member.Query().Count,
			"users":                client.User.Query().Count,
			"pages":                client.Page.Query().Count,
			"permissions":          client.Permission.Query().Count,
			"roles":                client.Role.Query().Count,
			"role_permissions":     client.RolePermission.Query().Count,
			"role_users":           client.RoleUser.Query().Count,
			"shop_users":           client.ShopUser.Query().Count,
			"themes":               client.Theme.Query().Count,
			"theme_pages":          client.ThemePage.Query().Count,
		} {
			n, err := count(ctx)
			if err != nil {
				t.Fatalf("count %s: %v", name, err)
			}
			out[name] = n
		}
		return out
	}

	runDemoSeed(t, client)
	first := counts()

	runDemoSeed(t, client)
	second := counts()

	for table, want := range first {
		if got := second[table]; got != want {
			t.Errorf("re-running the demo seed changed %s: %d → %d", table, want, got)
		}
	}

	// Merchant operational data must survive a re-seed: flat_rate and is_active
	// are values someone may have deliberately changed in the back office.
	method, err := client.ShippingMethod.Query().
		Where(entshippingmethod.IsActiveEQ(true), entshippingmethod.FlatRateGT(0)).
		First(ctx)
	if err != nil {
		t.Fatalf("query a charged shipping method: %v", err)
	}
	const tweaked int64 = 999
	if _, err := method.Update().SetFlatRate(tweaked).Save(ctx); err != nil {
		t.Fatalf("tweak shipping method: %v", err)
	}

	runDemoSeed(t, client)

	reloaded, err := client.ShippingMethod.Get(ctx, method.ID)
	if err != nil {
		t.Fatalf("reload shipping method: %v", err)
	}
	if reloaded.FlatRate != tweaked {
		t.Errorf("re-seeding overwrote an edited shipping method fee: got %d, want %d (the seed "+
			"must not undo back-office edits)", reloaded.FlatRate, tweaked)
	}
	if third := counts(); third["shipping_methods"] != first["shipping_methods"] {
		t.Errorf("re-seeding after an edit duplicated shipping methods: %d → %d",
			first["shipping_methods"], third["shipping_methods"])
	}
}

// Scenario: 基準 seed 不含 demo 資料 (spec demo-seed: Demo seeding is opt-in
// and never affects baseline seeding).
func TestBaselineSeedProvisionsNoDemoData(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	if err := seed.Run(ctx, client, seed.Options{
		AdminEmail:    "admin@example.com",
		AdminPassword: "admin-change-me",
	}); err != nil {
		t.Fatalf("baseline seed: %v", err)
	}

	for name, count := range map[string]func(context.Context) (int, error){
		"shops":                client.Shop.Query().Count,
		"products":             client.Product.Query().Count,
		"shipping_methods":     client.ShippingMethod.Query().Count,
		"member_tiers":         client.MemberTier.Query().Count,
		"publishable_keys":     client.PublishableKey.Query().Count,
		"shop_allowed_origins": client.ShopAllowedOrigin.Query().Count,
		"members":              client.Member.Query().Count,
	} {
		n, err := count(ctx)
		if err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if n != 0 {
			t.Errorf("baseline seed created %d %s rows; demo data must require the -demo flag", n, name)
		}
	}

	// The baseline itself still landed.
	if n, err := client.Permission.Query().Count(ctx); err != nil || n == 0 {
		t.Errorf("baseline seed did not create the permission catalog (n=%d err=%v)", n, err)
	}
	if n, err := client.Theme.Query().Count(ctx); err != nil || n == 0 {
		t.Errorf("baseline seed did not create the starter theme (n=%d err=%v)", n, err)
	}
}
