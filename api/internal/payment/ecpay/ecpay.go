package ecpay

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ksdevworks/ecommerce/api/internal/payment"
)

// ProviderName is the registry key, the {provider} webhook path segment, and
// the payments.provider column value.
const ProviderName = "ecpay"

// Currency is the only currency 綠界 accepts.
//
// This constant exists so the check below reads as a rule rather than as a
// string literal: TotalAmount is "整數新台幣元", and this codebase's amounts are
// int64 minor units — under TWD those are **the same number with no conversion**
// (the whole system contains no /100 or *100). That identity is what makes the
// integration trivial, and it is true only here: a USD order would be off by a
// factor of 100 and look completely reasonable while doing it.
const Currency = "TWD"

// Wire field names (綠界 /2862/, /2878/). Spelled out as constants because they
// are case-sensitive and a typo produces a wrong checksum, not an error.
const (
	fieldMerchantID        = "MerchantID"
	fieldMerchantTradeNo   = "MerchantTradeNo"
	fieldMerchantTradeDate = "MerchantTradeDate"
	fieldPaymentType       = "PaymentType"
	fieldTotalAmount       = "TotalAmount"
	fieldTradeDesc         = "TradeDesc"
	fieldItemName          = "ItemName"
	fieldReturnURL         = "ReturnURL"
	fieldChoosePayment     = "ChoosePayment"
	fieldEncryptType       = "EncryptType"
	fieldClientBackURL     = "ClientBackURL"

	fieldRtnCode      = "RtnCode"
	fieldTradeNo      = "TradeNo"
	fieldTradeAmt     = "TradeAmt"
	fieldPaymentDate  = "PaymentDate"
	fieldSimulatePaid = "SimulatePaid"
)

const (
	// rtnCodeSuccess — 綠界: 1 = 付款成功. **On its own it does not mean the money
	// arrived** — see SimulatePaid below.
	rtnCodeSuccess = "1"
	// rtnCodePendingConfirmation — 綠界 10300066「交易付款結果待確認中，請勿出貨」.
	// Neither success nor failure.
	rtnCodePendingConfirmation = "10300066"
	// simulatePaidYes — 綠界's back-office "模擬付款" button sends RtnCode=1,
	// RtnMsg=交易成功 and this flag, **and 綠界 does not settle any money**. The
	// documentation is explicit: do not ship, do not treat as paid.
	simulatePaidYes = "1"
)

// tradeDateLayout is 綠界's MerchantTradeDate / PaymentDate format.
const tradeDateLayout = "2006/01/02 15:04:05"

// taipei is UTC+8, hard-coded rather than loaded from tzdata.
//
// **This is the only timezone in the entire system, and it belongs to 綠界, not
// to the merchant.** change order-extras (design D3) established that the
// platform stores no per-shop timezone and deliberately avoids needing one; this
// does not overturn that, because the meaning of this timestamp is defined by
// 綠界's own clock, not by where the shop is.
var taipei = time.FixedZone("UTC+8", 8*60*60)

// maxItemNameRunes — 綠界 caps ItemName at 400 characters and its behaviour on
// overflow is to TRUNCATE, which produces a mangled trailing byte, which breaks
// the checksum, which loses the order. So the truncation has to be ours.
//
// Counted in runes, not bytes, for the same reason CartItem.gift_message is
// (change order-extras design D1): a byte cut lands in the middle of a Chinese
// character.
const maxItemNameRunes = 400

// maxTradeDescRunes — 綠界 caps TradeDesc at 200.
const maxTradeDescRunes = 200

