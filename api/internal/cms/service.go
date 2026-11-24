package cms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/ent"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
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

// Service implements page, shop-content and shop CRUD operations. Every write that
// affects rendering publishes its domain event after the write commits.
type Service struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher

	// imagePrefixes are the URL prefixes (each ending in "/") that CMS image
	// fields and rich text <img src> may use (change product-images-r2, design
	// D7). Empty = media not configured = rule not applied. Unexported on
	// purpose: it is legitimately empty in a deployment without media, which
	// the wiring audit would otherwise report as an unwired dependency.
	imagePrefixes []string
}

// RestrictImageDomain turns on the image-domain rule with the given prefixes
// (a trailing "/" is added when missing). Call once at wiring time.
func (s *Service) RestrictImageDomain(prefixes ...string) {
	s.imagePrefixes = nil
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		s.imagePrefixes = append(s.imagePrefixes, p)
	}
}

// schemas is the per-shop content schema store (change
// retire-hosted-storefront-and-themes group 1). It is derived from Client so
// every construction site of Service keeps working.
func (s *Service) schemas() *contentschema.Store {
	return &contentschema.Store{Client: s.Client}
}

// ImagePrefixes returns the configured image-domain prefixes (empty when media
// is not configured).
func (s *Service) ImagePrefixes() []string { return s.imagePrefixes }

// ── pages (merchant, tasks 6.5/6.6) ───────────────────────────────

type PageInput struct {
	TypeKey string          `json:"type_key"`
	Title   string          `json:"title"`
	Slug    string          `json:"slug"`
	Content json.RawMessage `json:"content"`
	Meta    json.RawMessage `json:"meta"`
}

// currentPageSchema resolves the page schema for (shop, typeKey) from the
// shop's own content schema (change retire-hosted-storefront-and-themes).
func (s *Service) currentPageSchema(ctx context.Context, shopID int, typeKey string) (json.RawMessage, error) {
	if _, err := s.Client.Shop.Get(ctx, shopID); err != nil {
		return nil, ErrNotFound
	}
	v, err := s.schemas().Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, contentschema.ErrNoSchema) {
			return nil, &ValidationError{Message: "shop has no content schema"}
		}
		return nil, err
	}
	schema, ok := v.Doc.Types[typeKey]
	if !ok {
		return nil, &ValidationError{
			Message: "type_key is not in the shop's content schema",
			Details: []Detail{{Pointer: "/type_key", Message: "unsupported page type"}},
		}
	}
	return schema, nil
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
	content, err := s.validateAndSanitize(schema, in.Content)
	if err != nil {
		return nil, err
	}
	in.Content = content
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
		content, err := s.validateAndSanitize(schema, in.Content)
		if err != nil {
			return nil, err
		}
		upd.SetContentJSON(content)
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
	// No sanitization here: content_json was sanitized when it was written
	// (CreatePage/UpdatePage), and publishing copies it verbatim — publish is
	// not new user input (change cms-rich-text-sanitization, task 2.3). Rows
	// written before that change are covered by cmd/sanitize-content.
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

// PageListEntry flags pages whose type_key is not in the shop's content schema.
type PageListEntry struct {
	*ent.Page
	Incompatible bool `json:"incompatible"`
}

func (s *Service) ListPages(ctx context.Context, shopID int) ([]PageListEntry, error) {
	if _, err := s.Client.Shop.Get(ctx, shopID); err != nil {
		return nil, ErrNotFound
	}
	types, err := s.schemas().Types(ctx, shopID)
	if err != nil {
		return nil, err
	}
	supported := map[string]bool{}
	for _, t := range types {
		supported[t.TypeKey] = true
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
		// Page exists but its type is not in the shop's content schema:
		// return it without a schema so the admin can still see/flag it.
		return p, nil, nil
	}
	return p, schema, nil
}

// ── shop global content (task 6.7) ────────────────────────────────

// UpdateShopContent validates against the shop's content schema (global document),
// saves immediately (Phase 1 沒有全域草稿), and bumps the tenant cache.
func (s *Service) UpdateShopContent(ctx context.Context, shopID int, content json.RawMessage) (*ent.Shop, error) {
	shop, err := s.Client.Shop.Get(ctx, shopID)
	if err != nil {
		return nil, ErrNotFound
	}
	globalSchema, ok, err := s.schemas().GlobalSchema(ctx, shopID)
	if err != nil {
		return nil, fmt.Errorf("cms: load content schema: %w", err)
	}
	if !ok {
		return nil, &ValidationError{Message: "shop has no content schema"}
	}
	if len(content) == 0 {
		content = json.RawMessage("{}")
	}
	content, err = s.validateAndSanitize(globalSchema, content)
	if err != nil {
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
	schema, _, err := s.schemas().GlobalSchema(ctx, shopID)
	if err != nil {
		return nil, nil, err
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
	Name   string `json:"name"`
	Status *int16 `json:"status"`
}

// createHomePage idempotently creates the shop's auto-created home page
// (spec page-management/Auto-created home page). It does not validate
// content against a page schema — a new shop has no content schema until one
// is registered — so a minimal empty-sections payload is used; normal editing
// goes through the PagesHandler once the shop has a schema.
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
// transaction (design D4).
func (s *Service) CreateShop(ctx context.Context, in ShopInput) (*ent.Shop, error) {
	name, status, err := s.validateShopInput(ctx, in)
	if err != nil {
		return nil, err
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("cms: begin tx: %w", err)
	}
	shop, err := createShopTx(ctx, tx, name, status)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cms: commit shop creation: %w", err)
	}
	return shop, nil
}

// CreateShopTx is CreateShop inside a transaction the caller owns (change
// platform-account-provisioning, design D8): the shop and its home page are
// written on tx, and committing or rolling back is the caller's job. Input is
// validated exactly as CreateShop does. It is the only other entry to the
// shop-creation body, so shop provisioning cannot drift from POST /admin/shops.
func (s *Service) CreateShopTx(ctx context.Context, tx *ent.Tx, in ShopInput) (*ent.Shop, error) {
	name, status, err := s.validateShopInput(ctx, in)
	if err != nil {
		return nil, err
	}
	return createShopTx(ctx, tx, name, status)
}

// validateShopInput returns the normalized name and status of a ShopInput, or a
// *ValidationError.
func (s *Service) validateShopInput(ctx context.Context, in ShopInput) (string, int16, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", 0, &ValidationError{
			Message: "name is required",
			Details: []Detail{{Pointer: "/name", Message: "required"}},
		}
	}
	status := int16(1)
	if in.Status != nil {
		if !validShopStatus(*in.Status) {
			return "", 0, &ValidationError{
				Message: "invalid status",
				Details: []Detail{{Pointer: "/status", Message: "must be 0, 1 or 2"}},
			}
		}
		status = *in.Status
	}
	return name, status, nil
}

// createShopTx is the transactional body shared by CreateShop and CreateShopTx.
func createShopTx(ctx context.Context, tx *ent.Tx, name string, status int16) (*ent.Shop, error) {
	shop, err := tx.Shop.Create().SetName(name).SetStatus(status).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, &ValidationError{Message: "shop could not be created"}
		}
		return nil, err
	}
	if err := createHomePage(ctx, tx, shop.ID); err != nil {
		return nil, err
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

// UpdateShop applies a partial update. If status actually changes, the
// caches embedding the status (publishable keys, render cache version) are
// invalidated through ShopStatusChanged so the new gating takes effect
// immediately.
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
		s.Dispatcher.Publish(ctx, events.ShopStatusChanged{ShopIDs: []int{shopID}})
	}
	return shop, nil
}
