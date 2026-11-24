// Package seed provisions baseline data (task 2.4): the permission catalog,
// platform roles, the initial platform administrator, and the demo theme
// "starter" (sections-based page schemas + tokens-based config schema,
// design D6). Optionally seeds a demo shop for local development and E2E.
//
// Seeding is idempotent: natural keys are looked up before insertion, and
// theme schemas are updated in place so re-running pushes schema changes.
package seed

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	entcategory "ksdevworks/ecommerce/api/internal/ent/category"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	entmembertier "ksdevworks/ecommerce/api/internal/ent/membertier"
	"ksdevworks/ecommerce/api/internal/ent/page"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	entproduct "ksdevworks/ecommerce/api/internal/ent/product"
	entproductcategory "ksdevworks/ecommerce/api/internal/ent/productcategory"
	entpublishablekey "ksdevworks/ecommerce/api/internal/ent/publishablekey"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/rolepermission"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	entshippingmethod "ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	entshopallowedorigin "ksdevworks/ecommerce/api/internal/ent/shopallowedorigin"
	entpaymentcredential "ksdevworks/ecommerce/api/internal/ent/shoppaymentprovidercredential"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/site"
	"ksdevworks/ecommerce/api/internal/ent/siteshop"
	"ksdevworks/ecommerce/api/internal/ent/theme"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
)

//go:embed schemas/starter/config_schema.json
var starterConfigSchema []byte

//go:embed schemas/starter/sections_defs.json
var starterSectionsDefs []byte

// Options configures a seed run.
type Options struct {
	AdminEmail    string
	AdminPassword string
	Demo          bool // additionally create a demo shop bound to demo.localhost

	// PaymentCredentialsEncryptionKey encrypts the demo shop's seeded ecpay
	// credentials (change ecpay-per-shop-credentials, design D9). Only used
	// when Demo is true. Callers pass config.Config.PaymentCredentialsEncryptionKey
	// so a seeded database is always readable by the server it seeds for.
	PaymentCredentialsEncryptionKey string
}

// PermissionCatalog is the Phase 1 permission node catalog (shop.*, page.*,
// user.*, theme.*).
var PermissionCatalog = []struct{ Name, Description string }{
	{"shop.view", "檢視商家設定與全域內容"},
	{"shop.create", "建立商家（平台層）"},
	{"shop.list", "列出所有商家（平台層）"},
	{"shop.update", "更新商家全域內容與切換主題"},
	// change platform-account-provisioning (design D10): platform-level, held
	// only by super_admin (the "*" grant); never given to a merchant role.
	{"account.view", "檢視後台帳號與其成員資格（平台層）"},
	{"account.manage", "建立後台帳號、指派成員資格、停用與重設密碼（平台層）"},
	{"shop.manage_domains", "管理站點網域與 site-shop 綁定（平台層）"},
	// change storefront-client-auth (design D11): allowed origins are a
	// contested global namespace like bound domains, hence platform-scoped;
	// publishable keys are randomly generated per shop, hence merchant-scoped
	// and self-service.
	{"shop.manage_client_origins", "登記商家自帶前端的瀏覽器 origin（平台層）"},
	{"publishable_key.view", "檢視商家的 publishable key"},
	{"publishable_key.create", "產生 publishable key"},
	{"publishable_key.revoke", "撤銷 publishable key"},
	{"page.view", "檢視頁面"},
	{"page.create", "建立頁面"},
	{"page.edit", "編輯頁面內容"},
	{"page.delete", "刪除頁面"},
	{"page.publish", "發佈與下架頁面"},
	{"user.view", "檢視商家成員"},
	{"user.manage_roles", "指派與移除商家角色"},
	{"theme.view", "檢視主題目錄"},
	{"theme.manage", "管理主題與頁型（平台層）"},
	{"category.view", "檢視商品分類"},
	{"category.create", "建立商品分類"},
	{"category.edit", "編輯商品分類"},
	{"category.delete", "刪除商品分類"},
	{"product.view", "檢視商品與 SKU"},
	{"product.create", "建立商品（含巢狀 SKU）"},
	{"product.edit", "編輯商品（含巢狀 SKU 新增/更新/移除）"},
	{"product.delete", "刪除商品"},
	{"order.view", "檢視訂單"},
	{"order.cancel", "取消訂單"},
	{"payment.view", "檢視訂單付款紀錄"},
	{"shipping_method.view", "檢視物流方式"},
	{"shipping_method.create", "建立物流方式"},
	{"shipping_method.edit", "編輯物流方式"},
	{"shipping_method.delete", "刪除物流方式"},
	{"shipment.view", "檢視出貨紀錄"},
	{"shipment.create", "建立出貨紀錄（出貨）"},
	{"shipment.update", "更新出貨狀態（送達/退貨）"},
	{"member_tier.view", "檢視會員等級"},
	{"member_tier.create", "建立會員等級"},
	{"member_tier.edit", "編輯會員等級"},
	{"member_tier.delete", "刪除會員等級"},
	{"point.view", "檢視會員點數餘額與流水帳"},
	{"point.adjust", "手動調整會員點數"},
	// change member-directory (design D4): exactly one node, because a
	// permission node with no endpoint behind it is a lie told to whoever
	// grants it. There is deliberately no `member.edit` — the platform has no
	// back-office write path to `members` at all today (registration, guest
	// claim and points.Service are the only writers). When a PATCH endpoint
	// exists (disabling a customer is the named next gap), that node gets its
	// consumer and can be added additively.
	{"member.view", "檢視顧客名冊與單一顧客"},
	// change merchant-reports (design D6): exactly one node, same rule as
	// member.view. There is deliberately no `report.export` — the platform has
	// no export endpoint, and a node that guards nothing is a lie told to
	// whoever grants it. There is no `report.manage` either: a report has
	// nothing to write.
	//
	// The shop's time zone is edited through the EXISTING `shop.update` node
	// (design D7), because it is a shop setting, not a report.
	{"report.view", "檢視營運報表（營業額與商品銷售排行）"},
	// change ecpay-per-shop-credentials (design D6): exactly one node. There is
	// no `payment_credential.view`/`.manage` split — the secret fields are
	// write-only at the API level regardless of who holds this node (GET never
	// returns HashKey/HashIV plaintext to anyone), so a separate view-only node
	// would gate nothing a holder of .manage doesn't already see less of.
	{"payment_credential.manage", "設定商家自己的金流商憑證（如綠界 MerchantID/HashKey/HashIV）"},
}