// Config carries the credentials and endpoints one 綠界 merchant account needs.
//
// One Config always represents exactly ONE shop's account (design D1 no
// longer holds a platform-wide singleton — change ecpay-per-shop-credentials).
// `Provider` itself stays exactly as pure and DB-free as it always was: it has
// no idea whether its Config came from an environment variable or a decrypted
// database row. The per-shop resolution — looking up the shop's stored
// credentials, decrypting HashKey/HashIV, and constructing a Config — lives in
// ShopResolver (resolver.go), which is the only file in this package that
// touches ent/DB.
type Config struct {
	MerchantID string
	HashKey    string
	HashIV     string
	// CheckoutURL is where the customer's browser POSTs the form. **This is the
	// only thing that differs between simulated and live operation** (design D1):
	// point it at api/cmd/ecpay-sim, at payment-stage.ecpay.com.tw, or at
	// payment.ecpay.com.tw — the fields and the checksum are byte-identical.
	CheckoutURL string
	// ReturnURL is the absolute, publicly reachable URL 綠界 POSTs the payment
	// result to. 綠界 only supports ports 80 and 443 and does not support
	// internationalized domain names.
	ReturnURL string
	// ClientBackURL is where 綠界's page sends the browser afterwards. Optional;
	// without it 綠界's page shows no "return to shop" button.
	ClientBackURL string
	// ChoosePayment defaults to "Credit". 綠界's own advice is to name a specific
	// method rather than "ALL", because they keep adding methods and a new one
	// can break the notification flow.
	ChoosePayment string
}

// Provider implements payment.Provider for 綠界 全方位金流.
type Provider struct {
	cfg Config
	now func() time.Time
}

// New constructs the provider. now may be nil (defaults to time.Now).
func New(cfg Config, now func() time.Time) *Provider {
	if cfg.ChoosePayment == "" {
		cfg.ChoosePayment = "Credit"
	}
	if now == nil {
		now = time.Now
	}
	return &Provider{cfg: cfg, now: now}
}

func (p *Provider) Name() string { return ProviderName }

// InitiatePayment builds the AioCheckOut V5 field set and its checksum.
//
// It performs **no network call**: a form POST is made by the customer's
// browser, not by us, so "initiating" a 綠界 payment is a pure function of the
// order plus configuration. That is also why the whole flow is testable without
// a network and why simulated and live mode cannot diverge.
func (p *Provider) InitiatePayment(_ context.Context, req payment.InitiateRequest) (*payment.InitiateResult, error) {
	// The identity TotalAmount == amount only holds for TWD. Refusing here is
	// what stops a USD order from silently being charged 1/100 of its value
	// (design D7).
	if req.Currency != Currency {
		return nil, fmt.Errorf("ecpay: 綠界 only accepts %s, order currency is %q", Currency, req.Currency)
	}
	if req.Amount <= 0 {
		return nil, fmt.Errorf("ecpay: TotalAmount must be positive, got %d", req.Amount)
	}

	now := p.now()
	tradeNo, err := NewMerchantTradeNo(req.OrderID, now)
	if err != nil {
		return nil, err
	}

	fields := map[string]string{
		fieldMerchantID:      p.cfg.MerchantID,
		fieldMerchantTradeNo: tradeNo,
		// 綠界 expects Taiwan local time in this format.
		fieldMerchantTradeDate: now.In(taipei).Format(tradeDateLayout),
		// Fixed by 綠界 for AioCheckOut V5.
		fieldPaymentType: "aio",
		// No conversion. See the Currency constant.
		fieldTotalAmount:   strconv.FormatInt(req.Amount, 10),
		fieldTradeDesc:     truncateRunes(fmt.Sprintf("訂單 %s", tradeNo), maxTradeDescRunes),
		fieldItemName:      itemName(req),
		fieldReturnURL:     p.cfg.ReturnURL,
		fieldChoosePayment: p.cfg.ChoosePayment,
		// 1 = SHA256, fixed for V5.
		fieldEncryptType: "1",
	}
	if p.cfg.ClientBackURL != "" {
		fields[fieldClientBackURL] = p.cfg.ClientBackURL
	}

	return &payment.InitiateResult{
		ProviderReference: tradeNo,
		// Deliberately empty: 綠界 cannot be reached with a GET (design D2).
		// Consumers must check RedirectForm first; the contract says so and
		// docs/storefront-integration.md repeats it.
		RedirectURL: "",
		RedirectForm: &payment.RedirectForm{
			Method: http.MethodPost,
			Action: p.cfg.CheckoutURL,
			Fields: Sign(p.cfg.HashKey, p.cfg.HashIV, fields),
		},
	}, nil
}

