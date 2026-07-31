package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/idempotency"
)

// Headers introduced by change checkout-idempotency. Both are also listed in the
// CORS middleware — Idempotency-Key in Allow-Headers (or browsers reject the
// actual request), Idempotency-Replayed in Expose-Headers (or JS cannot read
// it).
const (
	// IdempotencyKeyHeader is the industry-standard spelling (Stripe, and
	// draft-ietf-httpapi-idempotency-key-header). Inventing a third name would
	// buy nothing; using this one means the next engineer to integrate can guess
	// it without reading our docs.
	IdempotencyKeyHeader = "Idempotency-Key"
	// IdempotencyReplayedHeader marks a replayed response. It exists precisely
	// because the status code and body are deliberately indistinguishable from
	// the original (that is what idempotency means), which would otherwise leave
	// an integrator with no way at all to confirm their key is working. It is a
	// header rather than a body key on purpose: diagnostics, not data — so it
	// stays out of the response schemas, out of additionalProperties:false, and
	// out of the v1 surface lock.
	IdempotencyReplayedHeader = "Idempotency-Replayed"
)

// New error codes (change checkout-idempotency). All three can only ever occur
// on a request that carried the header, so nothing that worked before can start
// seeing them.
const (
	codeInvalidIdempotencyKey    = "invalid_idempotency_key"
	codeIdempotencyKeyReused     = "idempotency_key_reused"
	codeIdempotencyKeyInProgress = "idempotency_key_in_progress"
)

// readJSONBody reads at most 1 MiB of the request body and returns the raw
// bytes.
//
// Checkout needs the bytes twice — once to fingerprint, once to decode — so it
// cannot use decodeJSON, which streams straight into a decoder. decodeJSON is
// left untouched: every other endpoint keeps its exact current behavior.
func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpx.BadRequest(w, "request body too large")
		return nil, false
	}
	return body, true
}

// unmarshalJSON decodes already-read bytes, writing the same 400 decodeJSON
// would have written.
func unmarshalJSON(w http.ResponseWriter, raw []byte, dst any) bool {
	if err := json.Unmarshal(raw, dst); err != nil {
		httpx.BadRequest(w, "invalid JSON body")
		return false
	}
	return true
}

// idemGate carries a request's idempotency reservation, if it holds one.
type idemGate struct {
	svc *idempotency.Service
	log *slog.Logger
	id  int
}

// reservation returns the value to hand to the checkout service (nil when this
// request holds no reservation, which is the default and unchanged path).
func (g idemGate) reservation() *int {
	if g.id == 0 {
		return nil
	}
	id := g.id
	return &id
}

// release frees the key because this request did not create an order (design
// D6). Safe and cheap to call when no reservation is held.
//
// A failure to release is logged, not surfaced: the customer's problem is the
// error we are already about to report, and the stranded row expires on its own.
func (g idemGate) release(r *http.Request) {
	if g.id == 0 || g.svc == nil {
		return
	}
	if err := g.svc.Release(r.Context(), g.id); err != nil && g.log != nil {
		g.log.Error("release idempotency reservation", "reservation_id", g.id, "err", err)
	}
}

// beginIdempotency evaluates the Idempotency-Key header for one checkout
// request.
//
//	no header                → zero gate, replay 0, ok true (unchanged behavior)
//	bad format               → 422 invalid_idempotency_key,    ok false
//	bound to another request → 422 idempotency_key_reused,     ok false
//	another request running  → 409 idempotency_key_in_progress, ok false
//	already produced an order→ replayOrderID > 0, ok true (caller renders it)
//	reserved                 → gate holds the reservation, ok true
//
// scope is the caller scope for the fingerprint (idempotency.ScopeGuestCheckout
// or idempotency.MemberCheckoutScope) — see design D7 for why the caller, not
// just the body, has to be in it.
func beginIdempotency(
	w http.ResponseWriter, r *http.Request,
	svc *idempotency.Service, log *slog.Logger,
	shopID int, scope string, rawBody []byte,
) (idemGate, int, bool) {
	key := r.Header.Get(IdempotencyKeyHeader)
	if key == "" {
		return idemGate{}, 0, true
	}
	if svc == nil {
		// Wiring gap, not a client error. Answering with an error would take a
		// working checkout down; silently ignoring it would let duplicate orders
		// through with nobody the wiser. So: proceed, and make the gap loud —
		// internal/app/wire.go has zero Go test coverage (change
		// commerce-demo-seed), so this log and the e2e replay assertion are what
		// stand between a missed wire-up and a silent regression.
		if log != nil {
			log.Error("idempotency key supplied but no idempotency service is wired; "+
				"the request will NOT be deduplicated", "shop_id", shopID)
		}
		return idemGate{}, 0, true
	}

	res, err := svc.Begin(r.Context(), shopID, key, idempotency.Fingerprint(scope, rawBody))
	switch {
	case err == nil:
	case errors.Is(err, idempotency.ErrInvalidKey):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInvalidIdempotencyKey,
			"Idempotency-Key must be 8-255 printable ASCII characters", nil)
		return idemGate{}, 0, false
	case errors.Is(err, idempotency.ErrKeyReused):
		// No details: the existing details[].pointer convention is a JSON Pointer
		// into the request *body*, and this problem lives in a header. Inventing
		// a pointer to "/" would make that convention untrustworthy, so the code
		// carries the whole message instead (same treatment as
		// checkout_unavailable / registration_failed).
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeIdempotencyKeyReused,
			"this Idempotency-Key was already used for a different request", nil)
		return idemGate{}, 0, false
	case errors.Is(err, idempotency.ErrKeyInProgress):
		// 409, not 429: the caller is not being throttled, an identical request
		// is simply still running. Deliberately no Retry-After — that header is
		// this API's rate-limit signal, and borrowing it here would put an
		// exception into an otherwise simple rule.
		httpx.WriteError(w, http.StatusConflict, codeIdempotencyKeyInProgress,
			"another request with this Idempotency-Key is still being processed; retry shortly", nil)
		return idemGate{}, 0, false
	default:
		if log != nil {
			log.Error("begin idempotency reservation", "shop_id", shopID, "err", err)
		}
		httpx.Internal(w)
		return idemGate{}, 0, false
	}

	if res.ReplayOrderID > 0 {
		return idemGate{}, res.ReplayOrderID, true
	}
	return idemGate{svc: svc, log: log, id: res.ID}, 0, true
}
