package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/publishablekey"
	"ksdevworks/ecommerce/api/internal/ent/shopallowedorigin"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// ClientAuthHandler implements the back-office API for self-hosted storefront
// clients (change storefront-client-auth): publishable keys and the browser
// origins allowed to call the API on a shop's behalf.
//
// The two resources sit at different RBAC tiers on purpose (design D11):
//
//	publishable keys → shop-scoped. The key namespace is not contested (values
//	    are random), and rotation is the merchant's own operational rhythm, so
//	    it must be self-service.
//	allowed origins  → platform-scoped. An origin is a contested global
//	    namespace, exactly like a bound site domain (whose management node,
//	    shop.manage_domains, is platform-scoped for the same reason). Letting a
//	    merchant register origins would let them claim someone else's origin
//	    and add arbitrary sites to the platform's CORS allow set.
//
// Neither path runs in tenant context, so every query scopes itself by an
// explicit shop_id taken from the URL after RBAC (mirrors change
// member-identity-per-shop design D4).
type ClientAuthHandler struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher
	Authz      *AuthzMW
	Log        *slog.Logger
}

// MountShopAdmin registers both resources under /admin/shops/{shopID}.
func (h *ClientAuthHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("publishable_key.view")).Get("/publishable-keys", h.listKeys)
	r.With(h.Authz.RequireShopPermission("publishable_key.create")).Post("/publishable-keys", h.createKey)
	r.With(h.Authz.RequireShopPermission("publishable_key.revoke")).Delete("/publishable-keys/{keyID}", h.revokeKey)

	originGuard := h.Authz.RequirePlatformPermission("shop.manage_client_origins")
	r.With(originGuard).Get("/allowed-origins", h.listOrigins)
	r.With(originGuard).Post("/allowed-origins", h.createOrigin)
	r.With(originGuard).Delete("/allowed-origins/{originID}", h.deleteOrigin)
}

// publishableKeyJSON returns the key value in full, always. It is a public
// identifier (design D1), so masking it would only obstruct the person
// integrating a front end while protecting nothing.
func publishableKeyJSON(k *ent.PublishableKey) map[string]any {
	return map[string]any{
		"id":         k.ID,
		"key":        k.Key,
		"label":      k.Label,
		"created_at": k.CreatedAt,
		"revoked_at": k.RevokedAt,
	}
}

func allowedOriginJSON(o *ent.ShopAllowedOrigin) map[string]any {
	return map[string]any{
		"id":         o.ID,
		"origin":     o.Origin,
		"purpose":    o.Purpose,
		"created_at": o.CreatedAt,
	}
}

func (h *ClientAuthHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	// Revoked keys stay in the listing: "which key did that traffic use, and
	// when did we cut it off?" is precisely the question the soft delete
	// exists to answer (design D4).
	rows, err := h.Client.PublishableKey.Query().
		Where(publishablekey.ShopIDEQ(shopID)).
		Order(ent.Asc(publishablekey.FieldID)).
		All(r.Context())
	if err != nil {
		h.Log.Error("list publishable keys", "err", err, "shop", shopID)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, publishableKeyJSON(row))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (h *ClientAuthHandler) createKey(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Label) > 100 {
		httpx.Unprocessable(w, "invalid label", []httpx.ValidationDetail{{Pointer: "/label", Message: "must be at most 100 characters"}})
		return
	}
	value, err := tenant.NewPublishableKey()
	if err != nil {
		h.Log.Error("generate publishable key", "err", err)
		httpx.Internal(w)
		return
	}
	row, err := h.Client.PublishableKey.Create().
		SetShopID(shopID).
		SetKey(value).
		SetLabel(req.Label).
		Save(r.Context())
	if err != nil {
		h.Log.Error("create publishable key", "err", err, "shop", shopID)
		httpx.Internal(w)
		return
	}
	// No cache entry can exist for a brand-new key, but the event keeps every
	// mutation on this table flowing through the same path (CLAUDE.md: cache
	// invalidation lives in the domain-event layer, never in handlers).
	h.Dispatcher.Publish(r.Context(), events.StorefrontClientChanged{ShopID: shopID, Keys: []string{value}})
	httpx.WriteJSON(w, http.StatusCreated, publishableKeyJSON(row))
}

