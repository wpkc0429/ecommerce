package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// AdminAuthHandler implements the back-office auth API (task 3.4):
// login / refresh / logout with uniform 401s that never reveal whether the
// account exists, the password was wrong, or the account is disabled.
type AdminAuthHandler struct {
	Client  *ent.Client
	Issuer  *auth.TokenIssuer
	Refresh *auth.RefreshService
	Log     *slog.Logger
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func (h *AdminAuthHandler) tokenResponse(access, refresh string) tokenResponse {
	return tokenResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.Issuer.AccessTTL().Seconds()),
	}
}

func (h *AdminAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	u, err := h.Client.User.Query().Where(user.EmailEQ(email)).Only(ctx)
	if err != nil {
		auth.EqualizeVerifyTiming() // account-not-found ≈ wrong-password timing
		httpx.Unauthorized(w)
		return
	}
	if !auth.VerifyPassword(req.Password, u.PasswordHash) || u.Status != 1 {
		httpx.Unauthorized(w)
		return
	}

	sids, err := h.Client.ShopUser.Query().
		Where(shopuser.UserIDEQ(u.ID)).
		Select(shopuser.FieldShopID).
		Ints(ctx)
	if err != nil {
		h.Log.Error("login: load sids", "err", err)
		httpx.Internal(w)
		return
	}
	access, err := h.Issuer.IssueAdmin(u.ID, sids)
	if err != nil {
		h.Log.Error("login: issue access", "err", err)
		httpx.Internal(w)
		return
	}
	refresh, err := h.Refresh.IssueUser(ctx, u.ID)
	if err != nil {
		h.Log.Error("login: issue refresh", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, refresh))
}

func (h *AdminAuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()

	uid, newRefresh, err := h.Refresh.RotateUser(ctx, req.RefreshToken)
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	sids, err := h.Client.ShopUser.Query().
		Where(shopuser.UserIDEQ(uid)).
		Select(shopuser.FieldShopID).
		Ints(ctx)
	if err != nil {
		h.Log.Error("refresh: load sids", "err", err)
		httpx.Internal(w)
		return
	}
	access, err := h.Issuer.IssueAdmin(uid, sids)
	if err != nil {
		h.Log.Error("refresh: issue access", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, newRefresh))
}

func (h *AdminAuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.Refresh.RevokeUser(r.Context(), req.RefreshToken); err != nil {
		h.Log.Error("logout: revoke", "err", err)
		httpx.Internal(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminAuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	u, err := h.Client.User.Get(r.Context(), id.UserID)
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":    u.ID,
		"email": u.Email,
		"sids":  id.SIDs,
	})
}
