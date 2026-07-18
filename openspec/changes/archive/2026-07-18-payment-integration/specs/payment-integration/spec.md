## ADDED Requirements

### Requirement: Payment provider abstraction with a real-signature mock provider

系統 MUST 定義一個 provider 中立的 `PaymentProvider` 介面，至少提供「發起付款」（回傳 provider 端參照與導向資訊）與「驗證 webhook 簽章並解析付款結果」兩個能力。系統 MUST 提供至少一個可在 CI 完整測試、不依賴任何外部網路呼叫的 mock provider 實作；該 mock provider 的簽章驗證 MUST 是真實的 HMAC-SHA256 比對（不可對任何輸入一律回傳驗證成功）。新增支援真實金流商時，MUST 只需新增一個實作該介面的 provider，不需修改呼叫端（handler、webhook 路由、idempotency 邏輯）程式碼。

#### Scenario: Mock provider 拒絕缺少簽章的 webhook
- **WHEN** 呼叫 mock provider 的 webhook 驗證，請求未附帶簽章 header
- **THEN** 驗證失敗，不解析付款結果

#### Scenario: Mock provider 拒絕簽章不符的 webhook
- **WHEN** 呼叫 mock provider 的 webhook 驗證，附帶的簽章 header 與 body 用正確密鑰計算出的 HMAC-SHA256 不相符
- **THEN** 驗證失敗，不解析付款結果

#### Scenario: Mock provider 接受簽章正確的 webhook
- **WHEN** 呼叫 mock provider 的 webhook 驗證，附帶的簽章 header 是用正確密鑰對 body 計算出的 HMAC-SHA256
- **THEN** 驗證成功，回傳解析出的 `provider_reference` 與付款結果（成功或失敗）

### Requirement: Payment transaction records support multiple attempts per order

系統 MUST 有一個 shop-scoped 的 `payments` 資料表記錄每一次付款嘗試，欄位至少包含 `order_id`、`provider`、`provider_reference`、`amount`（BIGINT 整數最小貨幣單位）、`currency`（CHAR(3)）、`status`、建立/更新時間。`order_id` 欄位 MUST NOT 有唯一約束——同一張訂單 MUST 能有多筆付款記錄（例如第一次失敗、第二次重試成功）。`(provider, provider_reference)` 組合 MUST 唯一，作為 webhook 冪等查找的鍵。`payments.amount` MUST 等於發起付款當下訂單的 `total_amount`（本階段僅支援全額付款，不支援部分金額）。

#### Scenario: 同一訂單可有多筆付款記錄
- **WHEN** 會員對同一張訂單先發起一次付款（之後該筆以失敗告終），再發起第二次付款
- **THEN** 該訂單下存在兩筆獨立的 `payments` 記錄，各自有自己的 `provider_reference` 與 `status`

#### Scenario: 付款金額等於訂單金額快照
- **WHEN** 會員對一張 `total_amount` 為 N、幣別為 C 的訂單發起付款
- **THEN** 建立的 `payments` 記錄其 `amount = N`、`currency = C`

### Requirement: orders.payment_status enumeration is finalized by this change

`orders.payment_status` 的完整列舉值本階段定案為：`0`=未付款、`1`=已付款、`2`=已退款、`3`=部分退款。本階段的程式路徑 MUST ONLY 產生 `0`（既有初始值）與 `1`（webhook 確認付款成功時）；`2`、`3` 的列舉位置保留但本階段 MUST NOT 有任何程式路徑寫入。`orders.payment_status` 的寫入 MUST 只能透過 `order.Service.UpdatePaymentStatus`，MUST NOT 由本能力的任何程式碼直接寫入 `ent.Order`。

#### Scenario: Webhook 確認成功後訂單標記已付款
- **WHEN** 一筆 `payment_status = 0` 訂單對應的付款記錄收到驗證通過、結果為成功的 webhook
- **THEN** 該訂單 `payment_status` 變為 `1`

#### Scenario: 付款失敗不影響訂單付款狀態
- **WHEN** 一筆 `payment_status = 0` 訂單對應的付款記錄收到驗證通過、結果為失敗的 webhook
- **THEN** 該付款記錄 `status` 變為失敗，訂單 `payment_status` 維持 `0`（未付款），會員可對同一訂單再次發起付款

