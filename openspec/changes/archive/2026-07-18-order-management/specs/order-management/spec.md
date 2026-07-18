## ADDED Requirements

### Requirement: Checkout converts an active cart into an order

會員對自己的 active 購物車發起結帳（`POST /api/v1/shop/checkout`）時，系統 MUST 在單一資料庫交易內完成：驗證每個購物車品項可購買、對每個品項的 SKU 做原子扣庫存、建立訂單與訂單品項、將來源購物車 `status` 更新為 `1`（converted）。任一步驟失敗，整筆交易 MUST 回滾（不建立訂單、不變更庫存、不變更購物車狀態）。

#### Scenario: 結帳成功建立訂單並轉換購物車
- **WHEN** 會員的購物車內有可購買且庫存充足的品項，呼叫結帳
- **THEN** 建立一筆新訂單（含對應訂單品項），來源購物車 `status` 變為 `1`（converted）

#### Scenario: 空購物車結帳被拒
- **WHEN** 會員沒有 active 購物車，或其 active 購物車沒有任何品項，呼叫結帳
- **THEN** 回 422，不建立訂單

#### Scenario: 商品下架後結帳被拒
- **WHEN** 會員購物車內某品項對應的商品在結帳前被商家改為非已發佈（草稿），會員呼叫結帳
- **THEN** 回 422，指出該品項不可購買，不建立訂單，不影響任何庫存或其他品項

#### Scenario: SKU 已下架結帳被拒
- **WHEN** 會員購物車內某品項對應的 SKU 在結帳前被商家設為 `is_active = false`，會員呼叫結帳
- **THEN** 回 422，指出該品項不可購買，不建立訂單

#### Scenario: SKU 已被刪除結帳被拒
- **WHEN** 會員購物車內某品項對應的 SKU 已被刪除（`sku_id` 為 null），會員呼叫結帳
- **THEN** 回 422，指出該品項不可購買，不建立訂單

### Requirement: Checkout stock deduction is concurrency-safe and never oversells

結帳扣庫存 MUST 是原子操作（例如條件式 `UPDATE ... WHERE stock_qty >= ?` 並檢查受影響列數，或等效的列鎖機制），MUST NOT 先讀取庫存數量、於應用層判斷後再另行寫入。當多個結帳請求併發搶購同一個庫存有限的 SKU 時，系統 MUST 保證已扣減的庫存總量不超過扣減前的庫存，`product_skus.stock_qty` MUST NOT 變為負數；庫存不足以滿足某個結帳請求的品項時，該次結帳 MUST 整筆失敗（不部分成功），已由本次結帳扣掉的其他品項庫存 MUST 一併回滾。

#### Scenario: 併發結帳搶購同一 SKU 不超賣
- **WHEN** 某 SKU `stock_qty = 5`，10 個不同會員（或同一會員的 10 個並發請求）同時各自對數量為 1 的該 SKU 品項發起結帳
- **THEN** 恰好 5 個結帳成功，5 個因庫存不足失敗（422），該 SKU 最終 `stock_qty = 0`（不為負數），成功建立的訂單數量恰為 5

#### Scenario: 結帳品項之一庫存不足導致整單失敗、其餘品項庫存不變
- **WHEN** 購物車內有兩個品項，品項 A 庫存充足、品項 B 庫存不足以滿足購物車內數量，會員呼叫結帳
- **THEN** 回 422 指出品項 B 庫存不足，不建立訂單，品項 A 對應 SKU 的 `stock_qty` 不變（未被扣減）

### Requirement: Order line items are denormalized snapshots

訂單品項（`order_items`）建立時 MUST 複製當下的商品標題、SKU code 等顯示用文字為自身欄位（不僅存商品/SKU 的外鍵），價格與幣別快照 MUST 直接沿用來源購物車品項已經固化的 `price_amount`/`currency`（不重新查詢 SKU 現價）。訂單品項建立後，其顯示內容 MUST NOT 因商品被刪除、改名，或 SKU 被刪除、改價而改變。

#### Scenario: 商品刪除後訂單品項顯示內容不受影響
- **WHEN** 訂單建立後，其中某品項對應的商品（含其 SKU）被商家刪除
- **THEN** 查詢該訂單詳情時，該品項的商品標題、SKU code、價格、數量與刪除前完全相同

#### Scenario: 商品改名或改價後既有訂單品項不受影響
- **WHEN** 訂單建立後，其中某品項對應的商品標題或 SKU 現價被商家修改
- **THEN** 查詢該訂單詳情時，該品項顯示的標題與價格仍是結帳當下的快照值

### Requirement: Order three-axis status model

訂單 MUST 有三個獨立的狀態欄位：`status`（訂單本身生命週期，僅 `0`=已建立 或 `1`=已取消）、`payment_status`（付款狀態，本階段建立時固定為 `0`=未付款，本階段 MUST NOT 有任何流程將其變更為其他值）、`fulfillment_status`（出貨狀態，本階段建立時固定為 `0`=未出貨，本階段 MUST NOT 有任何流程將其變更為其他值）。系統 MUST 提供服務層方法（非直接資料庫寫入）供其他能力更新 `payment_status`/`fulfillment_status`，該方法 MUST 以 shop 範圍與訂單存在性為前提。

