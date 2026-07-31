## 1. 前置查證

- [x] 1.1 確認本變更確實不需要 migration：`members.password_hash` 為 nullable、`UNIQUE(shop_id, email)` 為 NULLS DISTINCT、`orders.member_id` 為 `NOT NULL`（design Migration Plan）
      — 查證結果：`20260718063844_init_schema.up.sql` 建 `members` 時 `password_hash character varying(255) NULL`；`20260730152723_member_identity_per_shop.up.sql:73` 為 `CREATE UNIQUE INDEX uq_members_shop_email ON members (shop_id, email) NULLS DISTINCT`；`20260718131711_add_order_management.up.sql` 的 `orders.member_id bigint NOT NULL`。三項皆成立，本變更零 migration。
- [x] 1.2 確認 `city` / `postal_code` / `country` 於 `api/internal/` 全樹除 `ShippingAddress.validate` 外無任何讀取點（design D8 的前提）
      — 查證結果：全樹 grep `PostalCode` / `.Country` / `.City` 僅命中 `order/service.go` 的型別宣告與 `validate()`，以及 `order/service_test.go`。shipping / payment / points 完全不觸碰。前提成立。
- [x] 1.3 記錄 v1 外部消費者現況：`bouquet/public/app.js` 是否仍為零 API 呼叫（本變更為純 additive，不依賴此事實，但依既有慣例留下紀錄）
      — 查證結果：`bouquet/public/` 全樹 grep `fetch(` / `api/v1` / `axios` / `XMLHttpRequest` **零命中**。窗口仍開著，但本變更（design D12）刻意不使用。
- [x] 1.4 盤點既有 token 家族的 audience 與簽章金鑰，確認新增 `order:{shop_id}` 不與任何一者衝突（design D3）

  | 家族 | audience | 簽章金鑰 |
  | --- | --- | --- |
  | 後台 access | `admin` | admin secret |
  | 頁面預覽 | `preview` | admin secret |
  | 會員 access | `shop:{id}` | member secret |
  | **訂單存取（新）** | `order:{id}` | member secret |

  四者互斥。`order:` 與 `shop:` 皆帶 shop id，但前綴不同故不可能相等；jwt 函式庫在 audience 驗證階段即拒絕，不倚賴 handler 比對。

## 2. 訂單存取 token（spec: guest-checkout, authentication）

- [x] 2.1 `internal/auth/jwt.go`：新增 `OrderAccessClaims`（`shop_id`）與 `OrderAud(shopID)`
      — 命名為 `OrderAud`（比照既有 `MemberAud`），非任務原稿的 `OrderAccessAud`。
- [x] 2.2 同檔：`IssueOrderAccess(orderID, shopID int, ttl time.Duration)` — member 簽章金鑰、`aud=order:{shopID}`、`sub=orderID`
- [x] 2.3 同檔：`VerifyOrderAccess(token string, shopID int) (orderID int, err error)` — audience 綁定商家，任何失敗回既有 `ErrInvalidToken`
      — 另檢查 `claims.ShopID != shopID`（audience 已擋，此為 defense in depth）。
- [x] 2.4 `internal/auth/jwt_test.go`：新增三向隔離單元測試
      — `TestTokenIsolationMatrix` 擴充為四家族全格覆蓋（訂單 token 打 member/admin/preview 三條驗證路徑、member/admin/preview 三種 token 打訂單驗證路徑、跨商家），另加 `TestOrderAccessTokenRoundTrip` / `...Expires` / `...RejectsForeignSecret`。訂單 token 的 `sub` 刻意取與有效 member id 相同的值，確保測到的是 audience 而非碰巧的 subject 不合法。
- [x] 2.5 `internal/auth/identity.go`：新增 `WithOrderAccess` / `OrderAccessFrom`（獨立 context key，與 `memberKey` 不共用——design D4）
- [x] 2.6 `internal/config/config.go`：新增 `OrderAccessTokenTTL`（`ORDER_ACCESS_TOKEN_TTL`，預設 24h）
      — 同步補上 `.env.example`。

