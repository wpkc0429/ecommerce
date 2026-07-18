package cms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"ksdevworks/ecommerce/api/internal/ent"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	entsite "ksdevworks/ecommerce/api/internal/ent/site"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
	"ksdevworks/ecommerce/api/internal/events"
)

// ErrNotFound marks missing resources (handler → 404).
var ErrNotFound = errors.New("cms: not found")

// slugRe: lowercase alnum + hyphen (design D7 / spec page-management).
var slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// reservedSlugs may never be user-created. "home" is reserved separately —
// only the system creates it (建店自動建立).
var reservedSlugs = map[string]bool{
	"api": true, "admin": true, "preview": true, "_next": true,
	"assets": true, "static": true, "healthz": true,
}

// Service implements theme/page/shop-content operations. Every write that
// affects rendering publishes its domain event after the write commits.
type Service struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher
}

// ── themes (platform) ─────────────────────────────────────────────

type ThemeInput struct {
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	LayoutKey    string          `json:"layout_key"`
	ConfigSchema json.RawMessage `json:"config_schema"`
	IsActive     *bool           `json:"is_active"`
}

func (s *Service) CreateTheme(ctx context.Context, in ThemeInput) (*ent.Theme, error) {
	if in.Code == "" || in.Name == "" || in.LayoutKey == "" {
		return nil, &ValidationError{Message: "code, name and layout_key are required"}
	}
	if len(in.ConfigSchema) == 0 {
		in.ConfigSchema = json.RawMessage("{}")
	}
	if err := ValidateSchemaDoc(in.ConfigSchema); err != nil {
		return nil, err
	}
	create := s.Client.Theme.Create().
		SetCode(in.Code).
		SetName(in.Name).
		SetLayoutKey(in.LayoutKey).
		SetConfigSchema(in.ConfigSchema)
	if in.IsActive != nil {
		create.SetIsActive(*in.IsActive)
	}
	t, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, &ValidationError{Message: "theme code already exists"}
	}
	return t, err
}

// UpdateTheme applies partial updates; any change propagates to every shop on
// the theme via ThemeUpdated (spec theme-system/Theme update propagation).
func (s *Service) UpdateTheme(ctx context.Context, themeID int, in ThemeInput) (*ent.Theme, error) {
	t, err := s.Client.Theme.Get(ctx, themeID)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := t.Update()
	if in.Name != "" {
		upd.SetName(in.Name)
	}
	if in.LayoutKey != "" {
		upd.SetLayoutKey(in.LayoutKey)
	}
	if len(in.ConfigSchema) > 0 {
		if err := ValidateSchemaDoc(in.ConfigSchema); err != nil {
			return nil, err
		}
		upd.SetConfigSchema(in.ConfigSchema)
	}
	if in.IsActive != nil {
		upd.SetIsActive(*in.IsActive)
	}
	t, err = upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.ThemeUpdated{ThemeID: t.ID})
	return t, nil
}

type ThemePageInput struct {
	TypeKey      string          `json:"type_key"`
	ComponentKey string          `json:"component_key"`
	PageSchema   json.RawMessage `json:"page_schema"`
}

func (s *Service) CreateThemePage(ctx context.Context, themeID int, in ThemePageInput) (*ent.ThemePage, error) {
	if _, err := s.Client.Theme.Get(ctx, themeID); err != nil {
		return nil, ErrNotFound
	}
	if in.TypeKey == "" || in.ComponentKey == "" {
		return nil, &ValidationError{Message: "type_key and component_key are required"}
	}
	if len(in.PageSchema) == 0 {
		in.PageSchema = json.RawMessage("{}")
	}
	if err := ValidateSchemaDoc(in.PageSchema); err != nil {
		return nil, err
	}
	tp, err := s.Client.ThemePage.Create().
		SetThemeID(themeID).
		SetTypeKey(in.TypeKey).
		SetComponentKey(in.ComponentKey).
		SetPageSchema(in.PageSchema).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// Spec theme-system/Theme page type registry: 重複 type_key 被拒 → 422.
		return nil, &ValidationError{Message: "type_key already exists in this theme"}
	}
	if err != nil {
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.ThemeUpdated{ThemeID: themeID})
	return tp, nil
}

