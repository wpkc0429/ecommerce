## Why

第一個外部前端 ONDO 溫度花室（`bouquet`）今天並不是直接呼叫本 API——它自己蓋了一個 Cloudflare Worker 後端處理下單，**而那個 Worker 有冪等鍵**：前端在進入結帳時產生一把 key（`resetCheckout()` 的 `k-<base36 時間>-<base36 隨機>`），Worker 以 KV 的 `idem:<key>` 查表，同一把 key 第二次送達就回放既有訂單而不重新建單。它的結帳 UI 也已經寫好對應的行為：15 秒逾時或 5xx 之後把送出鈕變成「重新送出」，重送**同一把 key**。

現在的架構決定是 **Worker 退場、ONDO 直接呼叫平台 API、平台成為唯一訂單權威**。但平台的訪客結帳沒有冪等鍵，於是那條已經寫好、已經在跑的重試路徑遷移過來之後會**建立第二筆訂單**——這是遷移會**失去**的一層保護，不是「還沒做的新功能」。

姊妹 change `guest-checkout` 的 design D11 當初明確決定不做冪等鍵，理由是：(1) 它是一套需要獨立設計的機制（鍵的作用域與儲存、TTL、回放語意、相同鍵不同 body 的處置），塞進去會讓那個變更變成兩個變更；(2) 它要解決的「重複下單」是**可逆的**（取消即可），與扣庫存的不可逆錯誤不在同一嚴重性等級；(3) 前端以停用送出按鈕處理雙擊是標準作法。同一份 design 把它**明確記錄為將來的候選變更**，而不是「已經想過所以不用做」。

三個理由今天的狀態：

| D11 的原始理由 | 今天 |
| --- | --- |
| 需要獨立設計，不該塞進 guest-checkout | **仍然成立，而且正是本變更存在的形式**——它現在就是那個獨立的 change |
| 重複下單可逆，可取消 | 技術上仍然成立，但它是**商家的**人工成本；而且「可逆」不等於「可偵測」：顧客與商家都要先發現有兩筆單 |
| 前端停用送出按鈕即可 | **不成立**。停用按鈕擋的是雙擊，擋不住**網路逾時後的重試**——而 ONDO 的 UI 本來就有「重新送出」，因為那是唯一能回答「我的單到底成立了沒有」的方式 |

D11 的第三點是本變更真正要推翻的部分：它把問題誤判為「使用者手滑」，但實際的問題是「回應遺失時，客戶端無法區分『請求沒到』與『請求到了但回應掉了』」。這個區分**只有伺服器能提供**——前端無論怎麼寫都做不到。

而決定性的論點是消費端的數量：**ONDO 是第一個自帶前端的商家，但不是唯一一個。** 若讓每個消費端各自在前置層兜住這個缺陷，第二家、第三家會各寫一次、各錯一次，而且每一次的錯法都不同。這個保護應該在平台。

## What Changes

- **兩個結帳端點接受選填的 `Idempotency-Key` 請求標頭**：`POST /api/v1/shop/guest/checkout`（訪客）與 `POST /api/v1/shop/checkout`（會員）。**未帶標頭時行為與本變更前逐位元組相同**——每次都建新訂單，這是且必須是預設。
- **同一把 key 的重送回放原本那筆訂單**：回應為 **201 ＋ 與原本同構的 body**（訪客路徑另附一枚**重新簽發**的訂單存取權杖，見 design D5），並帶診斷用的 `Idempotency-Replayed: true` 回應標頭。庫存 MUST NOT 被第二次扣減，MUST NOT 產生第二筆訂單。
- **新增資料表 `checkout_idempotency_keys`**（`UNIQUE(shop_id, idempotency_key)`）。唯一性以 **shop 為界**——商家 A 的 key 無法回放商家 B 的訂單，這是租戶隔離要求而非選配。鍵的預約與訂單的建立在**同一個資料庫交易**內定案（design D3）。
- **三個新錯誤碼**（皆為新增，不改動任何既有錯誤語意）：
  - `422 invalid_idempotency_key` — 標頭值不符格式（8–255 個可見 ASCII 字元）。
  - `422 idempotency_key_reused` — 同一把 key 配上不同的請求主體（以 SHA-256 比對原始位元組）。
  - `409 idempotency_key_in_progress` — 同一把 key 的另一個請求正在處理中，結果尚未確定。
- **未建立訂單的請求不佔用 key**：驗證失敗（422）、庫存不足、JSON 解析失敗等一律釋放預約，同一把 key 可立即重用。這不只是體面——ONDO 現有的「400 之後保留 key、讓顧客改欄位再送」流程**依賴**它（design D6）。
- **新增設定 `CHECKOUT_IDEMPOTENCY_TTL`**（預設 24h）。逾期以資料列自帶的 `expires_at` 判定，不倚賴任何清理排程。
- **契約與文件同步更新**：`api/openapi/openapi.yaml`（新標頭參數、新錯誤碼、回放語意）、`api/openapi/v1-surface.txt`（新增行，出現在 diff 中）、`docs/storefront-integration.md`（目前寫著「訪客結帳沒有冪等鍵，雙擊會建立兩筆訂單」——本變更之後這句話是錯的）。
- **e2e 新增回放斷言**：`e2e/commerce-path.mjs`。理由不是「多測一點」——`internal/app/wire.go` 零 Go 測試覆蓋（change `commerce-demo-seed` 實測），若忘了在 wire.go 接上冪等服務，**每一支整合測試仍會全綠**（它們自建 router），而真實伺服器會靜默地退化成「標頭被忽略」。守著這件事的只有 e2e。

