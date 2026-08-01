## 1. Provider 介面擴充（design D2／D3／D6）

- [x] 1.1 `internal/payment/provider.go`：新增 `RedirectForm{Method, Action, Fields}`，`InitiateResult` 新增 `RedirectForm *RedirectForm`
- [x] 1.2 `internal/payment/provider.go`：新增 `Ack` 列舉（Processed/Ignored/InvalidSignature/Malformed/Failed）、`WebhookResponse{Status, ContentType, Body}`，`Provider` 介面新增 `WebhookAck(Ack) WebhookResponse`；註解寫明「零值＝用平台預設」的理由
- [x] 1.3 `internal/payment/provider.go`：`Outcome` 新增 `OutcomePending`，推翻原註解「1:1 對應非 pending status」並說明理由
- [x] 1.4 `internal/payment/provider.go`：`InitiateRequest` 新增 `Items []InitiateItem{Name, Quantity, UnitAmount}`；`WebhookResult` 新增 `ProviderTransactionID string`、`PaidAt *time.Time`、`Amount int64`
- [x] 1.5 `internal/payment/constants.go`：`statusForOutcome` 支援 `OutcomePending → StatusPending`
- [x] 1.6 `internal/payment/mockprovider.go`：實作 `WebhookAck` 五個分支全回 `WebhookResponse{}`（回應逐位元組不變）

## 2. `payments` 資料模型（design D4／D12）

- [x] 2.1 `internal/ent/schema/payment.go`：新增 `provider_transaction_id`（varchar(64), Optional/Nillable）與 `paid_at`（Optional/Nillable time），附設計理由註解
- [x] 2.2 `cd api && go generate ./internal/ent`
- [x] 2.3 `make migrate-gen name=add_ecpay_payment_reconciliation`，依慣例手動移除 atlas 誤判要 DROP 的手寫約束，補上 up/down 的說明註解
- [x] 2.4 `make migrate` 套用並確認成功

## 3. `payment.Service` 行為（design D6／D7／D9／D12）

- [x] 3.1 `InitiatePayment`：載入訂單品項並填入 `InitiateRequest.Items`；把 `InitiateResult.RedirectForm` 一併回傳
- [x] 3.2 `HandleWebhook`：`OutcomePending` 分支——不做狀態轉換，但仍寫入 `provider_transaction_id`／`paid_at`
- [x] 3.3 `HandleWebhook`：`WebhookResult.Amount` 非 0 且不等於 `payments.amount` 時回明確錯誤，不推進任何狀態
- [x] 3.4 `HandleWebhook`：訂單已取消時，付款紀錄仍轉成功，但不呼叫 `UpdatePaymentStatus`、不發 `OrderPaymentSucceeded`，並留 WARN log（`Service` 需要一個 `Log *slog.Logger`，nil-safe）
- [x] 3.5 `HandleWebhook`：成功路徑寫入 `provider_transaction_id`／`paid_at`
- [x] 3.6 新增 `ListPaymentsAdmin(ctx, shopID, params)`：精確比對兩個識別碼 ＋ 數字比對 order_id，分頁、依 id 遞減（design D10）

## 4. ECPay provider（design D1／D5／D6／D7／D16）

- [x] 4.1 `internal/payment/ecpay/checkmac.go`：`CheckMacValue` 演算法（大小寫不敏感排序、HashKey/HashIV 夾擠、.NET UrlEncode 替換表含 `~ → %7e`、小寫、SHA256、大寫），註解附綠界文件來源
- [x] 4.2 `internal/payment/ecpay/checkmac_test.go`：**綠界官方範例向量**斷言 `6C51C9E6888DE861FD62FB1DD17029FC742634498FD813DC43D4243B5685B840`；另測大小寫不敏感排序與 `~` 的編碼
- [x] 4.3 `internal/payment/ecpay/tradeno.go`：`MerchantTradeNo` 產生（`O{orderID}T{base36 毫秒大寫}`）＋ 超過 20 字元硬失敗；單元測試含 10 位數 orderID 與「兩次不同」
- [x] 4.4 `internal/payment/ecpay/ecpay.go`：`Provider` 實作——`Name()`、`InitiatePayment`（TWD 檢查、欄位集、`ItemName` 依 rune 截到 400、`MerchantTradeDate` 用 UTC+8）、`VerifyWebhook`（form-urlencoded 原始位元組解析、未知欄位一併計入簽章、`RtnCode`/`SimulatePaid` 映射表）、`WebhookAck`（`1|OK` 等）
- [x] 4.5 `internal/payment/ecpay/ecpay.go`：匯出 `SignCallback(cfg, values) string`，供測試與模擬收銀台產生合法回呼（比照 `SignMockWebhook`）
- [x] 4.6 `internal/payment/ecpay/ecpay_test.go`：非 TWD 拒絕、`TotalAmount` 逐位元組等於金額、缺簽章／簽章錯誤、未知欄位參與簽章、四種 `RtnCode`/`SimulatePaid` 組合的 Outcome 映射、`WebhookAck` 五個分支的位元組

## 5. HTTP 層（design D2／D3／D10）

