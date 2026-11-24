package account

import (
	"context"
	"time"
)

// Delivery modes reported in API responses. Consumers MUST look at the mode
// before looking for a link: with "email" the response carries no link at all.
const (
	DeliveryManual = "manual" // the response carries the link; an admin hands it over
	DeliveryEmail  = "email"  // a mail was sent; the response carries no link
)

// LinkMessage is what a LinkDelivery gets to hand to the account owner.
type LinkMessage struct {
	Kind      string // KindSetup | KindReset
	Email     string
	URL       string
	ExpiresAt time.Time
}

// Delivery is the outcome reported back to the caller. URL is non-empty only
// when Mode is DeliveryManual.
type Delivery struct {
	Mode      string
	URL       string
	ExpiresAt time.Time
}

// LinkDelivery abstracts how a one-time link reaches its owner (design D4).
// Today the only implementation is ManualDelivery; a mail-sending one returns
// DeliveryEmail and an empty URL.
type LinkDelivery interface {
	Deliver(ctx context.Context, msg LinkMessage) (Delivery, error)
}

// ManualDelivery returns the link in the API response (there is no outbound
// mail for admin accounts yet).
type ManualDelivery struct{}

// Deliver implements LinkDelivery.
func (ManualDelivery) Deliver(_ context.Context, msg LinkMessage) (Delivery, error) {
	return Delivery{Mode: DeliveryManual, URL: msg.URL, ExpiresAt: msg.ExpiresAt}, nil
}

// SetPasswordURL builds {base}/#/set-password/{token}. The token sits in the
// fragment so browsers never send it to a server, proxy or Referer. The shape
// is a contract for every admin frontend (docs/admin-frontend-integration.md).
func SetPasswordURL(base, token string) string {
	return base + "/#/set-password/" + token
}
