// Package catalog implements the product-catalog domain (change
// product-catalog): shop-scoped categories, products and their SKUs.
// Structured like api/internal/cms — one Service with ValidationError/
// ErrNotFound — but deliberately does not import cms to avoid coupling two
// otherwise-independent domains for the sake of superficial code reuse.
//
// Unlike cms.Service, this Service has no events.Dispatcher: nothing in this
// change caches catalog reads (design D8 — the public endpoint queries the
// DB directly), so there is nothing to invalidate yet. A later proposal that
// adds catalog caching can add the dispatcher then.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entcategory "ksdevworks/ecommerce/api/internal/ent/category"
	entproduct "ksdevworks/ecommerce/api/internal/ent/product"
	entproductcategory "ksdevworks/ecommerce/api/internal/ent/productcategory"
	entproductimage "ksdevworks/ecommerce/api/internal/ent/productimage"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
)

// ErrNotFound marks missing resources (handler → 404).
var ErrNotFound = errors.New("catalog: not found")

// slugRe: lowercase alnum + hyphen, same convention as cms/pages (spec
// page-management, reused here for product/category slugs).
var slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Detail locates one validation problem (JSON Pointer-ish; catalog inputs
// are flat so pointers are just "/field").
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12 error envelope).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

// ConflictError carries 409 payloads — used by the category deletion guard
// (design D4 RESTRICT: non-empty categories cannot be deleted).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// Service implements category/product/SKU operations, all shop-scoped by an
// explicit shopID parameter (never ambient tenant context — admin routes
// don't set tenant.WithShopID; that's reserved for the public storefront
// path, same convention as cms.Service).
type Service struct {
	Client *ent.Client
}

func normalizeSlug(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if !slugRe.MatchString(slug) {
		return "", &ValidationError{
			Message: "invalid slug",
			Details: []Detail{{Pointer: "/slug", Message: "must match ^[a-z0-9-]+$"}},
		}
	}
	return slug, nil
}

// ── categories ─────────────────────────────────────────────────────

// CategoryInput is the payload of category creation.
type CategoryInput struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	ParentID *int   `json:"parent_id"`
	Position *int32 `json:"position"`
}

// CategoryUpdate is a partial update. ParentID/ClearParent distinguish three
// states client JSON can't express with a single nullable field: omit both
// to leave parent_id unchanged, set ParentID to reparent, set ClearParent to
// move the category to the root.
type CategoryUpdate struct {
	Name        *string `json:"name"`
	Slug        *string `json:"slug"`
	ParentID    *int    `json:"parent_id"`
	ClearParent bool    `json:"clear_parent"`
	Position    *int32  `json:"position"`
}

func (s *Service) validateParent(ctx context.Context, shopID, parentID int) error {
	exists, err := s.Client.Category.Query().
		Where(entcategory.IDEQ(parentID), entcategory.ShopIDEQ(shopID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("catalog: query parent category: %w", err)
	}
	if !exists {
		return &ValidationError{
			Message: "parent category does not exist in this shop",
			Details: []Detail{{Pointer: "/parent_id", Message: "unknown parent category"}},
		}
	}
	return nil
}

func (s *Service) CreateCategory(ctx context.Context, shopID int, in CategoryInput) (*ent.Category, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, &ValidationError{Message: "name is required", Details: []Detail{{Pointer: "/name", Message: "required"}}}
	}
	slug, err := normalizeSlug(in.Slug)
	if err != nil {
		return nil, err
	}
	if in.ParentID != nil {
		if err := s.validateParent(ctx, shopID, *in.ParentID); err != nil {
			return nil, err
		}
	}
	create := s.Client.Category.Create().SetShopID(shopID).SetName(name).SetSlug(slug)
	if in.ParentID != nil {
		create.SetParentID(*in.ParentID)
	}
	if in.Position != nil {
		create.SetPosition(*in.Position)
	}
	c, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, &ValidationError{Message: "category name or slug already exists in this shop"}
	}
	return c, err
}

// wouldCreateCycle reports whether reparenting categoryID under newParentID
// would make categoryID its own ancestor (design D3). Walks the ancestor
// chain from newParentID upward.
func (s *Service) wouldCreateCycle(ctx context.Context, shopID, categoryID, newParentID int) (bool, error) {
	if categoryID == newParentID {
		return true, nil
	}
	cur := newParentID
	for {
		c, err := s.Client.Category.Query().
			Where(entcategory.IDEQ(cur), entcategory.ShopIDEQ(shopID)).
			Only(ctx)
		if err != nil {
			return false, err
		}
		if c.ParentID == nil {
			return false, nil
		}
		if *c.ParentID == categoryID {
			return true, nil
		}
		cur = *c.ParentID
	}
}

