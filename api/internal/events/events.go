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

// ContentPublished — a content document was published (or, for the global
// document, replaced). Fired by both the new document API and the legacy page
// endpoints (change headless-content-store slice 2). Subscriber: the render
// cache invalidator.
type ContentPublished struct {
	ShopID int
	Key    string
	// Rev is the draft_rev that was published (0 when the publisher does not
	// know it, e.g. the legacy page endpoints). Only the publish hook reads it.
	Rev int64
}

func (ContentPublished) EventName() string { return "content.published" }

// ContentUnpublished — a content document was taken offline or deleted.
type ContentUnpublished struct {
	ShopID int
	Key    string
}

func (ContentUnpublished) EventName() string { return "content.unpublished" }

// ContentSchemaRegistered — a human registered a new content schema version for
// the shop (change headless-content-store). Hydrated output of every cached
// page and the bundle's global content depends on the schema, so the whole
// shop's render cache is invalidated.
type ContentSchemaRegistered struct {
	ShopID  int
	Version int
}

func (ContentSchemaRegistered) EventName() string { return "content.schema_registered" }

// ShopStatusChanged — a shop's status changed (or a shop was provisioned).
// Subscribers drop every cache entry that embeds the shop's status: the render
// cache version is bumped, and the publishable-key cache entries are
// invalidated so a disabled shop's keys stop serving at the same moment.
type ShopStatusChanged struct {
	ShopIDs []int
}

func (ShopStatusChanged) EventName() string { return "shop.status_changed" }

// StorefrontClientChanged — a shop's publishable keys or allowed origins were
// created, revoked or deleted (change storefront-client-auth, design D10).
// Subscribers drop the affected `pkey:{key}` / `corsorigin2:{origin}` cache
// entries so revocation takes effect immediately instead of after the TTL.
// Either slice may be empty; both name the values as they were before the
// mutation, which is what the cache is keyed by.
type StorefrontClientChanged struct {
	ShopID  int
	Keys    []string
	Origins []string
}

func (StorefrontClientChanged) EventName() string { return "storefront.client_changed" }

// OrderPlaced — order.Service.Checkout committed a new order (change
// order-notification-email, design D15). Published from *inside* Checkout,
// after the transaction commits, which makes "exactly one event per order
// created" a structural property rather than a discipline every entry point has
// to remember: Checkout is the only implementation of order creation in this
// API, and the paths that do not create an order (validation failure,
// insufficient stock, cart already converted, and — the one that matters — an
// Idempotency-Key replay) never reach it.
//
// It carries identifiers only, and that is the deliberate opposite of
// OrderPaymentSucceeded's AwardBasisAmount. The rule behind both is the same:
// a number whose *definition* is subtle (the points basis has been renamed
// twice for exactly this reason) must be computed once, by the publisher; a
// whole aggregate's *current shape* (items, address, the amount triple,
// delivery date) must not be copied into an event, because that produces a
// second serialization of the order that will drift from the first — the same
// reasoning change member-directory used (design D2) to refuse a second order
// payload inside the customer endpoint.
//
// Subscribers re-read the order themselves, carrying shop_id explicitly: this
// event is delivered outside tenant context, so the interceptor adds nothing.
type OrderPlaced struct {
	ShopID   int
	OrderID  int
	MemberID int
}

func (OrderPlaced) EventName() string { return "order.placed" }

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
