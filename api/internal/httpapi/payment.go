package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/payment"
)

// PaymentHandler implements both the member self-service "initiate payment"
// API (change payment-integration, no RBAC — mirrors OrderHandler/
// CartHandler: the JWT's member_id owns the order being paid), the merchant
// back-office payment records API (RBAC-gated payment.view, mirrors
// OrderHandler.MountShopAdmin), and the provider webhook endpoint (design
// D7 — authenticated by signature, not JWT, and therefore mounted outside
// every tenant/member/admin middleware group).
type PaymentHandler struct {
	Client  *ent.Client
	Service *payment.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// writePaymentError maps payment errors to design D12 responses (mirrors
// writeOrderError/writeCartError/writeCatalogError).
func writePaymentError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *payment.ValidationError
	var ce *payment.ConflictError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.As(err, &ce):
		httpx.Conflict(w, ce.Message)
	case errors.Is(err, payment.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("payment operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountShop: member self-service route mounted inside the existing
// tenant-resolved, member JWT authenticated /shop group (same MemberMW-
// protected chi.Group as OrderHandler.MountShop).
func (h *PaymentHandler) MountShop(r chi.Router) {
	r.Post("/orders/{orderID}/payments", h.initiate)
}

// MountShopAdmin: merchant back-office route under /admin/shops/{shopID},
// gated by RBAC payment.view (mirrors OrderHandler.MountShopAdmin).
func (h *PaymentHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("payment.view")).Get("/orders/{orderID}/payments", h.listAdmin)
}

// ── JSON serialization ────────────────────────────────────────────────

func paymentJSON(p *ent.Payment) map[string]any {
	return map[string]any{
		"id": p.ID, "order_id": p.OrderID,
		"provider": p.Provider, "provider_reference": p.ProviderReference,
		"amount": p.Amount, "currency": p.Currency, "status": p.Status,
		"created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}
}

func paymentInitiateJSON(p *ent.Payment, res *payment.InitiateResult) map[string]any {
	out := paymentJSON(p)
	out["redirect_url"] = res.RedirectURL
	return out
}

// ── member self-service ────────────────────────────────────────────────

// initiatePaymentRequest is intentionally all-optional: an empty/absent
// body selects payment.Service.DefaultProvider.
type initiatePaymentRequest struct {
	Provider string `json:"provider"`
}

func (h *PaymentHandler) initiate(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpx.BadRequest(w, "request body too large")
		return
	}
	var req initiatePaymentRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httpx.BadRequest(w, "invalid JSON body")
			return
		}
	}

	p, res, err := h.Service.InitiatePayment(r.Context(), shopID, memberID, orderID, req.Provider)
	if err != nil {
		writePaymentError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, paymentInitiateJSON(p, res))
}

// ── merchant back office ─────────────────────────────────────────────

func (h *PaymentHandler) listAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	rows, err := h.Service.ListForOrderAdmin(r.Context(), shopID, orderID)
	if err != nil {
		writePaymentError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, paymentJSON(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"payments": out})
}

// ── provider webhook (design D7: no TenantMW/MemberMW/AdminMW — identity is
// the provider's signature, not a JWT) ──────────────────────────────────

// Webhook handles POST /api/v1/webhooks/payments/{provider}. It is mounted
// directly under /api/v1 in router.go, outside every other middleware
// group: there is no tenant to resolve (the provider doesn't know our
// domain scheme) and no JWT to check (the caller is a payment provider's
// server, not a browser session).
func (h *PaymentHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")
	prov, ok := h.Service.Providers[providerName]
	if !ok {
		httpx.NotFound(w)
		return
	}

	// Raw bytes, not decodeJSON — signature verification MUST run over the
	// exact bytes the provider signed (design D1/D2).
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		httpx.BadRequest(w, "request body too large or unreadable")
		return
	}

	res, err := prov.VerifyWebhook(r.Context(), payment.WebhookRequest{Headers: r.Header, Body: body})
	if err != nil {
		if errors.Is(err, payment.ErrInvalidSignature) {
			httpx.Unauthorized(w)
			return
		}
		httpx.BadRequest(w, "malformed webhook payload")
		return
	}

	if _, err := h.Service.HandleWebhook(r.Context(), providerName, res); err != nil {
		if errors.Is(err, payment.ErrNotFound) {
			// Signature already authenticated the caller (design D7) — an
			// unrecognized provider_reference is acknowledged rather than
			// retried, and does not leak anything about why.
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
			return
		}
		h.Log.Error("payment webhook handling failed", "provider", providerName, "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