func (s *Service) UpdateCategory(ctx context.Context, shopID, categoryID int, in CategoryUpdate) (*ent.Category, error) {
	c, err := s.Client.Category.Query().
		Where(entcategory.IDEQ(categoryID), entcategory.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := c.Update()
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, &ValidationError{Message: "name cannot be empty", Details: []Detail{{Pointer: "/name", Message: "required"}}}
		}
		upd.SetName(name)
	}
	if in.Slug != nil {
		slug, err := normalizeSlug(*in.Slug)
		if err != nil {
			return nil, err
		}
		upd.SetSlug(slug)
	}
	if in.ClearParent {
		upd.ClearParentID()
	} else if in.ParentID != nil {
		if _, err := s.Client.Category.Query().
			Where(entcategory.IDEQ(*in.ParentID), entcategory.ShopIDEQ(shopID)).
			Only(ctx); ent.IsNotFound(err) {
			return nil, &ValidationError{
				Message: "parent category does not exist in this shop",
				Details: []Detail{{Pointer: "/parent_id", Message: "unknown parent category"}},
			}
		} else if err != nil {
			return nil, fmt.Errorf("catalog: query parent category: %w", err)
		}
		cycle, err := s.wouldCreateCycle(ctx, shopID, categoryID, *in.ParentID)
		if err != nil {
			return nil, fmt.Errorf("catalog: cycle check: %w", err)
		}
		if cycle {
			return nil, &ValidationError{
				Message: "this reparenting would create a category cycle",
				Details: []Detail{{Pointer: "/parent_id", Message: "would create a cycle"}},
			}
		}
		upd.SetParentID(*in.ParentID)
	}
	if in.Position != nil {
		upd.SetPosition(*in.Position)
	}
	c, err = upd.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, &ValidationError{Message: "category name or slug already exists in this shop"}
	}
	return c, err
}

