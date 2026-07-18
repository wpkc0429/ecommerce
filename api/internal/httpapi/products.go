package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// ProductsHandler implements merchant-scoped product CRUD (change
// product-catalog, task 6.2) and the public published-only catalog endpoint
// (task 7.1).
type ProductsHandler struct {
	Client  *ent.Client
	Service *catalog.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountShop: routes under /admin/shops/{shopID}.
func (h *ProductsHandler) MountShop(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("product.view")).Get("/products", h.list)
	r.With(h.Authz.RequireShopPermission("product.create")).Post("/products", h.create)
	r.With(h.Authz.RequireShopPermission("product.view")).Get("/products/{productID}", h.get)
	r.With(h.Authz.RequireShopPermission("product.edit")).Put("/products/{productID}", h.update)
	r.With(h.Authz.RequireShopPermission("product.delete")).Delete("/products/{productID}", h.delete)
}

// MountPublic: routes under the tenant-resolved /shop group (design D8 — no
// authentication, published-only). Callers mount this alongside the
// existing member-auth routes so both share the same TenantMW-scoped group.
func (h *ProductsHandler) MountPublic(r chi.Router) {
	r.Get("/products", h.publicList)
	r.Get("/products/{slug}", h.publicGet)
}

func skuJSON(s *ent.ProductSKU) map[string]any {
	return map[string]any{
		"id": s.ID, "sku_code": s.SkuCode, "options": s.Options,
		"price_amount": s.PriceAmount, "currency": s.Currency,
		"stock_qty": s.StockQty, "is_active": s.IsActive,
	}
}

func productJSON(p *ent.Product) map[string]any {
	out := map[string]any{
		"id": p.ID, "shop_id": p.ShopID, "title": p.Title, "slug": p.Slug,
		"description": p.Description, "status": p.Status, "meta": p.Meta,
		"updated_at": p.UpdatedAt,
	}
	if p.Edges.Skus != nil {
		skus := make([]map[string]any, 0, len(p.Edges.Skus))
		for _, s := range p.Edges.Skus {
			skus = append(skus, skuJSON(s))
		}
		out["skus"] = skus
	}
	if p.Edges.Categories != nil {
		catIDs := make([]int, 0, len(p.Edges.Categories))
		for _, c := range p.Edges.Categories {
			catIDs = append(catIDs, c.ID)
		}
		out["category_ids"] = catIDs
	}
	return out
}

func productPageJSON(pp *catalog.ProductPage) map[string]any {
	out := make([]map[string]any, 0, len(pp.Products))
	for _, p := range pp.Products {
		out = append(out, productJSON(p))
	}
	return map[string]any{
		"products": out, "page": pp.Page, "page_size": pp.PageSize, "total": pp.Total,
	}
}

// optionalIntQuery parses a query param as *int; missing/unparseable → nil
// (tolerant-input rule, design D3, same convention as paginationParam).
func optionalIntQuery(r *http.Request, name string) *int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &v
}

func optionalInt16Query(r *http.Request, name string) *int16 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseInt(raw, 10, 16)
	if err != nil {
		return nil
	}
	v16 := int16(v)
	return &v16
}

func (h *ProductsHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	params := catalog.ProductListParams{
		Page:       paginationParam(r, "page"),
		PageSize:   paginationParam(r, "page_size"),
		CategoryID: optionalIntQuery(r, "category_id"),
		Status:     optionalInt16Query(r, "status"),
	}
	pp, err := h.Service.ListProducts(r.Context(), shopID, params)
	if err != nil {
		h.Log.Error("list products", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, productPageJSON(pp))
}

func (h *ProductsHandler) create(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var in catalog.ProductInput
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := h.Service.CreateProduct(r.Context(), shopID, in)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, productJSON(p))
}

func (h *ProductsHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	productID, ok := intParam(r, "productID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	p, err := h.Service.GetProduct(r.Context(), shopID, productID)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, productJSON(p))
}

func (h *ProductsHandler) update(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	productID, ok := intParam(r, "productID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in catalog.ProductUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := h.Service.UpdateProduct(r.Context(), shopID, productID, in)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, productJSON(p))
}

func (h *ProductsHandler) delete(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	productID, ok := intParam(r, "productID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeleteProduct(r.Context(), shopID, productID); err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── public (published-only), spec product-catalog/Published-only public catalog endpoint ──

func (h *ProductsHandler) publicList(w http.ResponseWriter, r *http.Request) {
	shopID, ok := tenant.ShopID(r.Context())
	if !ok {
		httpx.NotFound(w)
		return
	}
	params := catalog.PublicListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
	}
	pp, err := h.Service.ListPublishedProducts(r.Context(), shopID, params)
	if err != nil {
		h.Log.Error("list published products", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, productPageJSON(pp))
}

func (h *ProductsHandler) publicGet(w http.ResponseWriter, r *http.Request) {
	shopID, ok := tenant.ShopID(r.Context())
	if !ok {
		httpx.NotFound(w)
		return
	}
	slug := chi.URLParam(r, "slug")
	p, err := h.Service.GetPublishedProductBySlug(r.Context(), shopID, slug)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, productJSON(p))
}
