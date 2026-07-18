package payment

import (
	"context"
	"errors"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entpayment "ksdevworks/ecommerce/api/internal/ent/payment"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
)

// Service implements payment operations. Like order.Service/cart.Service/
// catalog.Service, every method takes explicit shopID/memberID/orderID
// parameters rather than relying on ambient context — the one exception is
// HandleWebhook, which by construction has no tenant context to rely on
// (design D7): it derives shopID/orderID from the payment row it locates via
// (provider, provider_reference) and passes them explicitly onward.
//
// Orders is order.Service — the ONLY way this package ever advances
// orders.payment_status is Orders.UpdatePaymentStatus (order-management
// design D6). Nothing in this package writes ent.Order directly.
//
// Dispatcher is optional (nil-safe — see HandleWebhook) and publishes
// events.OrderPaymentSucceeded after a successful payment confirmation
// (change member-tiers-and-points design D1): this package still never
// imports points — it only publishes a generic domain event and knows
// nothing about who (if anyone) is subscribed.
type Service struct {
	Client     *ent.Client
	Orders     *order.Service
	Dispatcher *events.Dispatcher

	// Providers is the provider registry (design D1), keyed by
	// Provider.Name(). DefaultProvider is used when a caller does not
	// specify one explicitly.
	Providers       map[string]Provider
	DefaultProvider string
}

// InitiatePayment starts a new payment attempt for orderID on behalf of
// memberID (design members-self-service requirement). Rejects with
// ErrNotFound if the order does not exist or is not owned by memberID
// (order.Service.GetOrder already refuses to distinguish the two — no
// existence leakage), with ConflictError if the order's state does not
// allow payment (cancelled, or payment_status not unpaid — i.e. already
// paid/refunded), and with ValidationError if providerName does not resolve
// in the provider registry.
//
// amount/currency are always the order's own total_amount/currency snapshot
// — this change supports full-amount payment only (design Non-Goals).
func (s *Service) InitiatePayment(ctx context.Context, shopID, memberID, orderID int, providerName string) (*ent.Payment, *InitiateResult, error) {
	ord, err := s.Orders.GetOrder(ctx, shopID, memberID, orderID)
	if err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("payment: query order: %w", err)
	}
	if ord.Status == order.StatusCancelled {
		return nil, nil, &ConflictError{Message: "order is cancelled"}
	}
	if ord.PaymentStatus != order.PaymentStatusUnpaid {
		return nil, nil, &ConflictError{Message: "order is not payable"}
	}

	if providerName == "" {
		providerName = s.DefaultProvider
	}
	prov, ok := s.Providers[providerName]
	if !ok {
		return nil, nil, validationErr("/provider", "unknown payment provider")
	}

	res, err := prov.InitiatePayment(ctx, InitiateRequest{
		ShopID: shopID, OrderID: orderID,
		Amount: ord.TotalAmount, Currency: ord.Currency,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("payment: initiate with provider %s: %w", providerName, err)
	}

	row, err := s.Client.Payment.Create().
		SetShopID(shopID).SetOrderID(orderID).
		SetProvider(providerName).SetProviderReference(res.ProviderReference).
		SetAmount(ord.TotalAmount).SetCurrency(ord.Currency).
		SetStatus(StatusPending).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("payment: create payment row: %w", err)
	}
	return row, res, nil
}

// HandleWebhook applies an already-signature-verified webhook result
// (design D6/D7): idempotent lookup by (provider, provider_reference),
// a CAS conditional UPDATE (pending -> target) that is the primary
// concurrency/duplicate-delivery guard, and a defensive re-assertion of
// order.Service.UpdatePaymentStatus on repeated "succeeded" deliveries that
// closes the retry gap if a prior winning call's order update failed after
// the payment row had already flipped (see design D6 for the full
// walkthrough of why this is safe: UpdatePaymentStatus is a pure overwrite,
// not an increment, so re-asserting the same target value has no side
// effect beyond the first time).
//
// Returns ErrNotFound if no payment row matches the reference — signature
// verification has already authenticated the caller by this point, so this
// is not treated as a security event, just an unrecognized/premature
// reference.
func (s *Service) HandleWebhook(ctx context.Context, providerName string, res *WebhookResult) (*ent.Payment, error) {
	p, err := s.Client.Payment.Query().
		Where(entpayment.ProviderEQ(providerName), entpayment.ProviderReferenceEQ(res.ProviderReference)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("payment: query payment: %w", err)
	}

	target := statusForOutcome(res.Outcome)

	// CAS: only a row still pending can be claimed by this call. Affected
	// == 1 means this call is the one that won the pending -> target
	// transition.
	affected, err := s.Client.Payment.Update().
		Where(entpayment.IDEQ(p.ID), entpayment.StatusEQ(StatusPending)).
		SetStatus(target).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("payment: update payment status: %w", err)
	}
	if affected == 1 {
		p.Status = target
	} else {
		// Lost the CAS (already terminal, either from a prior delivery or a
		// concurrent one that just won) — re-read the current state so the
		// order-update decision below reflects reality, not staleness.
		p, err = s.Client.Payment.Get(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("payment: refresh payment: %w", err)
		}
	}

	// Only a "succeeded" outcome ever advances orders.payment_status
	// (design D6) — a "failed" attempt leaves the order unpaid so the
	// member can retry. Re-asserting on every delivery where the payment's
	// current status already equals "succeeded" (not just on the winning
	// call) is deliberate — see the doc comment above. This means
	// events.OrderPaymentSucceeded is published on every such delivery too,
	// not just the first (member-tiers-and-points design D4) — subscribers
	// must be idempotent.
	if target == StatusSucceeded && p.Status == StatusSucceeded {
		ord, err := s.Orders.UpdatePaymentStatus(ctx, p.ShopID, p.OrderID, OrderPaymentStatusPaid)
		if err != nil {
			return nil, fmt.Errorf("payment: mark order paid: %w", err)
		}
		if s.Dispatcher != nil {
			s.Dispatcher.Publish(ctx, events.OrderPaymentSucceeded{
				ShopID: ord.ShopID, OrderID: ord.ID, MemberID: ord.MemberID,
				TotalAmount: ord.TotalAmount, Currency: ord.Currency,
			})
		}
	}

	return p, nil
}

// ListForOrderAdmin returns every payment attempt for orderID within
// shopID, newest first (merchant back-office requirement). Verifies the
// order itself is visible in shopID first (order.Service.GetOrderAdmin) so
// a nonexistent/foreign order surfaces as ErrNotFound rather than silently
// returning an empty list.
func (s *Service) ListForOrderAdmin(ctx context.Context, shopID, orderID int) ([]*ent.Payment, error) {
	if _, err := s.Orders.GetOrderAdmin(ctx, shopID, orderID); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("payment: query order: %w", err)
	}
	rows, err := s.Client.Payment.Query().
		Where(entpayment.ShopIDEQ(shopID), entpayment.OrderIDEQ(orderID)).
		Order(entpayment.ByID(entsql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("payment: list payments: %w", err)
	}
	return rows, nil
}
