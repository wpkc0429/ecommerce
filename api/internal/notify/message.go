// Package notify implements order notification emails (change
// order-notification-email): the two customer-facing messages this platform
// sends — order placed, payment succeeded — and the machinery that gets them
// out without ever being able to break a checkout.
//
// ── Direction of dependency (design D1) ────────────────────────────────
//
// order/payment/shipping MUST NOT import this package. That is the direction
// that would matter: a Mailer reachable from order.Service.Checkout would sooner
// or later be wired into that method's error chain, and "the email failed" would
// become "the order did not happen". The only contact surface is the domain
// event layer plus one Subscribe line in app/wire.go — the same shape points
// uses.
//
// This package DOES import order, for one reason: order.ShippingAddress is the
// single definition of what lives in orders.shipping_address, and a second copy
// here would silently stop rendering any field a later change adds. payment and
// shipping already import order for the same class of reason. Nothing here calls
// a mutating order.Service method.
//
// ── Delivery model (design D5) ─────────────────────────────────────────
//
// events.Dispatcher.Publish is synchronous and runs on the request goroutine, so
// sending inline would put an SMTP round trip (0.5–1.5s to Gmail) on the
// checkout response. Service.Handle therefore only enqueues; a small fixed pool
// of workers does the reading, rendering and sending. This is NOT a work queue:
// nothing is persisted, nothing is retried, and a process restart loses whatever
// is still buffered. See design D5 for exactly what breaks and when.
package notify

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"strings"
)

// ErrHeaderInjection is returned when a value bound for a mail header contains a
// line break.
//
// This is not a theoretical concern: the shop name that goes into the subject
// and the From display name is merchant-entered free text (shops.name). A bare
// CR/LF there would let a merchant append arbitrary headers — Bcc being the
// obvious one — to every order email their shop sends.
var ErrHeaderInjection = errors.New("notify: header value contains a line break")

// Message is one email, fully rendered.
//
// **There is no From field, and that is deliberate** (design D3). Today every
// message leaves from one platform-owned mailbox configured on the sender, and a
// From here would be a field that always equals a config value — the exact
// mistake change member-directory named (a column that can only ever hold one
// value teaches every reader that it is variable). From/Reply-To belong in the
// same change that gives shops a sender identity to put in them, because that is
// what makes them mean something.
//
// TextBody is plain text. No HTML, no template engine: the rendered result is a
// string that a test can assert character for character, and there is no
// escaping obligation anywhere in this package.
type Message struct {
	To       string
	Subject  string
	TextBody string
}

// Encode renders the message as RFC 5322 bytes ready for SMTP DATA.
//
// Three things here are not optional, and all three fail only in a real mailbox
// — never on a developer's machine (design D14):
//
//   - The subject is RFC 2047 encoded-word. A Chinese subject placed raw into a
//     header is 8-bit data in a field defined as ASCII; MTAs mangle or reject it.
//   - The body declares AND uses base64. SMTP treats the body as 7-bit unless
//     8BITMIME is negotiated end to end, and a single relay without it corrupts
//     every Chinese character.
//   - Every header value is checked for line breaks first (see
//     ErrHeaderInjection).
//
// fromAddr/fromName come from configuration rather than the Message, per the
// type comment above.
func (m Message) Encode(fromAddr, fromName string) ([]byte, error) {
	for _, v := range []string{m.To, m.Subject, fromAddr, fromName} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, ErrHeaderInjection
		}
	}
	if strings.TrimSpace(m.To) == "" {
		return nil, errors.New("notify: message has no recipient")
	}

	from := fromAddr
	if fromName != "" {
		// mime.QEncoding leaves ASCII alone and encodes the rest, so an ASCII
		// shop name stays readable in the raw message.
		from = mime.QEncoding.Encode("UTF-8", fromName) + " <" + fromAddr + ">"
	}

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + mime.BEncoding.Encode("UTF-8", m.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(m.TextBody))
	return []byte(b.String()), nil
}

// wrapBase64 encodes s and folds it to 76-character lines with CRLF endings, as
// RFC 2045 requires. Some MTAs reject a single unbroken base64 line, and an
// order confirmation for a large basket comfortably exceeds any such limit.
func wrapBase64(s string) string {
	const lineLen = 76
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for i := 0; i < len(enc); i += lineLen {
		end := i + lineLen
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\r\n")
	}
	return b.String()
}

// String makes a Message printable in test failures without anyone reaching for
// fmt.Sprintf("%+v") on a struct full of PII by accident. It deliberately omits
// the body.
func (m Message) String() string {
	return fmt.Sprintf("notify.Message{To: <redacted>, Subject: %q, TextBody: %d bytes}", m.Subject, len(m.TextBody))
}