// DeleteCategory enforces the RESTRICT deletion guard (design D4): a
// category with children or product associations cannot be deleted.
func (s *Service) DeleteCategory(ctx context.Context, shopID, categoryID int) error {
	c, err := s.Client.Category.Query().
		Where(entcategory.IDEQ(categoryID), entcategory.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return ErrNotFound
	}
	hasChildren, err := s.Client.Category.Query().
		Where(entcategory.ShopIDEQ(shopID), entcategory.ParentIDEQ(categoryID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("catalog: query children: %w", err)
	}
	if hasChildren {
		return &ConflictError{Message: "category has child categories; move or delete them first"}
	}
	hasProducts, err := s.Client.ProductCategory.Query().
		Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.CategoryIDEQ(categoryID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("catalog: query product associations: %w", err)
	}
	if hasProducts {
		return &ConflictError{Message: "category has products assigned; remove them first"}
	}
	return s.Client.Category.DeleteOne(c).Exec(ctx)
}

func (s *Service) GetCategory(ctx context.Context, shopID, categoryID int) (*ent.Category, error) {
	c, err := s.Client.Category.Query().
		Where(entcategory.IDEQ(categoryID), entcategory.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return c, nil
}

// ListCategories returns the flat list of a shop's categories, ordered by
// position then id; callers reconstruct the tree via BuildCategoryTree.
func (s *Service) ListCategories(ctx context.Context, shopID int) ([]*ent.Category, error) {
	return s.Client.Category.Query().
		Where(entcategory.ShopIDEQ(shopID)).
		Order(entcategory.ByPosition(), entcategory.ByID()).
		All(ctx)
}

// CategoryNode is a tree-assembled category (design D3 — assembled in Go,
// not a recursive SQL CTE; see design.md for why).
type CategoryNode struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	Slug     string          `json:"slug"`
	ParentID *int            `json:"parent_id"`
	Position int32           `json:"position"`
	Children []*CategoryNode `json:"children"`
}

// BuildCategoryTree assembles a flat category list into a forest of roots.
// Categories whose parent_id points outside the given list (should not
// happen within one shop, but defensive) are treated as roots too.
func BuildCategoryTree(cats []*ent.Category) []*CategoryNode {
	nodes := make(map[int]*CategoryNode, len(cats))
	for _, c := range cats {
		nodes[c.ID] = &CategoryNode{
			ID: c.ID, Name: c.Name, Slug: c.Slug, ParentID: c.ParentID, Position: c.Position,
			Children: []*CategoryNode{},
		}
	}
	roots := make([]*CategoryNode, 0, len(cats))
	for _, c := range cats {
		n := nodes[c.ID]
		if c.ParentID != nil {
			if p, ok := nodes[*c.ParentID]; ok {
				p.Children = append(p.Children, n)
				continue
			}
		}
		roots = append(roots, n)
	}
	return roots
}

// ── products & SKUs ────────────────────────────────────────────────

// SKUInput is one SKU within a product create/update request (design D1 —
// price_amount is an integer minor-unit BIGINT, never float). ID is only
// meaningful on update: nil means "create a new SKU", set means "update the
// existing SKU with this id" (design/spec — nested upsert).
type SKUInput struct {
	ID          *int            `json:"id"`
	SKUCode     string          `json:"sku_code"`
	Options     json.RawMessage `json:"options"`
	PriceAmount int64           `json:"price_amount"`
	Currency    string          `json:"currency"`
	StockQty    int32           `json:"stock_qty"`
	IsActive    *bool           `json:"is_active"`
}

func validateSKUInput(in SKUInput) error {
	if strings.TrimSpace(in.SKUCode) == "" {
		return &ValidationError{Message: "sku_code is required", Details: []Detail{{Pointer: "/sku_code", Message: "required"}}}
	}
	if in.PriceAmount < 0 {
		return &ValidationError{Message: "price_amount must not be negative", Details: []Detail{{Pointer: "/price_amount", Message: "must be >= 0"}}}
	}
	if in.StockQty < 0 {
		return &ValidationError{Message: "stock_qty must not be negative", Details: []Detail{{Pointer: "/stock_qty", Message: "must be >= 0"}}}
	}
	return nil
}

// ProductInput is the payload of product creation, including nested SKUs and
// the (possibly empty) set of category associations (design D2 M2M).
type ProductInput struct {
	Title       string          `json:"title"`
	Slug        string          `json:"slug"`
	Subtitle    string          `json:"subtitle"`
	Description string          `json:"description"`
	Status      *int16          `json:"status"`
	Meta        json.RawMessage `json:"meta"`
	CategoryIDs []int           `json:"category_ids"`
	SKUs        []SKUInput      `json:"skus"`
}

func validProductStatus(v int16) bool { return v == 0 || v == 1 }

// maxSubtitleRunes is the subtitle length limit. Counted in runes, not bytes:
// the column is varchar(200) (characters) and ent's MaxLen counts bytes, which
// would reject 67 CJK characters.
const maxSubtitleRunes = 200

// normalizeSubtitle trims and length-checks a product subtitle.
func normalizeSubtitle(v string) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > maxSubtitleRunes {
		return "", &ValidationError{
			Message: "subtitle is too long",
			Details: []Detail{{Pointer: "/subtitle", Message: "must be at most 200 characters"}},
		}
	}
	return v, nil
}

func (s *Service) validateCategoryIDs(ctx context.Context, shopID int, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	n, err := s.Client.Category.Query().
		Where(entcategory.ShopIDEQ(shopID), entcategory.IDIn(ids...)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("catalog: validate category ids: %w", err)
	}
	// distinct id count must match: catches both unknown ids and cross-shop ids.
	distinct := map[int]bool{}
	for _, id := range ids {
		distinct[id] = true
	}
	if n != len(distinct) {
		return &ValidationError{
			Message: "one or more category_ids do not exist in this shop",
			Details: []Detail{{Pointer: "/category_ids", Message: "unknown category id"}},
		}
	}
	return nil
}

