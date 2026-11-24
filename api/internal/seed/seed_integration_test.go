package seed_test

import (
	"context"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	entcategory "ksdevworks/ecommerce/api/internal/ent/category"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	entmembertier "ksdevworks/ecommerce/api/internal/ent/membertier"
	entpermission "ksdevworks/ecommerce/api/internal/ent/permission"
	entproduct "ksdevworks/ecommerce/api/internal/ent/product"
	entproductcategory "ksdevworks/ecommerce/api/internal/ent/productcategory"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
	entpublishablekey "ksdevworks/ecommerce/api/internal/ent/publishablekey"
	entrole "ksdevworks/ecommerce/api/internal/ent/role"
	entrolepermission "ksdevworks/ecommerce/api/internal/ent/rolepermission"
	entroleuser "ksdevworks/ecommerce/api/internal/ent/roleuser"
	entshippingmethod "ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	entshopallowedorigin "ksdevworks/ecommerce/api/internal/ent/shopallowedorigin"
	entpaymentcredential "ksdevworks/ecommerce/api/internal/ent/shoppaymentprovidercredential"
	entshopuser "ksdevworks/ecommerce/api/internal/ent/shopuser"
	entuser "ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// pointsPerMinorUnit mirrors points.Service's platform-wide award ratio. It is
// duplicated rather than imported so this test asserts the demo data against
// the *stated* rule; importing the constant would make the test agree with the
// implementation by construction.
const pointsPerMinorUnit = 100

// testEncryptionKey is an arbitrary valid 32-byte key, hex-encoded (change
// ecpay-per-shop-credentials, design D4) — any test that seeds a demo shop
// needs one, since seedDemoPaymentCredentials encrypts the shop's sample
// ecpay credentials with it. strings.Repeat, not a hand-counted literal: a
// hex key one character short of 64 fails ParseKey, and miscounting zeroes by
// eye is exactly the kind of mistake that does not announce itself.
var testEncryptionKey = strings.Repeat("00", 32)

func runDemoSeed(t *testing.T, client *ent.Client) {
	t.Helper()
	if err := seed.Run(context.Background(), client, seed.Options{
		AdminEmail:                      "admin@example.com",
		AdminPassword:                   "admin-change-me",
		Demo:                            true,
		PaymentCredentialsEncryptionKey: testEncryptionKey,
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

// Scenario: 預設商家角色可使用顧客名冊 (spec member-directory/顧客名冊由
// `member.view` 授權; change member-directory design D4).
//
// Two things are being pinned here and neither is obvious from reading
// seed.go's tables:
//
//  1. member.view exists in the catalog at all — without the row, RBAC's
//     Snapshot.Allows returns false for everyone and the roster endpoints 403
//     for the whole platform. Fail-closed, but silently.
//  2. `editor` holds it. That grant is the disclosure decision (design D4): an
//     editor already reaches WIDER customer PII through order.view — every
//     order carries recipient name, phone and full address — so withholding
//     member.view would remove nothing while looking like a boundary.
//
// The negative half matters just as much: member.edit MUST NOT exist, because
// no endpoint writes to `members` from the back office. A permission node with
// nothing behind it is a lie told to whoever grants it.
func TestSeedGrantsMemberViewToMerchantRoles(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	node, err := client.Permission.Query().Where(entpermission.NameEQ("member.view")).Only(ctx)
	if err != nil {
		t.Fatalf("member.view must be in the permission catalog: %v", err)
	}

	for _, roleName := range []string{"merchant_owner", "editor"} {
		r, err := client.Role.Query().
			Where(entrole.NameEQ(roleName), entrole.ScopeEQ("merchant")).Only(ctx)
		if err != nil {
			t.Fatalf("role %s: %v", roleName, err)
		}
		granted, err := client.RolePermission.Query().
			Where(entrolepermission.RoleIDEQ(r.ID), entrolepermission.PermissionIDEQ(node.ID)).Exist(ctx)
		if err != nil {
			t.Fatalf("role_permission %s: %v", roleName, err)
		}
		if !granted {
			t.Errorf("role %s must be granted member.view (change member-directory design D4)", roleName)
		}
	}

	exists, err := client.Permission.Query().Where(entpermission.NameEQ("member.edit")).Exist(ctx)
	if err != nil {
		t.Fatalf("query member.edit: %v", err)
	}
	if exists {
		t.Error("member.edit must not exist: the platform has no back-office write path to `members`, " +
			"and a node with no consumer misleads whoever grants it (design D4)")
	}
}

// Scenario: payment_credential.manage 只授予 merchant_owner (spec
// shop-payment-credentials/design D6): unlike member.view/report.view, editor
// has no other path to a payment provider's HashKey/HashIV, so this boundary
// is real and worth pinning both directions — granted to merchant_owner,
// absent from editor.
func TestSeedGrantsPaymentCredentialManageOnlyToMerchantOwner(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	node, err := client.Permission.Query().Where(entpermission.NameEQ("payment_credential.manage")).Only(ctx)
	if err != nil {
		t.Fatalf("payment_credential.manage must be in the permission catalog: %v", err)
	}

	owner, err := client.Role.Query().Where(entrole.NameEQ("merchant_owner"), entrole.ScopeEQ("merchant")).Only(ctx)
	if err != nil {
		t.Fatalf("role merchant_owner: %v", err)
	}
	granted, err := client.RolePermission.Query().
		Where(entrolepermission.RoleIDEQ(owner.ID), entrolepermission.PermissionIDEQ(node.ID)).Exist(ctx)
	if err != nil {
		t.Fatalf("role_permission merchant_owner: %v", err)
	}
	if !granted {
		t.Error("merchant_owner must be granted payment_credential.manage")
	}

	editor, err := client.Role.Query().Where(entrole.NameEQ("editor"), entrole.ScopeEQ("merchant")).Only(ctx)
	if err != nil {
		t.Fatalf("role editor: %v", err)
	}
	editorGranted, err := client.RolePermission.Query().
		Where(entrolepermission.RoleIDEQ(editor.ID), entrolepermission.PermissionIDEQ(node.ID)).Exist(ctx)
	if err != nil {
		t.Fatalf("role_permission editor: %v", err)
	}
	if editorGranted {
		t.Error("editor must NOT be granted payment_credential.manage — it has no other path to a payment " +
			"provider's secret (design D6), unlike member.view/report.view")
	}
}

// Scenario: demo 商家的 ecpay 憑證已種入且可解密還原 (design D9): the values
// must round-trip back to 綠界's published sandbox sample, the same ones the
// platform-wide ECPAY_* defaults used before this change.
func TestDemoSeedPaymentCredentialsDecryptToTheSandboxSample(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	shopID, err := client.Shop.Query().OnlyID(ctx)
	if err != nil {
		t.Fatalf("demo shop: %v", err)
	}
	row, err := client.ShopPaymentProviderCredential.Query().
		Where(entpaymentcredential.ShopIDEQ(shopID), entpaymentcredential.ProviderEQ("ecpay")).
		Only(ctx)
	if err != nil {
		t.Fatalf("demo ecpay credentials: %v", err)
	}
	if row.MerchantID != "3002607" {
		t.Errorf("MerchantID = %q, want 3002607", row.MerchantID)
	}

	box, err := cryptbox.ParseKey(testEncryptionKey)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	hashKey, err := box.Decrypt(row.HashKeyCiphertext)
	if err != nil {
		t.Fatalf("decrypt hash key: %v", err)
	}
	if hashKey != "pwFHCqoQZGmho4w6" {
		t.Errorf("hash key = %q, want the 綠界 sandbox sample", hashKey)
	}
	hashIV, err := box.Decrypt(row.HashIvCiphertext)
	if err != nil {
		t.Fatalf("decrypt hash iv: %v", err)
	}
	if hashIV != "EkRm7iFT261dpevs" {
		t.Errorf("hash iv = %q, want the 綠界 sandbox sample", hashIV)
	}
}

// Scenario: 重新執行 demo seed 不會建立第二筆憑證 (design D9's idempotency
// convention — natural key `(shop_id, provider)`, same guard shape as every
// other seedDemo* helper).
func TestDemoSeedPaymentCredentialsAreIdempotent(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)
	runDemoSeed(t, client)

	shopID, err := client.Shop.Query().OnlyID(ctx)
	if err != nil {
		t.Fatalf("demo shop: %v", err)
	}
	n, err := client.ShopPaymentProviderCredential.Query().
		Where(entpaymentcredential.ShopIDEQ(shopID), entpaymentcredential.ProviderEQ("ecpay")).
		Count(ctx)
	if err != nil {
		t.Fatalf("count ecpay credentials: %v", err)
	}
	if n != 1 {
		t.Errorf("re-running demo seed produced %d ecpay credential rows for the demo shop, want 1", n)
	}
}

// Scenario: demo 目錄含「本季新品」分類，且重跑不重複 (spec demo-seed:
// demo 目錄含「本季新品」分類).
func TestDemoSeedProvisionsNewArrivalsCategory(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	count := func() (cats, links int) {
		cs, err := client.Category.Query().Where(entcategory.SlugEQ("new-arrivals")).All(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) == 1 {
			links, err = client.ProductCategory.Query().Where(entproductcategory.CategoryIDEQ(cs[0].ID)).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if cs[0].Name != "本季新品" {
				t.Errorf("category name = %q", cs[0].Name)
			}
		}
		return len(cs), links
	}
	cats, links := count()
	if cats != 1 || links < 2 {
		t.Fatalf("want 1 new-arrivals category with >=2 products, got %d categories, %d links", cats, links)
	}

	runDemoSeed(t, client)
	if c2, l2 := count(); c2 != cats || l2 != links {
		t.Fatalf("re-seed changed new-arrivals: %d/%d -> %d/%d", cats, links, c2, l2)
	}

	// Floral demo products carry a non-empty subtitle.
	n, err := client.Product.Query().
		Where(entproduct.SlugIn("anniversary-bouquet", "daisy-day-bouquet"), entproduct.SubtitleNEQ("")).
		Count(ctx)
	if err != nil || n != 2 {
		t.Fatalf("floral subtitles: n=%d err=%v", n, err)
	}
}

// Scenario: 新節點只屬於 super_admin (change platform-account-provisioning,
// design D10): account.view / account.manage exist, super_admin holds them via
// the "*" grant, and no merchant role does.
func TestSeedAccountNodesBelongToSuperAdminOnly(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	for _, name := range []string{"account.view", "account.manage"} {
		node, err := client.Permission.Query().Where(entpermission.NameEQ(name)).Only(ctx)
		if err != nil {
			t.Fatalf("%s must be in the permission catalog: %v", name, err)
		}
		holders, err := client.RolePermission.Query().
			Where(entrolepermission.PermissionIDEQ(node.ID)).QueryRole().Select(entrole.FieldName).Strings(ctx)
		if err != nil {
			t.Fatalf("holders of %s: %v", name, err)
		}
		if len(holders) != 1 || holders[0] != "super_admin" {
			t.Errorf("%s must be held by super_admin only, got %v", name, holders)
		}
	}
}

// roleNodes returns the permission names granted to the merchant role.
func roleNodes(t *testing.T, client *ent.Client, name string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	r, err := client.Role.Query().Where(entrole.NameEQ(name), entrole.ScopeEQ("merchant")).Only(ctx)
	if err != nil {
		t.Fatalf("role %s: %v", name, err)
	}
	names, err := r.QueryPermissions().Select(entpermission.FieldName).Strings(ctx)
	if err != nil {
		t.Fatalf("permissions of %s: %v", name, err)
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// Scenario: staff 角色只能處理訂單與出貨 (spec rbac, change merchant-staff-role
// design D1). EQUALITY, not containment: a sixth node added to staff by
// accident must turn this red.
func TestSeedStaffRoleHasExactlyTheFiveNodes(t *testing.T) {
	client := testutil.OpenDB(t)
	runDemoSeed(t, client)

	want := []string{"order.view", "payment.view", "shipment.view", "shipment.create", "shipment.update"}
	got := roleNodes(t, client, "staff")
	if len(got) != len(want) {
		t.Fatalf("staff nodes = %v, want exactly %v", got, want)
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("staff must hold %s", n)
		}
	}
	for _, n := range []string{"order.cancel", "shipment.return", "user.manage_members", "member.view", "report.view", "product.edit"} {
		if got[n] {
			t.Errorf("staff must NOT hold %s", n)
		}
	}
}

// Scenario: 既有 seed 角色的能力逐字不變 (design D2): the new shipment.return
// goes to owner AND editor, so splitting it out takes nothing away from them;
// user.manage_members is the owner's alone.
func TestSeedReturnAndMemberManagementGrants(t *testing.T) {
	client := testutil.OpenDB(t)
	runDemoSeed(t, client)

	owner, editor := roleNodes(t, client, "merchant_owner"), roleNodes(t, client, "editor")
	for _, n := range []string{"shipment.return"} {
		if !owner[n] || !editor[n] {
			t.Errorf("%s must be granted to merchant_owner and editor", n)
		}
	}
	if !owner["user.manage_members"] {
		t.Error("merchant_owner must hold user.manage_members")
	}
	if editor["user.manage_members"] {
		t.Error("editor must NOT hold user.manage_members")
	}
}

// Scenario: merchant_assignable 由 seed 設定 (design D4): editor and staff may be
// granted by a shop, merchant_owner only by the platform. Re-running the seed
// repairs a flipped value (the migration defaults every existing role to
// false, so the seed has to upsert the flag, not only set it on create).
func TestSeedMerchantAssignableFlagsAndIdempotence(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	check := func() {
		t.Helper()
		for name, want := range map[string]bool{"merchant_owner": false, "editor": true, "staff": true} {
			r := client.Role.Query().Where(entrole.NameEQ(name), entrole.ScopeEQ("merchant")).OnlyX(ctx)
			if r.MerchantAssignable != want {
				t.Errorf("%s.merchant_assignable = %v, want %v", name, r.MerchantAssignable, want)
			}
		}
	}
	check()

	// Simulate a database migrated before the seed knew the flag.
	client.Role.Update().Where(entrole.NameIn("editor", "staff")).SetMerchantAssignable(false).ExecX(ctx)
	runDemoSeed(t, client)
	check()

	nodes := roleNodes(t, client, "staff")
	runDemoSeed(t, client)
	if again := roleNodes(t, client, "staff"); len(again) != len(nodes) {
		t.Errorf("re-seed changed staff nodes: %v -> %v", nodes, again)
	}
	n := client.Role.Query().Where(entrole.NameEQ("staff")).CountX(ctx)
	if n != 1 {
		t.Errorf("staff role rows = %d, want 1", n)
	}
}

// The demo clerk exists with the staff role (used by e2e).
func TestDemoSeedProvisionsStaffAccount(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	runDemoSeed(t, client)

	u := client.User.Query().Where(entuser.EmailEQ("demo-staff@example.com")).OnlyX(ctx)
	if u.PasswordHash == nil || u.Status != 1 {
		t.Fatal("demo staff must be an active account with a password")
	}
	ru := client.RoleUser.Query().Where(entroleuser.UserIDEQ(u.ID)).WithRole().AllX(ctx)
	if len(ru) != 1 || ru[0].Edges.Role.Name != "staff" || ru[0].ShopID == nil {
		t.Fatalf("demo staff roles = %+v, want exactly staff at the demo shop", ru)
	}
	if !client.ShopUser.Query().Where(entshopuser.UserIDEQ(u.ID), entshopuser.StatusEQ(1)).ExistX(ctx) {
		t.Fatal("demo staff must hold an active membership")
	}
}
