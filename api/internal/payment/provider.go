// Package payment implements the payment-integration domain (change
// payment-integration): a provider-neutral gateway abstraction, a
// fully-testable mock provider, payment transaction records (shop-scoped,
// multiple attempts per order), and the webhook confirmation flow that
// advances orders.payment_status through the sole controlled entry point
// order.Service.UpdatePaymentStatus (order-management design D6) — this
// package never writes ent.Order directly.
//
// Adding a real provider (Stripe/綠界/藍新/...) later means implementing
// Provider once and registering it in Service.Providers; nothing in
// httpapi or Service's control flow needs to change (design D1).
//
// ── change ecpay-payment-provider: 那句話被撞了三次 ────────────────────
//
// 綠界 ECPay 是這個抽象遇到的第一家真實金流商，而上面那句承諾**有三處撐不住**
// （見該 change 的 design D2/D3/D6）。三處都是在**介面**上修好的，不是在呼叫端
// 特判修的——所以那句話今天仍然成立，只是介面比原本大：
//
//	D2  導向不只有「一個 URL」——綠界是 form POST 一組欄位 → InitiateResult.RedirectForm
//	D3  「這個 provider 的回呼要看到什麼位元組」是 provider 契約 → Provider.WebhookAck
//	D6  結果不只有成功／失敗——還有「收到了但不代表拿到錢」 → OutcomePending
//
// 撐住的那一個記在 WebhookRequest 上（design D14）：raw bytes 的設計連綠界
// 「簽章欄位就在 body 裡、且未知欄位必須一起參與計算」這種情況都接得住。
package payment

import (
	"context"
	"net/http"
	"time"
)

// Outcome is the result a provider's webhook reports for one payment attempt.
//
// **It is no longer 1:1 with the terminal payments.status values** (change
// ecpay-payment-provider, design D6, overturning the note payment-integration
// design D5 left here). A real provider has a third answer: "I received
// something, and it is neither success nor failure yet."
//
// 綠界有兩種這樣的通知，而**兩種都長得像成功**：
//
//   - SimulatePaid=1 —— 廠商後台的「模擬付款」按鈕送出的通知，RtnCode 也是 1，
//     RtnMsg 也是「交易成功」，但綠界不會撥款。只看 RtnCode 就會讓任何能登入商家
//     綠界後台的人把訂單標成已付款、核發點數、進待出貨清單，而帳上一毛錢都沒有。
//   - RtnCode=10300066「交易付款結果待確認中，請勿出貨」。
//
// 把它們映射成 OutcomeFailed 會踩到更難查的一條：payments 列被 CAS 成終局的
// failed，於是稍後真正的成功通知會輸掉 CAS、重讀後發現自己不是 succeeded，
// **訂單永遠不會被標記已付款**——錢進了商家的戶頭，而沒有任何東西噴錯。
type Outcome int

const (
	OutcomeSucceeded Outcome = iota
	OutcomeFailed
	// OutcomePending is "acknowledged, but not terminal". Service.HandleWebhook
	// performs no status transition for it — the payment row stays pending so a
	// later terminal delivery can still claim it.
	OutcomePending
)

// InitiateItem is one line of what is being bought, for providers that need to
// show or record it (change ecpay-payment-provider). 綠界的 ItemName 是必填欄位。
//
// It is provider-neutral on purpose: Stripe wants line items too, and every
// provider that shows the customer a payment page wants to show them what they
// are paying for. Providers that do not care simply ignore it.
type InitiateItem struct {
	Name       string
	Quantity   int32
	UnitAmount int64
}

// InitiateRequest carries everything a provider needs to start a payment
// attempt. Amount/Currency are always the order's own total_amount/currency
// snapshot (design D3/Non-Goals — this change only supports full-amount
// payment; a partial-payment feature would add validation here later).
type InitiateRequest struct {
	ShopID   int
	OrderID  int
	Amount   int64
	Currency string
	// Items is the order's own line snapshot. May be empty — nothing in this
	// package requires a provider to use it.
	Items []InitiateItem
}

// RedirectForm describes "POST these fields to this address" — the shape a real
// cashier actually takes (change ecpay-payment-provider, design D2).
//
// **Fields, not HTML.** The API is headless and its consumers are not all
// browsers (first-party SSR, a merchant's own static storefront, and the native
// app the render bundle contract has always promised to serve). Returning a blob
// of auto-submitting HTML would (a) make "the consumer is an HTML document" part
// of the v1 contract, (b) put an HTML-escaping obligation on this side of the
// wire for every value that flows into it, and (c) be assertable only by parsing
// a string. A field map is data, and every value in it can be asserted by name.
type RedirectForm struct {
	// Method is the HTTP method the consumer must use ("POST").
	Method string
	// Action is the absolute URL to submit to.
	Action string
	// Fields are the form fields, name → value. Form values are strings by
	// definition of the encoding — including amounts.
	Fields map[string]string
}

// InitiateResult is what a provider hands back after successfully starting
// a payment attempt. ProviderReference MUST be usable as an idempotent
// webhook lookup key (unique per attempt) — Service persists it verbatim in
// payments.provider_reference.
//
// Exactly one of RedirectURL / RedirectForm carries the answer: a provider that
// needs a form POST leaves RedirectURL empty and fills RedirectForm, and the
// reverse for a plain redirect. **Consumers MUST check RedirectForm first.**
type InitiateResult struct {
	ProviderReference string
	RedirectURL       string
	// RedirectForm is nil for providers that redirect with a plain URL.
	RedirectForm *RedirectForm
}