// roleDefs maps seeded roles to their scope and granted permissions.
var roleDefs = []struct {
	Name  string
	Scope string
	Perms []string // "*" = every catalog permission
}{
	{"super_admin", "platform", []string{"*"}},
	{"merchant_owner", "merchant", []string{
		"shop.view", "shop.update",
		"page.view", "page.create", "page.edit", "page.delete", "page.publish",
		"user.view", "user.manage_roles", "theme.view",
		"category.view", "category.create", "category.edit", "category.delete",
		"product.view", "product.create", "product.edit", "product.delete",
		"order.view", "order.cancel",
		"payment.view",
		"shipping_method.view", "shipping_method.create", "shipping_method.edit", "shipping_method.delete",
		"shipment.view", "shipment.create", "shipment.update",
		"member_tier.view", "member_tier.create", "member_tier.edit", "member_tier.delete",
		"point.view", "point.adjust",
		"member.view",
		// change merchant-reports (design D6).
		"report.view",
		// change storefront-client-auth: key rotation is the merchant's own
		// operational rhythm. Deliberately not granted to `editor` — minting a
		// client identity is an integration action, not content editing.
		"publishable_key.view", "publishable_key.create", "publishable_key.revoke",
		// change ecpay-per-shop-credentials: same reasoning as
		// publishable_key.* above, one order of magnitude more sensitive —
		// `editor` has no path today that lets it see or infer a payment
		// provider's HashKey/HashIV, so unlike member.view/report.view this
		// boundary is real and worth keeping. Deliberately not granted to
		// `editor`.
		"payment_credential.manage",
	}},
	{"editor", "merchant", []string{
		"shop.view", "page.view", "page.create", "page.edit", "theme.view",
		"category.view", "category.create", "category.edit",
		"product.view", "product.create", "product.edit",
		"order.view",
		"payment.view",
		"shipping_method.view", "shipping_method.create", "shipping_method.edit",
		"shipment.view", "shipment.create", "shipment.update",
		"member_tier.view", "member_tier.create", "member_tier.edit",
		"point.view", "point.adjust",
		// change member-directory (design D4): granted, and the reason is not
		// "editor sees everything". editor already reaches WIDER customer PII
		// through order.view — every order carries the recipient's name, phone
		// and full address, and that list is paginated and keyword-searchable
		// too. The roster adds email + points. Withholding member.view would
		// remove nothing from what an editor can see; it would only force the
		// same work through the order list. A boundary that stops nobody is
		// worse than none, because it stops the thinking.
		"member.view",
		// change merchant-reports (design D6): granted for the same reason, and
		// it is the stronger version of that argument. Every figure in a report
		// is a sum of orders `editor` can ALREADY read one page at a time
		// through order.view — the report adds convenience, not access.
		// Withholding it would remove nothing from what an editor can learn; it
		// would only make them add the numbers up by hand.
		//
		// Note what is NOT granted here: `shop.update`, which owns the time
		// zone. An editor sees the report and sees which zone cut its days, but
		// cannot change that zone — editing a shop setting is not content
		// editing.
		"report.view",
	}},
}

// starterPages defines the three seeded page types and their allowed section
// block types (design D6 組合式頁面結構).
var starterPages = []struct {
	TypeKey      string
	ComponentKey string
	Blocks       []string
}{
	{"home", "starter/home", []string{"hero", "rich_text", "feature_grid", "cta"}},
	{"about", "starter/about", []string{"rich_text", "hero"}},
	{"landing_page", "starter/landing", []string{"hero", "rich_text", "feature_grid", "cta"}},
}

