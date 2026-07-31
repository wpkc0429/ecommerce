// Package guest implements guest checkout (change guest-checkout): letting an
// unauthenticated visitor place a complete order in one request, identified
// only by an email address.
//
// This package is deliberately a *coordinator*, not a new domain. Every step
// it performs is a call into the service that already owns that state:
//
//	identity  → ent.Member (a passwordless row, design D2/D10)
//	cart      → cart.Service.ReplaceItems (design D7)
//	order     → order.Service.Checkout    (design D1 — UNCHANGED, not forked)
//
// The last one is the point of the whole package. order.Service.Checkout holds
// the only concurrency-safe conditional stock decrement in the system, guarded
// by a -race oversell test; a guest path that reimplemented or bypassed it
// would create a second copy of the code most expensive to get wrong. Overselling
// cannot be undone after the fact — duplicate orders can be cancelled, stock
// that was never there cannot be conjured. So guest checkout materializes its
// items into the identity's ordinary cart and then runs the ordinary checkout.
package guest

import (
	"context"
	"errors"
	"fmt"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/ent"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/order"
)

// statusActive mirrors members.status = 1 (啟用). A guest identity is created
// active on purpose (design D10): status expresses "the merchant disabled this
// customer", never "this customer has no password yet" — the latter is
// password_hash IS NULL, which is already what stops login.
const statusActive int16 = 1

// ErrCheckoutUnavailable is returned when the email resolves to an identity the
// merchant has disabled (members.status = 0). Guest checkout MUST NOT become a
// way around a ban just because it needs no password (design D6).
//
// The handler answers it with a dedicated 422 code and a neutral message. That
// does tell someone who already knows the address that this address is special
// at this shop — accepted, and narrower than general existence leakage: being
// refused is the whole point of a ban, and the alternative (pretend to succeed,
// silently drop the order) trades honesty for a worse experience.
var ErrCheckoutUnavailable = errors.New("guest: checkout unavailable for this identity")

// Detail locates one validation problem (JSON Pointer-ish; same convention as
// cart.Detail/order.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads for guest-level input (design D12
// envelope). Failures raised by the services this package calls keep their own
// error types and mappings — they are not rewrapped here, so a pointer like
// /items/1/sku_id still means exactly what cart.Service said it meant.
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// Service coordinates guest checkout. Like every other service in this API it
// takes an explicit shopID rather than reading ambient tenant context.
type Service struct {
	Client *ent.Client
	Cart   *cart.Service
	Orders *order.Service
}

// CheckoutInput is one guest checkout request: who to reach, what to buy, where
// and when to send it.
//
// The three optional extras added by change order-extras (per-item
// gift_message, shipping_method_id, requested_delivery_date) are carried
// through untouched — this package validates none of them. That is the point:
// their rules live in cart.Service and order.Service, so a guest gets exactly
// the same acceptance and exactly the same 422 pointers a member gets.
type CheckoutInput struct {
	Email                 string                `json:"email"`
	Items                 []cart.ItemInput      `json:"items"`
	ShippingAddress       order.ShippingAddress `json:"shipping_address"`
	ShippingMethodID      *int                  `json:"shipping_method_id"`
	RequestedDeliveryDate *string               `json:"requested_delivery_date"`

	// IdempotencyReservationID is set by the handler from the Idempotency-Key
	// header, never by the client (change checkout-idempotency) — hence
	// json:"-", so a request body cannot inject someone else's reservation.
	// Passed straight through to order.Service.Checkout, which finalizes it
	// inside the checkout transaction.
	IdempotencyReservationID *int `json:"-"`
}

// Checkout places an order for an unauthenticated visitor.
//
// Order of operations matters and is deliberate:
//
//  1. validate everything that can be judged from the request alone (email
//     format, shipping address, non-empty items) — a malformed request MUST NOT
//     leave an identity behind;
//  2. find or create the passwordless identity for (shopID, email);
//  3. atomically replace that identity's active cart with the requested lines;
//  4. run the ordinary checkout.
//
// Step 3 can still fail (an unknown SKU, an unpublished product), which leaves
// a passwordless identity with no orders. That is accepted: the row is exactly
// the one a successful request would have created, it is rate limited like the
// rest of the endpoint, and avoiding it would mean splitting cart validation
// away from cart writing purely for tidiness.
func (s *Service) Checkout(ctx context.Context, shopID int, in CheckoutInput) (*ent.Order, error) {
	email, ok := auth.NormalizeEmail(in.Email)
	if !ok {
		return nil, validationErr("/email", "a valid email address is required")
	}
	if err := in.ShippingAddress.Validate(); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, validationErr("/items", "at least one item is required")
	}

	m, err := s.resolveIdentity(ctx, shopID, email)
	if err != nil {
		return nil, err
	}
	if _, err := s.Cart.ReplaceItems(ctx, shopID, m.ID, in.Items); err != nil {
		return nil, err
	}
	return s.Orders.Checkout(ctx, shopID, m.ID, order.CheckoutInput{
		ShippingAddress:          in.ShippingAddress,
		ShippingMethodID:         in.ShippingMethodID,
		RequestedDeliveryDate:    in.RequestedDeliveryDate,
		IdempotencyReservationID: in.IdempotencyReservationID,
	})
}

// resolveIdentity returns the shop's customer identity for email, creating a
// passwordless one if none exists (design D2).
//
// Only email keys the identity. The shipping phone is deliberately NOT written
// to members.phone: that column carries UNIQUE(shop_id, phone), so storing it
// would open a second uniqueness axis on which a returning customer with a new
// address — or two people sharing a household number — collides, and "your
// phone number is already in use" is not something a checkout form can
// meaningfully ask a buyer to resolve. The phone belongs to the order's
// shipping address, which is what it actually describes.
//
// The lookup carries shop_id explicitly even though this runs inside tenant
// context and the interceptor would add it anyway (member-identity-per-shop
// design D4: the intent stays readable at the call site).
func (s *Service) resolveIdentity(ctx context.Context, shopID int, email string) (*ent.Member, error) {
	m, err := s.Client.Member.Query().
		Where(entmember.ShopIDEQ(shopID), entmember.EmailEQ(email)).
		Only(ctx)
	switch {
	case err == nil:
	case ent.IsNotFound(err):
		m, err = s.Client.Member.Create().
			SetShopID(shopID).SetEmail(email).SetStatus(statusActive).
			Save(ctx)
		if err != nil {
			if !ent.IsConstraintError(err) {
				return nil, fmt.Errorf("guest: create identity: %w", err)
			}
			// Lost a UNIQUE(shop_id, email) race with a concurrent guest
			// checkout for the same address — the winner's row is the identity.
			m, err = s.Client.Member.Query().
				Where(entmember.ShopIDEQ(shopID), entmember.EmailEQ(email)).
				Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("guest: reload identity after race: %w", err)
			}
		}
	default:
		return nil, fmt.Errorf("guest: query identity: %w", err)
	}

	if m.Status != statusActive {
		return nil, ErrCheckoutUnavailable
	}
	return m, nil
}
