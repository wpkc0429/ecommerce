package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// CategoriesHandler implements merchant-scoped category CRUD (change
// product-catalog, tasks 6.1).
type CategoriesHandler struct {
	Client  *ent.Client
	Service *catalog.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// writeCatalogError maps catalog errors to design D12 responses.
func writeCatalogError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *catalog.ValidationError
	var ce *catalog.ConflictError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.As(err, &ce):
		httpx.Conflict(w, ce.Message)
	case errors.Is(err, catalog.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("catalog operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountShop: routes under /admin/shops/{shopID}.
func (h *CategoriesHandler) MountShop(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("category.view")).Get("/categories", h.list)
	r.With(h.Authz.RequireShopPermission("category.create")).Post("/categories", h.create)
	r.With(h.Authz.RequireShopPermission("category.view")).Get("/categories/{categoryID}", h.get)
	r.With(h.Authz.RequireShopPermission("category.edit")).Put("/categories/{categoryID}", h.update)
	r.With(h.Authz.RequireShopPermission("category.delete")).Delete("/categories/{categoryID}", h.delete)
}

func categoryJSON(c *ent.Category) map[string]any {
	return map[string]any{
		"id": c.ID, "shop_id": c.ShopID, "parent_id": c.ParentID,
		"name": c.Name, "slug": c.Slug, "position": c.Position,
		"updated_at": c.UpdatedAt,
	}
}

func categoryNodeJSON(n *catalog.CategoryNode) map[string]any {
	children := make([]map[string]any, 0, len(n.Children))
	for _, c := range n.Children {
		children = append(children, categoryNodeJSON(c))
	}
	return map[string]any{
		"id": n.ID, "parent_id": n.ParentID, "name": n.Name, "slug": n.Slug,
		"position": n.Position, "children": children,
	}
}

// list returns the category tree by default; ?flat=1 returns the flat list
// (spec product-catalog/Shop-scoped category tree — 至少要能重建樹狀結構,
// flat is offered for admin UIs that prefer to build their own tree view).
func (h *CategoriesHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	cats, err := h.Service.ListCategories(r.Context(), shopID)
	if err != nil {
		h.Log.Error("list categories", "err", err)
		httpx.Internal(w)
		return
	}
	if r.URL.Query().Get("flat") == "1" {
		out := make([]map[string]any, 0, len(cats))
		for _, c := range cats {
			out = append(out, categoryJSON(c))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"categories": out})
		return
	}
	tree := catalog.BuildCategoryTree(cats)
	out := make([]map[string]any, 0, len(tree))
	for _, n := range tree {
		out = append(out, categoryNodeJSON(n))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"categories": out})
}

func (h *CategoriesHandler) create(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var in catalog.CategoryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := h.Service.CreateCategory(r.Context(), shopID, in)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, categoryJSON(c))
}

func (h *CategoriesHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	categoryID, ok := intParam(r, "categoryID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	c, err := h.Service.GetCategory(r.Context(), shopID, categoryID)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, categoryJSON(c))
}

func (h *CategoriesHandler) update(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	categoryID, ok := intParam(r, "categoryID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in catalog.CategoryUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := h.Service.UpdateCategory(r.Context(), shopID, categoryID, in)
	if err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, categoryJSON(c))
}

func (h *CategoriesHandler) delete(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	categoryID, ok := intParam(r, "categoryID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeleteCategory(r.Context(), shopID, categoryID); err != nil {
		writeCatalogError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