// Run executes the full seed inside one transaction.
func Run(ctx context.Context, client *ent.Client, opts Options) (err error) {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("seed: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	perms, err := seedPermissions(ctx, tx)
	if err != nil {
		return err
	}
	roles, err := seedRoles(ctx, tx, perms)
	if err != nil {
		return err
	}
	if err = seedAdmin(ctx, tx, opts, roles["super_admin"]); err != nil {
		return err
	}
	starterID, err := seedStarterTheme(ctx, tx)
	if err != nil {
		return err
	}
	if opts.Demo {
		box, berr := cryptbox.ParseKey(opts.PaymentCredentialsEncryptionKey)
		if berr != nil {
			return fmt.Errorf("seed: PaymentCredentialsEncryptionKey: %w", berr)
		}
		if err = seedDemoShop(ctx, tx, starterID, roles["merchant_owner"], box); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func seedPermissions(ctx context.Context, tx *ent.Tx) (map[string]int, error) {
	out := make(map[string]int, len(PermissionCatalog))
	for _, p := range PermissionCatalog {
		existing, err := tx.Permission.Query().Where(permission.NameEQ(p.Name)).Only(ctx)
		switch {
		case err == nil:
			out[p.Name] = existing.ID
		case ent.IsNotFound(err):
			created, cerr := tx.Permission.Create().SetName(p.Name).SetDescription(p.Description).Save(ctx)
			if cerr != nil {
				return nil, fmt.Errorf("seed: create permission %s: %w", p.Name, cerr)
			}
			out[p.Name] = created.ID
		default:
			return nil, fmt.Errorf("seed: query permission %s: %w", p.Name, err)
		}
	}
	return out, nil
}

func seedRoles(ctx context.Context, tx *ent.Tx, perms map[string]int) (map[string]int, error) {
	out := make(map[string]int, len(roleDefs))
	for _, rd := range roleDefs {
		r, err := tx.Role.Query().Where(role.NameEQ(rd.Name), role.ScopeEQ(rd.Scope)).Only(ctx)
		if ent.IsNotFound(err) {
			r, err = tx.Role.Create().SetName(rd.Name).SetScope(rd.Scope).Save(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("seed: role %s: %w", rd.Name, err)
		}
		out[rd.Name] = r.ID

		grant := rd.Perms
		if len(grant) == 1 && grant[0] == "*" {
			grant = make([]string, 0, len(perms))
			for name := range perms {
				grant = append(grant, name)
			}
		}
		for _, pname := range grant {
			pid, ok := perms[pname]
			if !ok {
				return nil, fmt.Errorf("seed: role %s references unknown permission %s", rd.Name, pname)
			}
			exists, err := tx.RolePermission.Query().
				Where(rolepermission.RoleIDEQ(r.ID), rolepermission.PermissionIDEQ(pid)).
				Exist(ctx)
			if err != nil {
				return nil, fmt.Errorf("seed: query role_permission: %w", err)
			}
			if !exists {
				if _, err := tx.RolePermission.Create().SetRoleID(r.ID).SetPermissionID(pid).Save(ctx); err != nil {
					return nil, fmt.Errorf("seed: link %s→%s: %w", rd.Name, pname, err)
				}
			}
		}
	}
	return out, nil
}

func seedAdmin(ctx context.Context, tx *ent.Tx, opts Options, superAdminRoleID int) error {
	email := strings.ToLower(strings.TrimSpace(opts.AdminEmail))
	if email == "" || opts.AdminPassword == "" {
		return fmt.Errorf("seed: SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD are required")
	}
	u, err := tx.User.Query().Where(user.EmailEQ(email)).Only(ctx)
	if ent.IsNotFound(err) {
		hash, herr := auth.HashPassword(opts.AdminPassword)
		if herr != nil {
			return herr
		}
		u, err = tx.User.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(1).Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("seed: admin user: %w", err)
	}

	exists, err := tx.RoleUser.Query().
		Where(roleuser.UserIDEQ(u.ID), roleuser.RoleIDEQ(superAdminRoleID), roleuser.ShopIDIsNil()).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query role_user: %w", err)
	}
	if !exists {
		if _, err := tx.RoleUser.Create().SetUserID(u.ID).SetRoleID(superAdminRoleID).Save(ctx); err != nil {
			return fmt.Errorf("seed: assign super_admin: %w", err)
		}
	}
	return nil
}

// StarterPageSchema builds the JSON Schema for one starter page type from the
// shared section block definitions. Exported for tests and tooling.
func StarterPageSchema(typeKey string) (json.RawMessage, error) {
	var defs map[string]json.RawMessage
	if err := json.Unmarshal(starterSectionsDefs, &defs); err != nil {
		return nil, fmt.Errorf("seed: parse sections defs: %w", err)
	}
	var blocks []string
	for _, sp := range starterPages {
		if sp.TypeKey == typeKey {
			blocks = sp.Blocks
		}
	}
	if blocks == nil {
		return nil, fmt.Errorf("seed: unknown starter page type %q", typeKey)
	}

	usedDefs := map[string]json.RawMessage{}
	refs := make([]map[string]string, 0, len(blocks))
	for _, b := range blocks {
		def, ok := defs[b]
		if !ok {
			return nil, fmt.Errorf("seed: unknown section block %q", b)
		}
		usedDefs[b] = def
		refs = append(refs, map[string]string{"$ref": "#/$defs/" + b})
	}

	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                "Starter theme — " + typeKey + " page",
		"type":                 "object",
		"additionalProperties": false,
		"$defs":                usedDefs,
		"properties": map[string]any{
			"sections": map[string]any{
				"type":     "array",
				"default":  []any{},
				"x-editor": "sections",
				"items":    map[string]any{"oneOf": refs},
			},
		},
	}
	return json.Marshal(schema)
}

// StarterConfigSchema returns the starter theme config schema (tokens 區段).
func StarterConfigSchema() json.RawMessage {
	return json.RawMessage(starterConfigSchema)
}

func seedStarterTheme(ctx context.Context, tx *ent.Tx) (int, error) {
	t, err := tx.Theme.Query().Where(theme.CodeEQ("starter")).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		t, err = tx.Theme.Create().
			SetCode("starter").
			SetName("Starter").
			SetLayoutKey("starter/main").
			SetConfigSchema(json.RawMessage(starterConfigSchema)).
			SetIsActive(true).
			Save(ctx)
		if err != nil {
			return 0, fmt.Errorf("seed: create starter theme: %w", err)
		}
	case err != nil:
		return 0, fmt.Errorf("seed: query starter theme: %w", err)
	default:
		// Keep schema current on re-run.
		t, err = t.Update().SetConfigSchema(json.RawMessage(starterConfigSchema)).Save(ctx)
		if err != nil {
			return 0, fmt.Errorf("seed: update starter theme: %w", err)
		}
	}

	for _, sp := range starterPages {
		schema, err := StarterPageSchema(sp.TypeKey)
		if err != nil {
			return 0, err
		}
		existing, err := tx.ThemePage.Query().
			Where(themepage.ThemeIDEQ(t.ID), themepage.TypeKeyEQ(sp.TypeKey)).
			Only(ctx)
		switch {
		case ent.IsNotFound(err):
			if _, err := tx.ThemePage.Create().
				SetThemeID(t.ID).
				SetTypeKey(sp.TypeKey).
				SetComponentKey(sp.ComponentKey).
				SetPageSchema(schema).
				Save(ctx); err != nil {
				return 0, fmt.Errorf("seed: create theme page %s: %w", sp.TypeKey, err)
			}
		case err != nil:
			return 0, fmt.Errorf("seed: query theme page %s: %w", sp.TypeKey, err)
		default:
			if _, err := existing.Update().
				SetComponentKey(sp.ComponentKey).
				SetPageSchema(schema).
				Save(ctx); err != nil {
				return 0, fmt.Errorf("seed: update theme page %s: %w", sp.TypeKey, err)
			}
		}
	}
	return t.ID, nil
}

func seedDemoShop(ctx context.Context, tx *ent.Tx, themeID, merchantOwnerRoleID int, box *cryptbox.Box) error {
	const demoDomain = "demo.localhost"

	s, err := tx.Site.Query().Where(site.DomainEQ(demoDomain)).Only(ctx)
	if ent.IsNotFound(err) {
		s, err = tx.Site.Create().SetDomain(demoDomain).Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("seed: demo site: %w", err)
	}

	// Reuse the shop already mapped to the demo site if present.
	var shopID int
	mapping, err := tx.SiteShop.Query().Where(siteshop.SiteIDEQ(s.ID)).First(ctx)
	switch {
	case err == nil:
		shopID = mapping.ShopID
	case ent.IsNotFound(err):
		content, _ := json.Marshal(map[string]any{
			"header": map[string]any{
				"site_title": "Demo Shop",
				"nav": []any{
					map[string]any{"label": "首頁", "href": "/"},
					map[string]any{"label": "關於我們", "href": "/about"},
				},
			},
			"footer": map[string]any{"text": "© 2026 Demo Shop"},
		})
		shop, cerr := tx.Shop.Create().
			SetName("Demo Shop").
			SetThemeID(themeID).
			SetStatus(1).
			SetContentJSON(content).
			Save(ctx)
		if cerr != nil {
			return fmt.Errorf("seed: demo shop: %w", cerr)
		}
		shopID = shop.ID
		if _, cerr := tx.SiteShop.Create().
			SetSiteID(s.ID).
			SetShopID(shopID).
			SetIsPrimary(true).
			Save(ctx); cerr != nil { // path_prefix NULL → default shop of the site
			return fmt.Errorf("seed: demo site_shop: %w", cerr)
		}
	default:
		return fmt.Errorf("seed: query demo mapping: %w", err)
	}

	// Auto-created home page (spec page-management/Auto-created home page):
	// created as a draft; publishing goes through the API.
	hasHome, err := tx.Page.Query().
		Where(page.ShopIDEQ(shopID), page.SlugEQ("home")).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query home page: %w", err)
	}
	if !hasHome {
		draft, _ := json.Marshal(map[string]any{
			"sections": []any{
				map[string]any{"type": "hero", "title": "歡迎來到 Demo Shop", "subtitle": "多租戶 CMS 起步範例"},
				map[string]any{"type": "feature_grid", "title": "我們的特色", "items": []any{
					map[string]any{"title": "快速", "text": "Redis 快取一條龍"},
					map[string]any{"title": "彈性", "text": "Schema 驅動的頁面組合"},
				}},
				map[string]any{"type": "cta", "text": "想了解更多嗎？"},
			},
		})
		if _, err := tx.Page.Create().
			SetShopID(shopID).
			SetTypeKey("home").
			SetTitle("首頁").
			SetSlug("home").
			SetStatus(0).
			SetContentJSON(draft).
			Save(ctx); err != nil {
			return fmt.Errorf("seed: demo home page: %w", err)
		}
	}

	if err := seedDemoCatalog(ctx, tx, shopID); err != nil {
		return err
	}
	// change commerce-demo-seed: the four blocks below are what makes the full
	// commerce chain (browse → checkout → pay → ship → award → return →
	// clawback) runnable on seeded data alone. Each carries its own natural-key
	// guard rather than sharing seedDemoCatalog's (design D4), so a database
	// seeded before this change gets the missing rows on the next run.
	if err := seedDemoFloral(ctx, tx, shopID); err != nil {
		return err
	}
	if err := seedDemoNewArrivals(ctx, tx, shopID); err != nil {
		return err
	}
	if err := seedDemoShipping(ctx, tx, shopID); err != nil {
		return err
	}
	if err := seedDemoTiers(ctx, tx, shopID); err != nil {
		return err
	}
	if err := seedDemoStorefrontClient(ctx, tx, shopID); err != nil {
		return err
	}
	if err := seedDemoCustomer(ctx, tx, shopID); err != nil {
		return err
	}
	if err := seedDemoPaymentCredentials(ctx, tx, shopID, box); err != nil {
		return err
	}

	// Demo merchant owner account for admin-UI/RBAC testing.
	const ownerEmail = "demo-owner@example.com"
	owner, err := tx.User.Query().Where(user.EmailEQ(ownerEmail)).Only(ctx)
	if ent.IsNotFound(err) {
		hash, herr := auth.HashPassword("demo-owner-change-me")
		if herr != nil {
			return herr
		}
		owner, err = tx.User.Create().SetEmail(ownerEmail).SetPasswordHash(hash).SetStatus(1).Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("seed: demo owner: %w", err)
	}
	isMember, err := tx.ShopUser.Query().
		Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDEQ(owner.ID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !isMember {
		if _, err := tx.ShopUser.Create().SetShopID(shopID).SetUserID(owner.ID).Save(ctx); err != nil {
			return fmt.Errorf("seed: demo shop_user: %w", err)
		}
	}
	hasRole, err := tx.RoleUser.Query().
		Where(roleuser.UserIDEQ(owner.ID), roleuser.RoleIDEQ(merchantOwnerRoleID), roleuser.ShopIDEQ(shopID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !hasRole {
		if _, err := tx.RoleUser.Create().
			SetUserID(owner.ID).
			SetRoleID(merchantOwnerRoleID).
			SetShopID(shopID).
			Save(ctx); err != nil {
			return fmt.Errorf("seed: demo owner role: %w", err)
		}
	}
	return nil
}

// seedDemoCatalog provisions a small sample category + product/SKU set for
// the demo shop (change product-catalog), so local dev / E2E has non-empty
// catalog data to exercise. Idempotent: skipped entirely if the demo product
// slug already exists.
func seedDemoCatalog(ctx context.Context, tx *ent.Tx, shopID int) error {
	hasProduct, err := tx.Product.Query().
		Where(entproduct.ShopIDEQ(shopID), entproduct.SlugEQ("classic-runner")).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query demo product: %w", err)
	}
	if hasProduct {
		return nil
	}

	shoes, err := tx.Category.Create().
		SetShopID(shopID).SetName("鞋類").SetSlug("shoes").Save(ctx)
	if err != nil {
		return fmt.Errorf("seed: demo category shoes: %w", err)
	}
	if _, err := tx.Category.Create().
		SetShopID(shopID).SetName("跑鞋").SetSlug("running-shoes").SetParentID(shoes.ID).Save(ctx); err != nil {
		return fmt.Errorf("seed: demo category running-shoes: %w", err)
	}
	bags, err := tx.Category.Create().
		SetShopID(shopID).SetName("包款").SetSlug("bags").Save(ctx)
	if err != nil {
		return fmt.Errorf("seed: demo category bags: %w", err)
	}

	runner, err := tx.Product.Create().
		SetShopID(shopID).
		SetTitle("經典跑鞋").
		SetSlug("classic-runner").
		SetDescription("輕量緩震，日常訓練首選。").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("seed: demo product classic-runner: %w", err)
	}
	if _, err := tx.ProductCategory.Create().SetShopID(shopID).SetProductID(runner.ID).SetCategoryID(shoes.ID).Save(ctx); err != nil {
		return fmt.Errorf("seed: demo product_category: %w", err)
	}
	skus := []struct {
		code  string
		size  string
		color string
		price int64
		stock int32
	}{
		{"RUNNER-M-BLK", "M", "black", 1290, 30},
		{"RUNNER-L-BLK", "L", "black", 1290, 18},
	}
	for _, sk := range skus {
		options, _ := json.Marshal(map[string]any{"size": sk.size, "color": sk.color})
		if _, err := tx.ProductSKU.Create().
			SetShopID(shopID).
			SetProductID(runner.ID).
			SetSkuCode(sk.code).
			SetOptions(options).
			SetPriceAmount(sk.price).
			SetCurrency("TWD").
			SetStockQty(sk.stock).
			Save(ctx); err != nil {
			return fmt.Errorf("seed: demo sku %s: %w", sk.code, err)
		}
	}

	tote, err := tx.Product.Create().
		SetShopID(shopID).
		SetTitle("帆布托特包").
		SetSlug("canvas-tote").
		SetDescription("耐用帆布材質，日常通勤好夥伴。").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("seed: demo product canvas-tote: %w", err)
	}
	if _, err := tx.ProductCategory.Create().SetShopID(shopID).SetProductID(tote.ID).SetCategoryID(bags.ID).Save(ctx); err != nil {
		return fmt.Errorf("seed: demo product_category: %w", err)
	}
	toteOptions, _ := json.Marshal(map[string]any{"color": "beige"})
	if _, err := tx.ProductSKU.Create().
		SetShopID(shopID).
		SetProductID(tote.ID).
		SetSkuCode("TOTE-BEIGE").
		SetOptions(toteOptions).
		SetPriceAmount(890).
		SetCurrency("TWD").
		SetStockQty(40).
		Save(ctx); err != nil {
		return fmt.Errorf("seed: demo sku TOTE-BEIGE: %w", err)
	}

	// A draft product (spec Published-only public catalog endpoint — must
	// not appear on the public endpoint) so E2E/manual QA can verify the
	// draft/published boundary against real seeded data.
	if _, err := tx.Product.Create().
		SetShopID(shopID).
		SetTitle("即將上市：限量聯名鞋款").
		SetSlug("upcoming-collab").
		SetDescription("尚未公開的草稿商品，用於驗證草稿不會出現在公開端點。").
		SetStatus(0).
		Save(ctx); err != nil {
		return fmt.Errorf("seed: demo draft product: %w", err)
	}

	return nil
}

