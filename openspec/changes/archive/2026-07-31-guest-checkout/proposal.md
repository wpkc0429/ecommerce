## Why

花店客人不會為了買一束花先註冊帳號。即將接入的第一個外部前端（ONDO 溫度花室）是花藝電商，其結帳是三步驟訪客流程（收件資訊 → 配送方式 → 付款），全站沒有任何登入 UI，前端狀態只有 `{ name, phone, addr, date, card }`。但本 API 的購物車與結帳全掛在 `MemberMW` 之後，`order.Service.Checkout` 需要 `memberID`——ONDO 目前**無法以任何方式完成一筆訂單**。

「訪客購物車」在 Phase 2 被記為 non-goal，理由是不想引入第二套購物車機制。這個理由仍然成立；但它不該連帶封殺「訪客下單」。兩者可以拆開：訪客結帳時自動以 email 建立一筆無密碼身分，訂單一開始就綁在 `member_id` 上，其餘一切（庫存扣減、點數核發、訂單查詢、金物流）沿用既有路徑，一條都不另開。

姊妹 change `member-identity-per-shop` 已消除本方向最大的障礙：`members` 現為每店獨立（`UNIQUE(shop_id, email)`），「既有平台身分加入本店需驗證密碼」那條分支已刪除，同一 email 在 ONDO 就只是 ONDO 的顧客。`Member.password_hash` 本即 `Optional().Nillable()`（原註解已寫明「reserved for ... passwordless guest identities」），schema 早已留好位置——本變更**不需要任何 migration**。

## What Changes

- 新增訪客結帳端點 `POST /api/v1/shop/guest/checkout`：以 email + 品項 + 收件資訊一次完成下單。系統找出或建立該商家的無密碼 `members` 身分，將品項整批寫入該身分的既有 `carts`，再呼叫**既有的** `order.Service.Checkout`。並發安全的條件式扣庫存因此原封不動地適用於訪客訂單。
- 新增**訂單存取 token**（`aud=order:{shop_id}`，以 member 簽章金鑰簽發，預設 24 小時）：訪客結帳成功時一併回傳。它只能存取**該筆訂單**，不是會員 session——看不到訂單歷史、不能改密碼、不能兌點、不能刷新。新增三個以該 token 授權的端點：查該訂單、對該訂單發起付款、查該訂單出貨進度。三者皆**不接受路徑上的訂單 id**，訂單由 token 指名（比照 `CartHandler` 以構造消除 IDOR 的既有慣例）。
- **不簽發完整會員 JWT**。若簽發，等於「知道某人 email 就能取得其身分、看到其訂單歷史與點數」。
- 註冊流程新增「認領」語意：`POST /api/v1/shop/auth/register` 的 email 若已是本店**無密碼**身分，改為替該身分設定密碼並簽發 token（而非統一拒絕）。這是方案的核心價值——顧客日後真的想登入時，歷史訂單與累積點數自動浮現；同時它也堵住本變更否則會製造的「訪客結帳搶佔他人 email，使該 email 永遠無法註冊」的封鎖攻擊。
- `internal/cart` 新增 `ReplaceItems`：單一交易內整批取代 active cart 內容（刪除既有品項 → 逐筆驗證並寫入）。訪客結帳的購物車建置因此是原子的，失敗不會留下半套購物車。**不**新增 guest cart 資料表、cart token 或任何第二套購物車機制。
- 放寬 `order.ShippingAddress` 的必填欄位：`recipient_name` / `phone` / `line1` 維持必填，`city` / `postal_code` / `country` 改為選填（欄位本身保留，序列化形狀不變）。三者目前**無任何程式碼讀取**，而對台灣單欄地址的花店強制要求 `country: "TW"` 是儀式而非驗證。此為**放寬**（先前被拒的請求現在會通過，先前會通過的行為完全不變），落在 D13 additive 範圍內。
- 新增 `ORDER_ACCESS_TOKEN_TTL` 設定（預設 24h）與訪客結帳端點的速率限制（沿用既有 `shop+IP` 寬鬆／`shop+IP+email` 嚴格兩條規則，新增獨立門檻設定）。

本變更**完全 additive**：不改任何既有端點的路徑、請求形狀、狀態碼或授權規則（唯一的行為改變是上述兩項放寬——註冊接受更多情形、地址驗證接受更多情形），因此**不動用** D13 的破例窗口。姊妹 change 已用掉的兩次破例不需要第三次。

不在本次範圍內（明確記錄，非遺漏）：

