package ecpay

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxMerchantTradeNoLen is 綠界's hard limit on MerchantTradeNo: 20 characters,
// alphanumeric only, "均為唯一值，不可重複使用" (/2862/).
//
// The documentation does not define the *scope* of that uniqueness (per
// MerchantID? across the test and production environments?), so this package
// takes the most conservative reading and generates values that are unique
// globally. It also does not spell out — but clearly implies — that a number is
// burnt even when its transaction failed, so a retry must use a new one.
const MaxMerchantTradeNoLen = 20

// NewMerchantTradeNo builds the identifier we send to 綠界 and store verbatim in
// payments.provider_reference (design D4/D5).
//
// Format: `O{orderID}T{base36(unixMilli), uppercase}` — e.g. O1234TMZQ8K3PA.
//
// **Uniqueness comes from the database, not from optimism.** orders.id is the
// serial primary key of a single table, so it is unique across every shop
// (shop_id is just a column) — which is why the shop id is deliberately absent.
// Two attempts on the same order are separated by the millisecond stamp; if two
// ever landed in the same millisecond, payments' UNIQUE(provider, provider_
// reference) rejects the second. The arbiter is a constraint, the same
// discipline as checkout_idempotency_keys and shipments.
//
// **The order id is in there for humans only.** A merchant looking at 綠界's
// back office sees O1234T… and knows it is order 1234. **Nothing parses this
// string** — the machine answer to "which order is this transaction" is
// GET /admin/shops/{shopID}/payments?q=… (design D10). Keeping "readable" and
// "reliable" as two separate mechanisms is what stops the next person from
// writing strings.Split(ref, "T")[0][1:].
func NewMerchantTradeNo(orderID int, now time.Time) (string, error) {
	no := "O" + strconv.Itoa(orderID) + "T" +
		strings.ToUpper(strconv.FormatInt(now.UnixMilli(), 36))
	if len(no) > MaxMerchantTradeNoLen {
		// Fail here rather than let 綠界 reject it: a 500 in our own handler with
		// this message is enormously easier to diagnose than a Chinese error code
		// on 綠界's cashier page. base36 milliseconds stay 8 characters until
		// ~2059, so this needs a 10-digit order id to trigger.
		return "", fmt.Errorf("ecpay: MerchantTradeNo %q is %d chars, 綠界 allows at most %d",
			no, len(no), MaxMerchantTradeNoLen)
	}
	return no, nil
}
