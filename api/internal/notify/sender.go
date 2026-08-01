package notify

import (
	"context"
	"sync"
)

// Sender delivers one rendered message. It is the seam this change is built
// around, and its shape is copied from payment.Provider for the same three
// reasons that one has: an interface, a testable fake, and a real mechanism
// behind it.
//
// **Nothing in this package is Gmail-specific.** Gmail lives entirely in the
// default values of SMTP_HOST/SMTP_PORT. Moving to a transactional provider with
// domain authentication (design D9's exit) is, in the cheapest case, three
// environment variables — no code here changes. What that move does NOT solve on
// its own is per-merchant sender identity; that one needs data this system does
// not have yet (design D3).
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// DiscardSender delivers nowhere and always succeeds. It is what gets wired when
// no SMTP credentials are configured (design D10).
//
// It is a real implementation rather than a nil Sender on purpose: the wiring
// audit's exemption table is empty precisely because "legitimately nil" and
// "forgot to wire it" are indistinguishable at the point where it matters.
// Service treats this concrete type as "notifications are off" and skips the
// whole path — including creating rows — so an unconfigured deployment behaves
// byte for byte as it did before this change existed.
type DiscardSender struct{}

func (DiscardSender) Send(context.Context, Message) error { return nil }

// RecordingSender captures messages instead of sending them (design D8/測試
// 硬約束: automated tests must never reach a real mail service).
//
// It is the counterpart of payment.MockProvider: the fake is what lets the whole
// path — event → dedupe → render → send — be asserted end to end with no
// credentials and no network.
type RecordingSender struct {
	mu   sync.Mutex
	msgs []Message
	// Err, when non-nil, is returned by every Send. It is how the failure path
	// (row marked failed, order untouched) gets tested without an unreachable
	// mail server.
	Err error
}

func (r *RecordingSender) Send(_ context.Context, msg Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.msgs = append(r.msgs, msg)
	return nil
}

// Messages returns a copy of everything captured so far.
func (r *RecordingSender) Messages() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// Reset drops captured messages (and keeps Err).
func (r *RecordingSender) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = nil
}