#### Scenario: 新訂單的三軸狀態初始值
- **WHEN** 結帳成功建立一筆新訂單
- **THEN** 該訂單 `status = 0`、`payment_status = 0`、`fulfillment_status = 0`

### Requirement: Order cancellation requires all three axes at their initial value, and restores stock

訂單取消（`POST .../orders/{id}/cancel`）僅在該訂單 `status = 0`（已建立）且 `payment_status = 0`（未付款）且 `fulfillment_status = 0`（未出貨）時允許；任一軸已偏離初始值，取消請求 MUST 被拒絕（409），訂單狀態與庫存 MUST NOT 變動。取消成功時，系統 MUST 在同一資料庫交易內將該訂單 `status` 更新為 `1`（已取消），並將該訂單每個仍可定位到存活 SKU 的品項數量歸還至該 SKU 的 `stock_qty`。

#### Scenario: 取消未付款未出貨的訂單成功並歸還庫存
- **WHEN** 一筆 `payment_status = 0`、`fulfillment_status = 0` 的訂單被取消
- **THEN** 訂單 `status` 變為 `1`，其每個品項對應 SKU（若仍存在）的 `stock_qty` 增加該品項的訂購數量

#### Scenario: 已取消訂單不可重複取消
- **WHEN** 對一筆 `status = 1`（已取消）的訂單再次呼叫取消
- **THEN** 回 409，訂單狀態與庫存不再變動

### Requirement: Structured shipping address captured at checkout

結帳請求 MUST 附帶結構化收件資訊（至少包含收件人姓名、電話、地址行、城市、郵遞區號、國別），系統 MUST 驗證必要欄位皆非空，缺漏 MUST 回 422。收件資訊 MUST 以訂單自身的欄位儲存（不透過其他表關聯），格式 MUST 讓後續能力可直接讀取解析，不需額外轉換。

#### Scenario: 缺少必要收件欄位被拒
- **WHEN** 結帳請求的收件資訊缺少電話欄位
- **THEN** 回 422，不建立訂單

#### Scenario: 收件資訊完整時結帳成功並可於訂單詳情讀回
- **WHEN** 結帳請求附帶完整的收件人姓名、電話、地址、城市、郵遞區號、國別
- **THEN** 訂單建立成功，訂單詳情回應包含與請求相符的收件資訊

### Requirement: Member self-service order access is scoped by member identity

會員自助訂單 API（`GET /api/v1/shop/orders`、`GET /api/v1/shop/orders/{id}`、`POST /api/v1/shop/orders/{id}/cancel`）MUST 僅依賴已驗證會員 JWT（`aud=shop:{shop_id}`）中的 `member_id` 判斷存取範圍，MUST NOT 套用 RBAC 角色權限節點系統。會員 MUST 只能查詢與取消自己名下的訂單；存取不屬於自己的訂單 id MUST 回 404（不得洩漏該訂單屬於其他會員）。

#### Scenario: 會員查詢自己的訂單列表
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/orders`
- **THEN** 回傳僅屬於該會員的訂單，分頁

#### Scenario: 跨會員查詢訂單詳情被拒
- **WHEN** 會員 A 嘗試以 `GET /api/v1/shop/orders/{id}` 查詢屬於會員 B 的訂單
- **THEN** 回 404

#### Scenario: 跨會員取消訂單被拒
- **WHEN** 會員 A 嘗試以 `POST /api/v1/shop/orders/{id}/cancel` 取消屬於會員 B 的訂單
- **THEN** 回 404，該訂單狀態不變

#### Scenario: 跨店重用會員 token 存取訂單被拒
- **WHEN** 以 shop A 簽發的會員 token 呼叫 shop B 網域的訂單 API
- **THEN** audience 不符，回 401

### Requirement: Merchant back-office order access is scoped by shop and RBAC

商家後台訂單 API（`GET /api/v1/admin/shops/{shopID}/orders`、`GET /api/v1/admin/shops/{shopID}/orders/{id}`、`POST /api/v1/admin/shops/{shopID}/orders/{id}/cancel`）MUST 依既有三層 RBAC 判定（spec rbac）：檢視需要 `order.view`，取消需要 `order.cancel`。商家 MUST 只能操作 URL 中 `shopID` 所屬的訂單；跨商家操作 MUST 依既有 cross-shop access guard 回 403。訂單列表 MUST 支援分頁，MAY 依訂單 `status` 篩選。

#### Scenario: 具權限商家檢視自己商家的訂單
- **WHEN** 持有 shop A `order.view` 權限的使用者呼叫 `GET /api/v1/admin/shops/{shopA}/orders`
- **THEN** 回傳僅屬於 shop A 的訂單，分頁

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `order.cancel` 權限，嘗試取消 shop A 的訂單
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的訂單管理 API
- **THEN** 回 403

#### Scenario: 依狀態篩選訂單列表
- **WHEN** 以 `status=1` 查詢 shop A 的訂單列表
- **THEN** 僅回傳該商家 `status = 1`（已取消）的訂單

### Requirement: Tenant data isolation for order tables

`orders`、`order_items` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop。

#### Scenario: 跨店查詢看不到彼此的訂單資料
- **WHEN** shop A 的會員或商家查詢自己的訂單
- **THEN** 回應不含任何 shop B 的訂單或品項資料