func (s *Service) UpdateThemePage(ctx context.Context, themeID, themePageID int, in ThemePageInput) (*ent.ThemePage, error) {
	tp, err := s.Client.ThemePage.Query().
		Where(themepage.IDEQ(themePageID), themepage.ThemeIDEQ(themeID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := tp.Update()
	if in.ComponentKey != "" {
		upd.SetComponentKey(in.ComponentKey)
	}
	if len(in.PageSchema) > 0 {
		if err := ValidateSchemaDoc(in.PageSchema); err != nil {
			return nil, err
		}
		upd.SetPageSchema(in.PageSchema)
	}
	tp, err = upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.ThemeUpdated{ThemeID: themeID})
	return tp, nil
}

func (s *Service) DeleteThemePage(ctx context.Context, themeID, themePageID int) error {
	n, err := s.Client.ThemePage.Delete().
		Where(themepage.IDEQ(themePageID), themepage.ThemeIDEQ(themeID)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.Dispatcher.Publish(ctx, events.ThemeUpdated{ThemeID: themeID})
	return nil
}

// ── theme switching (merchant, task 6.4) ──────────────────────────

// IncompatiblePage describes a page the target theme does not support.
type IncompatiblePage struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Slug    string `json:"slug"`
	TypeKey string `json:"type_key"`
}

// PrecheckTheme lists pages whose type_key the target theme lacks
// (spec theme-system/Theme switching with compatibility precheck).
func (s *Service) PrecheckTheme(ctx context.Context, shopID, themeID int) ([]IncompatiblePage, error) {
	supported, err := s.Client.ThemePage.Query().
		Where(themepage.ThemeIDEQ(themeID)).
		Select(themepage.FieldTypeKey).
		Strings(ctx)
	if err != nil {
		return nil, err
	}
	supportedSet := map[string]bool{}
	for _, k := range supported {
		supportedSet[k] = true
	}
	pages, err := s.Client.Page.Query().Where(entpage.ShopIDEQ(shopID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := []IncompatiblePage{}
	for _, p := range pages {
		if !supportedSet[p.TypeKey] {
			out = append(out, IncompatiblePage{ID: p.ID, Title: p.Title, Slug: p.Slug, TypeKey: p.TypeKey})
		}
	}
	return out, nil
}

// SwitchTheme applies an active theme to the shop, returning the
// incompatibility list. The switch itself always proceeds (換版即時生效);
// unsupported pages 404 on the storefront and are flagged in the admin list.
func (s *Service) SwitchTheme(ctx context.Context, shopID, themeID int) ([]IncompatiblePage, error) {
	t, err := s.Client.Theme.Get(ctx, themeID)
	if err != nil {
		return nil, &ValidationError{Message: "theme does not exist"}
	}
	if !t.IsActive {
		// Spec theme-system/Theme activation gating: 套用未開放主題被拒.
		return nil, &ValidationError{Message: "theme is not active"}
	}
	incompatible, err := s.PrecheckTheme(ctx, shopID, themeID)
	if err != nil {
		return nil, err
	}
	if _, err := s.Client.Shop.UpdateOneID(shopID).SetThemeID(themeID).Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.ThemeSwitched{ShopID: shopID})
	return incompatible, nil
}

// ── pages (merchant, tasks 6.5/6.6) ───────────────────────────────

type PageInput struct {
	TypeKey string          `json:"type_key"`
	Title   string          `json:"title"`
	Slug    string          `json:"slug"`
	Content json.RawMessage `json:"content"`
	Meta    json.RawMessage `json:"meta"`
}

// currentPageSchema resolves the page_schema for (shop's theme, typeKey).
func (s *Service) currentPageSchema(ctx context.Context, shopID int, typeKey string) (json.RawMessage, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, ErrNotFound
	}
	if shop.ThemeID == nil {
		return nil, &ValidationError{Message: "shop has no theme applied"}
	}
	tp, err := s.Client.ThemePage.Query().
		Where(themepage.ThemeIDEQ(*shop.ThemeID), themepage.TypeKeyEQ(typeKey)).
		Only(ctx)
	if err != nil {
		return nil, &ValidationError{
			Message: "type_key is not supported by the shop's current theme",
			Details: []Detail{{Pointer: "/type_key", Message: "unsupported page type"}},
		}
	}
	return tp.PageSchema, nil
}

func normalizeSlug(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if !slugRe.MatchString(slug) {
		return "", &ValidationError{
			Message: "invalid slug",
			Details: []Detail{{Pointer: "/slug", Message: "must match ^[a-z0-9-]+$"}},
		}
	}
	if slug == "home" || reservedSlugs[slug] {
		return "", &ValidationError{
			Message: "slug is reserved",
			Details: []Detail{{Pointer: "/slug", Message: "reserved slug"}},
		}
	}
	return slug, nil
}

func (s *Service) CreatePage(ctx context.Context, shopID int, in PageInput) (*ent.Page, error) {
	slug, err := normalizeSlug(in.Slug)
	if err != nil {
		return nil, err
	}
	schema, err := s.currentPageSchema(ctx, shopID, in.TypeKey)
	if err != nil {
		return nil, err
	}
	if len(in.Content) == 0 {
		in.Content = json.RawMessage("{}")
	}
	if err := ValidatePayload(schema, in.Content); err != nil {
		return nil, err
	}
	if len(in.Meta) == 0 {
		in.Meta = json.RawMessage("{}")
	}

	create := s.Client.Page.Create().
		SetShopID(shopID).
		SetTypeKey(in.TypeKey).
		SetTitle(in.Title).
		SetSlug(slug).
		SetStatus(0).
		SetContentJSON(in.Content).
		SetMeta(in.Meta)
	p, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		// Spec page-management: 同店 slug 重複 → 422.
		return nil, &ValidationError{
			Message: "slug already exists in this shop",
			Details: []Detail{{Pointer: "/slug", Message: "duplicate slug"}},
		}
	}
	return p, err
}