// CreateProduct creates a product with its nested SKUs and category
// associations in one transaction (巢狀建立, spec product-catalog).
func (s *Service) CreateProduct(ctx context.Context, shopID int, in ProductInput) (*ent.Product, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, &ValidationError{Message: "title is required", Details: []Detail{{Pointer: "/title", Message: "required"}}}
	}
	slug, err := normalizeSlug(in.Slug)
	if err != nil {
		return nil, err
	}
	status := int16(0)
	if in.Status != nil {
		if !validProductStatus(*in.Status) {
			return nil, &ValidationError{Message: "invalid status", Details: []Detail{{Pointer: "/status", Message: "must be 0 or 1"}}}
		}
		status = *in.Status
	}
	subtitle, err := normalizeSubtitle(in.Subtitle)
	if err != nil {
		return nil, err
	}
	meta := in.Meta
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}
	for _, sku := range in.SKUs {
		if err := validateSKUInput(sku); err != nil {
			return nil, err
		}
	}
	if err := s.validateCategoryIDs(ctx, shopID, in.CategoryIDs); err != nil {
		return nil, err
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: begin tx: %w", err)
	}
	p, err := tx.Product.Create().
		SetShopID(shopID).
		SetTitle(title).
		SetSlug(slug).
		SetSubtitle(subtitle).
		SetDescription(in.Description).
		SetStatus(status).
		SetMeta(meta).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return nil, &ValidationError{Message: "slug already exists in this shop", Details: []Detail{{Pointer: "/slug", Message: "duplicate slug"}}}
		}
		return nil, err
	}
	if err := linkCategories(ctx, tx, shopID, p.ID, in.CategoryIDs); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	for _, sku := range in.SKUs {
		if err := createSKU(ctx, tx, shopID, p.ID, sku); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("catalog: commit product creation: %w", err)
	}
	return s.GetProduct(ctx, shopID, p.ID)
}

// linkCategories associates the product with each (distinct) category, placing
// it LAST in that category's manual order (position = current max + 1, design
// D6).
func linkCategories(ctx context.Context, tx *ent.Tx, shopID, productID int, catIDs []int) error {
	seen := make(map[int]bool, len(catIDs))
	for _, catID := range catIDs {
		if seen[catID] {
			continue
		}
		seen[catID] = true
		var next int32
		last, err := tx.ProductCategory.Query().
			Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.CategoryIDEQ(catID)).
			Order(entproductcategory.ByPosition(sql.OrderDesc())).
			First(ctx)
		switch {
		case err == nil:
			next = last.Position + 1
		case ent.IsNotFound(err):
		default:
			return fmt.Errorf("catalog: read category order: %w", err)
		}
		if _, err := tx.ProductCategory.Create().
			SetShopID(shopID).SetProductID(productID).SetCategoryID(catID).SetPosition(next).
			Save(ctx); err != nil {
			return fmt.Errorf("catalog: link category: %w", err)
		}
	}
	return nil
}

// syncCategories makes the product's category set equal to want WITHOUT
// touching associations that stay: only removed categories are unlinked and
// only new ones are linked. A delete-all-then-recreate would silently reset
// every manual product order the merchant set (design D6).
func syncCategories(ctx context.Context, tx *ent.Tx, shopID, productID int, want []int) error {
	current, err := tx.ProductCategory.Query().
		Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.ProductIDEQ(productID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("catalog: load category associations: %w", err)
	}
	wantSet := make(map[int]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	have := make(map[int]bool, len(current))
	var remove []int
	for _, pc := range current {
		have[pc.CategoryID] = true
		if !wantSet[pc.CategoryID] {
			remove = append(remove, pc.CategoryID)
		}
	}
	if len(remove) > 0 {
		if _, err := tx.ProductCategory.Delete().
			Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.ProductIDEQ(productID), entproductcategory.CategoryIDIn(remove...)).
			Exec(ctx); err != nil {
			return fmt.Errorf("catalog: unlink categories: %w", err)
		}
	}
	var add []int
	for _, id := range want {
		if !have[id] {
			add = append(add, id)
			have[id] = true
		}
	}
	return linkCategories(ctx, tx, shopID, productID, add)
}

func createSKU(ctx context.Context, tx *ent.Tx, shopID, productID int, in SKUInput) error {
	options := in.Options
	if len(options) == 0 {
		options = json.RawMessage("{}")
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = "TWD"
	}
	create := tx.ProductSKU.Create().
		SetShopID(shopID).
		SetProductID(productID).
		SetSkuCode(strings.TrimSpace(in.SKUCode)).
		SetOptions(options).
		SetPriceAmount(in.PriceAmount).
		SetCurrency(currency).
		SetStockQty(in.StockQty)
	if in.IsActive != nil {
		create.SetIsActive(*in.IsActive)
	}
	_, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return &ValidationError{
			Message: "sku_code already exists in this shop",
			Details: []Detail{{Pointer: "/skus", Message: "duplicate sku_code: " + in.SKUCode}},
		}
	}
	return err
}