// itemName renders the order into 綠界's ItemName format: entries joined by
// '#' (綠界 shows each as its own line), each item "名稱 NT$單價 x 數量",
// followed by the order-level adjustments so the listed lines reconcile with
// TotalAmount (sum of items − discount + shipping = total):
//
//	玫瑰花束 NT$3400 x 1#會員折扣 -NT$170#運費 NT$200
//
// Length: 綠界 caps ItemName at 400 characters (see maxItemNameRunes). The
// adjustment lines are never dropped — they are what makes the page add up —
// so when the whole string would overflow, trailing ITEM entries are dropped
// whole and replaced by a "…等共 N 項" marker; only a single item that alone
// exceeds the budget is cut, on a rune boundary.
func itemName(req payment.InitiateRequest) string {
	var tail []string
	if req.DiscountAmount > 0 {
		tail = append(tail, fmt.Sprintf("會員折扣 -NT$%d", req.DiscountAmount))
	}
	if req.ShippingFee > 0 {
		tail = append(tail, fmt.Sprintf("運費 NT$%d", req.ShippingFee))
	}

	items := make([]string, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, fmt.Sprintf("%s NT$%d x %d", sanitizeItemText(it.Name), it.UnitAmount, it.Quantity))
	}
	if len(items) == 0 {
		// ItemName is required; an order with no line snapshot still has to buy
		// something as far as 綠界 is concerned.
		items = []string{fmt.Sprintf("訂單 %d", req.OrderID)}
	}

	tailStr := strings.Join(tail, "#")
	budget := maxItemNameRunes // runes available for the item part
	if tailStr != "" {
		budget -= runeLen(tailStr) + 1 // + the '#' joining items and tail
	}
	join := func(itemPart string) string {
		if tailStr == "" {
			return itemPart
		}
		return itemPart + "#" + tailStr
	}

	if all := strings.Join(items, "#"); runeLen(all) <= budget {
		return join(all)
	}
	if len(items) == 1 {
		return join(truncateRunes(items[0], budget))
	}
	// Overflow: keep as many whole items as fit next to a "…等共 N 項" marker.
	marker := fmt.Sprintf("…等共 %d 項", len(items))
	kept := ""
	for _, it := range items {
		cand := it
		if kept != "" {
			cand = kept + "#" + it
		}
		if runeLen(cand)+1+runeLen(marker) > budget {
			break
		}
		kept = cand
	}
	if kept == "" {
		// Even the first item alone does not fit next to the marker: cut it.
		kept = truncateRunes(items[0], budget-1-runeLen(marker))
	}
	return join(kept + "#" + marker)
}

// sanitizeItemText keeps a product title from breaking ItemName's format: '#'
// is 綠界's line separator (a title containing it would split into two bogus
// lines), so it becomes the full-width '＃'; control characters (newlines,
// tabs) are replaced by a space.
func sanitizeItemText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '#':
			return '＃'
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// VerifyWebhook authenticates a payment result notification and parses it.
//
// The body is parsed from the RAW bytes as form-urlencoded, and the checksum is
// recomputed over **every field present**, including ones this package has never
// heard of — 綠界 adds fields per payment method (NeedExtraPaidInfo brings six
// more, all lowercase), and dropping an unknown field changes the checksum
// input. This is exactly the property payment.WebhookRequest's raw-bytes design
// makes possible (design D14).
func (p *Provider) VerifyWebhook(_ context.Context, req payment.WebhookRequest) (*payment.WebhookResult, error) {
	values, err := url.ParseQuery(string(req.Body))
	if err != nil {
		return nil, fmt.Errorf("ecpay: parse notification body: %w", err)
	}

	fields := make(map[string]string, len(values))
	for k := range values {
		fields[k] = values.Get(k)
	}

	got := fields[FieldCheckMacValue]
	if got == "" {
		return nil, payment.ErrInvalidSignature
	}
	want := CheckMacValue(p.cfg.HashKey, p.cfg.HashIV, fields)
	// The checksum is a digest of a shared secret, not a MAC over attacker-
	// chosen data, and 綠界 sends it uppercase; compare case-insensitively so a
	// proxy lowercasing it does not read as forgery.
	if !strings.EqualFold(got, want) {
		return nil, payment.ErrInvalidSignature
	}

	ref := fields[fieldMerchantTradeNo]
	if ref == "" {
		return nil, fmt.Errorf("ecpay: notification has no %s", fieldMerchantTradeNo)
	}

	res := &payment.WebhookResult{
		ProviderReference:     ref,
		Outcome:               outcomeFor(fields),
		ProviderTransactionID: fields[fieldTradeNo],
	}
	if amt, err := strconv.ParseInt(fields[fieldTradeAmt], 10, 64); err == nil {
		res.Amount = amt
	}
	if t, err := time.ParseInLocation(tradeDateLayout, fields[fieldPaymentDate], taipei); err == nil {
		res.PaidAt = &t
	}
	return res, nil
}