type PageUpdate struct {
	Title   *string         `json:"title"`
	Slug    *string         `json:"slug"`
	Content json.RawMessage `json:"content"`
	Meta    json.RawMessage `json:"meta"`
}

// UpdatePage writes the working copy only (design D7 — 不影響線上).
func (s *Service) UpdatePage(ctx context.Context, shopID, pageID int, in PageUpdate) (*ent.Page, error) {
	p, err := s.Client.Page.Query().
		Where(entpage.IDEQ(pageID), entpage.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := p.Update()
	if in.Title != nil {
		upd.SetTitle(*in.Title)
	}
	if in.Slug != nil && *in.Slug != p.Slug {
		if p.Slug == "home" {
			return nil, &ValidationError{Message: "the home page slug cannot change"}
		}
		slug, err := normalizeSlug(*in.Slug)
		if err != nil {
			return nil, err
		}
		upd.SetSlug(slug)
	}
	if len(in.Content) > 0 {
		schema, err := s.currentPageSchema(ctx, shopID, p.TypeKey)
		if err != nil {
			return nil, err
		}
		if err := ValidatePayload(schema, in.Content); err != nil {
			return nil, err
		}
		upd.SetContentJSON(in.Content)
	}
	if len(in.Meta) > 0 {
		upd.SetMeta(in.Meta)
	}
	p, err = upd.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, &ValidationError{Message: "slug already exists in this shop"}
	}
	return p, err
}

// PublishPage validates the working copy, snapshots it to published_json and
// invalidates the page cache (design D7).
func (s *Service) PublishPage(ctx context.Context, shopID, pageID int) (*ent.Page, error) {
	p, err := s.Client.Page.Query().
		Where(entpage.IDEQ(pageID), entpage.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	schema, err := s.currentPageSchema(ctx, shopID, p.TypeKey)
	if err != nil {
		return nil, err
	}
	if err := ValidatePayload(schema, p.ContentJSON); err != nil {
		return nil, err
	}
	p, err = p.Update().
		SetPublishedJSON(p.ContentJSON).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.PagePublished{ShopID: shopID, Slug: p.Slug})
	return p, nil
}

