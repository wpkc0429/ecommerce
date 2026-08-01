## Why

`member_tiers.discount_percent`（0–100）自 change `member-tiers-and-points` 就存在，`admin-commerce-operations` 剛把它做成後台可設定、可顯示的欄位——**但結帳從頭到尾沒有任何一行程式讀它**。商家設定「VIP 九折」，顧客結帳照原價付款、金流照原價請款、商家對帳看不出異常。

這比「缺功能」更糟：缺功能顧客看得出來，**設定了卻沒作用是靜默失效**——商家以為自己在給折扣，實際上沒有，而且沒有任何畫面會告訴他這件事。

`member-tiers-and-points` 當初把折扣套用列為明確的 Non-Goal（「`discount_percent` 是保留、未使用的欄位」「本階段僅儲存不套用任何折扣邏輯」）。在**沒有後台 UI、沒有真實商家**的階段那是正確的取捨。兩個前提都已消失：後台已經把這個欄位擺在商家面前當成可設定項（`admin-commerce-operations` 自己的 Non-Goals 又一次把套用推遲），而第一個外部商家（ONDO 溫度花室）即將上線。**取捨的前提變了，所以取捨要重做**——正面論證見 design D1。

## What Changes

- **`order.Service.Checkout` 在結帳時解析顧客的會員等級並套用其 `discount_percent`**，兩條下單路徑（會員 `POST /api/v1/shop/checkout`、訪客 `POST /api/v1/shop/guest/checkout`）行為一致，因為兩者共用的就是這一個方法——不新增任何下單入口。
- **`orders` 新增四個欄位**：`discount_amount`（實際折抵金額，int64 最小貨幣單位）、`discount_percent`（當時套用的折扣率快照）、`member_tier_id`（資訊性，無 FK）、`member_tier_name`（名稱快照）。折扣是**一次性快照**，訂單建立後不因商家改動等級設定或顧客升降級而重算。
- **金額恆等式由 DB CHECK 改為 `total_amount = subtotal_amount − discount_amount + shipping_fee`**，並新增 `discount_amount >= 0` 與 `discount_amount <= subtotal_amount`（後者讓「折扣永遠不會把商品金額變成負的」成為資料庫事實）。折扣**只套在品項小計上，運費不打折**。
- **折抵金額的捨入規則寫進 spec**：`discount_amount = subtotal_amount × discount_percent / 100`，**整數運算、無條件捨去**——與既有的點數核發公式 `floor(subtotal / 100)` 是同一條規則，全系統只有一種捨入方向。
- **點數核發基準由 `subtotal_amount` 細化為 `subtotal_amount − discount_amount`（折後商品金額）**。這正是 `order-extras` design D5 理由 4 預告過的細化。`events.OrderPaymentSucceeded.SubtotalAmount` 因此**更名**為 `AwardBasisAmount`（更名而非新增欄位，理由同 `order-extras` design D8）。
- **`GET /api/v1/shop/points` 已經回傳 `level.discount_percent`**，所以會員前台的「結帳前預估折扣」**不需要任何新端點**；訪客刻意**沒有**結帳前預估（design D5 論證為何那是一個不該存在的端點）。
- 後台：會員等級畫面移除「本階段只儲存不套用」的說明並改為說明它會在結帳時生效；訂單詳情的金額組合由三件組變為四件組（折扣列僅在 `discount_amount > 0` 時出現）。
- `api/openapi/openapi.yaml` 與 `docs/storefront-integration.md` 同步更新；`v1-surface.txt` 只增不減（**純 additive，不動用已關閉的 D13 破例窗口**，逐項論證見 design D6）。

## Capabilities

### New Capabilities
- `tier-discount-application`: 會員等級折扣在結帳時的解析、整數捨入、金額恆等式、以及套用結果在訂單上的可稽核快照。

### Modified Capabilities
- `member-tiers-and-points`: `discount_percent` 由「僅儲存、無任何邏輯讀取」改為「結帳時套用」；點數核發基準由訂單品項小計細化為**折扣後**的品項小計。
- `order-management`: 訂單金額組成新增折抵項，恆等式改為 `total = subtotal − discount + fee`；訂單新增折扣快照欄位。
- `guest-checkout`: 訪客結帳的 email 若對應到一個已持有等級的身分，該訂單**套用**該等級的折扣（含隨之而來的、已評估並接受的資訊揭露邊界）。
- `admin-operations-ui`: 會員等級畫面不得再宣稱折扣不套用；訂單詳情必須呈現折抵金額與其來源等級。

## Impact

- `api/internal/ent/schema/order.go`：`Order` 新增四個欄位與三條 CHECK（其中一條是既有 `orders_total_amount_composition_check` 的取代）。
- `api/migrations/`：一支新 migration（純新增欄位＋約束替換，既有列 `discount_amount` 預設 0 即滿足新恆等式，**不需要資料回填**）；`atlas.sum` 重算。
- `api/internal/order/service.go`：`resolveTierDiscount`（交易**之外**，與 `resolveShippingMethod` 同階段）＋ 訂單建立時多寫四個欄位。**條件式扣庫存迴圈與其受影響列數檢查逐字不動。**
- `api/internal/events/events.go`：`OrderPaymentSucceeded.SubtotalAmount` → `AwardBasisAmount`。
- `api/internal/payment/service.go`：發佈事件時帶 `ord.SubtotalAmount - ord.DiscountAmount`。
- `api/internal/points/service.go`：核發基準改讀 `AwardBasisAmount`（一行）；`member_tiers` CRUD 不變。
- `api/internal/httpapi/order.go`：`orderJSON` 新增四個鍵。
- `api/openapi/openapi.yaml`（`Order`、`MemberTier`、兩條結帳端點的敘述）、`api/openapi/v1-surface.txt`（只增行）、`docs/storefront-integration.md`（新增「等級折扣」一節與錯誤／顯示對照）。
- `admin/views/member-tiers.js`、`admin/views/orders.js`、`admin/index.html`（少量文案與一列金額；**實測行數不得跨過 `admin-commerce-catalog` design D1 tripwire #4 的 ~3000 行**，見 design D8）。
- `e2e/commerce-path.mjs`（新增「等級折扣」區段：套用、快照不變、點數以折後為基準）、`e2e/happy-path.mjs`（同一顧客第二筆訂單在後台顯示折扣列）。
- `api/internal/seed`：**不改**。demo 的三級等級本來就帶 5%／10%，本變更讓那兩個數字第一次真的生效。