本變更**完全 additive**：不改任何既有端點的路徑、請求形狀、狀態碼或授權規則。新增一個**選填的請求標頭**、一張新表、三個只在帶了該標頭時才可能出現的錯誤碼——先前會成功的請求全部照常成功且回應相同。**不動用** D13 的破例窗口（該窗口已於 change `order-extras` 用盡並關閉）。

不在本次範圍內（明確記錄，非遺漏）：

- **不為付款發起端點加冪等鍵**（`POST /shop/orders/{id}/payments`、`POST /shop/guest/order/payments`）。見 design D9：`InitiatePayment` 既有的狀態守衛已擋住「對已付款訂單重複發起」，重送產生的是一筆永遠收不到 webhook 的 pending 嘗試，而不是第二筆訂單或第二次扣款；真正的請款冪等權威是金流商自己的參照。沒有消費端要求，且目前唯一的 provider 是 mock。
- **不為訂單取消、購物車寫入端點加冪等鍵**。取消的第二次呼叫已由既有的條件式 flip 回 409 且不產生第二個資源；購物車寫入不建立訂單也不扣庫存。
- **不做背景清理排程**。逾期以 `expires_at` 判定並由後續同鍵請求原子接管，成長界限與其限制見 design D8。
- **不做跨商家或跨端點共用的 key 命名空間**。
- **不把 key 變成必填**，也不提供「本商家強制要求冪等鍵」的設定。

## Capabilities

### New Capabilities

- `checkout-idempotency`: 客戶端提供的冪等鍵如何讓「重送一次結帳請求」成為安全操作——標頭的名稱與格式界限、鍵的唯一性以 shop 為界、預約與建單的原子性、回放的回應語意、相同鍵不同主體的處置、並發同鍵的確定性行為、未建立訂單的請求不佔用鍵、以及鍵的有效期。此能力橫跨訂單、訪客結帳與 API 契約，現行 specs 中無任何一份擁有它。

### Modified Capabilities

- `order-management`: 「Checkout converts an active cart into an order」新增「結帳交易 MUST 同時定案冪等鍵預約」——訂單存在與鍵已完成 MUST 是同一個原子事實，MUST NOT 是兩次先後的寫入；「所有下單路徑共用單一結帳實作」擴充為「冪等保護亦 MUST 只有一份實作，兩條下單路徑共用」。
- `guest-checkout`: 「訪客結帳以 email 建立或沿用無密碼顧客身分」的回應同構要求新增第四種情形（同鍵回放），並明確回放時**重新簽發**訂單存取權杖而非回傳原本那一枚（原本那枚未被儲存，且其 TTL 可能已耗盡）。

## Impact

**Schema / migration**

- 新增資料表 `checkout_idempotency_keys`：`shop_id`、`idempotency_key varchar(255)`、`request_hash char(64)`、`status smallint`、`order_id int NULL`（資訊性欄位，無 FK——沿用 `orders.shipping_method_id` 的既有慣例）、`expires_at timestamptz`、時間戳。`UNIQUE(shop_id, idempotency_key)`（**Postgres 預設 NULLS DISTINCT，兩欄皆 NOT NULL，不涉及 `NULLS NOT DISTINCT`**）＋ 兩條 CHECK。ent 可完整表達，因此**不需要手寫 migration**，但需重算 `atlas.sum`。
- 新表為租戶資料，MUST 註冊進 `internal/tenant` 的 `tenantOwned`。

**Go packages**

- `api/internal/ent/schema/order.go`：新增 `CheckoutIdempotencyKey` 實體。
- `api/internal/idempotency`（新套件）：預約／回放／釋放，以及格式驗證與請求雜湊。
- `api/internal/order/service.go`：`CheckoutInput` 新增選填的預約識別碼；`Checkout` 在既有交易內、扣庫存迴圈**之外**多一次條件式 UPDATE 定案該預約。扣庫存區塊逐字不動。
- `api/internal/guest/service.go`：`CheckoutInput` 新增 `json:"-"` 的預約欄位，原樣傳遞。
- `api/internal/httpapi/idempotency.go`（新檔）：兩個結帳 handler 共用的標頭解析／預約／回放輔助。
- `api/internal/httpapi/guest.go`、`order.go`：接上該輔助；`GuestHandler`／`OrderHandler` 各多一個 `Idempotency` 欄位。
- `api/internal/config/config.go`、`api/internal/app/wire.go`：設定與佈線。

**API 契約**

- `POST /api/v1/shop/guest/checkout` 與 `POST /api/v1/shop/checkout` 新增選填請求標頭 `Idempotency-Key`、新增 409 回應、422 新增兩個 code、201 新增 `Idempotency-Replayed` 回應標頭。**無新增路由**，因此路由覆蓋檢查的清單不變；`v1-surface.txt` 新增 `IdempotencyKey` 參數不入鎖（鎖只記路由與 schema 屬性），但錯誤碼敘述與新 component 需同步。
- `docs/storefront-integration.md`：第 5 節「訪客結帳」的「③ 送出按鈕要在請求期間停用／訪客結帳**沒有冪等鍵**」整段改寫，新增一節說明 key 怎麼產生、怎麼重試、回放時看到什麼。

**下游相依**

- ONDO（`bouquet`）：Worker 退場後，`state.submit.key` 由 body 的 `key` 欄位改為 `Idempotency-Key` 標頭，其餘重試邏輯（15s 逾時、重新送出、`handleSubmitResponse` 的 key 比對）**不需改變**。接入說明見本變更的 `docs/storefront-integration.md`。**接入動作發生在 bouquet repo，不在本變更範圍。**
- 未來任何新增的下單入口：MUST 沿用同一個冪等機制，MUST NOT 另寫一份。