## 3. 購物車整批取代（spec: guest-checkout）

- [x] 3.1 `internal/cart/service.go`：新增 `ItemInput` 與 `ReplaceItems(ctx, shopID, memberID, lines)` — 單一 `ent.Tx` 內找出或建立 active cart、刪除既有品項、逐行驗證後寫入（design D7）
      — 實作順序略有調整：**先**在交易外把全部 SKU 查完並驗證完，交易內才動購物車。這使絕大多數失敗情形連交易都不必開，且保持「驗證全過才寫入」的語意。
- [x] 3.2 驗證規則與 `AddItem` 一致（SKU 屬本店、`is_active`、商品已發佈、幣別一致、數量 > 0、庫存足夠），錯誤指標為 `/items/{i}/...`
      — 多一條 `AddItem` 沒有的規則：同一批品項之間幣別必須一致（`AddItem` 是逐筆比對購物車已鎖定的幣別，整批取代時該鎖定會先被釋放，故改由第一筆決定、其餘比對）。
- [x] 3.3 重複 `sku_id` 累加（沿用 `AddItem` 語意）；空清單回 422
      — 累加後才做庫存檢查（`{sku, 1}` + `{sku, 1}` 對 stock=1 會被拒），與 `AddItem` 連續呼叫兩次的結果一致。
- [x] 3.4 任一行失敗整筆回滾，購物車內容與呼叫前完全相同
- [x] 3.5 **不**新增會員自助的批次 HTTP 端點（design D7 的範圍限制）

## 4. 訪客結帳協調者（spec: guest-checkout, order-management）

- [x] 4.1 新增 `internal/guest/service.go`：`Service{Client, Cart, Orders}` 與 `Checkout(ctx, shopID, in) (*ent.Order, error)`
- [x] 4.2 email 正規化與格式驗證；以 `(shop_id, email)` 找出或建立 `password_hash IS NULL`、`status = 1` 的身分（design D2/D10），並發建立撞唯一約束時重查沿用
      — email 正規化抽到 `internal/auth/email.go` 的 `NormalizeEmail`（原本散在 `memberauth.go` 的 `emailRe` + 兩處 `strings.ToLower(strings.TrimSpace(...))`）。理由：註冊、登入、訪客結帳三條路徑必須對「同一個 email」有完全相同的定義，否則同一人會在同一商家取得兩筆大小寫相異的身分，而 `UNIQUE(shop_id, email)` 不會攔。附單元測試鎖住冪等性與大小寫折疊。
- [x] 4.3 收件電話**不**寫入 `members.phone`（design D2）
      — 整合測試 `TestGuestCheckoutDoesNotWritePhoneToIdentity` 以「另一位顧客已擁有該電話」的情境證明：若寫入，唯一約束會讓這筆完全合理的訂單失敗。
- [x] 4.4 身分 `status != 1` 時回專屬的 `ErrCheckoutUnavailable`（design D6 問題一）
- [x] 4.5 依序呼叫 `cart.Service.ReplaceItems` → `order.Service.Checkout`，**不修改 `order.Service.Checkout` 一行**（design D1）
      — `order/service.go` 唯一的改動是 `addr.validate()` → `addr.Validate()`（任務 7.1 的匯出）與註解，交易與扣庫存主體零改動。
- [x] 4.6 錯誤型別沿用被呼叫套件的 `ValidationError` / `ConflictError`，不重新包裝成新的錯誤語彙
      — 因此 `/items/1/sku_id`、`/shipping_address/phone` 這些指標在訪客回應中與會員路徑意義完全相同。

## 5. HTTP 層（spec: guest-checkout）

