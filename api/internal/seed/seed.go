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
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/page"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	entproduct "ksdevworks/ecommerce/api/internal/ent/product"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/rolepermission"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/site"
	"ksdevworks/ecommerce/api/internal/ent/siteshop"
	"ksdevworks/ecommerce/api/internal/ent/theme"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
	"ksdevworks/ecommerce/api/internal/ent/user"
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
}

// PermissionCatalog is the Phase 1 permission node catalog (shop.*, page.*,
// user.*, theme.*).
var PermissionCatalog = []struct{ Name, Description string }{
	{"shop.view", "檢視商家設定與全域內容"},
	{"shop.create", "建立商家（平台層）"},
	{"shop.list", "列出所有商家（平台層）"},
	{"shop.update", "更新商家全域內容與切換主題"},
	{"shop.manage_domains", "管理站點網域與 site-shop 綁定（平台層）"},
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
	}},
	{"editor", "merchant", []string{
		"shop.view", "page.view", "page.create", "page.edit", "theme.view",
		"category.view", "category.create", "category.edit",
		"product.view", "product.create", "product.edit",
		"order.view",
		"payment.view",
		"shipping_method.view", "shipping_method.create", "shipping_method.edit",
		"shipment.view", "shipment.create", "shipment.update",
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
		if err = seedDemoShop(ctx, tx, starterID, roles["merchant_owner"]); err != nil {
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

func seedDemoShop(ctx context.Context, tx *ent.Tx, themeID, merchantOwnerRoleID int) error {
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