func (h *ClientAuthHandler) revokeKey(w http.ResponseWriter, r *http.Request) {
	shopID, ok1 := intParam(r, "shopID")
	keyID, ok2 := intParam(r, "keyID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	ctx := r.Context()
	row, err := h.Client.PublishableKey.Query().
		Where(publishablekey.IDEQ(keyID), publishablekey.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	if row.RevokedAt == nil {
		if _, uerr := row.Update().SetRevokedAt(time.Now()).Save(ctx); uerr != nil {
			h.Log.Error("revoke publishable key", "err", uerr, "key_id", keyID)
			httpx.Internal(w)
			return
		}
	}
	// Revocation must take effect now, not when the 5-minute cache entry
	// expires (spec storefront-client-auth: 撤銷即時生效).
	h.Dispatcher.Publish(ctx, events.StorefrontClientChanged{ShopID: shopID, Keys: []string{row.Key}})
	w.WriteHeader(http.StatusNoContent)
}

func (h *ClientAuthHandler) listOrigins(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	rows, err := h.Client.ShopAllowedOrigin.Query().
		Where(shopallowedorigin.ShopIDEQ(shopID)).
		Order(ent.Asc(shopallowedorigin.FieldID)).
		All(r.Context())
	if err != nil {
		h.Log.Error("list allowed origins", "err", err, "shop", shopID)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, allowedOriginJSON(row))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"origins": out})
}

func (h *ClientAuthHandler) createOrigin(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		Origin  string `json:"origin"`
		Purpose string `json:"purpose"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// purpose is optional and defaults to storefront (change
	// admin-api-public-exposure, design D3): existing callers are unaffected.
	if req.Purpose == "" {
		req.Purpose = tenant.PurposeStorefront
	}
	if !tenant.ValidPurpose(req.Purpose) {
		httpx.Unprocessable(w, "invalid purpose", []httpx.ValidationDetail{
			{Pointer: "/purpose", Message: "must be \"storefront\" or \"admin\""},
		})
		return
	}
	origin := tenant.NormalizeOrigin(req.Origin)
	if origin == "" {
		httpx.Unprocessable(w, "invalid origin", []httpx.ValidationDetail{
			{Pointer: "/origin", Message: "must be a scheme://host[:port] http(s) origin without path or wildcard"},
		})
		return
	}
	ctx := r.Context()
	if _, err := h.Client.Shop.Get(ctx, shopID); err != nil {
		httpx.NotFound(w)
		return
	}
	row, err := h.Client.ShopAllowedOrigin.Create().
		SetShopID(shopID).
		SetOrigin(origin).
		SetPurpose(req.Purpose).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// Globally unique (design D5): an origin belongs to exactly one
			// shop, so the second claimant is rejected rather than silently
			// sharing the CORS grant.
			httpx.Unprocessable(w, "origin already registered", []httpx.ValidationDetail{
				{Pointer: "/origin", Message: "origin is already registered"},
			})
			return
		}
		h.Log.Error("create allowed origin", "err", err, "shop", shopID)
		httpx.Internal(w)
		return
	}
	h.Dispatcher.Publish(ctx, events.StorefrontClientChanged{ShopID: shopID, Origins: []string{origin}})
	httpx.WriteJSON(w, http.StatusCreated, allowedOriginJSON(row))
}

func (h *ClientAuthHandler) deleteOrigin(w http.ResponseWriter, r *http.Request) {
	shopID, ok1 := intParam(r, "shopID")
	originID, ok2 := intParam(r, "originID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	ctx := r.Context()
	row, err := h.Client.ShopAllowedOrigin.Query().
		Where(shopallowedorigin.IDEQ(originID), shopallowedorigin.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	if err := h.Client.ShopAllowedOrigin.DeleteOne(row).Exec(ctx); err != nil {
		h.Log.Error("delete allowed origin", "err", err, "origin_id", originID)
		httpx.Internal(w)
		return
	}
	h.Dispatcher.Publish(ctx, events.StorefrontClientChanged{ShopID: shopID, Origins: []string{row.Origin}})
	w.WriteHeader(http.StatusNoContent)
}
