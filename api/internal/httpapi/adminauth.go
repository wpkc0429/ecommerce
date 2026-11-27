package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/previeworigin"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// RefreshCookieName is the HttpOnly cookie that carries the refresh token in
// cookie mode (change admin-api-public-exposure, design D5). The __Secure-
// prefix makes browsers refuse it unless it is set with the Secure attribute.
const RefreshCookieName = "__Secure-admin_refresh"

// PreviewRefreshCookieName is the refresh cookie of a READ-ONLY preview session
// (change preview-readonly-session). It is a different cookie from
// RefreshCookieName and each is only honored for its own origin class: a
// preview session's cookie is never read for a normal admin origin, and a
// normal session's cookie is never read from a preview origin. It is
// host-only (no Domain attribute), Secure, HttpOnly and SameSite=Strict, so
// the browser does not send it to any other host.
const PreviewRefreshCookieName = "__Secure-admin_preview_refresh"

// refreshCookiePath confines the cookie to the auth endpoints, the only ones
// that ever read it.
const refreshCookiePath = "/api/v1/admin/auth"

// Refresh token transports (login body field "refresh_transport").
const (
	refreshTransportBody   = "body"
	refreshTransportCookie = "cookie"
)

// AdminAuthHandler implements the back-office auth API (task 3.4):
// login / refresh / logout with uniform 401s that never reveal whether the
// account exists, the password was wrong, or the account is disabled.
type AdminAuthHandler struct {
	Client  *ent.Client
	Issuer  *auth.TokenIssuer
	Refresh *auth.RefreshService
	// Engine backs GET /admin/me's platform_permissions (change
	// platform-account-provisioning, design D9). nil → always [].
	Engine *rbac.Engine
	Log    *slog.Logger

	// RefreshTTL is the refresh token lifetime, used as the cookie Max-Age in
	// cookie mode.
	RefreshTTL time.Duration

	// Preview / PreviewTTL: read-only preview sessions (change
	// preview-readonly-session). A login or refresh from an origin matching
	// Preview yields scope=preview_ro tokens living at most PreviewTTL in total.
	// nil/empty Preview = feature off.
	Preview    *previeworigin.Set
	PreviewTTL time.Duration

	// Account-level lockout (change admin-api-public-exposure, design D6).
	// Limiter nil or LockoutFailures <= 0 disables it. LockoutSecret keys the
	// HMAC that turns an e-mail into the counter key, so Redis never holds an
	// e-mail list (it is derived from the admin JWT secret, adding no new
	// secret to manage).
	Limiter         *ratelimit.Limiter
	LockoutFailures int
	LockoutWindow   time.Duration
	LockoutSecret   []byte
}

// lockoutKey is the Redis key counting failed logins of one e-mail across all
// source IPs. Unknown e-mails get a key too: the lockout behaves identically
// whether or not the account exists.
func (h *AdminAuthHandler) lockoutKey(email string) string {
	mac := hmac.New(sha256.New, h.LockoutSecret)
	_, _ = mac.Write([]byte("admin_login_lockout:" + email))
	return "admin_login_account:" + hex.EncodeToString(mac.Sum(nil))
}

func (h *AdminAuthHandler) lockoutEnabled() bool {
	return h.Limiter != nil && h.LockoutFailures > 0 && h.LockoutWindow > 0 && len(h.LockoutSecret) > 0
}

// setRefreshCookie writes (or, with an empty value, expires) the refresh cookie.
func (h *AdminAuthHandler) setRefreshCookie(w http.ResponseWriter, value string) {
	c := &http.Cookie{
		Name:     RefreshCookieName,
		Value:    value,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.RefreshTTL.Seconds()),
	}
	if value == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// setPreviewRefreshCookie writes (or expires) the preview session cookie; it
// lives exactly until the absolute end of the preview session.
func (h *AdminAuthHandler) setPreviewRefreshCookie(w http.ResponseWriter, value string, expires time.Time) {
	c := &http.Cookie{
		Name:     PreviewRefreshCookieName,
		Value:    value,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(time.Until(expires).Seconds()),
	}
	if value == "" || c.MaxAge <= 0 {
		c.Value, c.MaxAge = "", -1
	}
	http.SetCookie(w, c)
}

