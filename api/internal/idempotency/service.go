// Package idempotency implements the client-supplied idempotency key that makes
// re-sending a checkout request safe (change checkout-idempotency).
//
// The problem it solves is NOT the double-click — that one a frontend can fix by
// disabling its own button. It is the one a frontend cannot fix: when a response
// is lost, the client cannot tell "the request never arrived" from "the request
// arrived, the order exists, and only the reply went missing". Only the server
// can answer that, so only the server can make a retry safe.
//
// # The arbiter is the database, never a read-then-write
//
// Reserving a key is an INSERT against UNIQUE(shop_id, idempotency_key); losing
// that INSERT is how a duplicate request discovers it is a duplicate. Completing
// and releasing a reservation are a conditional UPDATE/DELETE plus an
// affected-rows check. Both halves are the discipline this codebase already
// applies wherever getting it wrong is unrecoverable:
//
//	payments(provider, provider_reference)   webhook idempotency (change payment-integration)
//	point_transactions(order_id, kind)       at most one award / clawback per order
//	UPDATE ... WHERE stock_qty >= ?          the checkout stock decrement
//
// # Scope
//
// A key belongs to (shop_id, key). Shop A can never replay shop B's key: the
// same string used by both is simply two independent rows. Every query below
// carries shop_id explicitly even though the table is registered in
// tenant.tenantOwned and the interceptor would add it — the intent stays
// readable at the call site, and correctness never depends on ambient context.
//
// The *fingerprint* (see Fingerprint) additionally covers the caller, which is
// what stops two customers of one shop who happened to pick the same weak key
// and post a byte-identical body from seeing each other's order.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	entidem "ksdevworks/ecommerce/api/internal/ent/checkoutidempotencykey"
)

// Reservation status values, mirrored by checkout_idempotency_keys.status and
// its CHECK constraint. order.Service.Checkout finalizes a reservation inside
// its own transaction, so it needs these too.
const (
	StatusInProgress int16 = 0
	StatusCompleted  int16 = 1
)

// Key bounds (design D2). Deliberately bounds only — no structure is required,
// because the IETF idempotency-key draft says the value is opaque and because
// demanding UUIDs would reject perfectly good generators for nothing.
const (
	// MinKeyLen guards the client against a bug it cannot otherwise see: a
	// one-character key all but guarantees a collision with another customer of
	// the same shop, and the symptom of that collision is *the other customer
	// cannot check out*. Eight is absurdly generous for any real generator
	// (UUIDv4 is 36) and free as a railing.
	MinKeyLen = 8
	// MaxKeyLen keeps the btree key sane on an endpoint that needs no
	// authentication. Nothing legitimate comes close.
	MaxKeyLen = 255
)

// Errors returned by Begin. Handlers map them to the three new error codes; no
// other package interprets them.
var (
	// ErrInvalidKey means the header value violated the bounds above.
	ErrInvalidKey = errors.New("idempotency: key must be 8-255 printable ASCII characters")
	// ErrKeyReused means this key is already bound to a different request
	// (different caller scope, or different request body bytes).
	ErrKeyReused = errors.New("idempotency: key already used for a different request")
	// ErrKeyInProgress means another request holding this key is still running,
	// so its outcome is genuinely unknown. Retrying later is safe: it will find
	// either the replay (the winner committed) or a free key (the winner failed
	// and released it).
	ErrKeyInProgress = errors.New("idempotency: another request with this key is in progress")
)

// ScopeGuestCheckout is the caller scope for the unauthenticated guest checkout
// endpoint. It is a constant because a guest has no identity yet — but the guest
// request body carries the email, so two guests can only collide by submitting
// byte-identical requests, which *is* the same purchase intent.
const ScopeGuestCheckout = "guest-checkout"

// MemberCheckoutScope is the caller scope for member checkout. Including the
// member id is not decoration (design D7): the member checkout body is just a
// shipping address, and two different customers shipping to the same address
// produce byte-identical bodies. Without this, a key collision would hand one
// customer another customer's order.
func MemberCheckoutScope(memberID int) string {
	return "member-checkout:" + strconv.Itoa(memberID)
}

// Fingerprint identifies one request: the caller scope plus the raw request body
// bytes, SHA-256, hex.
//
// The body is hashed raw, with no normalization. A canonicalizing hash sounds
// friendlier but has a fatal property: canonicalization means "parse into the
// fields we know about and re-emit", so it silently erases any field we have not
// added yet — and the first change to add an optional key would make two
// genuinely different requests hash the same, replaying the wrong order. Raw
// bytes never drift from the actual request and need no maintenance.
//
// The cost is real and must be documented for consumers: a retry has to resend
// byte-identical content. Every practical retry does (it re-serializes the same
// object through the same code path); a client that changes the payload between
// attempts is expressing a new intent and should mint a new key.
func Fingerprint(scope string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(scope))
	h.Write([]byte{'\n'})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateKey checks the header value against the bounds. It deliberately
// enforces no structure.
func ValidateKey(key string) error {
	if len(key) < MinKeyLen || len(key) > MaxKeyLen {
		return ErrInvalidKey
	}
	for i := 0; i < len(key); i++ {
		// Printable ASCII only: excludes control characters and newlines (this
		// value reaches logs and psql), and excludes multi-byte encodings whose
		// "length" would be ambiguous.
		if key[i] < 0x21 || key[i] > 0x7E {
			return ErrInvalidKey
		}
	}
	return nil
}

// Reservation is the outcome of Begin. Exactly one of the two fields is set.
type Reservation struct {
	// ID > 0: this call owns the key. The caller MUST either pass it to
	// order.Service.Checkout (which finalizes it inside the order transaction)
	// or Release it.
	ID int
	// ReplayOrderID > 0: this key already produced that order. Nothing was
	// reserved; there is nothing to release.
	ReplayOrderID int
}