- **不引入 email 發送、email 驗證或 magic link**。系統目前沒有任何 email 發送能力（無 SMTP、無 mailer、`go.mod` 零相關依賴），引入是獨立的一個 change，會拖住 ONDO 上線。其代價是「認領無密碼身分不需要證明擁有該 email」——本變更以 design D6 明確記錄此殘留風險與其邊界。
- **不做訪客結帳的冪等鍵（idempotency key）**。重複送出會建立兩筆訂單（各自正確扣庫存，不超賣），可由商家取消。這與既有會員結帳的暴露面不同，但引入冪等鍵是一套需要獨立設計的機制（鍵的儲存、TTL、回放語意），不應塞進本變更。
- **不新增訪客購物車資料表、guest cart token 或跨裝置同步**。ONDO 已在前端 `state.cart` 持有購物車。
- **不讓訂單存取 token 取消訂單**。取消是帳號層動作（見 design D4）。
- **不新增會員自助的批次購物車 HTTP 端點**。`ReplaceItems` 只是服務層方法，訪客結帳是它目前唯一的呼叫者；沒有消費者的端點不做。

## Capabilities

### New Capabilities
- `guest-checkout`: 未認證訪客如何完成一筆訂單——自動建立無密碼顧客身分、複用既有結帳路徑（含其並發安全保證）、以及只能存取單一訂單的短效訂單存取 token 的形狀與邊界。此能力橫跨身分、購物車、訂單與 token 體系，現行 specs 中無任何一份擁有它。

### Modified Capabilities
- `authentication`: 「Member registration and login in shop context」新增無密碼身分的認領路徑（本店 email 已存在且 `password_hash IS NULL` 時，註冊改為設定密碼而非拒絕），並隨之更正回應同構要求的精確邊界（可區分的不再是「是否為本店顧客」而是「是否已設定密碼」）；「Token isolation between admin and member」擴充為三個 token 家族的隔離矩陣，納入訂單存取 token。
- `member-identity`: 新增「無密碼顧客身分」的定位——其建立時機、無法登入的性質、與認領的關係，以及認領前後資料歸屬的不變式。
- `order-management`: 「Structured shipping address captured at checkout」的必填欄位集合縮小為 `recipient_name` / `phone` / `line1`；新增「任何下單路徑 MUST 複用同一條扣庫存路徑」的不變式（訪客結帳 MUST NOT 擁有第二條結帳實作）。

## Impact

**Schema / migration**

- **無**。`members.password_hash` 已是 nullable、`UNIQUE(shop_id, email)` 已允許無密碼身分共存，`orders.member_id` 維持 `NOT NULL`（訪客也是會員）。本變更不新增資料表、不改欄位、不產生 migration，`atlas.sum` 不變。

**Go packages**

- `api/internal/auth/jwt.go`：新增 `OrderAccessClaims` 與 `IssueOrderAccess` / `VerifyOrderAccess`（member 簽章金鑰、`aud=order:{shop_id}`）。
- `api/internal/guest`（新套件）：訪客結帳的協調者——解析或建立身分、呼叫 `cart.Service.ReplaceItems`、呼叫 `order.Service.Checkout`。不重寫其中任何一段。
- `api/internal/cart/service.go`：新增 `ReplaceItems`（單一交易整批取代）。
- `api/internal/httpapi/guest.go`（新檔）：訪客結帳與三個 token 授權端點。
- `api/internal/httpapi/middleware.go`：新增 `NewOrderAccessMiddleware`。
- `api/internal/httpapi/memberauth.go`：`Register` 新增認領分支。
- `api/internal/httpapi/ratelimitmw.go`：新增 `NewGuestCheckoutRateLimit`（沿用既有規則型別）。
- `api/internal/httpapi/router.go`、`api/internal/app/wire.go`、`api/internal/config/config.go`：佈線與設定。
- `api/internal/order/service.go`：`ShippingAddress.validate` 的必填集合縮小。

**API 契約**

- 新增 `POST /api/v1/shop/guest/checkout`、`GET /api/v1/shop/guest/order`、`POST /api/v1/shop/guest/order/payments`、`GET /api/v1/shop/guest/order/shipment`（皆為新路徑）。
- `POST /api/v1/shop/auth/register` 的請求／回應形狀不變；成功條件擴大（無密碼身分改為認領成功）。
- `POST /api/v1/shop/checkout` 的請求形狀不變；`city` / `postal_code` / `country` 由必填改為選填。

**下游相依**

- ONDO 接入面：前端需新增一個 email 欄位（現行結帳表單只有 `name` / `phone` / `addr`），並以 `sku_id` 表達品項。接入呼叫順序見 tasks.md 末的接入說明。
- 未來的 `email-verification` change：本變更的 design D6 是它最主要的驅動理由；屆時認領路徑應改為需要驗證，本變更的資料模型不需改動。