// ── change commerce-demo-seed ───────────────────────────────────────────
//
// Everything below exists so that `make seed-demo` alone is enough to run the
// full commerce chain. Before this change the demo shop had a catalog and
// nothing else: shipping_methods, member_tiers, publishable_keys,
// shop_allowed_origins and members were all empty, which meant three features
// that had already shipped could not be exercised on demo data at all
// (checkout with a shipping method, publishable-key tenant resolution, and the
// "a qualifying tier exists" half of the loyalty level recompute).

// demoFloralPrices mirrors ONDO 溫度花室's own pricing shape (design D3/D10):
// a base price per bouquet plus the size deltas S −400 / M 0 / L +600. The
// numbers are not decoration — they are load-bearing.
//
// The pre-existing demo catalog tops out at 1290, so a plausible order there
// awards 25 points (subtotal/100, floor). Any tier ladder a merchant would
// actually configure is therefore unreachable, and points.recomputeLevel
// answers "no qualifying tier" forever. With a bouquet at 3200–4400 a single
// single-item order clears the 30-point tier below, so BOTH the upgrade and
// the downgrade-on-return path are observable on seeded data.
//
// Keep these in step with demoTiers: changing one without the other silently
// removes the only reason either exists.
var demoFloralProducts = []struct {
	Title, Slug, Subtitle, Description string
	SKUPrefix                          string
	BasePrice                          int64
}{
	{
		Title:       "深紅｜週年束",
		Slug:        "anniversary-bouquet",
		Subtitle:    "Anniversary Bouquet",
		Description: "深紅玫瑰與酒紅葉材，為紀念日而生。可附手寫卡片。",
		SKUPrefix:   "BQ-ANNI",
		BasePrice:   3800,
	},
	{
		Title:       "白日｜洋甘菊",
		Slug:        "daisy-day-bouquet",
		Subtitle:    "Daisy Day Bouquet",
		Description: "洋甘菊與小花的組合，像午後的窗邊。可附手寫卡片。",
		SKUPrefix:   "BQ-DAISY",
		BasePrice:   3600,
	},
}

