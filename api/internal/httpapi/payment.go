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

// MountShopAdmin: merchant back-office routes under /admin/shops/{shopID},
// gated by RBAC payment.view (mirrors OrderHandler.MountShopAdmin).
//
// The shop-wide list is the manual-reconciliation entry point (change
// ecpay-payment-provider, design D10): the per-order route below can only be
// used by someone who already knows the order id, while reconciliation starts
// from a number in the provider's back office. It reuses payment.view rather
// than introducing a node of its own — anyone who can already read one order's
// payments sees strictly more than this returns, and a permission node with no
// consumer is not added (change member-directory's rule).
func (h *PaymentHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("payment.view")).Get("/payments", h.listShopAdmin)
	r.With(h.Authz.RequireShopPermission("payment.view")).Get("/orders/{orderID}/payments", h.listAdmin)
}

// ── JSON serialization ────────────────────────────────────────────────

func paymentJSON(p *ent.Payment) map[string]any {
	return map[string]any{
		"id": p.ID, "order_id": p.OrderID,
		"provider": p.Provider, "provider_reference": p.ProviderReference,
		// change ecpay-payment-provider (design D4/D12): the provider's OWN
		// identifier and its own statement of when the money moved. Both are null
		// for providers that report neither — which includes every row that
		// predates this change.
		"provider_transaction_id": p.ProviderTransactionID,
		"paid_at":                 p.PaidAt,
		"amount":                  p.Amount, "currency": p.Currency, "status": p.Status,
		"created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}
}

func paymentInitiateJSON(p *ent.Payment, res *payment.InitiateResult) map[string]any {
	out := paymentJSON(p)
	out["redirect_url"] = res.RedirectURL
	// change ecpay-payment-provider (design D2): a real cashier takes a form
	// POST, which a single URL string cannot express. Additive — redirect_url is
	// untouched, and consumers are told (in the contract and in
	// docs/storefront-integration.md) to check this key first.
	out["redirect_form"] = redirectFormJSON(res.RedirectForm)
	return out
}

func redirectFormJSON(f *payment.RedirectForm) any {
	if f == nil {
		return nil
	}
	// Fields is emitted as-is: a name→value map of form fields, never HTML. The
	// consumer builds the form in its own framework, where escaping is already
	// solved and where a native app can ignore the DOM entirely.
	return map[string]any{"method": f.Method, "action": f.Action, "fields": f.Fields}
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

// listShopAdmin is the manual-reconciliation lookup (design D10): paste the
// number from the provider's back office, get the order it belongs to.
func (h *PaymentHandler) listShopAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	res, err := h.Service.ListAdmin(r.Context(), shopID, payment.AdminListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
		Q:        r.URL.Query().Get("q"),
	})
	if err != nil {
		writePaymentError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(res.Payments))
	for _, p := range res.Payments {
		out = append(out, paymentJSON(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"payments": out, "total": res.Total, "page": res.Page, "page_size": res.PageSize,
	})
}

// ── provider webhook (design D7: no TenantMW/MemberMW/AdminMW — identity is
// the provider's signature, not a JWT) ──────────────────────────────────

// writeAck emits the response this provider's caller requires (change
// ecpay-payment-provider, design D3).
//
// **The provider decides the bytes; this function decides nothing except where
// the default lives.** A zero WebhookResponse means "no provider-specific
// requirement", and the API answers in its own envelope (design D12) — which is
// why MockProvider's responses are byte-for-byte what they were before this
// mechanism existed, and why the envelope is still defined only in httpx.
func writeAck(w http.ResponseWriter, prov payment.Provider, ack payment.Ack) {
	if res := prov.WebhookAck(ack); !res.IsZero() {
		w.Header().Set("Content-Type", res.ContentType)
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	switch ack {
	case payment.AckProcessed:
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case payment.AckIgnored:
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
	case payment.AckInvalidSignature:
		httpx.Unauthorized(w)
	case payment.AckMalformed:
		httpx.BadRequest(w, "malformed webhook payload")
	default: // payment.AckFailed
		httpx.Internal(w)
	}
}

// Webhook handles POST /api/v1/webhooks/payments/{provider}. It is mounted
// directly under /api/v1 in router.go, outside every other middleware
// group: there is no tenant to resolve (the provider doesn't know our
// domain scheme) and no JWT to check (the caller is a payment provider's
// server, not a browser session).
//
// **Where the provider's protocol starts and ours ends** (design D3): the two
// failures below happen before the caller has been authenticated at all — an
// unregistered {provider} is nobody's webhook, and an unreadable body hit OUR
// 64 KiB limit. Everything from signature verification onward is answered in the
// provider's own terms. That line can be stated in one sentence, which is why it
// holds up.
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
			writeAck(w, prov, payment.AckInvalidSignature)
			return
		}
		writeAck(w, prov, payment.AckMalformed)
		return
	}

	if _, err := h.Service.HandleWebhook(r.Context(), providerName, res); err != nil {
		if errors.Is(err, payment.ErrNotFound) {
			// Signature already authenticated the caller (design D7) — an
			// unrecognized provider_reference is acknowledged rather than
			// retried, and does not leak anything about why. Retrying it could
			// never make it recognizable.
			writeAck(w, prov, payment.AckIgnored)
			return
		}
		// Includes payment.ErrAmountMismatch (design D7): we deliberately want
		// the provider to retry and, when it gives up, to record a delivery
		// failure on its side too — a disagreement about money should ring on
		// both systems, not on neither.
		h.Log.Error("payment webhook handling failed", "provider", providerName, "err", err)
		writeAck(w, prov, payment.AckFailed)
		return
	}
	writeAck(w, prov, payment.AckProcessed)
}
