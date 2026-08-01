package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/guest"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/idempotency"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/shipping"
)

// GuestHandler implements guest checkout and the small set of endpoints an
// order access token can reach (change guest-checkout).
//
// Two auth models live here and they never mix:
//
//	POST /guest/checkout    — no credential at all (tenant context only)
//	GET  /guest/order       ─┐
//	POST /guest/order/payments ├ order access token names the order (no path id)
//	GET  /guest/order/shipment ┘
//
// The read/write routes take no order id from the client (design D4). That is
// CartHandler's convention applied to a second resource: with no externally
// supplied identifier there is no identifier to tamper with, so horizontal
// escalation is impossible by construction rather than by a comparison someone
// has to remember to write.
type GuestHandler struct {
	Client   *ent.Client
	Service  *guest.Service
	Orders   *order.Service
	Payments *payment.Service
	Shipping *shipping.Service
	Issuer   *auth.TokenIssuer
	Cfg      *config.Config
	Log      *slog.Logger

	// Idempotency backs the optional Idempotency-Key header (change
	// checkout-idempotency). nil disables the header's effect — which is what
	// most tests want, and which is why beginIdempotency logs loudly rather than
	// failing when a real request arrives with a key and no service.
	Idempotency *idempotency.Service
}

// writeGuestError maps guest checkout failures to design D12 responses. Errors
// raised by the services guest.Service calls keep their own mappings — a cart
// validation failure is reported exactly as the cart API would report it, so
// pointers like /items/1/sku_id mean the same thing everywhere.
func writeGuestError(w http.ResponseWriter, log *slog.Logger, err error) {
	var gve *guest.ValidationError
	var cve *cart.ValidationError
	switch {
	case errors.As(err, &gve):
		httpx.Unprocessable(w, gve.Message, gve.Details)
	case errors.Is(err, guest.ErrCheckoutUnavailable):
		// Deliberately its own code with a neutral message (design D6): the
		// merchant disabled this customer, and guest checkout must not be a way
		// around that just because it needs no password.
		httpx.WriteError(w, http.StatusUnprocessableEntity, "checkout_unavailable",
			"unable to complete checkout with the provided contact details", nil)
	case errors.As(err, &cve):
		httpx.Unprocessable(w, cve.Message, cve.Details)
	case errors.Is(err, cart.ErrNotFound):
		httpx.NotFound(w)
	default:
		writeOrderError(w, log, err)
	}
}

// MountShop mounts the unauthenticated checkout route. Called from inside the
// /shop group (tenant resolved) but outside MemberMW.
func (h *GuestHandler) MountShop(r chi.Router, rateLimit func(http.Handler) http.Handler) {
	r.With(orPassthrough(rateLimit)).Post("/checkout", h.checkout)
}

// MountOrderAccess mounts the routes an order access token can reach. Called
// from inside the OrderAccessMW-protected group.
func (h *GuestHandler) MountOrderAccess(r chi.Router) {
	r.Get("/order", h.getOrder)
	r.Post("/order/payments", h.initiatePayment)
	r.Get("/order/shipment", h.getShipment)
	// change partial-shipment, design D6: guests get the plural form too. ONDO
	// has no login UI at all, so "member only" would mean "nobody" — the same
	// reasoning that gave guests the tier discount (tier-discount-application
	// design D9).
	r.Get("/order/shipments", h.listShipments)
}

// ── guest checkout ─────────────────────────────────────────────────────

func (h *GuestHandler) checkout(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	// The body is read whole rather than streamed into a decoder because the
	// idempotency fingerprint is taken over its raw bytes (change
	// checkout-idempotency, design D7).
	raw, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	gate, replayOrderID, ok := beginIdempotency(w, r, h.Idempotency, h.Log, shopID,
		idempotency.ScopeGuestCheckout, raw)
	if !ok {
		return
	}
	if replayOrderID > 0 {
		h.replayCheckout(w, r, shopID, replayOrderID)
		return
	}

	var in guest.CheckoutInput
	if !unmarshalJSON(w, raw, &in) {
		gate.release(r)
		return
	}
	in.IdempotencyReservationID = gate.reservation()

	o, err := h.Service.Checkout(r.Context(), shopID, in)
	if err != nil {
		// Nothing was created, so the key must not stay spent (design D6): the
		// standard "fix the field and resend with the same key" flow depends on
		// this, and a corrected resend necessarily carries different bytes.
		gate.release(r)
		writeGuestError(w, h.Log, err)
		return
	}
	h.writeCheckoutResult(w, shopID, o)
}

