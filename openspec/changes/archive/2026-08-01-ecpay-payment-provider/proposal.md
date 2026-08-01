## Why

`payment-integration` 交付的是一個**參考架構**：provider 中立的介面、一個真實 HMAC 驗簽的 mock provider、`payments` 交易紀錄、webhook 冪等。它從第一天就聲明「新增真實金流商只需實作 `Provider` 介面，呼叫端一行都不用改」——而這句話**從來沒有被任何真實金流商驗證過**。

ONDO 即將上線，金流商已定案為**綠界 ECPay**。把 ECPay 接上去的第一個動作就是去撞那句話，而它三處撞不過去（本 change 的核心內容）：

1. ECPay 的 `AioCheckOut` 是把一組欄位以 **form POST** 送到綠界收銀台，**不是 GET 導向一個 URL**——`InitiateResult.RedirectURL` 這個單一字串裝不下它。
2. ECPay 的付款結果通知要求商店回**純文字 `1|OK`**，收不到就無止境重送——而 `Provider` 介面**沒有任何地方能表達「這個 provider 的回應長什麼樣」**，webhook handler 目前一律寫 D12 的 JSON envelope。
3. ECPay 流程裡有**兩個**識別碼：`MerchantTradeNo`（我們產生，長度 ≤20、限英數字）與 `TradeNo`（綠界產生，只在回呼時才知道）。`payments.provider_reference` 只有一欄。人工對帳時商家手上拿到的可能是任何一個。

再者，`payment-integration` 的 Non-Goal 是「不串接任何真實金流商（沒有憑證、CI 不打外網）」。這個限制**今天仍然成立**，但它不再是「所以什麼都不做」的理由：ECPay 的線路形狀（欄位名、`CheckMacValue` 演算法、回應約定）是公開文件，可以**完全照真的做**，只把「打誰」變成可注入的設定。既有的 mock provider 是「機制真實、形狀隨便」；本 change 要的是「**形狀真實、端點可注入**」——兩者不互相取代。

最後，使用者提出的一個必須被查證的問題：**訂單取消後留下的資訊是否足夠人工對帳？** 查證結果（證據見 design D8/D9）發現的不是「不夠」，而是**一個比不夠更糟的洞**：已取消訂單仍會被 webhook 推進成已付款、並且會核發點數。

## What Changes

### 新增：ECPay provider（形狀真實、端點可注入）

- 新套件 `api/internal/payment/ecpay`：`AioCheckOut V5` 的**真實欄位集**、**真實 `CheckMacValue` 演算法**（含 .NET `UrlEncode` 相容的字元替換表）、真實的 form-urlencoded 回呼解析與 `1|OK` 回應約定。
- `MerchantTradeNo` 由我們產生（`O{orderID}T{base36 毫秒}`，≤20 字元、純英數），存進 `payments.provider_reference`；`TradeNo` 由回呼帶回，存進**新欄位** `payments.provider_transaction_id`。
- **不打真實綠界端點**：收銀台位址是設定值（`ECPAY_CHECKOUT_URL`），預設指向本 repo 新增的**本機模擬收銀台** `api/cmd/ecpay-sim`（獨立 binary，**不在 API router 上**）。它以綠界的 HashKey/HashIV **獨立驗證我們產生的 `CheckMacValue`**，再以綠界的欄位形狀把回呼 POST 回 `ReturnURL`。改成打綠界測試站只需改一個環境變數。

### 修改：`Provider` 介面（三處，皆為 Go 內部介面，非 v1 API）

- `InitiateResult` 新增 `RedirectForm *RedirectForm{Method, Action, Fields}`——**表單欄位集，不是一段 HTML**（design D2）。
- `Provider` 新增 `WebhookAck(ack Ack) WebhookResponse`：**「這個 provider 的回呼要看到什麼位元組」成為 provider 契約的一部分**，而不是 handler 裡的 provider 特判（design D3）。
- `Outcome` 新增 `OutcomePending`：綠界有兩種「收到了但這不代表拿到錢」的回呼——**`SimulatePaid=1`（`RtnCode` 也是 1，但綠界不撥款）**與 `RtnCode=10300066`（待確認中請勿出貨）。今天的 `Outcome` 只有成功／失敗兩個值，兩種寫法都會出事（design D6）。