// UnpublishPage takes the page offline (前台 404).
func (s *Service) UnpublishPage(ctx context.Context, shopID, pageID int) (*ent.Page, error) {
	p, err := s.Client.Page.Query().
		Where(entpage.IDEQ(pageID), entpage.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	p, err = p.Update().SetStatus(0).Save(ctx)
	if err != nil {
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.PageUnpublished{ShopID: shopID, Slug: p.Slug})
	return p, nil
}

func (s *Service) DeletePage(ctx context.Context, shopID, pageID int) error {
	p, err := s.Client.Page.Query().
		Where(entpage.IDEQ(pageID), entpage.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return ErrNotFound
	}
	if p.Slug == "home" {
		return &ValidationError{Message: "the home page cannot be deleted"}
	}
	if err := s.Client.Page.DeleteOne(p).Exec(ctx); err != nil {
		return err
	}
	if p.Status == 1 {
		s.Dispatcher.Publish(ctx, events.PageUnpublished{ShopID: shopID, Slug: p.Slug})
	}
	return nil
}

// PageListEntry flags pages unsupported by the current theme (換版後於後台
// 標示「不受新主題支援」).
type PageListEntry struct {
	*ent.Page
	Incompatible bool `json:"incompatible"`
}

func (s *Service) ListPages(ctx context.Context, shopID int) ([]PageListEntry, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, ErrNotFound
	}
	supported := map[string]bool{}
	if shop.ThemeID != nil {
		keys, err := s.Client.ThemePage.Query().
			Where(themepage.ThemeIDEQ(*shop.ThemeID)).
			Select(themepage.FieldTypeKey).
			Strings(ctx)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			supported[k] = true
		}
	}
	pages, err := s.Client.Page.Query().
		Where(entpage.ShopIDEQ(shopID)).
		Order(entpage.ByID()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PageListEntry, 0, len(pages))
	for _, p := range pages {
		out = append(out, PageListEntry{Page: p, Incompatible: !supported[p.TypeKey]})
	}
	return out, nil
}

func (s *Service) GetPage(ctx context.Context, shopID, pageID int) (*ent.Page, json.RawMessage, error) {
	p, err := s.Client.Page.Query().
		Where(entpage.IDEQ(pageID), entpage.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	schema, err := s.currentPageSchema(ctx, shopID, p.TypeKey)
	if err != nil {
		// Page exists but its type is unsupported by the current theme:
		// return it without a schema so the admin can still see/flag it.
		return p, nil, nil
	}
	return p, schema, nil
}

// ── shop global content (task 6.7) ────────────────────────────────

// UpdateShopContent validates against the current theme's config_schema,
// saves immediately (Phase 1 沒有全域草稿), and bumps the tenant cache.
func (s *Service) UpdateShopContent(ctx context.Context, shopID int, content json.RawMessage) (*ent.Shop, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, ErrNotFound
	}
	if shop.ThemeID == nil {
		return nil, &ValidationError{Message: "shop has no theme applied"}
	}
	theme, err := s.Client.Theme.Get(ctx, *shop.ThemeID)
	if err != nil {
		return nil, fmt.Errorf("cms: load theme: %w", err)
	}
	if len(content) == 0 {
		content = json.RawMessage("{}")
	}
	if err := ValidatePayload(theme.ConfigSchema, content); err != nil {
		return nil, err
	}
	shop, err = shop.Update().SetContentJSON(content).Save(ctx)
	if err != nil {
		return nil, err
	}
	s.Dispatcher.Publish(ctx, events.ShopContentUpdated{ShopID: shopID})
	return shop, nil
}

// GetShopContent returns the raw content plus the schema (admin editor input).
func (s *Service) GetShopContent(ctx context.Context, shopID int) (json.RawMessage, json.RawMessage, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	var schema json.RawMessage
	if shop.ThemeID != nil {
		if theme, err := s.Client.Theme.Get(ctx, *shop.ThemeID); err == nil {
			schema = theme.ConfigSchema
		}
	}
	return shop.ContentJSON, schema, nil
}

// ── shops (platform, this change) ─────────────────────────────────

// validShopStatus reports whether v is one of the shops.status values defined
// by spec multi-tenancy/Shop status gating (0 停用/1 啟用/2 審核中).
func validShopStatus(v int16) bool {
	return v == 0 || v == 1 || v == 2
}

// ShopInput is the payload of platform shop creation (design D4).
type ShopInput struct {
	Name    string `json:"name"`
	ThemeID *int   `json:"theme_id"`
	Status  *int16 `json:"status"`
}

// createHomePage idempotently creates the shop's auto-created home page
// (spec page-management/Auto-created home page). It does not validate
// content against a page_schema — the shop may not have a theme applied yet
// (design D4) — so a minimal empty-sections payload is used; normal editing
// goes through the existing PagesHandler once a theme is applied.
func createHomePage(ctx context.Context, tx *ent.Tx, shopID int) error {
	exists, err := tx.Page.Query().
		Where(entpage.ShopIDEQ(shopID), entpage.SlugEQ("home")).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("cms: query home page: %w", err)
	}
	if exists {
		return nil
	}
	_, err = tx.Page.Create().
		SetShopID(shopID).
		SetTypeKey("home").
		SetTitle("首頁").
		SetSlug("home").
		SetStatus(0).
		SetContentJSON(json.RawMessage(`{"sections":[]}`)).
		SetMeta(json.RawMessage("{}")).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("cms: create home page: %w", err)
	}
	return nil
}