- [x] 5.1 `internal/httpapi/middleware.go`：`NewOrderAccessMiddleware(issuer)` — 需 TenantMW 已解析 shop，驗證 `aud=order:{shop}`，將 orderID 放進 context
- [x] 5.2 `internal/httpapi/guest.go`（新檔）：`GuestHandler`，`POST /guest/checkout`（無 token）
- [x] 5.3 同檔：`GET /guest/order`、`POST /guest/order/payments`、`GET /guest/order/shipment`（token 授權，**路徑不帶訂單 id**——design D4）
- [x] 5.4 付款與出貨端點以 `order.MemberID` 作為既有服務方法的 `memberID` 參數（design D4）
- [x] 5.5 結帳回應：`{order, order_access_token, token_type, expires_in}`，201
      — 簽發 token 失敗（HMAC 層級錯誤，實務上不會發生）時仍回 201 但省略 token 並記錄 error：訂單已 commit，回 500 只會誘使客戶端重試而建立第二筆訂單。
- [x] 5.6 `internal/httpapi/ratelimitmw.go`：`NewGuestCheckoutRateLimit`，沿用 `shopIPRule` / `shopIPEmailRule`（design D9）
- [x] 5.7 `internal/config/config.go`：新增訪客結帳的四個限流門檻（寬鬆 20/10min、嚴格 5/10min）
- [x] 5.8 `internal/httpapi/router.go`：於 `/api/v1/shop` 群組內掛載 `/guest/*`（在 TenantMW 之下、MemberMW 之外），限流中介層排在 TenantMW 之後
- [x] 5.9 `internal/app/wire.go`：建構 `guest.Service` 與 `GuestHandler`，接上 issuer / cfg / 限流器

## 6. 註冊認領（spec: authentication, member-identity）

- [x] 6.1 `internal/httpapi/memberauth.go`：`Register` 由 `Exist` 改為載入該列，依 `password_hash` 是否為 NULL 分三路（design D5）
- [x] 6.2 認領以 `Where(IDEQ, PasswordHashIsNil())` 條件寫入並檢查受影響列數；0 列（並發認領）走 `registerFailed`
      — 述詞另帶 `ShopIDEQ`（後台慣例的顯式範圍化，雖然此處在租戶上下文中）。
- [x] 6.3 認領成功的回應與全新註冊同構（同樣的 token 三元組與狀態碼）
- [x] 6.4 確認拒絕分支的 `EqualizeVerifyTiming` 仍在，且認領分支的時序不構成新的側通道（認領同樣付出一次 Argon2id 雜湊成本）
      — 三條分支的成本：新建＝1 次 hash、認領＝1 次 hash、拒絕＝1 次 `EqualizeVerifyTiming`（1 次 Argon2id 驗證）。三者同量級，時序不再是可用的區分手段；可區分的是狀態碼本身（design D5 已明文記錄此邊界的移動）。
- [x] 6.5 確認 `Login` 對 `password_hash IS NULL` 的既有 401 行為未被改動
      — `m.PasswordHash == nil` 檢查原樣保留；整合測試 `TestPasswordHolderIsNotReclaimableAndPasswordlessCannotLogin` 覆蓋。

## 7. 收件地址必填放寬（spec: order-management）

- [x] 7.1 `internal/order/service.go`：`ShippingAddress.validate` 的必填集合縮為 `recipient_name` / `phone` / `line1`
      — 一併由 `validate()` 改名為匯出的 `Validate()`，讓 `guest.Service` 能在建立任何身分之前就擋下不完整的地址（否則格式錯誤會留下一筆無訂單的身分）。
- [x] 7.2 JSON 序列化形狀不變（`city` / `postal_code` / `country` 仍總是輸出，不加 `omitempty`）——既有訂單與既有回應形狀不受影響
      — 整合測試明確斷言這三個鍵**存在且為空字串**，而非消失。
- [x] 7.3 確認既有 `order_integration_test.go` 的 `validShippingAddress()` 不需改動（放寬不影響完整輸入）
      — 確認無須改動。但 `order/service_test.go` 的單元測試需要更新（它斷言缺少 `city`/`postal_code`/`country` 會失敗）——已改為斷言新的必填集合，並新增「台灣單欄地址可通過」與「只有選填欄位仍不足」兩案。