// cookieRefreshToken reads the refresh token from the cookie. ok is false when
// there is no cookie. The caller must also require X-Admin-Refresh.
func cookieRefreshToken(r *http.Request, name string) (string, bool) {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	// RefreshToken is omitted in cookie mode (the token travels in the
	// HttpOnly cookie instead); in body mode it is never empty, so the default
	// response is byte-for-byte what it was before the field gained omitempty.
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	// Scope is "preview_ro" for a read-only preview session and absent for a
	// normal one (so normal responses are byte-for-byte what they were).
	Scope string `json:"scope,omitempty"`
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
		Email            string `json:"email"`
		Password         string `json:"password"`
		RefreshTransport string `json:"refresh_transport"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	transport := req.RefreshTransport
	switch transport {
	case "", refreshTransportBody:
		transport = refreshTransportBody
	case refreshTransportCookie:
	default:
		httpx.Unprocessable(w, "invalid refresh_transport", []httpx.ValidationDetail{
			{Pointer: "/refresh_transport", Message: "must be \"body\" or \"cookie\""},
		})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	// Account-level lockout (design D6): checked BEFORE any password work, so a
	// locked e-mail answers 429 even for the right password, and identically
	// whether or not the account exists.
	var lockKey string
	if h.lockoutEnabled() {
		lockKey = h.lockoutKey(email)
		if res := h.Limiter.Peek(ctx, lockKey, h.LockoutFailures, h.LockoutWindow); !res.Allowed {
			// Only the HMAC prefix is logged - never the address (PII).
			h.Log.Warn("admin_login_lockout", "email_hash", strings.TrimPrefix(lockKey, "admin_login_account:")[:12])
			httpx.TooManyRequests(w, res.RetryAfter)
			return
		}
	}
	fail := func() {
		if lockKey != "" {
			h.Limiter.Record(ctx, lockKey, h.LockoutWindow)
		}
		httpx.Unauthorized(w)
	}

	u, err := h.Client.User.Query().Where(user.EmailEQ(email)).Only(ctx)
	if err != nil {
		auth.EqualizeVerifyTiming() // account-not-found ~ wrong-password timing
		fail()
		return
	}
	if u.PasswordHash == nil {
		// Password not set yet (provisioned account): same answer and same
		// timing as an unknown account.
		auth.EqualizeVerifyTiming()
		fail()
		return
	}
	if !auth.VerifyPassword(req.Password, *u.PasswordHash) || u.Status != 1 {
		fail()
		return
	}
	if lockKey != "" {
		h.Limiter.Reset(ctx, lockKey)
	}

	sids, err := h.Client.ShopUser.Query().
		Where(shopuser.UserIDEQ(u.ID), shopuser.StatusEQ(1)).
		Select(shopuser.FieldShopID).
		Ints(ctx)
	if err != nil {
		h.Log.Error("login: load sids", "err", err)
		httpx.Internal(w)
		return
	}
	if origin, fromPreview := requestPreviewOrigin(h.Preview, r); fromPreview {
		h.loginPreview(w, r, u.ID, sids, origin, transport)
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
	if transport == refreshTransportCookie {
		h.setRefreshCookie(w, refresh)
		httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, ""))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, refresh))
}

// previewTokenResponse builds the response of a read-only preview session.
func (h *AdminAuthHandler) previewTokenResponse(access, refresh string, accessTTL time.Duration) tokenResponse {
	return tokenResponse{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int(accessTTL.Seconds()), Scope: auth.ScopePreviewRO,
	}
}

// previewAccessTTL is the access token lifetime of a preview session: the
// normal access TTL, but never past the session's absolute end.
func (h *AdminAuthHandler) previewAccessTTL(end time.Time) time.Duration {
	ttl := min(h.Issuer.AccessTTL(), time.Until(end))
	return ttl.Truncate(time.Second)
}

// loginPreview issues a READ-ONLY session bound to the preview origin the login
// came from. No schema is involved: scope and origin live in the access token
// claims and in the refresh token's (hash-protected) prefix.
func (h *AdminAuthHandler) loginPreview(w http.ResponseWriter, r *http.Request, userID int, sids []int, origin, transport string) {
	ctx := r.Context()
	tag := auth.OriginTag(origin)
	refresh, end, err := h.Refresh.IssuePreviewUser(ctx, userID, tag, h.PreviewTTL)
	if err != nil {
		h.Log.Error("login: issue preview refresh", "err", err)
		httpx.Internal(w)
		return
	}
	accessTTL := h.previewAccessTTL(end)
	access, err := h.Issuer.IssueAdminPreview(userID, sids, origin, accessTTL)
	if err != nil {
		h.Log.Error("login: issue preview access", "err", err)
		httpx.Internal(w)
		return
	}
	// Audit: ids and an origin tag only - no e-mail, no origin, no token.
	h.Log.Info("admin_preview_session", "event", "login", "user_id", userID, "origin_tag", tag, "scope", auth.ScopePreviewRO)
	if transport == refreshTransportCookie {
		h.setPreviewRefreshCookie(w, refresh, end)
		httpx.WriteJSON(w, http.StatusOK, h.previewTokenResponse(access, "", accessTTL))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.previewTokenResponse(access, refresh, accessTTL))
}

// refreshInput resolves the refresh token of a refresh/logout request. A token
// in the JSON body wins and keeps the original (body mode) behavior exactly.
// Otherwise the HttpOnly cookie is used, and ONLY together with the
// X-Admin-Refresh header (design D5): without it the request is refused with 401
// before anything is rotated or revoked. ok=false means the response has been
// written.
func (h *AdminAuthHandler) refreshInput(w http.ResponseWriter, r *http.Request) (token string, viaCookie bool, previewOrigin string, ok bool) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &req) {
		return "", false, "", false
	}
	// Which class of origin is calling decides which session family is valid
	// (and which cookie is even looked at): a read-only token is refused from a
	// normal origin and an ordinary token from a preview origin - no upgrade,
	// no confusion in either direction.
	previewOrigin, fromPreview := requestPreviewOrigin(h.Preview, r)
	cookieName := RefreshCookieName
	if fromPreview {
		cookieName = PreviewRefreshCookieName
	}
	token = req.RefreshToken
	if token != "" {
		viaCookie = false
	} else if c, has := cookieRefreshToken(r, cookieName); has {
		if r.Header.Get(AdminRefreshHeader) != "1" {
			httpx.Unauthorized(w)
			return "", true, "", false
		}
		token, viaCookie = c, true
	} else {
		return "", false, "", true // nothing supplied: let the body-mode path answer as before
	}
	if auth.IsPreviewRefresh(token) != fromPreview {
		httpx.Unauthorized(w)
		return "", viaCookie, "", false
	}
	return token, viaCookie, previewOrigin, true
}

func (h *AdminAuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	token, viaCookie, previewOrigin, ok := h.refreshInput(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	var (
		uid        int
		newRefresh string
		err        error
		end        time.Time
	)
	if previewOrigin != "" {
		uid, newRefresh, end, err = h.Refresh.RotatePreviewUser(ctx, token, auth.OriginTag(previewOrigin))
	} else {
		uid, newRefresh, err = h.Refresh.RotateUser(ctx, token)
	}
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	sids, err := h.Client.ShopUser.Query().
		Where(shopuser.UserIDEQ(uid), shopuser.StatusEQ(1)).
		Select(shopuser.FieldShopID).
		Ints(ctx)
	if err != nil {
		h.Log.Error("refresh: load sids", "err", err)
		httpx.Internal(w)
		return
	}
	if previewOrigin != "" {
		accessTTL := h.previewAccessTTL(end)
		if accessTTL <= 0 {
			httpx.Unauthorized(w)
			return
		}
		access, err := h.Issuer.IssueAdminPreview(uid, sids, previewOrigin, accessTTL)
		if err != nil {
			h.Log.Error("refresh: issue preview access", "err", err)
			httpx.Internal(w)
			return
		}
		h.Log.Info("admin_preview_session", "event", "refresh", "user_id", uid,
			"origin_tag", auth.OriginTag(previewOrigin), "scope", auth.ScopePreviewRO)
		if viaCookie {
			h.setPreviewRefreshCookie(w, newRefresh, end)
			httpx.WriteJSON(w, http.StatusOK, h.previewTokenResponse(access, "", accessTTL))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, h.previewTokenResponse(access, newRefresh, accessTTL))
		return
	}
	access, err := h.Issuer.IssueAdmin(uid, sids)
	if err != nil {
		h.Log.Error("refresh: issue access", "err", err)
		httpx.Internal(w)
		return
	}
	if viaCookie {
		h.setRefreshCookie(w, newRefresh)
		httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, ""))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.tokenResponse(access, newRefresh))
}

func (h *AdminAuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	token, viaCookie, previewOrigin, ok := h.refreshInput(w, r)
	if !ok {
		return
	}
	if err := h.Refresh.RevokeUser(r.Context(), token); err != nil {
		h.Log.Error("logout: revoke", "err", err)
		httpx.Internal(w)
		return
	}
	if previewOrigin != "" {
		h.Log.Info("admin_preview_session", "event", "logout", "origin_tag", auth.OriginTag(previewOrigin), "scope", auth.ScopePreviewRO)
		if viaCookie {
			h.setPreviewRefreshCookie(w, "", time.Time{})
		}
	} else if viaCookie {
		h.setRefreshCookie(w, "")
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
	if err != nil || u.Status != 1 {
		// A disabled account's still-valid access token gets no identity back.
		httpx.Unauthorized(w)
		return
	}
	perms, err := h.platformPermissions(r.Context(), u.ID)
	if err != nil {
		h.Log.Error("me: platform permissions", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":                   u.ID,
		"email":                u.Email,
		"display_name":         u.DisplayName,
		"sids":                 id.SIDs,
		"platform_permissions": perms,
	})
}

// UpdateMe sets or clears the caller's own display name, the name teammates
// see in the design history (never their email). Only the caller's own record.
func (h *AdminAuthHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	var body struct {
		DisplayName *string `json:"display_name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.DisplayName == nil {
		httpx.Unprocessable(w, "display_name is required (empty clears it)", nil)
		return
	}
	name, err := design.CleanDisplayName(*body.DisplayName)
	if err != nil {
		msg := "display_name is not acceptable"
		var de *design.Error
		if errors.As(err, &de) {
			msg = de.Message
		}
		httpx.Unprocessable(w, msg, nil)
		return
	}
	upd := h.Client.User.UpdateOneID(id.UserID).Where(user.StatusEQ(1))
	if name == "" {
		upd.ClearDisplayName()
	} else {
		upd.SetDisplayName(name)
	}
	if err := upd.Exec(r.Context()); err != nil {
		if ent.IsNotFound(err) {
			httpx.Unauthorized(w)
			return
		}
		h.Log.Error("me: update display name", "err", err)
		httpx.Internal(w)
		return
	}
	var out any
	if name != "" {
		out = name
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": id.UserID, "display_name": out})
}

// platformPermissions is the decided permission set at platform ctx, sorted by
// name; [] without a platform role. UI visibility only — server guards decide.
func (h *AdminAuthHandler) platformPermissions(ctx context.Context, userID int) ([]string, error) {
	out := []string{}
	if h.Engine == nil {
		return out, nil
	}
	snap, err := h.Engine.Snapshot(ctx, userID, rbac.PlatformCtx)
	if err != nil {
		return nil, err
	}
	if !snap.IsPlatformAdmin() {
		return out, nil
	}
	names, err := h.Client.Permission.Query().Order(permission.ByName()).Select(permission.FieldName).Strings(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if snap.Allows(n) {
			out = append(out, n)
		}
	}
	return out, nil
}
