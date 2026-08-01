// Package events is the domain-event layer that centralizes cache
// invalidation (design D8): services publish events after their transaction
// commits; subscribers (render cache, route cache) react. Handlers must never
// be called before the write is durable — 失效與寫入交易成功綁定.
package events

import (
	"context"
	"log/slog"
)

// Event is a domain event. Concrete types below.
type Event interface{ EventName() string }

// PagePublished — publish action completed; the page's cache key is deleted.
type PagePublished struct {
	ShopID int
	Slug   string
}

func (PagePublished) EventName() string { return "page.published" }

// PageUnpublished — page taken offline; its cache key is deleted.
type PageUnpublished struct {
	ShopID int
	Slug   string
}

func (PageUnpublished) EventName() string { return "page.unpublished" }

// ShopContentUpdated — shop global payload changed; tenant-wide version bump.
type ShopContentUpdated struct{ ShopID int }

func (ShopContentUpdated) EventName() string { return "shop.content_updated" }

// ThemeSwitched — shop switched theme; tenant-wide version bump.
type ThemeSwitched struct{ ShopID int }

func (ThemeSwitched) EventName() string { return "shop.theme_switched" }

// ThemeUpdated — platform updated a theme; every shop on it gets a bump.
type ThemeUpdated struct{ ThemeID int }

func (ThemeUpdated) EventName() string { return "theme.updated" }

// SiteMappingChanged — sites/site_shop rows changed; route cache entries for
// the affected hosts are dropped, and mapped shops get a version bump.
type SiteMappingChanged struct {
	Hosts   []string
	ShopIDs []int
}

func (SiteMappingChanged) EventName() string { return "site.mapping_changed" }

// StorefrontClientChanged — a shop's publishable keys or allowed origins were
// created, revoked or deleted (change storefront-client-auth, design D10).
// Subscribers drop the affected `pkey:{key}` / `corsorigin:{origin}` cache
// entries so revocation takes effect immediately instead of after the TTL.
// Either slice may be empty; both name the values as they were before the
// mutation, which is what the cache is keyed by.
type StorefrontClientChanged struct {
	ShopID  int
	Keys    []string
	Origins []string
}

func (StorefrontClientChanged) EventName() string { return "storefront.client_changed" }

// OrderPaymentSucceeded — payment.Service.HandleWebhook advanced an order's
// payment_status to paid (change member-tiers-and-points design D1): the
// first non-cache consumer of this dispatcher. Published every time the
// webhook's re-assertion branch re-confirms an already-succeeded payment
// (see payment.Service.HandleWebhook's own doc comment), not just on the
// first delivery — subscribers MUST be idempotent on (ShopID, OrderID).
//
// AwardBasisAmount is the money the customer actually paid for goods:
// orders.subtotal_amount − orders.discount_amount. It is NOT the payable total
// (which includes shipping) and NOT the list-price subtotal (which ignores the
// member tier discount).
//
// The name states the role rather than the arithmetic on purpose (change
// tier-discount-application, design D7): the basis is defined in exactly one
// place — the publisher, right next to the order row it reads — and points.Service
// never re-derives it. A name describing the arithmetic would start lying the
// first time something else is netted off.
//
// This field has now been renamed twice (TotalAmount → SubtotalAmount →
// AwardBasisAmount) and both times for the same reason (order-extras design D8):
// adding a field beside it instead would let any construction site that forgets
// the new one keep compiling and silently award the wrong number, while a rename
// stops the build until every site has been revisited.
type OrderPaymentSucceeded struct {
	ShopID           int
	OrderID          int
	MemberID         int
	AwardBasisAmount int64
	Currency         string
}

func (OrderPaymentSucceeded) EventName() string { return "order.payment_succeeded" }

// OrderReturned — shipping.Service.AdvanceShipment transitioned a shipment
// (and its order's fulfillment_status) to returned (change
// member-tiers-and-points design D1/D5). Unlike OrderPaymentSucceeded, this
// transition's own CAS guard means it is published at most once per order in
// practice, but subscribers should still treat it as idempotent.
type OrderReturned struct {
	ShopID   int
	OrderID  int
	MemberID int
}

func (OrderReturned) EventName() string { return "order.returned" }

// Handler consumes events; it must be non-blocking-ish and never panic the
// request (the dispatcher recovers and logs).
type Handler func(ctx context.Context, e Event)

// Dispatcher fans events out synchronously (post-commit, same request).
type Dispatcher struct {
	log      *slog.Logger
	handlers []Handler
}

func NewDispatcher(log *slog.Logger) *Dispatcher {
	return &Dispatcher{log: log}
}

// Subscribe registers a handler for all events (handlers filter by type).
func (d *Dispatcher) Subscribe(h Handler) {
	d.handlers = append(d.handlers, h)
}

// Subscribers returns the registered handlers, in subscription order.
//
// It exists for exactly one reason, and no production path calls it: **"who
// subscribed to what" is otherwise not a checkable fact.** The packages on both
// ends of a domain event never import each other (points deliberately never
// imports order/payment/shipping), so the entire connection between them is a
// line of app/wire.go — and deleting that line has been measured to leave the
// whole Go suite green while the real server silently stops awarding points
// (change commerce-demo-seed). Handing out the subscriber list lets a CI test
// assert the set, which is the only thing that turns that deletion red anywhere
// other than an e2e script nobody is obliged to run (change
// contract-router-completeness, design D8).
//
// The result is a copy: reading the wiring must not be a way to change it.
func (d *Dispatcher) Subscribers() []Handler {
	out := make([]Handler, len(d.handlers))
	copy(out, d.handlers)
	return out
}

// Publish delivers events to every subscriber. Call only after the writing
// transaction committed successfully.
func (d *Dispatcher) Publish(ctx context.Context, evs ...Event) {
	for _, e := range evs {
		for _, h := range d.handlers {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						d.log.Error("event handler panic", "event", e.EventName(), "panic", rec)
					}
				}()
				h(ctx, e)
			}()
		}
	}
}
