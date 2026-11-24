package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/application"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/turnstile"
)

// MaxApplicationBody is the request-size limit of the public application
// endpoints (design D2).
const MaxApplicationBody = 16 << 10

// ApplicationsPublicHandler serves the unauthenticated merchant application
// intake (change platform-website, design D2/D3/D6). It sits outside every
// tenant, member and admin middleware: the caller is an anonymous browser on
// the marketing website.
type ApplicationsPublicHandler struct {
	Service   *application.Service
	Turnstile turnstile.Verifier
	Limiter   *ratelimit.Limiter
	// EmailLimit / EmailWindow: the per-e-mail quota (no IP in the key), checked
	// AFTER Turnstile so that a bot without a token cannot burn someone's quota.
	EmailLimit  int
	EmailWindow time.Duration
	Log         *slog.Logger
}

// MountPublic registers the two public routes under /api/v1/public.
// rate guards submit, verifyRate guards verify (both IP-scoped, in front of
// everything else).
func (h *ApplicationsPublicHandler) MountPublic(r chi.Router, rate, verifyRate func(http.Handler) http.Handler) {
	r.With(limitBody(MaxApplicationBody), orPassthrough(rate)).Post("/shop-applications", h.submit)
	r.With(limitBody(MaxApplicationBody), orPassthrough(verifyRate)).Post("/shop-applications/verify", h.verify)
}

// limitBody reads at most max bytes up front and answers 413 beyond that, so no
// later middleware (the rate limiter's e-mail peek) or handler can be fed more.
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				httpx.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large", nil)
				return
			}
			if r.Body != nil {
				body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
				if err != nil {
					var mbe *http.MaxBytesError
					if errors.As(err, &mbe) {
						httpx.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large", nil)
						return
					}
					httpx.BadRequest(w, "invalid request body")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			next.ServeHTTP(w, r)
		})
	}
}

type submitRequest struct {
	Email           string   `json:"email"`
	ContactName     string   `json:"contact_name"`
	Phone           string   `json:"phone"`
	ShopName        string   `json:"shop_name"`
	BusinessSummary string   `json:"business_summary"`
	ConsentVersion  string   `json:"consent_version"`
	TurnstileToken  string   `json:"turnstile_token"`
	Website         string   `json:"website"` // honeypot: must be empty
	ElapsedSeconds  *float64 `json:"elapsed_seconds"`
}

// receivedBody is the one and only success body: identical for a new
// application, a duplicate, a cooling-down e-mail and an existing account.
var receivedBody = map[string]string{"status": "received"}

func (h *ApplicationsPublicHandler) submit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.BadRequest(w, "invalid JSON body")
		return
	}
	if req.Website != "" {
		httpx.Unprocessable(w, "request rejected", []httpx.ValidationDetail{{Pointer: "/website", Message: "must be empty"}})
		return
	}
	v, err := h.Service.Validate(application.SubmitInput{
		Email: req.Email, ContactName: req.ContactName, Phone: req.Phone, ShopName: req.ShopName,
		BusinessSummary: req.BusinessSummary, ConsentVersion: req.ConsentVersion, ElapsedSeconds: req.ElapsedSeconds,
	})
	if err != nil {
		var ve *application.ValidationError
		if errors.As(err, &ve) {
			httpx.Unprocessable(w, ve.Message, []httpx.ValidationDetail{{Pointer: ve.Pointer, Message: ve.Message}})
			return
		}
		h.Log.Error("application: validate", "err", err)
		httpx.Internal(w)
		return
	}

	// The only bot gate. An unreachable siteverify is 503, never a pass.
	switch err := h.Turnstile.Verify(r.Context(), req.TurnstileToken, clientIP(r)); {
	case err == nil:
	case errors.Is(err, turnstile.ErrInvalid):
		httpx.Unprocessable(w, "verification failed", []httpx.ValidationDetail{{Pointer: "/turnstile_token", Message: "verification failed, please retry"}})
		return
	default:
		h.Log.Error("application: turnstile unavailable", "err", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "verification_unavailable", "verification service unavailable, please retry later", nil)
		return
	}

	// Per-e-mail quota across all IPs. The key holds a hash, not the address.
	if res := h.Limiter.Allow(r.Context(), "application_email:"+emailKey(v), h.EmailLimit, h.EmailWindow); !res.Allowed {
		httpx.TooManyRequests(w, res.RetryAfter)
		return
	}
	capped, err := h.Service.DailyCapReached(r.Context())
	if err != nil {
		h.Log.Error("application: daily cap", "err", err)
		httpx.Internal(w)
		return
	}
	if capped {
		httpx.TooManyRequests(w, time.Hour)
		return
	}

	if err := h.Service.Submit(r.Context(), v, clientIP(r)); err != nil {
		h.Log.Error("application: submit", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, receivedBody)
}

func emailKey(v *application.Valid) string {
	sum := sha256.Sum256([]byte(v.Email()))
	return hex.EncodeToString(sum[:16])
}

func (h *ApplicationsPublicHandler) verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.BadRequest(w, "invalid JSON body")
		return
	}
	if err := h.Service.Verify(r.Context(), req.Token); err != nil {
		if errors.Is(err, application.ErrTokenInvalid) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("application: verify", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "verified"})
}

// NewApplicationRateLimit builds the per-IP limits of the submit endpoint
// (broad: all requests from an IP; tight: per IP and per e-mail hash). The
// per-e-mail-only quota lives in the handler, after Turnstile.
func NewApplicationRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		ipRule("application", cfg.ApplicationBroadLimit, cfg.ApplicationBroadWindow),
		{limit: cfg.ApplicationTightLimit, window: cfg.ApplicationTightWindow, keyFn: func(r *http.Request) (string, bool) {
			email := peekEmail(r)
			if email == "" {
				return "", false
			}
			sum := sha256.Sum256([]byte(email))
			return "application:ipemail:" + clientIP(r) + ":" + hex.EncodeToString(sum[:16]), true
		}},
	})
}

// NewApplicationVerifyRateLimit limits the verify endpoint per IP.
func NewApplicationVerifyRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		ipRule("application_verify", cfg.ApplicationBroadLimit, cfg.ApplicationBroadWindow),
	})
}
