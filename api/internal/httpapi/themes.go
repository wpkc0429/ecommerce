package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/theme"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// ThemesHandler implements platform theme management (task 6.3) and the
// merchant theme-switch API (task 6.4).
type ThemesHandler struct {
	Client  *ent.Client
	Service *cms.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// writeCMSError maps cms errors to design D12 responses.
func writeCMSError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *cms.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.Is(err, cms.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("cms operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountPlatform: /admin/themes* (platform guard, theme.manage).
func (h *ThemesHandler) MountPlatform(r chi.Router) {
	guard := h.Authz.RequirePlatformPermission("theme.manage")
	r.With(guard).Get("/themes", h.listAll)
	r.With(guard).Post("/themes", h.create)
	r.With(guard).Get("/themes/{themeID}", h.get)
	r.With(guard).Put("/themes/{themeID}", h.update)
	r.With(guard).Post("/themes/{themeID}/pages", h.createPage)
	r.With(guard).Put("/themes/{themeID}/pages/{themePageID}", h.updatePage)
	r.With(guard).Delete("/themes/{themeID}/pages/{themePageID}", h.deletePage)
}

// MountShop: theme browsing + switching under /admin/shops/{shopID}.
func (h *ThemesHandler) MountShop(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("theme.view")).Get("/themes", h.listActive)
	r.With(h.Authz.RequireShopPermission("shop.update")).Get("/theme/precheck", h.precheck)
	r.With(h.Authz.RequireShopPermission("shop.update")).Post("/theme", h.switchTheme)
}

func themeJSON(t *ent.Theme, pages []*ent.ThemePage) map[string]any {
	out := map[string]any{
		"id": t.ID, "code": t.Code, "name": t.Name,
		"layout_key": t.LayoutKey, "is_active": t.IsActive,
		"config_schema": t.ConfigSchema,
	}
	if pages != nil {
		pp := make([]map[string]any, 0, len(pages))
		for _, p := range pages {
			pp = append(pp, map[string]any{
				"id": p.ID, "type_key": p.TypeKey,
				"component_key": p.ComponentKey, "page_schema": p.PageSchema,
			})
		}
		out["pages"] = pp
	}
	return out
}

func (h *ThemesHandler) listAll(w http.ResponseWriter, r *http.Request) {
	themes, err := h.Client.Theme.Query().Order(theme.ByID()).All(r.Context())
	if err != nil {
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(themes))
	for _, t := range themes {
		out = append(out, themeJSON(t, nil))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"themes": out})
}

func (h *ThemesHandler) listActive(w http.ResponseWriter, r *http.Request) {
	themes, err := h.Client.Theme.Query().
		Where(theme.IsActiveEQ(true)).
		Order(theme.ByID()).
		All(r.Context())
	if err != nil {
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(themes))
	for _, t := range themes {
		out = append(out, map[string]any{"id": t.ID, "code": t.Code, "name": t.Name})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"themes": out})
}

func (h *ThemesHandler) create(w http.ResponseWriter, r *http.Request) {
	var in cms.ThemeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	t, err := h.Service.CreateTheme(r.Context(), in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, themeJSON(t, nil))
}

func (h *ThemesHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "themeID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	t, err := h.Client.Theme.Get(r.Context(), id)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	pages, err := h.Client.ThemePage.Query().
		Where(themepage.ThemeIDEQ(id)).
		Order(themepage.ByID()).
		All(r.Context())
	if err != nil {
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, themeJSON(t, pages))
}

func (h *ThemesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "themeID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in cms.ThemeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	t, err := h.Service.UpdateTheme(r.Context(), id, in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, themeJSON(t, nil))
}

func (h *ThemesHandler) createPage(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "themeID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in cms.ThemePageInput
	if !decodeJSON(w, r, &in) {
		return
	}
	tp, err := h.Service.CreateThemePage(r.Context(), id, in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": tp.ID, "type_key": tp.TypeKey, "component_key": tp.ComponentKey,
	})
}

func (h *ThemesHandler) updatePage(w http.ResponseWriter, r *http.Request) {
	themeID, ok1 := intParam(r, "themeID")
	tpID, ok2 := intParam(r, "themePageID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	var in cms.ThemePageInput
	if !decodeJSON(w, r, &in) {
		return
	}
	tp, err := h.Service.UpdateThemePage(r.Context(), themeID, tpID, in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": tp.ID, "type_key": tp.TypeKey, "component_key": tp.ComponentKey,
	})
}

func (h *ThemesHandler) deletePage(w http.ResponseWriter, r *http.Request) {
	themeID, ok1 := intParam(r, "themeID")
	tpID, ok2 := intParam(r, "themePageID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeleteThemePage(r.Context(), themeID, tpID); err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ThemesHandler) precheck(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	themeID, err := strconv.Atoi(r.URL.Query().Get("theme_id"))
	if err != nil || themeID <= 0 {
		httpx.BadRequest(w, "theme_id query parameter required")
		return
	}
	list, cerr := h.Service.PrecheckTheme(r.Context(), shopID, themeID)
	if cerr != nil {
		writeCMSError(w, h.Log, cerr)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"incompatible_pages": list})
}

func (h *ThemesHandler) switchTheme(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var req struct {
		ThemeID int `json:"theme_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	list, err := h.Service.SwitchTheme(r.Context(), shopID, req.ThemeID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"theme_id":           req.ThemeID,
		"incompatible_pages": list,
	})
}
