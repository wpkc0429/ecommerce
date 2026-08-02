package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/shoptz"
)

// ShopSettingsHandler lets a merchant edit its OWN shop-level settings (change
// merchant-reports, design D7). Today that is exactly one field: the time zone.
//
// Deliberately NOT folded into the existing PUT /admin/shops/{shopID}. That
// route is platform-scoped (ShopsHandler.MountPlatform guards it with
// RequirePlatformPermission), so a merchant cannot reach it at all — and its
// body is cms.ShopUpdate (name/status/theme/content), which would make "update
// my theme content" a request that can also silently change the time zone.
//
// There is deliberately NO GET (design D7): nothing consumes it. The effective
// zone travels on every report response, which is the only place that needs to
// know it, and an endpoint with no consumer is the thing member-directory
// design D4 ruled out.
type ShopSettingsHandler struct {
	Client          *ent.Client
	Authz           *AuthzMW
	Log             *slog.Logger
	DefaultTimezone string
}

// MountShopAdmin registers the merchant-scoped settings route under
// /admin/shops/{shopID}. Flat pattern on the existing sub-router, so the
// contract check's chi.Walk blind spot (a template carrying both a sub-router
// and method handlers) does not apply.
func (h *ShopSettingsHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("shop.update")).Put("/settings", h.update)
}

// effectiveTimezoneJSON is the shape every consumer of a shop's zone sees.
//
// `source` is not decoration (design D1): "the merchant chose Asia/Taipei" and
// "the merchant chose nothing and the platform default happens to be
// Asia/Taipei" produce identical arithmetic and must never produce the same
// sentence on screen — only the second one should make a merchant in Tokyo stop
// and look.
func effectiveTimezoneJSON(eff shoptz.Effective) map[string]any {
	return map[string]any{"name": eff.Name, "source": eff.Source}
}

func shopSettingsJSON(s *ent.Shop, eff shoptz.Effective) map[string]any {
	return map[string]any{
		"shop_id": s.ID,
		// The stored value: null when the merchant has never set one. This is
		// the raw fact; effective_timezone below is what reports actually use.
		"timezone":           s.Timezone,
		"effective_timezone": effectiveTimezoneJSON(eff),
	}
}

func (h *ShopSettingsHandler) update(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)

	// A pointer-to-pointer distinction is not needed here: this endpoint owns
	// exactly one field, so "absent" and "null" can mean the same thing —
	// clear the setting. Stated explicitly because the moment a second field
	// arrives, that equivalence stops being safe and this struct needs
	// *string-vs-omitted handling.
	var body struct {
		Timezone *string `json:"timezone"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	upd := h.Client.Shop.Update().Where(entshop.IDEQ(shopID))
	if body.Timezone == nil || *body.Timezone == "" {
		// Back to unset — NOT "set to the platform default value". Those are
		// two different facts (design D1), and only NULL expresses the first.
		upd = upd.ClearTimezone()
	} else {
		if err := shoptz.Validate(*body.Timezone); err != nil {
			httpx.Unprocessable(w, "timezone must be a valid IANA time zone name (e.g. Asia/Taipei)",
				[]map[string]any{{"pointer": "/timezone", "message": err.Error()}})
			return
		}
		upd = upd.SetTimezone(*body.Timezone)
	}
	// A shop-scoped UPDATE that touches ONLY this column: name, status, theme
	// and content_json are not in the statement at all, so this endpoint cannot
	// disturb them even by accident.
	n, err := upd.Save(r.Context())
	if err != nil {
		h.Log.Error("update shop settings", "err", err)
		httpx.Internal(w)
		return
	}
	if n == 0 {
		httpx.NotFound(w)
		return
	}

	s, err := h.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(r.Context())
	if err != nil {
		h.Log.Error("reload shop settings", "err", err)
		httpx.Internal(w)
		return
	}
	eff, _, err := shoptz.Resolve(s.Timezone, h.DefaultTimezone)
	if err != nil {
		h.Log.Error("resolve shop timezone", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shopSettingsJSON(s, eff))
}