## 8. 測試

- [x] 8.1 `internal/cart/` 或整合測試：`ReplaceItems` 的原子性（失敗不留半套）、累加、清單為空、跨店 SKU 被拒
      — 放在 `cart_integration_test.go`（服務層直呼，因為它沒有 HTTP 路由）：`TestReplaceItemsSwapsContentsAtomically` / `...AccumulatesDuplicateSKUs` / `...RejectsAndLeavesCartUntouched`（8 個 subtest）/ `...RelocksCurrency`。
- [x] 8.2 新增 `internal/httpapi/guest_integration_test.go`：全新 email 建單、回頭客沿用同一身分、已是有密碼會員的 email、缺 email 422、收件電話不寫入身分
- [x] 8.3 訂單存取 token 範圍測試：讀該單、發起付款、讀出貨；無法列訂單歷史、無法取消、無法讀他單、過期 401、查詢參數不被接受
      — `TestOrderAccessTokenScope`（8 個 subtest）+ `TestOrderAccessTokenExpiryRejected`。「無法取消」同時斷言會員取消路由回 401 **與** `/guest/order/cancel` 根本不存在（404）。
- [x] 8.4 三向 token 隔離整合測試：會員 token 打 guest 端點 401、訂單 token 打會員端點 401、後台 token 401、跨商家 401
- [x] 8.5 **訪客結帳並發不超賣**（10 個不同 email 搶 stock=5，恰好 5 成 5 敗，`stock_qty = 0`）
- [x] 8.6 **訪客與會員混合併發搶同一 SKU**：成功總數不超過原庫存（證明兩條入口共用同一份保護）
      — `TestGuestAndMemberCheckoutShareOneStockGuard`：4 會員 + 4 訪客同時搶 stock=4，恰好 4 成功、最終為 0。
- [x] 8.7 既有 `TestCheckoutConcurrentOversellingProtection` 一行不改地保留並通過
      — 未改動一個字元；與 8.5／8.6 一併以 `-race` 連續重跑三次全綠。
- [x] 8.8 訪客結帳取代既有購物車內容的行為測試
- [x] 8.9 認領流程整合測試
      — `TestGuestIdentityClaimedByRegistration`（含真實付款 webhook → 點數核發 → 認領後 `GET /shop/orders` 與 `GET /shop/points` 皆看得到）、`TestClaimedIdentityCanCancelItsGuestOrder`、`TestConcurrentClaimOnlyOneWins`、`TestPasswordHolderIsNotReclaimableAndPasswordlessCannotLogin`。
- [x] 8.10 訪客結帳限流測試：同 email 超限 429、跨商家互不干擾
      — 加在 `ratelimit_integration_test.go`（既有的短視窗門檻環境）：`TestRateLimitBlocksGuestCheckoutOverThreshold`、`TestRateLimitGuestCheckoutShopScopeIsolation`。
- [x] 8.11 停用顧客（`status = 0`）的 email 訪客結帳被拒 422、不建單、庫存不變
- [x] 8.12 地址放寬測試：僅三個必填欄位可成功結帳（會員與訪客兩條路徑各一）、缺 `phone` 仍 422
- [x] 8.13 `make test` 全綠
- [x] 8.14 `make test-int` 全綠（含 `-race`）
      — 全套 `make test-int` 一次通過（httpapi 122s）；三個並發測試另以 `-race -count=1` 連續重跑三次全綠。
- [x] 8.15 `make lint` 無新增問題（`golangci-lint run` → 0 issues）

## 9. 收尾

- [x] 9.1 確認 `make migrate` 為 no-op、`api/migrations/` 與 `atlas.sum` 未變動
      — `git status --porcelain api/migrations/` 空輸出；`make migrate` 回報 `version=20260730161118`（仍是姊妹 change 的版本）。