- [x] 5.1 `internal/httpapi/payment.go`：`Webhook` 改走 `prov.WebhookAck(ack)`；零值時沿用既有 `httpx` 回應；未註冊 provider 與 body 讀取失敗仍由我們自己答
- [x] 5.2 `internal/httpapi/payment.go`：`paymentJSON` 新增 `provider_transaction_id`／`paid_at`；`paymentInitiateJSON` 新增 `redirect_form`
- [x] 5.3 `internal/httpapi/payment.go`：新增 `listShopPaymentsAdmin` handler 與 `MountShopAdmin` 上的 `GET /payments` 路由（`payment.view`）
- [x] 5.4 確認 `httpapi.Deps` 未新增欄位（沿用 `PaymentHandler`），`testutil.FillNonNil` 不需 overrides

## 6. 設定與接線（design D11／D13）

- [x] 6.1 `internal/config`：新增 `PaymentDefaultProvider`（預設 `mock`）與 `ECPay`（MerchantID/HashKey/HashIV/CheckoutURL/ReturnURL/ClientBackURL/ChoosePayment），全部有預設值
- [x] 6.2 `internal/app/wire.go`：建構並註冊 ecpay provider，`DefaultProvider` 取自設定；`paymentService.Log` 接上
- [x] 6.3 `.env.example`：新增全部新環境變數與說明
- [x] 6.4 確認 `internal/app/wiring_integration_test.go` 的 `expectedSubscribers` 不需更動（本 change 未新增訂閱者），並跑檢查 ⑤⑥⑦

## 7. 模擬收銀台（design D11）

- [x] 7.1 `api/cmd/ecpay-sim/main.go`：`POST /Cashier/AioCheckOut/V5`——重算並比對 `CheckMacValue`，顯示三顆按鈕（成功／失敗／模擬付款）
- [x] 7.2 送出後以綠界欄位形狀 form-urlencoded POST 到 `ReturnURL`，斷言回應恰為 `1|OK` 並在頁面上顯示結果，再導向 `ClientBackURL`
- [x] 7.3 `Makefile` 新增 `ecpay-sim` 目標

## 8. API 契約（**改端點必須同步**）

- [x] 8.1 `api/openapi/openapi.yaml`：`Payment` 與 `PaymentInitiateResult` 新增 `provider_transaction_id`／`paid_at`／`redirect_form`；新增 `PaymentRedirectForm` schema（`fields` 用 `additionalProperties: {type: string}`，description 說明 `TotalAmount` 無換算）
- [x] 8.2 `api/openapi/openapi.yaml`：新增 `GET /api/v1/admin/shops/{shopID}/payments`（含 `q`／`page`／`page_size` 參數）
- [x] 8.3 `api/openapi/openapi.yaml`：webhook 端點的 200 回應新增 `text/plain` content，描述 `1|OK` 約定
- [x] 8.4 重新產生 `v1-surface.txt`（`-openapi.update-surface`）並**逐行確認 diff 只有新增、沒有刪除**
- [x] 8.5 `make openapi` 四道契約檢查通過

## 9. 測試

- [x] 9.1 `internal/payment/service_integration_test.go`：`OutcomePending` 序列（非終局 → 未付款 → 真成功 → 已付款）、金額不符拒絕推進、`provider_transaction_id`／`paid_at` 寫入
- [x] 9.2 `internal/payment/service_integration_test.go`：**已取消訂單收到成功回呼**——付款轉成功、訂單不變、`OrderPaymentSucceeded` 未發出
- [x] 9.3 `internal/payment/service_integration_test.go`：`TestCancelledOrderRetainsPaymentTrail`——取消後付款紀錄的識別碼／金額／幣別／時間完整保留（design D8 的證據）
- [x] 9.4 `internal/httpapi/payment_integration_test.go`：ECPay 端到端（發起 → 表單欄位斷言 → 自簽回呼 → `1|OK` 位元組斷言 → 訂單已付款）
- [x] 9.5 `internal/httpapi/payment_integration_test.go`：反查端點（三種 `q`、RBAC 403、跨店、空清單、分頁）
- [x] 9.6 `internal/httpapi/openapi_conformance_integration_test.go`：新 schema 納入取樣（`PaymentRedirectForm`），反查端點回應驗證
- [x] 9.7 `e2e/commerce-path.mjs`：新增 ECPay 鏈（以 `{"provider":"ecpay"}` 發起、JS 端自算 `CheckMacValue`、斷言回應主體恰為 `1|OK`、訂單轉已付款）與「模擬付款不付錢」斷言

## 10. 文件

- [x] 10.1 `docs/storefront-integration.md`：新增「表單導向付款」一節（`redirect_form` 的判斷規則 ＋ 可複製的建 form 片段 ＋ `redirect_url` 為空的說明）
- [x] 10.2 `CLAUDE.md`：新增本 change 的架構要點（介面三處擴充、兩個識別碼、`1|OK`、已取消訂單的處理、模擬收銀台不在 router 上、退款是人工流程）

## 11. 驗收閘門

- [x] 11.1 `make test` 全綠（含契約檢查 ①②④）
- [x] 11.2 `make test-int` 全綠（含契約檢查 ③ 與接線檢查 ⑤⑥⑦）
- [x] 11.3 `make openapi` 全綠
- [x] 11.4 `cd api && golangci-lint run` 全綠
- [x] 11.5 `make seed-demo` ＋ 起 API ＋ `cd e2e && node commerce-path.mjs` 全綠（先確認 :8080 沒有殘留舊 server）