// outcomeFor maps 綠界's RtnCode/SimulatePaid onto the provider-neutral Outcome
// (design D6).
//
//	RtnCode == 1 且 SimulatePaid != 1   → Succeeded
//	RtnCode == 1 且 SimulatePaid == 1   → Pending  ← 綠界不會撥款
//	RtnCode == 10300066                 → Pending  ← 待確認中，請勿出貨
//	其他                                 → Failed
//
// The two Pending rows are the reason payment.Outcome grew a third value. Both
// of them look like an ordinary result if you only read RtnCode, and treating
// either as Failed makes the payment row terminal — after which the real success
// notification can no longer advance the order, and nothing anywhere errors.
func outcomeFor(fields map[string]string) payment.Outcome {
	switch fields[fieldRtnCode] {
	case rtnCodeSuccess:
		if fields[fieldSimulatePaid] == simulatePaidYes {
			return payment.OutcomePending
		}
		return payment.OutcomeSucceeded
	case rtnCodePendingConfirmation:
		return payment.OutcomePending
	default:
		return payment.OutcomeFailed
	}
}

// Acknowledgement bodies. 綠界 requires the literal text `1|OK` — no quotes, no
// JSON, no trailing newline. Its documentation lists the near-misses it sees in
// the wild ("1|OK", 1|ok, 1OK, OK, _OK, 1\OK, empty), and a JSON envelope is
// simply one more of them. Failing to answer correctly makes 綠界 redeliver
// every 5–15 minutes, up to 4 times a day.
var (
	ackOK               = []byte("1|OK")
	ackInvalidSignature = []byte("0|CheckMacValue Error")
	ackMalformed        = []byte("0|Bad Request")
	ackFailed           = []byte("0|Internal Error")
)

// WebhookAck states what 綠界 has to see (design D3).
//
// AckIgnored answers `1|OK` on purpose: a MerchantTradeNo we do not recognize
// will not become recognizable by being sent again, so redelivering it costs
// both sides and tells 綠界 nothing. **AckFailed is the only non-2xx**, because
// it is the only case where a retry can actually help.
func (p *Provider) WebhookAck(ack payment.Ack) payment.WebhookResponse {
	const plain = "text/plain; charset=utf-8"
	switch ack {
	case payment.AckProcessed, payment.AckIgnored:
		return payment.WebhookResponse{Status: http.StatusOK, ContentType: plain, Body: ackOK}
	case payment.AckInvalidSignature:
		return payment.WebhookResponse{Status: http.StatusOK, ContentType: plain, Body: ackInvalidSignature}
	case payment.AckMalformed:
		return payment.WebhookResponse{Status: http.StatusOK, ContentType: plain, Body: ackMalformed}
	default: // payment.AckFailed
		return payment.WebhookResponse{Status: http.StatusInternalServerError, ContentType: plain, Body: ackFailed}
	}
}
