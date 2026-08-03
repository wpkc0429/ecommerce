## MODIFIED Requirements

### Requirement: Payment provider abstraction with a real-signature mock provider

系統 MUST 定義一個 provider 中立的 `PaymentProvider` 介面，至少提供「發起付款」（回傳 provider 端參照與導向資訊）、「驗證 webhook 簽章並解析付款結果」，以及「宣告該 provider 的 webhook 呼叫者所要求的回應形狀」三個能力。系統 MUST 提供至少一個可在 CI 完整測試、不依賴任何外部網路呼叫的 mock provider 實作；該 mock provider 的簽章驗證 MUST 是真實的 HMAC-SHA256 比對（不可對任何輸入一律回傳驗證成功）。新增支援真實金流商時，MUST 只需新增一個實作該介面的 provider，不需修改 `Service.HandleWebhook` 的控制流程或既有 provider 的 idempotency 邏輯。**若該 provider 的憑證是逐商家解析而非平台層共用一份**（change ecpay-per-shop-credentials），它 MAY 額外實作一個「依商家解析」的可選介面；HTTP 層在呼叫 `VerifyWebhook` 前 MUST 先以此介面解析出對應商家的 provider 實例，這是唯一因此新增的呼叫端控制流程，且 MUST 是對介面能力的判斷，MUST NOT 是對 provider 名稱字串的特判。

**導向資訊 MUST 能表達「以表單 POST 一組欄位到某個位址」**，而不只是「導向某個 URL」——真實金流商（如綠界）的收銀台以 form POST 接收欄位集，單一 URL 字串表達不了。表單導向資訊 MUST 是**結構化的欄位集**（方法、目標位址、欄位名對欄位值），MUST NOT 是一段由 API 產生的 HTML。

**webhook 回應形狀 MUST 是 provider 契約的一部分**，MUST NOT 由 HTTP 層針對特定 provider 名稱做特判。介面 MUST 允許 provider 表達「沒有特殊要求」，此時 HTTP 層以本 API 自己的錯誤信封回應——既有 provider 的回應因此 MUST 逐位元組不變。

解析付款結果 MUST 能表達第三種結果「**非終局**」（收到通知但尚未構成成功或失敗），而不只是成功與失敗兩種。收到非終局結果時，系統 MUST NOT 使該筆付款紀錄離開待付款狀態。

#### Scenario: Mock provider 拒絕缺少簽章的 webhook
- **WHEN** 呼叫 mock provider 的 webhook 驗證，請求未附帶簽章 header
- **THEN** 驗證失敗，不解析付款結果

#### Scenario: Mock provider 拒絕簽章不符的 webhook
- **WHEN** 呼叫 mock provider 的 webhook 驗證，附帶的簽章 header 與 body 用正確密鑰計算出的 HMAC-SHA256 不相符
- **THEN** 驗證失敗，不解析付款結果

#### Scenario: Mock provider 接受簽章正確的 webhook
- **WHEN** 呼叫 mock provider 的 webhook 驗證，附帶的簽章 header 是用正確密鑰對 body 計算出的 HMAC-SHA256
- **THEN** 驗證成功，回傳解析出的 `provider_reference` 與付款結果（成功或失敗）

#### Scenario: 表單導向以結構化欄位集表達
- **WHEN** 某個 provider 需要以表單 POST 送出欄位集才能完成付款
- **THEN** 發起付款的回應帶有方法、目標位址與欄位名/值的對應表，且不含任何 HTML

#### Scenario: Mock provider 的 webhook 回應未因介面擴充而改變
- **WHEN** 對 mock provider 的 webhook 端點送出簽章正確的成功通知
- **THEN** 回應與本能力擴充前完全相同（JSON `{"status":"ok"}`）

#### Scenario: 非終局結果不使付款紀錄轉為終局
- **WHEN** 某個 provider 的 webhook 解析出「非終局」結果
- **THEN** 該筆付款紀錄仍為待付款，訂單付款狀態不變，且後續真正的成功通知仍能推進訂單

### Requirement: Webhook confirms payment idempotently without tenant middleware

Webhook 端點 `POST /api/v1/webhooks/payments/{provider}` MUST NOT 要求會員或商家 JWT、MUST NOT 依賴租戶網域解析；身分驗證 MUST 完全透過該 provider 的簽章驗證機制完成。**若 provider 的憑證是逐商家解析**（change ecpay-per-shop-credentials），系統 MAY 額外提供 `POST /api/v1/webhooks/payments/{provider}/{shopID}` 形狀的路由；該形狀同樣 MUST NOT 要求會員或商家 JWT、MUST NOT 依賴租戶網域解析——URL 中的 shop id 本身 MUST NOT 被當作已驗證的事實看待，它只決定驗證時要嘗試哪一個商家的密鑰，真正的身分驗證仍然完全由簽章比對結果決定；用不匹配的 shop id 打這個路由的後果 MUST 是簽章驗證失敗，MUST NOT 是任何資料被信任或洩漏。簽章驗證失敗 MUST 拒絕請求，MUST NOT 更新任何付款或訂單資料。簽章驗證通過後，系統 MUST 依 `(provider, provider_reference)` 冪等地定位對應付款記錄；同一個 `provider_reference` 的重複投遞（例如同一個成功結果的 webhook 被呼叫兩次）MUST NOT 造成訂單付款狀態被重複推進或產生資料不一致——第二次（含以後）呼叫 MUST 是安全的 no-op 或收斂到與第一次相同的最終狀態，MUST NOT 回傳錯誤。

#### Scenario: 簽章驗證失敗的 webhook 被拒
- **WHEN** 對 webhook 端點送出一個簽章不正確（或缺少簽章）的請求
- **THEN** 請求被拒絕，對應付款記錄與訂單狀態皆不變

#### Scenario: 成功付款端到端流程
- **WHEN** 會員對自己的訂單發起付款後，webhook 端點收到一個驗證通過、宣告該筆 `provider_reference` 付款成功的請求
- **THEN** 對應付款記錄 `status` 變為成功，訂單 `payment_status` 變為 `1`（已付款）

#### Scenario: 重複投遞同一筆成功 webhook 是安全的 no-op
- **WHEN** 同一個宣告付款成功、簽章正確的 webhook 請求（相同 `provider_reference`）被連續呼叫兩次
- **THEN** 兩次呼叫皆不回錯誤，訂單 `payment_status` 最終仍是 `1`（已付款），未產生第二筆狀態推進或不一致資料

#### Scenario: 未知 provider_reference 的 webhook 不影響任何資料
- **WHEN** 一個簽章驗證通過、但 `provider_reference` 不對應任何既有付款記錄的 webhook 請求送達
- **THEN** 不更新任何付款或訂單資料

#### Scenario: 帶 shop id 的路由用錯商家不會洩漏任何資訊
- **WHEN** 對 `POST /api/v1/webhooks/payments/{provider}/{shopID}` 送出一個以其他商家密鑰簽出的合法通知內容，其中 `{shopID}` 不是該通知實際所屬的商家
- **THEN** 驗證失敗，回應不透露該筆通知本應屬於哪個商家，也不更新任何資料