### Requirement: Member can initiate payment for their own order

會員自助 API `POST /api/v1/shop/orders/{id}/payments` MUST 僅依賴會員 JWT 中的 `member_id` 判斷該訂單是否屬於呼叫者（比照既有訂單自助 API 慣例，MUST NOT 套用 RBAC）；操作不屬於自己的訂單 id MUST 回 404。訂單 `status` 為已取消，或 `payment_status` 不是未付款時，MUST 拒絕發起付款（409）。發起成功時 MUST 建立一筆 `status` 為 pending 的付款記錄，並回傳 provider 導向資訊。

#### Scenario: 會員對自己未付款且未取消的訂單發起付款
- **WHEN** 會員對自己名下一張 `status=0`（已建立）、`payment_status=0`（未付款）的訂單呼叫發起付款 API
- **THEN** 回應包含 provider 導向資訊，該訂單下新增一筆 `status` 為 pending 的付款記錄

#### Scenario: 對已取消訂單發起付款被拒
- **WHEN** 會員對自己名下一張 `status=1`（已取消）的訂單呼叫發起付款 API
- **THEN** 回 409，不建立付款記錄

#### Scenario: 對已付款訂單發起付款被拒
- **WHEN** 會員對自己名下一張 `payment_status=1`（已付款）的訂單呼叫發起付款 API
- **THEN** 回 409，不建立付款記錄

#### Scenario: 跨會員發起付款被拒
- **WHEN** 會員 A 嘗試對屬於會員 B 的訂單呼叫發起付款 API
- **THEN** 回 404，不建立付款記錄

### Requirement: Webhook confirms payment idempotently without tenant middleware

Webhook 端點 `POST /api/v1/webhooks/payments/{provider}` MUST NOT 要求會員或商家 JWT、MUST NOT 依賴租戶網域解析；身分驗證 MUST 完全透過該 provider 的簽章驗證機制完成。簽章驗證失敗 MUST 拒絕請求，MUST NOT 更新任何付款或訂單資料。簽章驗證通過後，系統 MUST 依 `(provider, provider_reference)` 冪等地定位對應付款記錄；同一個 `provider_reference` 的重複投遞（例如同一個成功結果的 webhook 被呼叫兩次）MUST NOT 造成訂單付款狀態被重複推進或產生資料不一致——第二次（含以後）呼叫 MUST 是安全的 no-op 或收斂到與第一次相同的最終狀態，MUST NOT 回傳錯誤。

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

### Requirement: Merchant back-office can view an order's payment records

商家後台 API `GET /api/v1/admin/shops/{shopID}/orders/{id}/payments` MUST 依既有三層 RBAC 判定，檢視需要 `payment.view` 權限節點。商家 MUST 只能查詢 URL 中 `shopID` 所屬訂單的付款記錄；跨商家操作 MUST 依既有 cross-shop access guard 回 403。目標訂單不存在於該商家時 MUST 回 404。

#### Scenario: 具權限商家檢視自己商家訂單的付款紀錄
- **WHEN** 持有 shop A `payment.view` 權限的使用者查詢 shop A 一張訂單的付款紀錄
- **THEN** 回傳該訂單底下所有付款記錄（含 pending/succeeded/failed 各種狀態）

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `payment.view` 權限，查詢 shop A 一張訂單的付款紀錄
- **THEN** 回 403

#### Scenario: 跨店查詢被拒
- **WHEN** 僅屬 shop A 的使用者查詢 shop B 一張訂單的付款紀錄
- **THEN** 回 403

### Requirement: Tenant data isolation for the payments table

`payments` 表 MUST 納入既有租戶隔離機制（spec multi-tenancy）：會員自助發起付款與商家後台查詢 MUST 被強制限定於請求所屬的 shop。Webhook 端點沒有 ambient 租戶 context，MUST 僅依已驗證簽章下查得的付款記錄本身所帶的 `shop_id`/`order_id` 操作，MUST NOT 信任請求中任何呼叫方可控的 shop/order 識別參數。

#### Scenario: 跨店查詢看不到彼此的付款資料
- **WHEN** shop A 的商家查詢自己商家訂單的付款紀錄
- **THEN** 回應不含任何 shop B 的付款記錄
