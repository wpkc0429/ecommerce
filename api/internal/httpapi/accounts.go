package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/provision"
)

// AccountsHandler implements platform-level back-office account management and
// the unauthenticated password setup endpoints (change
// platform-account-provisioning). No response here ever contains a password
// hash, and no request accepts a password except the owner's own setup call.
type AccountsHandler struct {
	Service *account.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountPlatform registers the platform routes under /admin (admin-authenticated).
func (h *AccountsHandler) MountPlatform(r chi.Router) {
	view := h.Authz.RequirePlatformPermission("account.view")
	manage := h.Authz.RequirePlatformPermission("account.manage")
	r.With(view).Get("/accounts", h.list)
	r.With(view).Get("/accounts/{userID}", h.get)
	r.With(manage).Post("/accounts", h.create)
	r.With(manage).Post("/accounts/{userID}/memberships", h.addMembership)
	r.With(manage).Delete("/accounts/{userID}/memberships/{shopID}", h.removeMembership)
	r.With(manage).Post("/accounts/{userID}/disable", h.disable)
	r.With(manage).Post("/accounts/{userID}/enable", h.enable)
	r.With(manage).Post("/accounts/{userID}/password-reset", h.passwordReset)
}

// MountAuth registers the unauthenticated password setup endpoints under /admin.
func (h *AccountsHandler) MountAuth(r chi.Router, rateLimit func(http.Handler) http.Handler) {
	rl := orPassthrough(rateLimit)
	r.With(rl).Post("/auth/password-setup/inspect", h.inspect)
	r.With(rl).Post("/auth/password-setup", h.setPassword)
}

// writeAccountError maps service errors to the uniform envelope.
func writeAccountError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *account.ValidationError
	var ce *provision.ConflictError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, []httpx.ValidationDetail{{Pointer: ve.Pointer, Message: ve.Message}})
	case errors.As(err, &ce):
		httpx.Conflict(w, ce.Message)
	case errors.Is(err, account.ErrEmailTaken):
		httpx.Conflict(w, "an account with this email already exists")
	case errors.Is(err, account.ErrAlreadyMember):
		httpx.Conflict(w, "this e-mail is already a member of this shop")
	case errors.Is(err, account.ErrAccountExists):
		httpx.Conflict(w, "an account with this e-mail already exists; sign in first to accept the invitation")
	case errors.Is(err, account.ErrSelfMembership):
		httpx.Conflict(w, "you cannot change your own membership")
	case errors.Is(err, account.ErrNotDelegable), errors.Is(err, account.ErrWrongAccount):
		httpx.Forbidden(w)
	case errors.Is(err, account.ErrSelfDisable):
		httpx.Conflict(w, "you cannot disable your own account")
	case errors.Is(err, account.ErrLastSuperAdmin):
		httpx.Conflict(w, "this would leave no active super_admin")
	case errors.Is(err, account.ErrNotFound), errors.Is(err, account.ErrTokenInvalid):
		httpx.NotFound(w)
	default:
		log.Error("account operation failed", "err", err)
		httpx.Internal(w)
	}
}

// linkJSON renders a credential link outcome. "delivery" is always present and
// authoritative; the link key exists only for manual delivery.
func linkJSON(kind string, d account.Delivery) map[string]any {
	out := map[string]any{"delivery": d.Mode, "expires_at": d.ExpiresAt}
	if d.Mode == account.DeliveryManual && d.URL != "" {
		if kind == account.KindReset {
			out["reset_url"] = d.URL
		} else {
			out["setup_url"] = d.URL
		}
	}
	return out
}

func summaryJSON(s account.Summary) map[string]any {
	return map[string]any{
		"id": s.ID, "email": s.Email, "status": s.Status,
		"has_password": s.HasPassword, "created_at": s.CreatedAt,
	}
}

func roleRefsJSON(rs []account.RoleRef) []map[string]any {
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, map[string]any{"id": r.ID, "name": r.Name})
	}
	return out
}

func detailJSON(d *account.Detail) map[string]any {
	out := summaryJSON(d.Summary)
	mem := make([]map[string]any, 0, len(d.Memberships))
	for _, m := range d.Memberships {
		mem = append(mem, map[string]any{"shop_id": m.ShopID, "shop_name": m.ShopName, "roles": roleRefsJSON(m.Roles)})
	}
	out["memberships"] = mem
	out["platform_roles"] = roleRefsJSON(d.PlatformRoles)
	return out
}

func (h *AccountsHandler) list(w http.ResponseWriter, r *http.Request) {
	p, err := h.Service.List(r.Context(), r.URL.Query().Get("q"), paginationParam(r, "page"), paginationParam(r, "page_size"))
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	items := make([]map[string]any, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		items = append(items, summaryJSON(a))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"accounts": items, "page": p.Page, "page_size": p.PageSize, "total": p.Total,
	})
}

func (h *AccountsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "userID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	d, err := h.Service.Get(r.Context(), id)
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detailJSON(d))
}

func (h *AccountsHandler) create(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	// Unknown fields (a "password", say) are ignored by design: the API has no
	// way to take a password from an administrator.
	var req struct {
		Email  string `json:"email"`
		ShopID *int   `json:"shop_id"`
		RoleID *int   `json:"role_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, d, err := h.Service.CreateAccount(r.Context(), account.CreateInput{
		Email: req.Email, ShopID: req.ShopID, RoleID: req.RoleID, CreatedBy: &actor.UserID,
	})
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	out := linkJSON(account.KindSetup, d)
	out["user_id"] = u.ID
	out["email"] = u.Email
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *AccountsHandler) addMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "userID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		ShopID int `json:"shop_id"`
		RoleID int `json:"role_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.Service.AddMembership(r.Context(), id, req.ShopID, req.RoleID); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user_id": id, "shop_id": req.ShopID, "role_id": req.RoleID})
}

func (h *AccountsHandler) removeMembership(w http.ResponseWriter, r *http.Request) {
	id, ok1 := intParam(r, "userID")
	shopID, ok2 := intParam(r, "shopID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.RemoveMembership(r.Context(), id, shopID); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccountsHandler) disable(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	id, ok2 := intParam(r, "userID")
	if !ok || !ok2 {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.Disable(r.Context(), actor.UserID, id); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccountsHandler) enable(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "userID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.Enable(r.Context(), id); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccountsHandler) passwordReset(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.AdminFrom(r.Context())
	id, ok2 := intParam(r, "userID")
	if !ok || !ok2 {
		httpx.NotFound(w)
		return
	}
	d, err := h.Service.IssueReset(r.Context(), actor.UserID, id)
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	out := linkJSON(account.KindReset, d)
	out["user_id"] = id
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ── unauthenticated ───────────────────────────────────────────────

func (h *AccountsHandler) inspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in, err := h.Service.Inspect(r.Context(), req.Token)
	if err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"email": in.Email, "kind": in.Kind, "expires_at": in.ExpiresAt})
}

func (h *AccountsHandler) setPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.Service.CompletePasswordSetup(r.Context(), req.Token, req.Password); err != nil {
		writeAccountError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
