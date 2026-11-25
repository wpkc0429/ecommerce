package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/content/hook"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
)

// ContentHookHandler is the admin API of the publish hook (change
// headless-content-store slice 3, design D7). Everything here needs
// content.manage: the hook is an integration, not content editing.
//
// The URL is write-only. No response of this handler ever contains it, the
// signing secret, or anything the receiver answered; GET and PUT return only
// the host, a masked URL and the state (PublishHookStatus).
type ContentHookHandler struct {
	Notifier *hook.Notifier
	Authz    *AuthzMW
	Limiter  *ratelimit.Limiter
	Log      *slog.Logger
}

// Rate limits: test and manual fire three per minute per shop (design D7
// point 7); configuration changes thirty per minute per shop and caller.
const (
	hookCallLimit   = 3
	hookConfigLimit = 30
	hookWindow      = time.Minute
)

// MountShopAdmin mounts the routes under /admin/shops/{shopID}.
func (h *ContentHookHandler) MountShopAdmin(r chi.Router) {
	manage := h.Authz.RequireShopPermission("content.manage")
	r.With(manage).Get("/content/publish-hook", h.get)
	r.With(manage).Put("/content/publish-hook", h.set)
	r.With(manage).Delete("/content/publish-hook", h.remove)
	r.With(manage).Post("/content/publish-hook/test", h.test)
	r.With(manage).Post("/content/publish-hook/fire", h.fire)
	r.With(manage).Post("/content/publish-hook/signing-secret", h.rotate)
}

func (h *ContentHookHandler) limit(w http.ResponseWriter, r *http.Request, scope string, limit int, perCaller bool) bool {
	shopID, _ := shopIDParam(r)
	key := fmt.Sprintf("content_hook_%s:%d", scope, shopID)
	if perCaller {
		id, ok := auth.AdminFrom(r.Context())
		if !ok {
			httpx.Unauthorized(w)
			return false
		}
		key = fmt.Sprintf("%s:%d", key, id.UserID)
	}
	if res := h.Limiter.Allow(r.Context(), key, limit, hookWindow); !res.Allowed {
		httpx.TooManyRequests(w, res.RetryAfter)
		return false
	}
	return true
}

func (h *ContentHookHandler) writeError(w http.ResponseWriter, err error) {
	var ue *hook.URLError
	switch {
	case errors.As(err, &ue):
		httpx.Unprocessable(w, "the hook URL is not acceptable", []cms.Detail{{Pointer: "/url", Message: ue.Code + ": " + ue.Message}})
	case errors.Is(err, hook.ErrNotConfigured):
		httpx.NotFound(w)
	case errors.Is(err, hook.ErrDisabled):
		httpx.WriteError(w, http.StatusConflict, "conflict", "the publish hook is disabled; enable it first", nil)
	case errors.Is(err, hook.ErrCapReached):
		httpx.WriteError(w, http.StatusTooManyRequests, "hook_daily_cap_reached", "the daily call cap of this shop's publish hook is reached", nil)
	default:
		h.Log.Error("content hook", "err", err)
		httpx.Internal(w)
	}
}

func (h *ContentHookHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	st, err := h.Notifier.Status(r.Context(), shopID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

type setHookBody struct {
	URL     *string `json:"url"`
	Enabled *bool   `json:"enabled"`
}

// set creates or changes the hook. With ?dry_run=true the URL is only checked
// against the policy (shape, host allowlist); nothing is stored and nothing is
// called.
func (h *ContentHookHandler) set(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "config", hookConfigLimit, true) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	var in setHookBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.URL == nil && in.Enabled == nil {
		httpx.Unprocessable(w, "send a url and/or enabled", []cms.Detail{{Pointer: "/url", Message: "required unless enabled is sent"}})
		return
	}
	if queryBool(r, "dry_run") {
		if in.URL == nil {
			httpx.Unprocessable(w, "dry_run needs a url", []cms.Detail{{Pointer: "/url", Message: "required"}})
			return
		}
		st, err := h.Notifier.Validate(*in.URL)
		if err != nil {
			h.writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
		return
	}
	st, err := h.Notifier.Set(r.Context(), shopID, id.UserID, hook.SetInput{URL: in.URL, Enabled: in.Enabled})
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (h *ContentHookHandler) remove(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "config", hookConfigLimit, true) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	if err := h.Notifier.Delete(r.Context(), shopID, id.UserID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// callResult is the only thing a test or fire call reports: the category and
// the receiver's status code, never its body.
func callResult(res hook.Result) map[string]any {
	return map[string]any{"ok": res.OK(), "result": res.Status, "http_status": res.HTTPStatus}
}

func (h *ContentHookHandler) test(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "call", hookCallLimit, false) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	res, err := h.Notifier.Test(r.Context(), shopID, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, callResult(res))
}

func (h *ContentHookHandler) fire(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "call", hookCallLimit, false) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	res, err := h.Notifier.Fire(r.Context(), shopID, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, callResult(res))
}

// rotate generates a new signing secret and returns it once; it is not
// readable afterwards.
func (h *ContentHookHandler) rotate(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "config", hookConfigLimit, true) {
		return
	}
	shopID, _ := shopIDParam(r)
	id, _ := auth.AdminFrom(r.Context())
	secret, err := h.Notifier.RotateSecret(r.Context(), shopID, id.UserID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"signing_secret": secret,
		"note":           "shown once; the receiver verifies X-Content-Signature = sha256=HMAC(secret, timestamp + \".\" + body)",
	})
}