// ProductUpdate is a partial update. CategoryIDs/SKUs use pointer-to-slice so
// "field omitted" (nil, leave unchanged) is distinguishable from "field sent
// as an empty array" (non-nil empty slice, replace with nothing / delete
// all) — ordinary encoding/json cannot make that distinction with a bare
// slice field.
type ProductUpdate struct {
	Title       *string         `json:"title"`
	Slug        *string         `json:"slug"`
	Subtitle    *string         `json:"subtitle"`
	Description *string         `json:"description"`
	Status      *int16          `json:"status"`
	Meta        json.RawMessage `json:"meta"`
	CategoryIDs *[]int          `json:"category_ids"`
	SKUs        *[]SKUInput     `json:"skus"`
}

// UpdateProduct applies a partial update, replacing the category set when
// category_ids is present (spec: 全量取代語意 — implemented as a set diff so
// surviving associations keep their manual position) and upserting
// SKUs when skus is present (spec: 巢狀建立與更新 — id'd items update,
// id-less items are created, existing ids missing from the payload are
// removed). Everything happens in one transaction.
func (s *Service) UpdateProduct(ctx context.Context, shopID, productID int, in ProductUpdate) (*ent.Product, error) {
	existing, err := s.Client.Product.Query().
		Where(entproduct.IDEQ(productID), entproduct.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}

	var slug string
	if in.Slug != nil {
		var err error
		slug, err = normalizeSlug(*in.Slug)
		if err != nil {
			return nil, err
		}
	}
	if in.Status != nil && !validProductStatus(*in.Status) {
		return nil, &ValidationError{Message: "invalid status", Details: []Detail{{Pointer: "/status", Message: "must be 0 or 1"}}}
	}
	var subtitle string
	if in.Subtitle != nil {
		var err error
		subtitle, err = normalizeSubtitle(*in.Subtitle)
		if err != nil {
			return nil, err
		}
	}
	if in.CategoryIDs != nil {
		if err := s.validateCategoryIDs(ctx, shopID, *in.CategoryIDs); err != nil {
			return nil, err
		}
	}
	if in.SKUs != nil {
		for _, sku := range *in.SKUs {
			if err := validateSKUInput(sku); err != nil {
				return nil, err
			}
		}
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: begin tx: %w", err)
	}
	upd := tx.Product.UpdateOne(existing)
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			_ = tx.Rollback()
			return nil, &ValidationError{Message: "title cannot be empty", Details: []Detail{{Pointer: "/title", Message: "required"}}}
		}
		upd.SetTitle(title)
	}
	if in.Slug != nil {
		upd.SetSlug(slug)
	}
	if in.Subtitle != nil {
		upd.SetSubtitle(subtitle)
	}
	if in.Description != nil {
		upd.SetDescription(*in.Description)
	}
	if in.Status != nil {
		upd.SetStatus(*in.Status)
	}
	if len(in.Meta) > 0 {
		upd.SetMeta(in.Meta)
	}
	if _, err := upd.Save(ctx); err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return nil, &ValidationError{Message: "slug already exists in this shop", Details: []Detail{{Pointer: "/slug", Message: "duplicate slug"}}}
		}
		return nil, err
	}

	if in.CategoryIDs != nil {
		if err := syncCategories(ctx, tx, shopID, productID, *in.CategoryIDs); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}

	if in.SKUs != nil {
		if err := upsertSKUs(ctx, tx, shopID, productID, *in.SKUs); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("catalog: commit product update: %w", err)
	}
	return s.GetProduct(ctx, shopID, productID)
}