// replayCheckout answers a repeat of an already-completed checkout.
//
// The order is loaded fresh, not reconstructed from a stored response: what the
// caller wants on a retry is precisely "what happened to my order", so a replay
// that showed the state at creation time would age into a lie.
func (h *GuestHandler) replayCheckout(w http.ResponseWriter, r *http.Request, shopID, orderID int) {
	o, err := h.Orders.GetOrderAdmin(r.Context(), shopID, orderID)
	if err != nil {
		// The key says completed and points here, and the CHECK constraint makes
		// that pairing a database fact — so a missing order means a broken
		// invariant, not an ordinary 404.
		h.Log.Error("guest checkout replay: order named by idempotency key is missing",
			"shop_id", shopID, "order_id", orderID, "err", err)
		httpx.Internal(w)
		return
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	h.writeCheckoutResult(w, shopID, o)
}

// writeCheckoutResult renders the guest checkout response. The first attempt and
// a replay share this one function on purpose: the two are required to be
// indistinguishable, and two copies of a "must stay identical" serializer drift.
//
// The order access token is re-issued rather than stored and returned (design
// D5). It is a bearer credential — the whole reason it is a stateless JWT is so
// it never has to be stored — and a stored one would come back carrying its
// original exp, so a replay a day later would hand the customer an expired
// token: a response shaped like success and useless in fact. Re-issuing grants
// nothing new: same order, same audience, same reach.
func (h *GuestHandler) writeCheckoutResult(w http.ResponseWriter, shopID int, o *ent.Order) {
	token, err := h.Issuer.IssueOrderAccess(o.ID, shopID, h.orderAccessTTL())
	if err != nil {
		// The order is already committed; failing here would tell the customer
		// their purchase did not happen when it did. Log and answer without a
		// token — the order is still reachable by the merchant, and by the
		// customer once they register with the same address.
		h.Log.Error("guest checkout: issue order access token", "shop_id", shopID, "order_id", o.ID, "err", err)
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"order": orderJSON(o)})
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"order":              orderJSON(o),
		"order_access_token": token,
		"token_type":         "Bearer",
		"expires_in":         int(h.orderAccessTTL().Seconds()),
	})
}

// defaultOrderAccessTTL applies when no Cfg is wired (tests that construct the
// handler directly). Real deployments always carry config.OrderAccessTokenTTL.
const defaultOrderAccessTTL = 24 * time.Hour

func (h *GuestHandler) orderAccessTTL() time.Duration {
	if h.Cfg != nil && h.Cfg.OrderAccessTokenTTL > 0 {
		return h.Cfg.OrderAccessTokenTTL
	}
	return defaultOrderAccessTTL
}

// ── order access token routes ──────────────────────────────────────────

// tokenOrder loads the order named by the verified order access token. The
// token is the authority here, exactly as RBAC is for GetOrderAdmin's other
// caller; there is no client-supplied id to check against.
func (h *GuestHandler) tokenOrder(w http.ResponseWriter, r *http.Request) (int, *ent.Order, bool) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return 0, nil, false
	}
	orderID, ok := auth.OrderAccessFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return 0, nil, false
	}
	o, err := h.Orders.GetOrderAdmin(r.Context(), shopID, orderID)
	if err != nil {
		writeOrderError(w, h.Log, err)
		return 0, nil, false
	}
	return shopID, o, true
}

func (h *GuestHandler) getOrder(w http.ResponseWriter, r *http.Request) {
	_, o, ok := h.tokenOrder(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderJSON(o))
}

func (h *GuestHandler) initiatePayment(w http.ResponseWriter, r *http.Request) {
	shopID, o, ok := h.tokenOrder(w, r)
	if !ok {
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

	// o.MemberID is the guest's own identity — passing it satisfies
	// InitiatePayment's ownership check with the correct value rather than
	// bypassing it. Every state guard it applies (cancelled → 409, already paid
	// → 409) is unchanged.
	p, res, err := h.Payments.InitiatePayment(r.Context(), shopID, o.MemberID, o.ID, req.Provider)
	if err != nil {
		writePaymentError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, paymentInitiateJSON(p, res))
}

func (h *GuestHandler) getShipment(w http.ResponseWriter, r *http.Request) {
	shopID, o, ok := h.tokenOrder(w, r)
	if !ok {
		return
	}
	sh, err := h.Shipping.GetShipmentForMember(r.Context(), shopID, o.MemberID, o.ID)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shipmentJSON(sh))
}

// listShipments returns every parcel of the token's order (change
// partial-shipment). Nothing shipped yet is 200 with an empty array, unlike the
// singular endpoint's 404 — see ShippingHandler.listMineShipments.
func (h *GuestHandler) listShipments(w http.ResponseWriter, r *http.Request) {
	shopID, o, ok := h.tokenOrder(w, r)
	if !ok {
		return
	}
	rows, err := h.Shipping.ListShipmentsForMember(r.Context(), shopID, o.MemberID, o.ID)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shipmentsJSON(rows))
}