- [x] 9.2 `make seed-demo` 後 `cd e2e && node happy-path.mjs` 通過
      — 17/17 PASSED，`e2e/happy-path.mjs` 一行未改。另對執行中的服務做了一次手動煙霧測試（見下）。
- [x] 9.3 更新 `CLAUDE.md` 架構註記與 `openspec/project.md` 的約束條目
- [x] 9.4 整理 ONDO 接入訪客結帳所需的前端作法作為交付說明
      — 見本檔末。

### 手動煙霧測試紀錄（對執行中的 dev 服務，seed-demo + 一個 stock=5 的商品）

| 步驟 | 結果 |
| --- | --- |
| `POST /api/v1/shop/guest/checkout`（`Host: demo.localhost`、無任何憑證、email 帶大小寫與尾空白） | 201，`member_id=1`、`total_amount=360000`、`shipping_address` 的 `city`/`postal_code`/`country` 為空字串但鍵存在 |
| SKU 庫存 | 5 → 3（由既有 checkout 扣減） |
| `GET /guest/order`（帶 token） | 200，該筆訂單 |
| `POST /guest/order/payments`（帶 token） | 201，含 `redirect_url` |
| `GET /guest/order/shipment`（尚未出貨） | 404 |
| `GET /api/v1/shop/orders`（帶訂單 token） | **401** |
| `GET /guest/order`（不帶 token） | **401** |
| `POST /shop/auth/register`（同 email，小寫） | 200 + token 三元組（認領） |
| 認領後 `GET /shop/orders` | `total=1`，正是那筆訪客訂單 |
| 認領後 `GET /shop/me` | `{"email":"ondo.buyer@example.com","id":1}`（大小寫已正規化、`member_id` 未變） |
| 再次以同 email 註冊 | 422 `registration_failed` |

## ONDO 接入說明（交付給 bouquet 專案）

接續 `storefront-client-auth` 的接入說明（每個請求帶 `X-Publishable-Key`、origin 需由平台登記）。以下是**訪客結帳**這一段前端要做的事。

### 前端必須新增的欄位

ONDO 現行結帳表單是 `state.form = { name, phone, addr, date, card }`，**沒有 email 欄位**。訪客結帳以 email 作為顧客身分的鍵，所以第一步（收件資訊）要加一格 email，且是必填。

好消息是地址不用拆：`city` / `postal_code` / `country` 已改為選填，`addr` 那一欄直接送 `line1` 即可（design D8）。

`date`（希望到貨日）與 `card`（卡號）目前 API 都不收：到貨日還沒有對應欄位（可暫時併進 `line1` 或等後續 change），卡號**絕對不要送到本 API**——付款一律走 `redirect_url` 導向金流。

### 呼叫順序

購物車完全留在前端（`state.cart`），不需要任何 `/cart/*` 呼叫。按下「送出訂單」時做一次請求：

**① 建立訂單**

```js
const res = await fetch(`${API}/api/v1/shop/guest/checkout`, {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    "X-Publishable-Key": PUBLISHABLE_KEY,   // 沒有 Authorization
  },
  body: JSON.stringify({
    email: form.email,
    items: cart.map(l => ({ sku_id: l.skuId, quantity: l.qty })),
    shipping_address: {
      recipient_name: form.name,
      phone: form.phone,
      line1: form.addr,
    },
  }),
});
```

`items[].sku_id` 是 API 的 SKU 識別碼，要從 `GET /api/v1/shop/products` 的回應取得（`products[].skus[].id`），不是 ONDO 前端自己的 `state.cart[].key`。**送出按鈕要在請求期間停用**——訪客結帳沒有冪等鍵，雙擊會建立兩筆訂單（design D11）。

回應 `201`：

```json
{
  "order": { "id": 1, "status": 0, "payment_status": 0, "total_amount": 360000,
             "currency": "TWD", "items": [...], "shipping_address": {...} },
  "order_access_token": "eyJhbGciOiJIUzI1NiIs...",
  "token_type": "Bearer",
  "expires_in": 86400
}
```