// upsertSKUs implements the nested upsert semantics documented on
// ProductUpdate.SKUs.
func upsertSKUs(ctx context.Context, tx *ent.Tx, shopID, productID int, skus []SKUInput) error {
	existing, err := tx.ProductSKU.Query().
		Where(entproductsku.ShopIDEQ(shopID), entproductsku.ProductIDEQ(productID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("catalog: load existing skus: %w", err)
	}
	existingByID := make(map[int]*ent.ProductSKU, len(existing))
	for _, sk := range existing {
		existingByID[sk.ID] = sk
	}
	keep := make(map[int]bool, len(skus))

	for _, in := range skus {
		if in.ID == nil {
			if err := createSKU(ctx, tx, shopID, productID, in); err != nil {
				return err
			}
			continue
		}
		sk, ok := existingByID[*in.ID]
		if !ok {
			return &ValidationError{
				Message: "sku id does not belong to this product",
				Details: []Detail{{Pointer: "/skus", Message: fmt.Sprintf("unknown sku id %d", *in.ID)}},
			}
		}
		keep[*in.ID] = true
		options := in.Options
		if len(options) == 0 {
			options = json.RawMessage("{}")
		}
		currency := strings.TrimSpace(in.Currency)
		if currency == "" {
			currency = "TWD"
		}
		upd := sk.Update().
			SetSkuCode(strings.TrimSpace(in.SKUCode)).
			SetOptions(options).
			SetPriceAmount(in.PriceAmount).
			SetCurrency(currency).
			SetStockQty(in.StockQty)
		if in.IsActive != nil {
			upd.SetIsActive(*in.IsActive)
		}
		if _, err := upd.Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return &ValidationError{
					Message: "sku_code already exists in this shop",
					Details: []Detail{{Pointer: "/skus", Message: "duplicate sku_code: " + in.SKUCode}},
				}
			}
			return fmt.Errorf("catalog: update sku %d: %w", *in.ID, err)
		}
	}

	for id := range existingByID {
		if !keep[id] {
			if err := tx.ProductSKU.DeleteOneID(id).Exec(ctx); err != nil {
				return fmt.Errorf("catalog: delete sku %d: %w", id, err)
			}
		}
	}
	return nil
}

func (s *Service) DeleteProduct(ctx context.Context, shopID, productID int) error {
	p, err := s.Client.Product.Query().
		Where(entproduct.IDEQ(productID), entproduct.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return ErrNotFound
	}
	// SKUs and product_category rows cascade at the DB level (design D2/D7 —
	// OnDelete(Cascade) on the product side of both edges).
	return s.Client.Product.DeleteOne(p).Exec(ctx)
}

// orderSkusByID pins the order of every eager-loaded SKU collection to id
// ascending — i.e. the order the merchant entered them (change
// commerce-demo-seed, design D8).
//
// Without it ent emits a bare `WHERE product_id IN (...)` and Postgres is free
// to return the rows in whatever order the plan produces; measured output for a
// two-SKU product was [id=2, id=1]. For a consumer the SKU array IS the size
// selector, so an undefined order is a size picker that reshuffles itself
// between page loads, with nothing in the API telling anyone not to rely on it.
//
// id ascending rather than sku_code or price: sku_code is merchant-authored
// free text whose lexical order is not size order ("L" < "M" < "S"), and price
// order is undefined again among equally-priced SKUs. Creation order is the
// only answer the merchant already controls without a new column. It also
// matches every other list endpoint in this codebase — ListShippingMethods,
// ListActiveShippingMethods and ListMemberTiers all order explicitly; none
// leaves it to the database.
func orderSkusByID(q *ent.ProductSKUQuery) { q.Order(entproductsku.ByID()) }

// orderCategories pins eager-loaded category collections to (position, id) so
// `categories` / `category_ids` have a defined order (design D2).
func orderCategories(q *ent.CategoryQuery) { q.Order(entcategory.ByPosition(), entcategory.ByID()) }

// orderImages pins eager-loaded product images to (position, id) (change
// product-images-r2): the array order IS the gallery order.
func orderImages(q *ent.ProductImageQuery) { q.Order(entproductimage.ByPosition(), entproductimage.ByID()) }

