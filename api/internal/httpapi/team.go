package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// TeamHandler implements the shop owner's member management (change
// merchant-staff-role): invitations, their acceptance, and disabling or
// enabling a membership. It reuses account.Service (the token table, the
// LinkDelivery and the password rules) instead of duplicating them.
type TeamHandler struct {
	Service *account.Service
	Engine  *rbac.Engine
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountShopAdmin registers the owner-facing routes under /admin/shops/{shopID}.
// rateLimit (may be nil) bounds invitation creation per shop and caller.
func (h *TeamHandler) MountShopAdmin(r chi.Router, rateLimit func(http.Handler) http.Handler) {
	guard := h.Authz.RequireShopPermission("user.manage_members")
	r.With(guard, orPassthrough(rateLimit)).Post("/invitations", h.invite)
	r.With(guard).Get("/invitations", h.listInvitations)
	r.With(guard).Delete("/invitations/{invitationID}", h.revokeInvitation)
	r.With(guard).Post("/users/{userID}/disable", h.setEnabled(false))
	r.With(guard).Post("/users/{userID}/enable", h.setEnabled(true))
}

// MountAuth registers the unauthenticated invitation endpoints under /admin.
func (h *TeamHandler) MountAuth(r chi.Router, rateLimit func(http.Handler) http.Handler) {
	rl := orPassthrough(rateLimit)
	r.With(rl).Post("/auth/invitations/inspect", h.inspect)
	r.With(rl).Post("/auth/invitations/accept", h.acceptAnonymous)
}

// MountAdmin registers the authenticated accept endpoint under /admin. It has
// no RBAC guard on purpose: the caller is not yet a member of anything the
// invitation grants, and the token plus the e-mail equality check are the
// authority (listed in expectedUnguardedAdminRoutes).
func (h *TeamHandler) MountAdmin(r chi.Router) {
	r.Post("/invitations/accept", h.acceptAuthenticated)
}

func inviteKey(r *http.Request) (string, bool) {
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		return "", false
	}
	shopID, ok := shopIDParam(r)
	if !ok {
		return "", false
	}
	return "admin_invite:shop:" + strconv.Itoa(shopID) + ":user:" + strconv.Itoa(id.UserID), true
}

func (h *TeamHandler) invite(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	shopID, ok2 := shopIDParam(r)
	if !ok || !ok2 {
		httpx.Unauthorized(w)
		return
	}
	var req struct {
		Email  string `json:"email"`
		RoleID int    `json:"role_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	byPlatform, err := account.IsPlatformCaller(r.Context(), h.Engine, actor.UserID)
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	inv, err := h.Service.Invite(r.Context(), account.InviteInput{
		ShopID: shopID, RoleID: req.RoleID, Email: req.Email, CreatedBy: actor.UserID, ByPlatform: byPlatform,
	})
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	// Same keys whether or not the e-mail already has an account (design D6).
	out := map[string]any{
		"invitation_id": inv.ID,
		"delivery":      inv.Delivery.Mode,
		"expires_at":    inv.Delivery.ExpiresAt,
	}
	if inv.Delivery.Mode == account.DeliveryManual && inv.Delivery.URL != "" {
		out["invite_url"] = inv.Delivery.URL
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *TeamHandler) listInvitations(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	items, err := h.Service.ListInvitations(r.Context(), shopID)
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, i := range items {
		out = append(out, map[string]any{
			"id": i.ID, "email": i.Email, "role_id": i.RoleID, "role_name": i.RoleName,
			"expires_at": i.ExpiresAt, "created_at": i.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"invitations": out})
}

func (h *TeamHandler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "invitationID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.RevokeInvitation(r.Context(), shopID, id); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TeamHandler) setEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.AdminFrom(r.Context())
		shopID, ok2 := shopIDParam(r)
		target, ok3 := intParam(r, "userID")
		if !ok || !ok2 || !ok3 {
			httpx.NotFound(w)
			return
		}
		byPlatform, err := account.IsPlatformCaller(r.Context(), h.Engine, actor.UserID)
		if err != nil {
			writeAccountError(w, h.Log, err)
			return
		}
		if err := h.Service.SetMembershipEnabled(r.Context(), actor.UserID, shopID, target, byPlatform, enabled); err != nil {
			writeAccountError(w, h.Log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── invitation acceptance ─────────────────────────────────────────

func (h *TeamHandler) inspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in, err := h.Service.InspectInvitation(r.Context(), req.Token)
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"shop_name": in.ShopName, "role_name": in.RoleName, "email": in.Email,
		"requires_login": in.RequiresLogin, "expires_at": in.ExpiresAt,
	})
}

func (h *TeamHandler) acceptAnonymous(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := h.Service.AcceptAnonymous(r.Context(), req.Token, req.Password); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TeamHandler) acceptAuthenticated(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := h.Service.AcceptAuthenticated(r.Context(), actor.UserID, req.Token); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