`order.id` 就是訂單編號（可取代目前隨機產生的 `state.orderNo`）。`order_access_token` 請存起來（`sessionStorage` 或 `localStorage` 皆可），有效 24 小時。

**② 發起付款**

```js
const pay = await fetch(`${API}/api/v1/shop/guest/order/payments`, {
  method: "POST",
  headers: {
    "X-Publishable-Key": PUBLISHABLE_KEY,
    "Authorization": `Bearer ${orderAccessToken}`,
  },
});
const { redirect_url } = await pay.json();   // 201
window.location.href = redirect_url;
```

**注意路徑裡沒有訂單 id**——token 已經指名了那一筆訂單。傳 id 也沒有用（沒有這個參數）。

**③ 之後查訂單／查出貨**

```
GET /api/v1/shop/guest/order            → 該筆訂單（含 payment_status / fulfillment_status）
GET /api/v1/shop/guest/order/shipment   → 該筆訂單的出貨資訊（尚未出貨時 404）
```

同樣帶 `X-Publishable-Key` + `Authorization: Bearer <order_access_token>`，同樣不帶訂單 id。

### 這個 token 能做什麼、不能做什麼

| 能 | 不能 |
| --- | --- |
| 讀取它指名的那一筆訂單 | 列出訂單歷史（`GET /api/v1/shop/orders` 回 401） |
| 對那一筆訂單發起付款 | 取消訂單 |
| 讀那一筆訂單的出貨資訊 | 讀取／修改顧客資料、密碼 |
| | 讀取或兌換點數 |
| | 換發任何其他 token（沒有 refresh） |

它**不是**登入。ONDO 不會因為引入訪客結帳而獲得會員 session；要有 session，顧客必須自己註冊（見下）。

### 24 小時後顧客要查訂單怎麼辦

叫他以**同一個 email** 註冊：

```
POST /api/v1/shop/auth/register   { "email": "...", "password": "..." }
```

因為訂單從建立的那一刻就綁在這個 email 的顧客身分上，註冊完成後 `GET /api/v1/shop/orders` 與 `GET /api/v1/shop/points` 立刻看得到全部歷史訂單與累積點數，不需要任何「綁定訂單」的動作。這也是訪客結帳自動建帳號的全部意義。

**平台側必須知道的一件事**：目前系統沒有 email 驗證能力，所以「以同一 email 註冊即取得先前訪客訂單」是**未經驗證**的關聯——理論上先猜到 email 又搶先註冊的人會看到那些訂單的收件資訊。風險邊界與選擇理由記在 design D6，正確的修法是後續的 `email-verification` change。ONDO 的顧客說明文案不要寫成「我們驗證了你的 email」。

### 錯誤排查對照

| 現象 | 意義 |
| --- | --- |
| 422 `validation_failed`，details 指向 `/email` | email 缺漏或格式不合法 |
| 422 `validation_failed`，details 指向 `/items/{i}/sku_id` | 該 SKU 不屬於本店／已停用／商品未發佈 |
| 422 `validation_failed`，details 指向 `/items/{i}/quantity` | 數量 ≤ 0 或超過現有庫存 |
| 422 `validation_failed`，details 指向 `/shipping_address/...` | 缺 `recipient_name` / `phone` / `line1`（其餘欄位不必填） |
| 422 `validation_failed`，message 為 `insufficient stock` | 送出當下庫存被別人買走了（結帳期間的原子檢查） |
| 422 `checkout_unavailable` | 該 email 的顧客身分已被商家停用 |
| 429 `rate_limited` | 同一 IP／email 短時間內建單太多；`Retry-After` 是秒數 |
| 401（訪客端點） | 訂單 token 缺少、過期，或不屬於這家商家 |
| 404（訪客端點且已帶 token） | 路徑打錯（訪客端點不接受訂單 id），或該訂單尚未出貨（shipment） |
| 404 `unknown_publishable_key` | 金鑰問題，見 `storefront-client-auth` 的接入說明 |