func (s *Service) GetProduct(ctx context.Context, shopID, productID int) (*ent.Product, error) {
	p, err := s.Client.Product.Query().
		Where(entproduct.IDEQ(productID), entproduct.ShopIDEQ(shopID)).
		WithSkus(orderSkusByID).
		WithCategories(orderCategories).
		WithImages(orderImages).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// ProductListParams are validated/normalized admin list inputs (pagination
// pattern mirrors cms.ShopListParams).
type ProductListParams struct {
	Page       int
	PageSize   int
	CategoryID *int
	Status     *int16
	// Q is an optional keyword (change admin-commerce-catalog, design D6).
	// Empty/absent means "no keyword filter" and the query is byte-for-byte
	// what it was before this field existed.
	Q string
}

// ProductPage is a page of products plus pagination metadata.
type ProductPage struct {
	Products []*ent.Product
	Total    int
	Page     int
	PageSize int
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	return page, pageSize
}

func (s *Service) ListProducts(ctx context.Context, shopID int, params ProductListParams) (*ProductPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)

	q := s.Client.Product.Query().Where(entproduct.ShopIDEQ(shopID))
	if params.CategoryID != nil {
		q = q.Where(entproduct.HasCategoriesWith(entcategory.IDEQ(*params.CategoryID)))
	}
	if params.Status != nil {
		q = q.Where(entproduct.StatusEQ(*params.Status))
	}
	// Keyword search (design D6). Title, slug and sku_code are the three
	// identifiers a merchant actually has for a product; the search must run
	// over the whole catalog rather than the current page, because a search
	// box that only filters the loaded page reports "not found" for products
	// that exist two pages away and the merchant then creates a duplicate.
	// ILIKE '%…%' does not use an index — fine at one shop's catalog size,
	// see design D6 for the path when it stops being fine.
	if kw := strings.TrimSpace(params.Q); kw != "" {
		q = q.Where(entproduct.Or(
			entproduct.TitleContainsFold(kw),
			entproduct.SlugContainsFold(kw),
			entproduct.HasSkusWith(entproductsku.SkuCodeContainsFold(kw)),
		))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	products, err := q.
		WithSkus(orderSkusByID).
		WithCategories(orderCategories).
		WithImages(orderImages).
		Order(entproduct.ByID()).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return &ProductPage{Products: products, Total: total, Page: page, PageSize: pageSize}, nil
}

// ── public (published-only) reads, spec product-catalog/Published-only public catalog endpoint ──

// PublicListParams are the inputs of the storefront list.
type PublicListParams struct {
	Page     int
	PageSize int
	// CategorySlug optionally restricts the list to products in that category
	// or any descendant category (design D7). Unknown slug -> empty page.
	CategorySlug string
}

// descendantIDs returns rootID plus the ids of every descendant category in
// cats (a flat list of one shop's categories). Pure and cycle-safe: a node is
// visited at most once, so corrupt parent links cannot loop it.
func descendantIDs(cats []*ent.Category, rootID int) []int {
	children := make(map[int][]int, len(cats))
	for _, c := range cats {
		if c.ParentID != nil {
			children[*c.ParentID] = append(children[*c.ParentID], c.ID)
		}
	}
	seen := map[int]bool{rootID: true}
	out := []int{rootID}
	for queue := []int{rootID}; len(queue) > 0; {
		cur := queue[0]
		queue = queue[1:]
		for _, ch := range children[cur] {
			if !seen[ch] {
				seen[ch] = true
				out = append(out, ch)
				queue = append(queue, ch)
			}
		}
	}
	return out
}

// ListPublishedProducts returns only status=1 products for the given shop
// (spec: published-only public endpoint), optionally filtered by category
// (including subcategories).
func (s *Service) ListPublishedProducts(ctx context.Context, shopID int, params PublicListParams) (*ProductPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	statusPublished := int16(1)

	q := s.Client.Product.Query().Where(entproduct.ShopIDEQ(shopID), entproduct.StatusEQ(statusPublished))

	slug := strings.ToLower(strings.TrimSpace(params.CategorySlug))
	requestedCat := 0
	if slug != "" {
		cats, err := s.ListCategories(ctx, shopID)
		if err != nil {
			return nil, err
		}
		for _, c := range cats {
			if c.Slug == slug {
				requestedCat = c.ID
				break
			}
		}
		if requestedCat == 0 {
			// Collection query, not a resource lookup (design D5).
			return &ProductPage{Products: []*ent.Product{}, Total: 0, Page: page, PageSize: pageSize}, nil
		}
		q = q.Where(entproduct.HasCategoriesWith(entcategory.IDIn(descendantIDs(cats, requestedCat)...)))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	q = q.WithSkus(orderSkusByID).WithCategories(orderCategories).WithImages(orderImages).Limit(pageSize).Offset((page - 1) * pageSize)
	if requestedCat == 0 {
		q = q.Order(entproduct.ByID())
	} else {
		// Direct members first by their position in THIS category, then the
		// products reached only through descendants by id (design D7). The
		// join matches at most one product_category row per product (its PK
		// is (product_id, category_id)), so no row is duplicated. shop_id is
		// spelled out rather than left to the tenant interceptor, which does
		// not see a hand-built join.
		q = q.Order(func(sel *sql.Selector) {
			pc := sql.Table(entproductcategory.Table)
			sel.LeftJoin(pc).OnP(sql.And(
				sql.ColumnsEQ(pc.C(entproductcategory.FieldProductID), sel.C(entproduct.FieldID)),
				sql.EQ(pc.C(entproductcategory.FieldCategoryID), requestedCat),
				sql.EQ(pc.C(entproductcategory.FieldShopID), shopID),
			))
			sel.OrderExpr(
				sql.Expr(pc.C(entproductcategory.FieldPosition)+" ASC NULLS LAST"),
				sql.Expr(sel.C(entproduct.FieldID)+" ASC"),
			)
		})
	}
	products, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	return &ProductPage{Products: products, Total: total, Page: page, PageSize: pageSize}, nil
}

// GetPublishedProductBySlug returns a single published product with its SKUs
// (spec: draft products 404 on this path).
func (s *Service) GetPublishedProductBySlug(ctx context.Context, shopID int, slug string) (*ent.Product, error) {
	p, err := s.Client.Product.Query().
		Where(entproduct.ShopIDEQ(shopID), entproduct.SlugEQ(strings.ToLower(strings.TrimSpace(slug))), entproduct.StatusEQ(1)).
		WithSkus(orderSkusByID).
		WithCategories(orderCategories).
		WithImages(orderImages).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// ── public category tree ───────────────────────────────────────────

// ListCategoriesPublic returns the shop's whole category tree (design D4):
// the same BuildCategoryTree the admin endpoint serializes, ordered by
// position then id at every level.
func (s *Service) ListCategoriesPublic(ctx context.Context, shopID int) ([]*CategoryNode, error) {
	cats, err := s.ListCategories(ctx, shopID)
	if err != nil {
		return nil, err
	}
	return BuildCategoryTree(cats), nil
}

// ── manual product order inside a category (design D6) ─────────────

// CategoryProductOrderItem is one direct member of a category, in order.
type CategoryProductOrderItem struct {
	ProductID int
	Title     string
	Status    int16
}

// GetCategoryProductOrder lists the category's DIRECT products by
// (position, product_id).
func (s *Service) GetCategoryProductOrder(ctx context.Context, shopID, categoryID int) ([]CategoryProductOrderItem, error) {
	if _, err := s.GetCategory(ctx, shopID, categoryID); err != nil {
		return nil, err
	}
	links, err := s.Client.ProductCategory.Query().
		Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.CategoryIDEQ(categoryID)).
		Order(entproductcategory.ByPosition(), entproductcategory.ByProductID()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(links))
	for _, l := range links {
		ids = append(ids, l.ProductID)
	}
	products := map[int]*ent.Product{}
	if len(ids) > 0 {
		ps, err := s.Client.Product.Query().
			Where(entproduct.ShopIDEQ(shopID), entproduct.IDIn(ids...)).
			Select(entproduct.FieldID, entproduct.FieldTitle, entproduct.FieldStatus).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			products[p.ID] = p
		}
	}
	out := make([]CategoryProductOrderItem, 0, len(links))
	for _, l := range links {
		p := products[l.ProductID]
		if p == nil {
			continue
		}
		out = append(out, CategoryProductOrderItem{ProductID: p.ID, Title: p.Title, Status: p.Status})
	}
	return out, nil
}

// SetCategoryProductOrder rewrites the order of the category's direct
// products. productIDs MUST be exactly the current direct members (a
// permutation): a partial list would leave the missing products' positions
// ambiguous relative to the listed ones.
func (s *Service) SetCategoryProductOrder(ctx context.Context, shopID, categoryID int, productIDs []int) error {
	if _, err := s.GetCategory(ctx, shopID, categoryID); err != nil {
		return err
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("catalog: begin tx: %w", err)
	}
	links, err := tx.ProductCategory.Query().
		Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.CategoryIDEQ(categoryID)).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	member := make(map[int]bool, len(links))
	for _, l := range links {
		member[l.ProductID] = true
	}
	seen := make(map[int]bool, len(productIDs))
	valid := len(productIDs) == len(links)
	for _, id := range productIDs {
		if !member[id] || seen[id] {
			valid = false
			break
		}
		seen[id] = true
	}
	if !valid {
		_ = tx.Rollback()
		return &ValidationError{
			Message: "product_ids must be exactly the products directly in this category, each once",
			Details: []Detail{{Pointer: "/product_ids", Message: "must be a permutation of the category's current products"}},
		}
	}
	for i, id := range productIDs {
		if _, err := tx.ProductCategory.Update().
			Where(entproductcategory.ShopIDEQ(shopID), entproductcategory.CategoryIDEQ(categoryID), entproductcategory.ProductIDEQ(id)).
			SetPosition(int32(i)).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("catalog: set product position: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: commit product order: %w", err)
	}
	return nil
}
