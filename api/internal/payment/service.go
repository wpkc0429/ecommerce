package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entpayment "ksdevworks/ecommerce/api/internal/ent/payment"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
)

// ErrAmountMismatch is returned by HandleWebhook when a provider reports an
// amount that disagrees with the one we recorded (change
// ecpay-payment-provider, design D7).
//
// It is deliberately an error rather than a silent branch: the HTTP layer maps
// it to the provider's "please retry" acknowledgement, so 綠界 retries a bounded
// number of times and then records a delivery failure in ITS back office. The
// value of that is not tamper-proofing (the signature already did that) — it is
// turning "the two systems disagree about how much money this is" from a state
// nobody would ever notice into a bell that rings on both sides.
var ErrAmountMismatch = errors.New("payment: provider reported an amount that differs from the recorded one")

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
//
// Log is optional (nil-safe) and used for exactly one thing: the "paid, but the
// order was already cancelled" warning in HandleWebhook — a situation only a
// human can resolve (change ecpay-payment-provider, design D9).
type Service struct {
	Client     *ent.Client
	Orders     *order.Service
	Dispatcher *events.Dispatcher
	Log        *slog.Logger

	// Providers is the provider registry (design D1), keyed by
	// Provider.Name(). DefaultProvider is used when a caller does not
	// specify one explicitly.
	//
	// Values are `any` rather than Provider (change ecpay-per-shop-credentials,
	// design D1): an entry may instead be a ShopScopedProvider (a provider
	// resolved per shop rather than fixed at startup) — mock stays a plain
	// Provider, ecpay is a ShopScopedProvider. ResolveProvider is the one place
	// that distinguishes the two; every other call site goes through it.
	Providers       map[string]any
	DefaultProvider string
}

