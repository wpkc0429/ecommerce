package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// PagesHandler implements page CRUD + draft/publish (tasks 6.5/6.6), the shop
// global content editor (task 6.7), and preview-token issuance (task 7.4).
type PagesHandler struct {
	Client  *ent.Client
	Service *cms.Service
	Issuer  *auth.TokenIssuer
	Cfg     *config.Config
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountShop: routes under /admin/shops/{shopID}.
func (h *PagesHandler) MountShop(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("page.view")).Get("/pages", h.list)
	r.With(h.Authz.RequireShopPermission("page.create")).Post("/pages", h.create)
	r.With(h.Authz.RequireShopPermission("page.view")).Get("/pages/{pageID}", h.get)
	r.With(h.Authz.RequireShopPermission("page.edit")).Put("/pages/{pageID}", h.update)
	r.With(h.Authz.RequireShopPermission("page.delete")).Delete("/pages/{pageID}", h.delete)
	r.With(h.Authz.RequireShopPermission("page.publish")).Post("/pages/{pageID}/publish", h.publish)
	r.With(h.Authz.RequireShopPermission("page.publish")).Post("/pages/{pageID}/unpublish", h.unpublish)
	r.With(h.Authz.RequireShopPermission("page.view")).Get("/pages/{pageID}/preview-token", h.previewToken)

	r.With(h.Authz.RequireShopPermission("shop.view")).Get("/content", h.getContent)
	r.With(h.Authz.RequireShopPermission("shop.update")).Put("/content", h.updateContent)
	r.With(h.Authz.RequireShopPermission("page.view")).Get("/page-types", h.pageTypes)
}

// pageTypes lists the page types of the shop's current theme (admin
// create-page form data source).
func (h *PagesHandler) pageTypes(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	ctx := r.Context()
	shop, err := h.Client.Shop.Get(ctx, shopID)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	out := []map[string]any{}
	if shop.ThemeID != nil {
		tps, err := h.Client.ThemePage.Query().
			Where(themepage.ThemeIDEQ(*shop.ThemeID)).
			Order(themepage.ByID()).
			All(ctx)
		if err != nil {
			httpx.Internal(w)
			return
		}
		for _, tp := range tps {
			out = append(out, map[string]any{
				"type_key":      tp.TypeKey,
				"component_key": tp.ComponentKey,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"page_types": out})
}

func pageJSON(p *ent.Page, extra map[string]any) map[string]any {
	out := map[string]any{
		"id":             p.ID,
		"type_key":       p.TypeKey,
		"title":          p.Title,
		"slug":           p.Slug,
		"status":         p.Status,
		"content_json":   p.ContentJSON,
		"published_json": p.PublishedJSON,
		"meta":           p.Meta,
		"updated_at":     p.UpdatedAt,
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (h *PagesHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	entries, err := h.Service.ListPages(r.Context(), shopID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"id": e.ID, "type_key": e.TypeKey, "title": e.Title, "slug": e.Slug,
			"status": e.Status, "incompatible": e.Incompatible, "updated_at": e.UpdatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"pages": out})
}

func (h *PagesHandler) create(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var in cms.PageInput
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := h.Service.CreatePage(r.Context(), shopID, in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, pageJSON(p, nil))
}

func (h *PagesHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	pageID, ok := intParam(r, "pageID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	p, schema, err := h.Service.GetPage(r.Context(), shopID, pageID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	extra := map[string]any{"page_schema": schema, "incompatible": schema == nil}
	httpx.WriteJSON(w, http.StatusOK, pageJSON(p, extra))
}

func (h *PagesHandler) update(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	pageID, ok := intParam(r, "pageID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in cms.PageUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := h.Service.UpdatePage(r.Context(), shopID, pageID, in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pageJSON(p, nil))
}

func (h *PagesHandler) delete(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	pageID, ok := intParam(r, "pageID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeletePage(r.Context(), shopID, pageID); err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PagesHandler) publish(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	pageID, ok := intParam(r, "pageID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	p, err := h.Service.PublishPage(r.Context(), shopID, pageID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pageJSON(p, nil))
}

func (h *PagesHandler) unpublish(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	pageID, ok := intParam(r, "pageID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	p, err := h.Service.UnpublishPage(r.Context(), shopID, pageID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pageJSON(p, nil))
}

// previewToken issues a short-lived token for the working-copy preview
// endpoint (task 7.4). The RBAC guard already verified page.view.
func (h *PagesHandler) previewToken(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	pageID, ok := intParam(r, "pageID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	id, _ := auth.AdminFrom(r.Context())
	p, _, err := h.Service.GetPage(r.Context(), shopID, pageID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	tok, err := h.Issuer.IssuePreview(id.UserID, shopID, p.Slug, h.Cfg.PreviewTokenTTL)
	if err != nil {
		h.Log.Error("issue preview token", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"preview_token": tok,
		"expires_in":    int(h.Cfg.PreviewTokenTTL.Seconds()),
		"slug":          p.Slug,
	})
}

func (h *PagesHandler) getContent(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	content, schema, err := h.Service.GetShopContent(r.Context(), shopID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"content":       content,
		"config_schema": schema,
	})
}

func (h *PagesHandler) updateContent(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var req struct {
		Content json.RawMessage `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	shop, err := h.Service.UpdateShopContent(r.Context(), shopID, req.Content)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"shop_id": shop.ID,
		"content": shop.ContentJSON,
	})
}