// demoFloralSizes are ONDO's own three sizes and price deltas. Stock is sized
// for repeated E2E runs, not for realism-at-any-cost (design D7): a returned
// order does NOT restore stock (only cancellation does), so every commerce E2E
// run permanently consumes a few units. These numbers allow well over a dozen
// runs before a re-seed is needed, while still reading like a florist's stock
// rather than a placeholder.
var demoFloralSizes = []struct {
	Suffix string
	Size   string
	Delta  int64
	Stock  int32
}{
	{"S", "S", -400, 24},
	{"M", "M", 0, 36},
	{"L", "L", 600, 18},
}

// seedDemoFloral adds the high-ticket, multi-size, gift-message-friendly
// product line the commerce chain needs. It does NOT touch the pre-existing
// shoes/bags catalog: those rows are seedDemoCatalog's own idempotency anchor
// and are referenced by product-catalog's spec examples (design D10).
//
// Idempotent on its own natural key so a database seeded before this change
// picks the block up on the next run — seedDemoCatalog's guard would have
// skipped it (design D4).
func seedDemoFloral(ctx context.Context, tx *ent.Tx, shopID int) error {
	hasFloral, err := tx.Product.Query().
		Where(entproduct.ShopIDEQ(shopID), entproduct.SlugEQ(demoFloralProducts[0].Slug)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query demo floral product: %w", err)
	}
	if hasFloral {
		return nil
	}

	// Looked up rather than blindly created: the block guard above is on the
	// product, and a partially hand-edited development database may already
	// hold the category.
	const floralSlug = "floral-gifts"
	floral, err := tx.Category.Query().
		Where(entcategory.ShopIDEQ(shopID), entcategory.SlugEQ(floralSlug)).
		Only(ctx)
	if ent.IsNotFound(err) {
		floral, err = tx.Category.Create().
			SetShopID(shopID).SetName("花藝禮品").SetSlug(floralSlug).Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("seed: demo category floral-gifts: %w", err)
	}

	for _, p := range demoFloralProducts {
		prod, err := tx.Product.Create().
			SetShopID(shopID).
			SetTitle(p.Title).
			SetSlug(p.Slug).
			SetSubtitle(p.Subtitle).
			SetDescription(p.Description).
			SetStatus(1).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("seed: demo product %s: %w", p.Slug, err)
		}
		if _, err := tx.ProductCategory.Create().
			SetShopID(shopID).SetProductID(prod.ID).SetCategoryID(floral.ID).Save(ctx); err != nil {
			return fmt.Errorf("seed: demo product_category %s: %w", p.Slug, err)
		}
		// Created in S → M → L order on purpose: catalog responses order SKUs
		// by id (change commerce-demo-seed, design D8), so creation order is
		// what a storefront's size selector will show.
		for _, sz := range demoFloralSizes {
			code := p.SKUPrefix + "-" + sz.Suffix
			options, _ := json.Marshal(map[string]any{"size": sz.Size})
			if _, err := tx.ProductSKU.Create().
				SetShopID(shopID).
				SetProductID(prod.ID).
				SetSkuCode(code).
				SetOptions(options).
				SetPriceAmount(p.BasePrice + sz.Delta).
				SetCurrency("TWD").
				SetStockQty(sz.Stock).
				Save(ctx); err != nil {
				return fmt.Errorf("seed: demo sku %s: %w", code, err)
			}
		}
	}
	return nil
}