// ResolveProvider returns the Provider registered under providerName. If the
// registration implements ShopScopedProvider, it resolves shopID's own
// instance (change ecpay-per-shop-credentials, design D1) — shopID is unused
// for registrations that are plain Providers (mock), so 0 is a safe value to
// pass when the caller has no shop id (the {provider}-only webhook route).
//
// This is the ONE place that checks "does this provider need shop
// resolution" — by interface capability, never by comparing providerName to a
// literal string (design D1 of ecpay-payment-provider, extended by this
// change: the exception it carved stays narrow and generic).
func (s *Service) ResolveProvider(ctx context.Context, providerName string, shopID int) (Provider, error) {
	raw, ok := s.Providers[providerName]
	if !ok {
		return nil, validationErr("/provider", "unknown payment provider")
	}
	if ssp, ok := raw.(ShopScopedProvider); ok {
		return ssp.ResolveForShop(ctx, shopID)
	}
	prov, ok := raw.(Provider)
	if !ok {
		return nil, fmt.Errorf("payment: registry entry %q implements neither Provider nor ShopScopedProvider", providerName)
	}
	return prov, nil
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
	prov, err := s.ResolveProvider(ctx, providerName, shopID)
	if err != nil {
		if errors.Is(err, ErrProviderNotConfigured) {
			// design D5: a shop that has not set up this provider's credentials
			// gets a clear, typed rejection — never a silent fallback to another
			// shop's or a platform-default credential.
			return nil, nil, validationErr("/provider", "payment provider not configured for this shop")
		}
		var ve *ValidationError
		if errors.As(err, &ve) {
			return nil, nil, ve
		}
		return nil, nil, fmt.Errorf("payment: resolve provider %s: %w", providerName, err)
	}

	res, err := prov.InitiatePayment(ctx, InitiateRequest{
		ShopID: shopID, OrderID: orderID,
		Amount: ord.TotalAmount, Currency: ord.Currency,
		// change ecpay-payment-provider: a real cashier shows the customer what
		// they are paying for (綠界's ItemName is a required field). These are the
		// order's own frozen line snapshots, never a live SKU re-query.
		Items:          initiateItems(ord),
		ShippingFee:    ord.ShippingFee,
		DiscountAmount: ord.DiscountAmount,
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

// initiateItems maps the order's own line snapshot onto the provider-neutral
// item shape. GetOrder eager-loads items; an order without them simply yields
// nothing, and Items is optional for every provider.
func initiateItems(ord *ent.Order) []InitiateItem {
	items := ord.Edges.Items
	if len(items) == 0 {
		return nil
	}
	out := make([]InitiateItem, 0, len(items))
	for _, it := range items {
		out = append(out, InitiateItem{
			Name: it.ProductTitle, Quantity: it.Quantity, UnitAmount: it.PriceAmount,
		})
	}
	return out
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
//
// ── change ecpay-payment-provider adds three behaviours ────────────────
//
//	D6  OutcomePending performs NO transition, so the row stays claimable by a
//	    later terminal delivery.
//	D7  A reported amount that disagrees with the stored one aborts everything.
//	D9  A CANCELLED order records the money but is never advanced to paid, and
//	    publishes no loyalty event.
//
// shopID is the shop id carried in the webhook URL for shop-scoped providers
// (change ecpay-per-shop-credentials, design D2), 0 for the {provider}-only
// route (mock). When non-zero, it MUST match the located payment's own
// shop_id or the delivery is treated as unrecognized — a defensive assertion,
// not the primary guard: using the wrong shop's URL already fails signature
// verification with that shop's key before this function is ever called
// (design D2's safety argument), so in the ordinary case this check can never
// actually trip.
func (s *Service) HandleWebhook(ctx context.Context, providerName string, shopID int, res *WebhookResult) (*ent.Payment, error) {
	p, err := s.Client.Payment.Query().
		Where(entpayment.ProviderEQ(providerName), entpayment.ProviderReferenceEQ(res.ProviderReference)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("payment: query payment: %w", err)
	}
	if shopID > 0 && p.ShopID != shopID {
		return nil, ErrNotFound
	}

	// design D7: before any state moves, the two systems must agree on how much
	// money this is. A provider that does not report an amount sends 0.
	if res.Amount != 0 && res.Amount != p.Amount {
		return nil, fmt.Errorf("%w: provider says %d, payment %d records %d",
			ErrAmountMismatch, res.Amount, p.ID, p.Amount)
	}

	// design D4/D12: the provider's own identifier and payment timestamp are
	// recorded on EVERY delivery that carries them, including non-terminal ones
	// — 綠界's first notification for a transaction may well be a pending one,
	// and its TradeNo is exactly what the merchant reconciles with.
	if p, err = s.recordProviderFacts(ctx, p, res); err != nil {
		return nil, err
	}

	target := statusForOutcome(res.Outcome)

	// design D6: a non-terminal delivery is acknowledged and nothing else. The
	// row must stay pending — that is the entire reason Outcome has a third value.
	if target == StatusPending {
		return p, nil
	}

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

	// change order-and-invitation-polish, design D3: a succeeded payment always
	// has a paid_at. The mock provider reports no timestamp and 綠界 can omit
	// PaymentDate, which left paid_at NULL on a row whose status says paid —
	// the merchant's reconciliation view could not say WHEN. The provider's own
	// stamp (recordProviderFacts above) wins when there is one; otherwise this
	// is the moment we processed the confirmation. Also runs on redelivery, so a
	// row that already succeeded without a stamp heals on its next delivery.
	if p.Status == StatusSucceeded && p.PaidAt == nil {
		if p, err = s.stampPaidAt(ctx, p); err != nil {
			return nil, err
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
		ord, err := s.Orders.GetOrderAdmin(ctx, p.ShopID, p.OrderID)
		if err != nil {
			if errors.Is(err, order.ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("payment: query order for confirmation: %w", err)
		}

		// ── change ecpay-payment-provider, design D9 ────────────────────
		//
		// The customer was still on the provider's cashier page when the order
		// got cancelled, and paid anyway. **Measured before this branch existed:**
		// the order came out status=cancelled AND payment_status=paid, with the
		// stock already restored and OrderPaymentSucceeded published — so the
		// customer earned loyalty points for an order that will never ship, and
		// nothing anywhere errored.
		//
		// The payment row still becomes succeeded above: the money really moved,
		// and recording it as anything else would be lying in the financial trail
		// to make a status table look tidy — the exact opposite of what manual
		// reconciliation needs. What must not happen is the ORDER advancing.
		//
		// Refunding this is a manual process (design Non-Goals/D15). The system's
		// obligation ends at "the merchant can always find it", which is what
		// GET /admin/shops/{shopID}/payments exists for.
		if ord.Status == order.StatusCancelled {
			if s.Log != nil {
				s.Log.Warn("payment succeeded for a CANCELLED order — needs a manual refund",
					"shop_id", p.ShopID, "order_id", p.OrderID, "payment_id", p.ID,
					"provider", p.Provider, "provider_reference", p.ProviderReference,
					"provider_transaction_id", derefString(p.ProviderTransactionID),
					"amount", p.Amount, "currency", p.Currency)
			}
			return p, nil
		}

		ord, err = s.Orders.UpdatePaymentStatus(ctx, p.ShopID, p.OrderID, OrderPaymentStatusPaid)
		if err != nil {
			return nil, fmt.Errorf("payment: mark order paid: %w", err)
		}
		if s.Dispatcher != nil {
			// The loyalty award basis, and the single place it is defined
			// (change tier-discount-application, design D7): what the customer
			// actually paid for goods. Not TotalAmount — that includes the
			// courier's fee, and points reward the purchase, not the logistics
			// (change order-extras, design D5). Not SubtotalAmount alone — a tier
			// discount and the points it earns are two forms of one loyalty
			// budget, so awarding on the list price would multiply the merchant's
			// cost behind a knob showing them a single number. The amount charged
			// is still ord.TotalAmount.
			s.Dispatcher.Publish(ctx, events.OrderPaymentSucceeded{
				ShopID: ord.ShopID, OrderID: ord.ID, MemberID: ord.MemberID,
				AwardBasisAmount: ord.SubtotalAmount - ord.DiscountAmount,
				Currency:         ord.Currency,
			})
		}
	}

	return p, nil
}

// recordProviderFacts persists the provider's own transaction identifier and
// payment timestamp (change ecpay-payment-provider, design D4/D12).
//
// Both are written **only when currently unset**: 綠界 redelivers, and the first
// delivery that carried a value is the authoritative one. A later delivery
// carrying nothing must not erase it, and one carrying the same thing has
// nothing to add.
func (s *Service) recordProviderFacts(ctx context.Context, p *ent.Payment, res *WebhookResult) (*ent.Payment, error) {
	upd := s.Client.Payment.UpdateOneID(p.ID)
	changed := false
	if res.ProviderTransactionID != "" && derefString(p.ProviderTransactionID) == "" {
		upd = upd.SetProviderTransactionID(res.ProviderTransactionID)
		changed = true
	}
	if res.PaidAt != nil && p.PaidAt == nil {
		upd = upd.SetPaidAt(*res.PaidAt)
		changed = true
	}
	if !changed {
		return p, nil
	}
	out, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("payment: record provider transaction facts: %w", err)
	}
	return out, nil
}

// stampPaidAt sets paid_at to now on a payment that has none, and only then (a
// provider's value, or a concurrent writer's, is never overwritten). It returns
// the row as stored.
func (s *Service) stampPaidAt(ctx context.Context, p *ent.Payment) (*ent.Payment, error) {
	if _, err := s.Client.Payment.Update().
		Where(entpayment.IDEQ(p.ID), entpayment.PaidAtIsNil()).
		SetPaidAt(time.Now()).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("payment: stamp paid_at: %w", err)
	}
	out, err := s.Client.Payment.Get(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("payment: reload payment: %w", err)
	}
	return out, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// ── manual reconciliation (change ecpay-payment-provider, design D10) ──

// AdminListParams filters the shop-wide payment ledger.
type AdminListParams struct {
	Page     int
	PageSize int
	// Q is matched EXACTLY (after trimming) against provider_reference and
	// provider_transaction_id, and additionally against the order id when it
	// parses as a positive integer.
	//
	// **Exact, not a substring match — deliberately unlike the order list's Q**
	// (change admin-commerce-operations design D3, which ILIKEs recipient
	// name/phone). The two answer different questions: that one looks for a
	// *person* and needs fuzziness; this one is "paste the number from 綠界's
	// back office". A substring search here would add no capability, could not
	// use an index, and would make `1` return half the table.
	Q string
}

// PaymentPage is a page of payments plus pagination metadata (mirrors
// order.OrderPage).
type PaymentPage struct {
	Payments []*ent.Payment
	Total    int
	Page     int
	PageSize int
}

// ListAdmin answers "I have this transaction from the provider's back office —
// which order is it, and does the amount match?".
//
// That is the direction the pre-existing endpoint could not go: ListForOrderAdmin
// requires you to already know the order id, while manual reconciliation starts
// from the provider's own number. Everything else it needs (both identifiers,
// amount, currency, timestamps) was already on the row — the gap was never the
// data, only the lookup (design D8).
//
// A foreign shop's identifier simply matches nothing: the shop_id predicate is
// unconditional, so this cannot reach across the tenant boundary even if a
// caller guesses a valid reference. An empty list is the honest answer for a
// list endpoint, and it leaks less than "it exists but is not yours".
func (s *Service) ListAdmin(ctx context.Context, shopID int, params AdminListParams) (*PaymentPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)

	q := s.Client.Payment.Query().Where(entpayment.ShopIDEQ(shopID))
	if kw := strings.TrimSpace(params.Q); kw != "" {
		preds := []predicate.Payment{
			entpayment.ProviderReferenceEQ(kw),
			entpayment.ProviderTransactionIDEQ(kw),
		}
		// "他說訂單編號是 1234" is the same question from the other side, and the
		// merchant should not have to know which box to type it into (the same
		// reasoning as the order list's keyword).
		if id, err := strconv.Atoi(kw); err == nil && id > 0 {
			preds = append(preds, entpayment.OrderIDEQ(id))
		}
		q = q.Where(entpayment.Or(preds...))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("payment: count payments: %w", err)
	}
	// Explicit order, never the database's choice (change commerce-demo-seed
	// design D8).
	rows, err := q.Order(entpayment.ByID(entsql.OrderDesc())).
		Limit(pageSize).Offset((page - 1) * pageSize).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("payment: list payments: %w", err)
	}
	return &PaymentPage{Payments: rows, Total: total, Page: page, PageSize: pageSize}, nil
}

// normalizePage mirrors order.normalizePage's bounds so the two paginated
// back-office lists behave identically.
func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	return page, pageSize
}
