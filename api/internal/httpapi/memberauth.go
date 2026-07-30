package httpapi

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// MemberAuthHandler implements the storefront member auth API in shop context
// (task 3.5, design D9): register / login / logout / refresh.
//
// Since change member-identity-per-shop, customer identity is per-shop: every
// lookup below is scoped to the resolved shop, and an email already used at
// another shop is simply an unrelated row — registering it here creates a
// brand-new identity with its own password (design D1). Registration
// responses stay isomorphic across success and every failure mode, now to
// avoid leaking whether the email is *this shop's* customer (design D8).
type MemberAuthHandler struct {
	Client  *ent.Client
	Issuer  *auth.TokenIssuer
	Refresh *auth.RefreshService
	Log     *slog.Logger
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (h *MemberAuthHandler) tokenResponse(access, refresh string) tokenResponse {
	return tokenResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.Issuer.AccessTTL().Seconds()),
	}
}

func shopFrom(w http.ResponseWriter, r *http.Request) (int, bool) {
	shopID, ok := tenant.ShopID(r.Context())
	if !ok {
		// Member routes are always mounted behind the tenant middleware.
		httpx.Internal(w)
		return 0, false
	}
	return shopID, true
}

func (h *MemberAuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !emailRe.MatchString(email) {
		httpx.Unprocessable(w, "invalid registration payload", []httpx.ValidationDetail{{Pointer: "/email", Message: "invalid email format"}})
		return
	}
	if len(req.Password) < 8 {
		httpx.Unprocessable(w, "invalid registration payload", []httpx.ValidationDetail{{Pointer: "/password", Message: "password must be at least 8 characters"}})
		return
	}
	ctx := r.Context()

	// Identity is per-shop (design D1/D4): the lookup is explicitly scoped to
	// the resolved shop rather than relying on the tenant interceptor, so the
	// intent stays readable at the call site. The same email at another shop
	// is an unrelated row and never surfaces here.
	taken, err := h.Client.Member.Query().
		Where(member.ShopIDEQ(shopID), member.EmailEQ(email)).
		Exist(ctx)
	if err != nil {
		h.Log.Error("register: query member", "err", err)
		httpx.Internal(w)
		return
	}
	if taken {
		// Already a customer of *this* shop. There is no "join with your
		// existing password" path any more (design D1) — reject uniformly,
		// burning one Argon2id verification so the rejection costs roughly as
		// much as the successful hash-and-create below (design D8).
		auth.EqualizeVerifyTiming()
		h.registerFailed(w)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		h.Log.Error("register: hash", "err", err)
		httpx.Internal(w)
		return
	}
	m, err := h.Client.Member.Create().
		SetShopID(shopID).SetEmail(email).SetPasswordHash(hash).SetStatus(1).
		Save(ctx)
	if err != nil {
		// UNIQUE(shop_id, email) race (same email registered concurrently at
		// this shop) → the same generic failure.
		h.registerFailed(w)
		return
	}
	h.issueTokens(w, r, m.ID, shopID)
}

// registerFailed is the uniform rejection for any non-validation registration
// failure (email already this shop's customer, races). Identical response for
// every case so it never reveals whether the email is already registered here
// (design D8).
func (h *MemberAuthHandler) registerFailed(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnprocessableEntity, "registration_failed",
		"unable to register with the provided credentials", nil)
}

func (h *MemberAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	// Scoped to the resolved shop (design D1/D4): credentials set at another
	// shop MUST NOT authenticate here, even when email and password both
	// match that other shop's row.
	m, err := h.Client.Member.Query().
		Where(member.ShopIDEQ(shopID), member.EmailEQ(email)).
		Only(ctx)
	if err != nil {
		auth.EqualizeVerifyTiming()
		httpx.Unauthorized(w)
		return
	}
	if m.PasswordHash == nil || !auth.VerifyPassword(req.Password, *m.PasswordHash) || m.Status != 1 {
		httpx.Unauthorized(w)
		return
	}
	h.issueTokens(w, r, m.ID, shopID)
}

func (h *MemberAuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	mid, newRefresh, err := h.Refresh.RotateMember(r.Context(), req.RefreshToken, shopID)
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	access, err := h.Issuer.IssueMember(mid, shopID)
	if err != nil {
		h.Log.Error("member refresh: issue access", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, newRefresh))
}

func (h *MemberAuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.Refresh.RevokeMember(r.Context(), req.RefreshToken, shopID); err != nil {
		h.Log.Error("member logout: revoke", "err", err)
		httpx.Internal(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *MemberAuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	mid, ok := auth.MemberFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	// Runs in tenant context, so the interceptor scopes this read to the
	// resolved shop (design D4): a member id belonging to another shop is
	// simply not found → 401.
	m, err := h.Client.Member.Get(r.Context(), mid)
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	resp := map[string]any{"id": m.ID}
	if m.Email != nil {
		resp["email"] = *m.Email
	}
	if m.Phone != nil {
		resp["phone"] = *m.Phone
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (h *MemberAuthHandler) issueTokens(w http.ResponseWriter, r *http.Request, memberID, shopID int) {
	access, err := h.Issuer.IssueMember(memberID, shopID)
	if err != nil {
		h.Log.Error("member auth: issue access", "err", err)
		httpx.Internal(w)
		return
	}
	refresh, err := h.Refresh.IssueMember(r.Context(), memberID, shopID)
	if err != nil {
		h.Log.Error("member auth: issue refresh", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, refresh))
}
