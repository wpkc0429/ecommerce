// Package design holds the design-history domain (change design-history,
// slice 1): the append-only record of every AI redesign of a shop's storefront
// or admin, its state machine, the hash-chained event log, the service
// credential and the retention purge.
//
// The platform owns this record; the AI edit service is only a writer with a
// narrow credential. Nothing in this package lets a service credential approve.
package design

import (
	"net/http"
	"regexp"
)

// Statuses of a request (design D4).
const (
	StatusDrafting         = "drafting"
	StatusPreviewing       = "previewing"
	StatusAwaitingApproval = "awaiting_approval"
	StatusApproved         = "approved"
	StatusPublished        = "published"
	StatusRejected         = "rejected"
	StatusFailed           = "failed"
	StatusCancelled        = "cancelled"
	StatusReverted         = "reverted"
)

// Event kinds (design D2).
const (
	EvCreated           = "created"
	EvClaimed           = "claimed"
	EvHeadUpdated       = "head_updated"
	EvGuardRun          = "guard_run"
	EvPreviewRegistered = "preview_registered"
	EvScreenshotsDone   = "screenshots_complete"
	EvSubmitted         = "submitted"
	EvChangesRequested  = "changes_requested"
	EvApproved          = "approved"
	EvRejected          = "rejected"
	EvCancelled         = "cancelled"
	EvGuardRechecked    = "guard_rechecked"
	EvPublished         = "published"
	EvFailed            = "failed"
	EvRevertRequested   = "revert_requested"
	EvRevertedMark      = "reverted_mark"
	EvExpired           = "expired"
)

// Actor types of an event.
const (
	ActorService = "service"
	ActorUser    = "user"
	ActorSystem  = "system"
)

// Sites and kinds.
const (
	SiteStorefront = "storefront"
	SiteAdmin      = "admin"
	KindChange     = "change"
	KindRevert     = "revert"
)

// Permission nodes (seed/seed.go).
const (
	PermView          = "design.view"
	PermRequest       = "design.request"
	PermApprove       = "design.approve"
	PermRevert        = "design.revert"
	PermServiceManage = "design_service.manage"
)

// Error is a domain failure with the HTTP status and stable code it maps to.
type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// Sentinel-style constructors; each returns a fresh value so Details can be set.
func ErrNotFound() *Error { return newErr(http.StatusNotFound, "not_found", "not found") }
func ErrInvalidState(msg string) *Error {
	return newErr(http.StatusConflict, "invalid_state", msg)
}
func ErrInvalid(msg string) *Error {
	return newErr(http.StatusUnprocessableEntity, "validation_failed", msg)
}

var (
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	idemKeyR = regexp.MustCompile(`^[\x21-\x7e]{8,255}$`)
)

// IsSite reports whether s is a valid site.
func IsSite(s string) bool { return s == SiteStorefront || s == SiteAdmin }

// IsTerminal reports whether a status is final (a request in it no longer
// blocks a new one). published is final for the machine but may later be
// marked reverted.
func IsTerminal(s string) bool {
	switch s {
	case StatusPublished, StatusRejected, StatusFailed, StatusCancelled, StatusReverted:
		return true
	}
	return false
}

// ActiveStatuses are the non-terminal ones (the partial unique index).
var ActiveStatuses = []string{StatusDrafting, StatusPreviewing, StatusAwaitingApproval, StatusApproved}