// seedDemoNewArrivals provisions the 「本季新品」 category (change
// product-subtitle-public-categories, design D3 — a collection is a category,
// not a flag) and puts the two floral products in it. It also backfills the
// floral products' subtitle on databases seeded before the subtitle column
// existed, but only where the subtitle is still empty: a value a merchant has
// typed is theirs.
//
// Idempotent on its own natural keys (category slug; the (product, category)
// pair), so a database seeded before this change picks it up on the next run.
func seedDemoNewArrivals(ctx context.Context, tx *ent.Tx, shopID int) error {
	const slug = "new-arrivals"
	cat, err := tx.Category.Query().
		Where(entcategory.ShopIDEQ(shopID), entcategory.SlugEQ(slug)).
		Only(ctx)
	if ent.IsNotFound(err) {
		cat, err = tx.Category.Create().SetShopID(shopID).SetName("本季新品").SetSlug(slug).Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("seed: demo category %s: %w", slug, err)
	}
	for i, p := range demoFloralProducts {
		prod, err := tx.Product.Query().
			Where(entproduct.ShopIDEQ(shopID), entproduct.SlugEQ(p.Slug)).
			Only(ctx)
		if ent.IsNotFound(err) {
			continue // floral block absent (hand-edited dev DB): nothing to link
		}
		if err != nil {
			return fmt.Errorf("seed: query demo product %s: %w", p.Slug, err)
		}
		if prod.Subtitle == "" {
			if _, err := prod.Update().SetSubtitle(p.Subtitle).Save(ctx); err != nil {
				return fmt.Errorf("seed: demo subtitle %s: %w", p.Slug, err)
			}
		}
		linked, err := tx.ProductCategory.Query().
			Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.ProductIDEQ(prod.ID), entproductcategory.CategoryIDEQ(cat.ID)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("seed: query demo %s link: %w", slug, err)
		}
		if linked {
			continue
		}
		if _, err := tx.ProductCategory.Create().
			SetShopID(shopID).SetProductID(prod.ID).SetCategoryID(cat.ID).SetPosition(int32(i)).Save(ctx); err != nil {
			return fmt.Errorf("seed: demo %s link %s: %w", slug, p.Slug, err)
		}
	}
	return nil
}