// WebhookRequest carries the raw inbound webhook request. Body is
// deliberately raw bytes, not a pre-parsed struct: signature verification
// (HMAC or otherwise) MUST run over the exact bytes the provider signed —
// decoding then re-encoding JSON can silently change byte content (field
// order, whitespace) and break verification. Headers is the raw
// http.Header because signature schemes vary per provider (header name,
// encoding); this is the same shape stripe-go's webhook verification takes.
//
// **This decision held up under the first real provider** (change
// ecpay-payment-provider, design D14), for a reason stronger than the one it was
// written for: 綠界的簽章欄位 CheckMacValue **就在 body 裡**，而演算法要求把
// 「其餘所有欄位」排序後重組——包含我們沒有定義的欄位。一個先解析成預設 struct 的
// 介面會丟掉那些欄位，簽章直接算不對；而且綠界送的是 form-urlencoded，任何 JSON
// 預設都會壞。三個介面裡有兩個要改，這一個一個字都不用改。
type WebhookRequest struct {
	Headers http.Header
	Body    []byte
}

// WebhookResult is what a provider's webhook verification yields once the
// signature has checked out: which payment attempt this refers to, and what it
// reports about it.
type WebhookResult struct {
	ProviderReference string
	Outcome           Outcome

	// ProviderTransactionID is the provider's OWN identifier for this
	// transaction, when it has a second one (change ecpay-payment-provider,
	// design D4). Empty means "this provider has only one identifier".
	//
	// 綠界流程裡有兩個號碼：MerchantTradeNo（我們產生，發起付款時就存進
	// provider_reference——因為 webhook 的查找鍵必須在那個時間點就存在，所以這是
	// 被迫的而不是偏好）與 TradeNo（綠界產生，只有回呼送達時才知道）。丟掉後者，
	// 人工對帳就少一半入口；而綠界的退款 API 兩個號碼都是必填參數，所以這一欄
	// 也是日後任何自動退款的前提。
	ProviderTransactionID string

	// PaidAt is when the PROVIDER says the money moved. nil when it does not
	// report one. Deliberately not time.Now() and deliberately not left to the
	// row's updated_at: updated_at means "last write", so any future write would
	// silently redefine the number the merchant reconciles against.
	PaidAt *time.Time

	// Amount is the amount the provider reports for this transaction, in the
	// same minor units as payments.amount. 0 means "not reported".
	// Service.HandleWebhook refuses to advance anything when a reported non-zero
	// amount disagrees with the stored one.
	Amount int64
}

// Ack names what became of one inbound webhook, in provider-neutral terms
// (change ecpay-payment-provider, design D3).
//
// **The HTTP layer decides which one applies; the provider decides what bytes it
// looks like.** That split is the whole point: 綠界要求回純文字 `1|OK`，收不到就
// 每隔 5~15 分鐘重送、當天最多 4 次，而它的文件甚至列了一份常見錯誤回應清單
// （`"1|OK"` 帶引號、`1|ok`、`1OK`、`OK`…）——一個 JSON envelope 當然也在上面。
// 在介面擴充之前，這件事只能寫成 handler 裡的 `if providerName == "ecpay"`，
// 而那正是 payment-integration design D1 立起來要擋的東西。
type Ack int

const (
	// AckProcessed — signature valid, payment row located, state applied.
	AckProcessed Ack = iota
	// AckIgnored — signature valid, but the reference matches nothing. Retrying
	// cannot make it match, so this is an acknowledgement, not an error.
	AckIgnored
	// AckInvalidSignature — authentication failed.
	AckInvalidSignature
	// AckMalformed — signature valid, payload unparseable.
	AckMalformed
	// AckFailed — WE failed. The provider SHOULD retry.
	AckFailed
)

// WebhookResponse is the exact HTTP response a provider's caller requires.
//
// **The zero value means "no provider-specific requirement"**, and the HTTP
// layer then answers in the API's own JSON envelope (design D12). That default
// is what lets MockProvider stay byte-for-byte unchanged while keeping the
// envelope defined in exactly one place — internal/httpx — instead of growing a
// second copy inside every provider that has no opinion about it.
//
// It is not an optional interface in disguise: WebhookAck is mandatory, so a new
// provider cannot compile without stopping to answer "what does my caller need
// to see". It is merely allowed to answer "the same as everyone else".
type WebhookResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// IsZero reports whether this response carries no provider-specific
// requirement.
func (r WebhookResponse) IsZero() bool {
	return r.Status == 0 && r.ContentType == "" && len(r.Body) == 0
}

// Provider is the provider-neutral gateway abstraction (design D1). Every
// provider — mock or real — implements exactly these capabilities;
// Service and the HTTP layer only ever talk to this interface.
type Provider interface {
	// Name is the provider's registry key, also the {provider} path segment
	// on the webhook route and the payments.provider column value.
	Name() string
	// InitiatePayment starts a new payment attempt and returns provider-side
	// reference/redirect information. It MUST NOT perform any local state
	// mutation — Service owns creating the payments.Payment row.
	InitiatePayment(ctx context.Context, req InitiateRequest) (*InitiateResult, error)
	// VerifyWebhook authenticates an inbound webhook call by signature and,
	// only once authenticated, parses the payment outcome it reports.
	// Returns ErrInvalidSignature (or a wrapped form of it) when the
	// signature is missing or does not match.
	VerifyWebhook(ctx context.Context, req WebhookRequest) (*WebhookResult, error)
	// WebhookAck returns the exact response this provider's webhook caller
	// requires for the given acknowledgement. Returning the zero
	// WebhookResponse defers to the API's own JSON envelope.
	WebhookAck(ack Ack) WebhookResponse
}