// CreateShop creates a shop and its auto-created home page in one
// transaction (design D4). theme_id is optional: a shop can be created
// without a theme and have one applied later via the existing theme-switch
// flow.
func (s *Service) CreateShop(ctx context.Context, in ShopInput) (*ent.Shop, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, &ValidationError{
			Message: "name is required",
			Details: []Detail{{Pointer: "/name", Message: "required"}},
		}
	}
	status := int16(1)
	if in.Status != nil {
		if !validShopStatus(*in.Status) {
			return nil, &ValidationError{
				Message: "invalid status",
				Details: []Detail{{Pointer: "/status", Message: "must be 0, 1 or 2"}},
			}
		}
		status = *in.Status
	}
	if in.ThemeID != nil {
		if _, err := s.Client.Theme.Get(ctx, *in.ThemeID); err != nil {
			return nil, &ValidationError{
				Message: "theme does not exist",
				Details: []Detail{{Pointer: "/theme_id", Message: "unknown theme"}},
			}
		}
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("cms: begin tx: %w", err)
	}
	create := tx.Shop.Create().SetName(name).SetStatus(status)
	if in.ThemeID != nil {
		create.SetThemeID(*in.ThemeID)
	}
	shop, err := create.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return nil, &ValidationError{Message: "shop could not be created"}
		}
		return nil, err
	}
	if err := createHomePage(ctx, tx, shop.ID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cms: commit shop creation: %w", err)
	}
	return shop, nil
}

// ShopListParams are the (validated/normalized) pagination inputs of
// ListShops (design D3).
type ShopListParams struct {
	Page     int
	PageSize int
}

// ShopPage is a page of shops plus pagination metadata (design D3).
type ShopPage struct {
	Shops    []*ent.Shop
	Total    int
	Page     int
	PageSize int
}

// ListShops returns a page of shops ordered by id (design D3). Out-of-range
// pagination inputs are normalized rather than rejected.
func (s *Service) ListShops(ctx context.Context, params ShopListParams) (*ShopPage, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}

	total, err := s.Client.Shop.Query().Count(ctx)
	if err != nil {
		return nil, err
	}
	shops, err := s.Client.Shop.Query().
		Order(entshop.ByID()).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return &ShopPage{Shops: shops, Total: total, Page: page, PageSize: pageSize}, nil
}

// GetShop returns a single shop by id.
func (s *Service) GetShop(ctx context.Context, shopID int) (*ent.Shop, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, ErrNotFound
	}
	return shop, nil
}

// ShopUpdate is a partial update of a shop's platform-managed fields
// (design D2 — name/status/meta; content_json stays the merchant-facing
// editor's responsibility via UpdateShopContent).
type ShopUpdate struct {
	Name   *string         `json:"name"`
	Status *int16          `json:"status"`
	Meta   json.RawMessage `json:"meta"`
}

// UpdateShop applies a partial update. If status actually changes, the route
// resolution cache of every domain bound to this shop is invalidated so the
// new gating takes effect immediately (design D5) instead of waiting out the
// route cache TTL.
func (s *Service) UpdateShop(ctx context.Context, shopID int, in ShopUpdate) (*ent.Shop, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := shop.Update()
	statusChanged := false
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, &ValidationError{
				Message: "name cannot be empty",
				Details: []Detail{{Pointer: "/name", Message: "required"}},
			}
		}
		upd.SetName(name)
	}
	if in.Status != nil {
		if !validShopStatus(*in.Status) {
			return nil, &ValidationError{
				Message: "invalid status",
				Details: []Detail{{Pointer: "/status", Message: "must be 0, 1 or 2"}},
			}
		}
		if *in.Status != shop.Status {
			statusChanged = true
			upd.SetStatus(*in.Status)
		}
	}
	if len(in.Meta) > 0 {
		upd.SetMeta(in.Meta)
	}
	shop, err = upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	if statusChanged {
		// Best-effort: even if the domain lookup fails, still publish with
		// ShopIDs so the render cache version bump (design D8) is not
		// skipped — only the route-cache host invalidation degrades.
		domains, _ := shop.QuerySites().Select(entsite.FieldDomain).Strings(ctx)
		s.Dispatcher.Publish(ctx, events.SiteMappingChanged{Hosts: domains, ShopIDs: []int{shopID}})
	}
	return shop, nil
}