### 修改：v1 公開契約（**全部 additive**）

- `PaymentInitiateResult` 新增 `redirect_form`（`object | null`）。既有的 `redirect_url` **一個字都不動**。
- 新端點 `GET /api/v1/admin/shops/{shopID}/payments`（RBAC `payment.view`）：以綠界端的識別碼**反查訂單**，這是人工對帳唯一缺的那一步。

### 修正：已取消訂單收到付款成功回呼（查證後發現的既有 bug）

- 目前：付款紀錄轉成功 → 訂單 `payment_status` 推進成已付款 → **發出 `OrderPaymentSucceeded` → 核發點數**，而該訂單 `status` 是已取消、庫存已歸還。
- 修正後：付款紀錄**仍如實記為成功**（錢真的動了，對帳需要它），但**不推進訂單、不發事件、不核發點數**，並留下 WARN log。

## Capabilities

### New Capabilities

- `ecpay-payment-provider`: 綠界 ECPay 這一家金流商的線路形狀契約——`AioCheckOut` 欄位集與 form POST 語意、`CheckMacValue` 演算法、`MerchantTradeNo` 產生規則與長度界線、付款結果通知的 `RtnCode` 語意與 `1|OK` 回應約定、TWD 以外幣別的拒絕、模擬模式的定義與界線。

### Modified Capabilities

- `payment-integration`: ①`Provider` 介面新增「表單導向」與「回呼回應形狀」兩項能力，並把 `Outcome` 擴充為含「非終局」；②`payments` 新增服務商端交易識別碼與付款時間兩欄；③新增「以服務商端識別碼反查訂單」的商家後台能力（人工對帳）；④明訂已取消訂單收到成功回呼時的行為。

## Impact

- **API（新增，additive）**：`GET /api/v1/admin/shops/{shopID}/payments`；`PaymentInitiateResult.redirect_form`。`api/openapi/openapi.yaml` 與 `api/openapi/v1-surface.txt` 同步更新（**只增行**）。
- **資料庫**：`payments` 新增 `provider_transaction_id`（varchar(64), NULL）與 `paid_at`（timestamptz, NULL）；一支新的 versioned migration。無 CHECK 運算式變更。
- **Go 套件**：新增 `internal/payment/ecpay`、`cmd/ecpay-sim`；修改 `internal/payment`（介面、service、mock）、`internal/httpapi/payment.go`、`internal/app/wire.go`、`internal/config`。
- **設定**：新增 `ECPAY_*` 七個環境變數與 `PAYMENT_DEFAULT_PROVIDER`，皆有預設值，**不設定時系統行為與今天完全相同**（預設 provider 仍是 `mock`）。
- **不影響**：admin 前端一行未改（tripwire #4′／#5 不觸發）；`internal/events` 的事件型別不變；事件訂閱者集合不變。
- **文件**：`docs/storefront-integration.md` 新增「表單導向付款」一節（自帶前端要照著寫）。

## Non-Goals

- **不執行退款**。退款採**人工流程**：商家在綠界後台操作，本平台不提供任何退款 API、不呼叫綠界的退款端點。`orders.payment_status` 的 `2 已退款`／`3 部分退款` 與 `payments.status` 的 `3 已退款` **維持保留未用**，本 change 沒有任何程式路徑會寫入它們（與 `payment-integration` 一致）。本 change 交付的是讓人工退款**做得到**的前提：找得到那筆交易、對得上金額與時間。
- **不做部分金額付款**（partial capture）：`payments.amount` 永遠等於發起當下的 `orders.total_amount`。
- **不串接真實綠界端點**：不呼叫 `payment.ecpay.com.tw` 或 `payment-stage.ecpay.com.tw`，CI 與測試不打外網。改成打真站是一個環境變數，不是一次 change。
- **不做綠界的訂單查詢 API**（`QueryTradeInfo`）：那是一條主動出站的網路呼叫，與「不打外網」直接衝突；本 change 的對帳答案是本地紀錄 ＋ 反查端點。
- **不做定期定額、超商代碼、物流串接**：`ChoosePayment` 是設定值，預設 `Credit`。
- **不改 admin 前端**：新的反查端點今天只有 API 契約與測試兩個消費者，後台 UI 留給後續 change。
