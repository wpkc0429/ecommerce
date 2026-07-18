package payment

import "errors"

// ErrNotFound marks missing resources (handler → 404). Mirrors order.
// ErrNotFound/cart.ErrNotFound/catalog.ErrNotFound: also returned when an
// orderID exists but is owned by a different member, so member-scoped calls
// never leak cross-member existence.
var ErrNotFound = errors.New("payment: not found")

// Detail locates one validation problem (JSON Pointer-ish; same convention
// as order.Detail/cart.Detail/catalog.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D10, reused from
// order/cart/catalog's shared error envelope convention).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// ConflictError carries 409 payloads (design D10): the order's state
// doesn't allow the requested payment operation (cancelled, already paid).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }
