package payment

// Order-level payment_status enumeration (design D4) — this change is the
// one that finalizes the full enumeration space order-management left open
// (order.PaymentStatusUnpaid == 0 is order-management's own constant, kept
// there; the values below belong to this change and are the only new
// values introduced to orders.payment_status).
//
// Only OrderPaymentStatusPaid is ever written by any code path in this
// change (via order.Service.UpdatePaymentStatus, from
// Service.HandleWebhook). OrderPaymentStatusRefunded/
// OrderPaymentStatusPartiallyRefunded reserve their enumeration slot for a
// future refunds change — nothing in this change produces them.
const (
	OrderPaymentStatusPaid              int16 = 1
	OrderPaymentStatusRefunded          int16 = 2
	OrderPaymentStatusPartiallyRefunded int16 = 3
)

// payments.status enumeration (design D5). Pending is the row's initial
// value (Service.InitiatePayment); Succeeded/Failed are the only terminal
// values any code path in this change writes (via Service.HandleWebhook's
// CAS). Refunded reserves its slot for a future refunds change, same
// reasoning as OrderPaymentStatusRefunded above.
const (
	StatusPending   int16 = 0
	StatusSucceeded int16 = 1
	StatusFailed    int16 = 2
	StatusRefunded  int16 = 3
)

// statusForOutcome maps a provider's webhook Outcome onto the payments.status
// value Service.HandleWebhook's CAS targets.
//
// OutcomePending maps to StatusPending, i.e. the value the row already holds
// (change ecpay-payment-provider, design D6). HandleWebhook reads that as "there
// is no transition to make" and leaves the row claimable by a later terminal
// delivery — which is the entire point of the third Outcome.
func statusForOutcome(o Outcome) int16 {
	switch o {
	case OutcomeSucceeded:
		return StatusSucceeded
	case OutcomePending:
		return StatusPending
	default:
		return StatusFailed
	}
}