// demoShippingMethods are ONDO's three real options plus one deliberately
// deactivated row.
//
// The inactive one is the same discipline as the draft product above: change
// order-extras requires that a deactivated method never appears on the public
// list and that checkout refuses it with 422. Without a deactivated row in the
// seed those two rules can only ever be checked against a test fixture — not
// against the data a merchant or an integrator actually sees (design D9).
var demoShippingMethods = []struct {
	Name     string
	Carrier  string
	FlatRate int64
	IsActive bool
}{
	{"台北市當日配送", "自有車隊", 200, true},
	{"全台隔日配送", "黑貓宅急便", 150, true},
	{"門市自取", "門市", 0, true},
	{"離島加價配送", "中華郵政", 350, false},
}

// seedDemoShipping provisions the demo shop's shipping methods.
//
// Idempotent per row on (shop_id, name) rather than as one block: the rows are
// independent of each other, so adding a method to this list later must reach
// databases that were already seeded — the same "existing ones stay, missing
// ones get created" shape seedStarterTheme uses for theme pages (design D4).
// Existing rows are never updated: flat_rate and is_active are merchant
// operational data, and re-running the seed must not undo a value someone
// changed in the back office.
func seedDemoShipping(ctx context.Context, tx *ent.Tx, shopID int) error {
	for _, sm := range demoShippingMethods {
		exists, err := tx.ShippingMethod.Query().
			Where(entshippingmethod.ShopIDEQ(shopID), entshippingmethod.NameEQ(sm.Name)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("seed: query demo shipping method %s: %w", sm.Name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ShippingMethod.Create().
			SetShopID(shopID).
			SetName(sm.Name).
			SetCarrier(sm.Carrier).
			SetFlatRate(sm.FlatRate).
			SetIsActive(sm.IsActive).
			Save(ctx); err != nil {
			return fmt.Errorf("seed: demo shipping method %s: %w", sm.Name, err)
		}
	}
	return nil
}

// demoTiers is the loyalty ladder, chosen together with demoFloralProducts
// (design D3) — see that comment for why the two cannot be picked separately.
//
//	一般會員   0 pts  — min_points 0 means anyone with any ledger activity has a
//	                   tier, which is the shape merchants actually configure.
//	                   The "no tier" state stays observable anyway: a customer
//	                   who has never earned a point still has level_id NULL.
//	銀卡會員  30 pts  — reachable by ONE bouquet (3200–4400 → 32–44 points), so
//	                   the upgrade path runs on a realistic order.
//	金卡會員 100 pts  — reachable by three, so the ladder has a top that is not
//	                   merely decorative.
//
// discount_percent is reserved and unused this phase (member-tiers-and-points
// Non-Goals); it is populated so the back office has something to display.
var demoTiers = []struct {
	Name            string
	MinPoints       int32
	DiscountPercent *int16
}{
	{"一般會員", 0, nil},
	{"銀卡會員", 30, ptr[int16](5)},
	{"金卡會員", 100, ptr[int16](10)},
}

func ptr[T any](v T) *T { return &v }

// seedDemoTiers provisions the demo shop's member tiers, idempotent per row on
// (shop_id, name) for the same reasons as seedDemoShipping.
func seedDemoTiers(ctx context.Context, tx *ent.Tx, shopID int) error {
	for _, t := range demoTiers {
		exists, err := tx.MemberTier.Query().
			Where(entmembertier.ShopIDEQ(shopID), entmembertier.NameEQ(t.Name)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("seed: query demo member tier %s: %w", t.Name, err)
		}
		if exists {
			continue
		}
		create := tx.MemberTier.Create().
			SetShopID(shopID).SetName(t.Name).SetMinPoints(t.MinPoints)
		if t.DiscountPercent != nil {
			create.SetDiscountPercent(*t.DiscountPercent)
		}
		if _, err := create.Save(ctx); err != nil {
			return fmt.Errorf("seed: demo member tier %s: %w", t.Name, err)
		}
	}
	return nil
}

// DemoPublishableKey is the demo shop's publishable key. Fixed, not random,
// and deliberately self-describing (design D5).
//
// Three reasons, weakest first:
//
//  1. Idempotency becomes exact — the natural key IS the value, so "already
//     there?" is one primary-key lookup. Keying it on `label` instead would
//     contradict that column's own schema comment ("purely descriptive — no
//     code path makes any decision from it").
//  2. It can be pasted into docs, curl commands and an integrator's front end
//     without first logging into the back office to fetch it.
//  3. (decisive) It widens nothing. A publishable key is an IDENTIFIER, NOT A
//     CREDENTIAL (change storefront-client-auth, design D1: "its disclosure is
//     not a security incident. It grants nothing"), and the demo shop is
//     already resolvable with no key at all via `Host: demo.localhost`. The
//     entropy in a generated key exists to stop enumeration of which shops the
//     platform hosts; "demo.localhost hosts a demo shop" is not a secret.
//
// The value uses only the random segment's own alphabet (a–z). It deliberately
// does NOT use an underscore-delimited segment like "pk_demo_…": design D8 of
// storefront-client-auth reserves that grammar for a future environment infix
// ("pk_test_…"), and "demo" sitting in that position would read as one.
//
// Exported so E2E and tooling can address the demo shop without a round trip.
const DemoPublishableKey = "pk_demoseedstorefrontkeynotsecret"

// DemoAllowedOrigin is registered for the demo shop so the per-shop CORS path
// (change storefront-client-auth) has real data behind it — the platform's own
// CORS_ALLOWED_ORIGINS covers localhost:3000/3001 but not the demo host.
const DemoAllowedOrigin = "http://demo.localhost:3000"

// seedDemoStorefrontClient provisions the demo shop's storefront client
// identity: one publishable key and one registered browser origin. Both are
// idempotent on their own globally-unique columns.
func seedDemoStorefrontClient(ctx context.Context, tx *ent.Tx, shopID int) error {
	hasKey, err := tx.PublishableKey.Query().
		Where(entpublishablekey.KeyEQ(DemoPublishableKey)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query demo publishable key: %w", err)
	}
	if !hasKey {
		if _, err := tx.PublishableKey.Create().
			SetShopID(shopID).
			SetKey(DemoPublishableKey).
			SetLabel("demo storefront (seed)").
			Save(ctx); err != nil {
			return fmt.Errorf("seed: demo publishable key: %w", err)
		}
	}

	hasOrigin, err := tx.ShopAllowedOrigin.Query().
		Where(entshopallowedorigin.OriginEQ(DemoAllowedOrigin)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query demo allowed origin: %w", err)
	}
	if !hasOrigin {
		if _, err := tx.ShopAllowedOrigin.Create().
			SetShopID(shopID).
			SetOrigin(DemoAllowedOrigin).
			Save(ctx); err != nil {
			return fmt.Errorf("seed: demo allowed origin: %w", err)
		}
	}
	return nil
}

// Demo customer credentials, same convention as the demo merchant owner above.
const (
	DemoCustomerEmail    = "demo-customer@example.com"
	DemoCustomerPassword = "demo-customer-change-me"
)

// seedDemoCustomer provisions a demo shop customer that can log in.
//
// Its purpose is not decoration: the member checkout path needs an identity,
// and having one already seeded means E2E does not have to call the register
// endpoint on every run — which would burn the registration rate-limit budget
// and make repeated runs flaky (design D7). It also gives the back office a
// customer to look at on a freshly seeded database.
//
// The query carries shop_id explicitly: seeding runs outside tenant context,
// so the interceptor adds nothing (change member-identity-per-shop, design D4).
func seedDemoCustomer(ctx context.Context, tx *ent.Tx, shopID int) error {
	exists, err := tx.Member.Query().
		Where(entmember.ShopIDEQ(shopID), entmember.EmailEQ(DemoCustomerEmail)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query demo customer: %w", err)
	}
	if exists {
		return nil
	}
	hash, err := auth.HashPassword(DemoCustomerPassword)
	if err != nil {
		return err
	}
	if _, err := tx.Member.Create().
		SetShopID(shopID).
		SetEmail(DemoCustomerEmail).
		SetPasswordHash(hash).
		SetStatus(1).
		Save(ctx); err != nil {
		return fmt.Errorf("seed: demo customer: %w", err)
	}
	return nil
}

// seedDemoPaymentCredentials gives the demo shop its own ecpay credentials
// (change ecpay-per-shop-credentials, design D9): the values used are 綠界's
// own published AIO test-store sample (the same ones that used to be the
// ECPAY_* platform-wide defaults before this change), so make seed-demo /
// e2e's ecpay flow behaves exactly as it did before per-shop credentials
// existed. Idempotent: `(shop_id, provider)` is a natural key.
func seedDemoPaymentCredentials(ctx context.Context, tx *ent.Tx, shopID int, box *cryptbox.Box) error {
	exists, err := tx.ShopPaymentProviderCredential.Query().
		Where(entpaymentcredential.ShopIDEQ(shopID), entpaymentcredential.ProviderEQ(ecpay.ProviderName)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("seed: query demo ecpay credentials: %w", err)
	}
	if exists {
		return nil
	}
	hashKeyCT, err := box.Encrypt("pwFHCqoQZGmho4w6")
	if err != nil {
		return fmt.Errorf("seed: encrypt demo ecpay hash key: %w", err)
	}
	hashIVCT, err := box.Encrypt("EkRm7iFT261dpevs")
	if err != nil {
		return fmt.Errorf("seed: encrypt demo ecpay hash iv: %w", err)
	}
	if _, err := tx.ShopPaymentProviderCredential.Create().
		SetShopID(shopID).
		SetProvider(ecpay.ProviderName).
		SetMerchantID("3002607").
		SetHashKeyCiphertext(hashKeyCT).
		SetHashIvCiphertext(hashIVCT).
		Save(ctx); err != nil {
		return fmt.Errorf("seed: demo ecpay credentials: %w", err)
	}
	return nil
}
