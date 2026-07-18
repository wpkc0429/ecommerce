package httpapi

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/ent/shopmember"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// MemberAuthHandler implements the storefront member auth API in shop context
// (task 3.5, design D9): register / login / refresh / logout. Registration
// responses are isomorphic whether the platform identity is brand new or an
// existing member joining another shop — cross-shop existence never leaks.
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

	m, err := h.Client.Member.Query().Where(member.EmailEQ(email)).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		// Brand-new platform identity.
		hash, herr := auth.HashPassword(req.Password)
		if herr != nil {
			h.Log.Error("register: hash", "err", herr)
			httpx.Internal(w)
			return
		}
		m, err = h.Client.Member.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(1).Save(ctx)
		if err != nil {
			// Unique race (same email registered concurrently) → generic failure.
			h.registerFailed(w)
			return
		}
	case err != nil:
		h.Log.Error("register: query member", "err", err)
		httpx.Internal(w)
		return
	default:
		// Existing platform identity joining this shop: the password must
		// verify; failures are indistinguishable from other rejections.
		if m.PasswordHash == nil || !auth.VerifyPassword(req.Password, *m.PasswordHash) || m.Status != 1 {
			h.registerFailed(w)
			return
		}
	}

	if err := h.ensureMembership(r, m.ID, shopID); err != nil {
		h.Log.Error("register: membership", "err", err)
		httpx.Internal(w)
		return
	}
	h.issueTokens(w, r, m.ID, shopID)
}

// registerFailed is the uniform rejection for any non-validation registration
// failure (wrong password on an existing identity, disabled account, races).
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

	m, err := h.Client.Member.Query().Where(member.EmailEQ(email)).Only(ctx)
	if err != nil {
		auth.EqualizeVerifyTiming()
		httpx.Unauthorized(w)
		return
	}
	if m.PasswordHash == nil || !auth.VerifyPassword(req.Password, *m.PasswordHash) || m.Status != 1 {
		httpx.Unauthorized(w)
		return
	}
	// First interaction with this shop auto-creates the membership (design D9).
	if err := h.ensureMembership(r, m.ID, shopID); err != nil {
		h.Log.Error("login: membership", "err", err)
		httpx.Internal(w)
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

// ensureMembership creates the (shop, member) link if missing. Runs without
// the tenant hook stamping issues because the ids are explicit and match the
// tenant context by construction.
func (h *MemberAuthHandler) ensureMembership(r *http.Request, memberID, shopID int) error {
	ctx := r.Context()
	exists, err := h.Client.ShopMember.Query().
		Where(shopmember.ShopIDEQ(shopID), shopmember.MemberIDEQ(memberID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = h.Client.ShopMember.Create().SetShopID(shopID).SetMemberID(memberID).Save(ctx)
	if err != nil && !ent.IsConstraintError(err) { // concurrent join is fine
		return err
	}
	return nil
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
