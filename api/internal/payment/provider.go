// Package payment implements the payment-integration domain (change
// payment-integration): a provider-neutral gateway abstraction, a
// fully-testable mock provider, payment transaction records (shop-scoped,
// multiple attempts per order), and the webhook confirmation flow that
// advances orders.payment_status through the sole controlled entry point
// order.Service.UpdatePaymentStatus (order-management design D6) — this
// package never writes ent.Order directly.
//
// Adding a real provider (Stripe/綠界/藍新/...) later means implementing
// Provider once and registering it in Service.Providers; nothing in
// httpapi or Service's control flow needs to change (design D1).
package payment

import (
	"context"
	"net/http"
)

// Outcome is the terminal result a provider's webhook reports for one
// payment attempt (design D5 — Outcome maps 1:1 onto the non-pending
// payments.status values this change ever produces; "refunded" has no
// Outcome counterpart because no webhook in this change produces it).
type Outcome int

const (
	OutcomeSucceeded Outcome = iota
	OutcomeFailed
)

// InitiateRequest carries everything a provider needs to start a payment
// attempt. Amount/Currency are always the order's own total_amount/currency
// snapshot (design D3/Non-Goals — this change only supports full-amount
// payment; a partial-payment feature would add validation here later).
type InitiateRequest struct {
	ShopID   int
	OrderID  int
	Amount   int64
	Currency string
}

// InitiateResult is what a provider hands back after successfully starting
// a payment attempt. ProviderReference MUST be usable as an idempotent
// webhook lookup key (unique per attempt) — Service persists it verbatim in
// payments.provider_reference.
type InitiateResult struct {
	ProviderReference string
	RedirectURL       string
}

// WebhookRequest carries the raw inbound webhook request. Body is
// deliberately raw bytes, not a pre-parsed struct: signature verification
// (HMAC or otherwise) MUST run over the exact bytes the provider signed —
// decoding then re-encoding JSON can silently change byte content (field
// order, whitespace) and break verification. Headers is the raw
// http.Header because signature schemes vary per provider (header name,
// encoding); this is the same shape stripe-go's webhook verification takes.
type WebhookRequest struct {
	Headers http.Header
	Body    []byte
}

// WebhookResult is what a provider's webhook verification yields once the
// signature has checked out: which payment attempt this refers to, and its
// terminal outcome.
type WebhookResult struct {
	ProviderReference string
	Outcome           Outcome
}

// Provider is the provider-neutral gateway abstraction (design D1). Every
// provider — mock or real — implements exactly these two capabilities;
// Service and the HTTP layer only ever talk to this interface.
type Provider interface {
	// Name is the provider's registry key, also the {provider} path segment
	// on the webhook route and the payments.provider column value.
	Name() string
	// InitiatePayment starts a new payment attempt and returns provider-side
	// reference/redirect information. It MUST NOT perform any local state
	// mutation — Service owns creating the payments.Payment row.
	InitiatePayment(ctx context.Context, req InitiateRequest) (*InitiateResult, error)
	// VerifyWebhook authenticates an inbound webhook call by signature and,
	// only once authenticated, parses the payment outcome it reports.
	// Returns ErrInvalidSignature (or a wrapped form of it) when the
	// signature is missing or does not match.
	VerifyWebhook(ctx context.Context, req WebhookRequest) (*WebhookResult, error)
}