// Service owns checkout_idempotency_keys. Like every other service here it takes
// an explicit shopID rather than reading ambient tenant context.
type Service struct {
	Client *ent.Client
	// TTL bounds how long a key stays replayable. Its lower bound is the real
	// retry span (seconds to hours: a 15s timeout, a customer reopening the tab
	// that evening); its upper bound is the reverse failure — too long and a
	// client that wrongly reuses a key turns a genuine second purchase into a
	// silent replay, which neither the shop nor the customer can see. 24h is the
	// boundary between "still today" and "certainly a new order", and matches
	// ORDER_ACCESS_TOKEN_TTL's identical reasoning.
	TTL time.Duration
}

func (s *Service) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return 24 * time.Hour
}

// Begin reserves key for shopID against fingerprint.
//
// Order of operations matters. The INSERT comes first so the common case (a
// fresh key) costs exactly one statement, and so a duplicate request is turned
// away *before* it touches any domain logic — a loser never reaches the stock
// decrement, which is what makes "concurrent same key ⇒ exactly one order"
// true by construction rather than by timing.
//
//	INSERT succeeds            → reserved, this call owns the key
//	INSERT hits UNIQUE         → load the row and decide:
//	    expired                → atomically take it over (a fresh reservation)
//	    fingerprint differs    → ErrKeyReused
//	    status in_progress     → ErrKeyInProgress
//	    status completed       → replay that order
func (s *Service) Begin(ctx context.Context, shopID int, key, fingerprint string) (*Reservation, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	now := time.Now()
	row, err := s.Client.CheckoutIdempotencyKey.Create().
		SetShopID(shopID).
		SetIdempotencyKey(key).
		SetRequestHash(fingerprint).
		SetStatus(StatusInProgress).
		SetExpiresAt(now.Add(s.ttl())).
		Save(ctx)
	if err == nil {
		return &Reservation{ID: row.ID}, nil
	}
	if !ent.IsConstraintError(err) {
		return nil, fmt.Errorf("idempotency: reserve key: %w", err)
	}
	return s.resolveExisting(ctx, shopID, key, fingerprint)
}

// resolveExisting decides what an already-taken key means for this request.
func (s *Service) resolveExisting(ctx context.Context, shopID int, key, fingerprint string) (*Reservation, error) {
	existing, err := s.Client.CheckoutIdempotencyKey.Query().
		Where(entidem.ShopIDEQ(shopID), entidem.IdempotencyKeyEQ(key)).
		Only(ctx)
	if err != nil {
		// The row vanished between the failed INSERT and this read (only an
		// expiry takeover by a concurrent request can do that, and it re-creates
		// the row rather than deleting it — so this is a genuine anomaly, not a
		// normal race). Surface it rather than guessing.
		return nil, fmt.Errorf("idempotency: load existing key: %w", err)
	}

	if !existing.ExpiresAt.After(time.Now()) {
		res, taken, err := s.takeOverExpired(ctx, existing.ID, fingerprint)
		if err != nil {
			return nil, err
		}
		if taken {
			return res, nil
		}
		// Another request took it over first; re-read and fall through to the
		// ordinary decision below against the fresh state.
		existing, err = s.Client.CheckoutIdempotencyKey.Query().
			Where(entidem.ShopIDEQ(shopID), entidem.IdempotencyKeyEQ(key)).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("idempotency: reload key after takeover race: %w", err)
		}
	}

	// A key is bound to one request. Anything else — a changed body, a different
	// customer, the other checkout endpoint — is a reuse, and refusing is the
	// safe answer: replaying would answer a new intent with old data, which is
	// worse than creating a second order.
	if existing.RequestHash != fingerprint {
		return nil, ErrKeyReused
	}
	if existing.Status == StatusCompleted && existing.OrderID != nil {
		return &Reservation{ReplayOrderID: *existing.OrderID}, nil
	}
	return nil, ErrKeyInProgress
}

// takeOverExpired atomically claims an expired row as a fresh reservation. The
// affected-rows check is what makes it safe under concurrency: of two requests
// racing to take over the same expired key, exactly one sees 1.
func (s *Service) takeOverExpired(ctx context.Context, id int, fingerprint string) (*Reservation, bool, error) {
	now := time.Now()
	n, err := s.Client.CheckoutIdempotencyKey.Update().
		Where(entidem.IDEQ(id), entidem.ExpiresAtLTE(now)).
		SetRequestHash(fingerprint).
		SetStatus(StatusInProgress).
		ClearOrderID().
		SetExpiresAt(now.Add(s.ttl())).
		Save(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("idempotency: take over expired key: %w", err)
	}
	if n != 1 {
		return nil, false, nil
	}
	return &Reservation{ID: id}, true, nil
}

// Release frees a reservation whose request did not create an order (design D6):
// a bad body, a validation failure, insufficient stock, a disabled customer.
//
// Releasing rather than recording the failure is not politeness, it is a
// compatibility requirement. A client that gets "phone is required" fixes the
// field and resends — ONDO's checkout does exactly this, keeping the same key.
// A corrected resend necessarily carries different bytes, so a retained row
// (even one holding only the hash) would answer it with "key reused" and the
// customer could never submit at all.
//
// The status predicate makes it impossible for this to delete a reservation that
// already produced an order, even if some future caller invokes it out of order.
func (s *Service) Release(ctx context.Context, reservationID int) error {
	_, err := s.Client.CheckoutIdempotencyKey.Delete().
		Where(entidem.IDEQ(reservationID), entidem.StatusEQ(StatusInProgress)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("idempotency: release reservation: %w", err)
	}
	return nil
}
