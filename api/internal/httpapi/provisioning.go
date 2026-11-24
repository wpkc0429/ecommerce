package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/provision"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// ProvisioningHandler implements the one-call shop opening flow and the
// derived setup checklist (change platform-account-provisioning, design D8).
type ProvisioningHandler struct {
	Service *provision.Service
	Engine  *rbac.Engine
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountPlatform registers POST /admin/shop-provisioning.
func (h *ProvisioningHandler) MountPlatform(r chi.Router) {
	r.With(h.Authz.RequirePlatformPermission("shop.create")).Post("/shop-provisioning", h.provision)
}

// MountShop registers the checklist inside the /admin/shops/{shopID} group.
// It sits there (not as a sibling of /shops/{shopID}) so chi's mount of that
// group needs no second registration of the same prefix; the guard is still
// the platform one.
func (h *ProvisioningHandler) MountShop(r chi.Router) {
	r.With(h.Authz.RequirePlatformPermission("shop.view")).Get("/setup-status", h.setupStatus)
}

func (h *ProvisioningHandler) provision(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	var req struct {
		Name              string   `json:"name"`
		ThemeID           *int     `json:"theme_id"`
		Status            *int16   `json:"status"`
		Domain            string   `json:"domain"`
		PathPrefix        string   `json:"path_prefix"`
		StorefrontOrigins []string `json:"storefront_origins"`
		OwnerEmail        string   `json:"owner_email"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// shop.create is the route guard; the inputs present decide the rest.
	snap, err := h.Engine.Snapshot(r.Context(), actor.UserID, rbac.PlatformCtx)
	if err != nil {
		h.Log.Error("provisioning: snapshot", "err", err)
		httpx.Internal(w)
		return
	}
	need := []string{"account.manage"}
	if req.Domain != "" {
		need = append(need, "shop.manage_domains")
	}
	if len(req.StorefrontOrigins) > 0 {
		need = append(need, "shop.manage_client_origins")
	}
	for _, n := range need {
		if !snap.Allows(n) {
			httpx.Forbidden(w)
			return
		}
	}

	res, err := h.Service.ProvisionShop(r.Context(), provision.Input{
		Name: req.Name, ThemeID: req.ThemeID, Status: req.Status,
		Domain: req.Domain, PathPrefix: req.PathPrefix,
		StorefrontOrigins: req.StorefrontOrigins, OwnerEmail: req.OwnerEmail,
		ActorID: actor.UserID,
	})
	if err != nil {
		var ve *cms.ValidationError
		if errors.As(err, &ve) {
			httpx.Unprocessable(w, ve.Message, ve.Details)
			return
		}
		writeAccountError(w, h.Log, err)
		return
	}

	owner := map[string]any{"user_id": res.OwnerID, "email": res.OwnerEmail, "created": res.Created}
	if res.Delivery != nil {
		for k, v := range linkJSON(account.KindSetup, *res.Delivery) {
			owner[k] = v
		}
	}
	var binding any
	if res.Binding != nil {
		binding = map[string]any{
			"site_id": res.Binding.SiteID, "domain": res.Binding.Domain,
			"path_prefix": res.Binding.PathPrefix, "is_primary": res.Binding.IsPrimary,
		}
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"shop": shopJSON(res.Shop), "binding": binding, "owner": owner, "storefront_origins": res.Origins,
	})
}

func (h *ProvisioningHandler) setupStatus(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	st, err := h.Service.SetupStatus(r.Context(), shopID)
	if err != nil {
		if errors.Is(err, provision.ErrShopNotFound) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("setup status", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"shop_id":                 shopID,
		"theme_applied":           st.ThemeApplied,
		"domain_bound":            st.DomainBound,
		"owner_assigned":          st.OwnerAssigned,
		"owner_password_set":      st.OwnerPasswordSet,
		"home_published":          st.HomePublished,
		"shipping_method_active":  st.ShippingMethodActive,
		"payment_credentials_set": st.PaymentCredentialsSet,
		"publishable_key_active":  st.PublishableKeyActive,
	})
}
