# order-management Specification

## Purpose
TBD - created by archiving change order-management. Update Purpose after archive.
## Requirements
### Requirement: Checkout converts an active cart into an order

會員對自己的 active 購物車發起結帳（`POST /api/v1/shop/checkout`）時，系統 MUST 在單一資料庫交易內完成：驗證每個購物車品項可購買、對每個品項的 SKU 做原子扣庫存、建立訂單與訂單品項、將來源購物車 `status` 更新為 `1`（converted）。任一步驟失敗，整筆交易 MUST 回滾（不建立訂單、不變更庫存、不變更購物車狀態）。

結帳請求 MAY 附帶運送方式與指定配送日；這兩者的驗證 MUST 在交易開始之前完成，任一驗證失敗 MUST 回 422 且 MUST NOT 觸及庫存或購物車狀態。訂單的三個金額欄位（`subtotal_amount`／`shipping_fee`／`total_amount`）MUST 於同一交易內算定並寫入。

結帳請求 MAY 附帶冪等鍵（spec checkout-idempotency）。附帶時，該鍵的預約 MUST 於同一交易內定案——「訂單存在」與「該鍵已完成並指向該訂單」MUST 是同一個原子事實，MUST NOT 由交易外的第二次寫入達成。定案失敗 MUST 使整筆交易回滾（不建立訂單、不變更庫存、不變更購物車狀態）。未附帶冪等鍵時，結帳的行為 MUST 與引入該能力之前完全相同。

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

#### Scenario: 運送方式驗證失敗不觸及庫存
- **WHEN** 會員的購物車品項全部可購買，但結帳請求附帶一個已停用的運送方式識別碼
- **THEN** 回 422，不建立訂單，購物車 `status` 維持 `0`（active），所有 SKU 的 `stock_qty` 不變

#### Scenario: 冪等鍵的定案與訂單建立同進同退
- **WHEN** 帶冪等鍵的結帳在建立訂單之後、交易提交之前發生失敗
- **THEN** 整筆交易回滾：不建立訂單、庫存不變、購物車 `status` 維持 `0`（active），且該冪等鍵 MUST NOT 停留在「已完成」狀態

### Requirement: Checkout stock deduction is concurrency-safe and never oversells

結帳扣庫存 MUST 是原子操作（例如條件式 `UPDATE ... WHERE stock_qty >= ?` 並檢查受影響列數，或等效的列鎖機制），MUST NOT 先讀取庫存數量、於應用層判斷後再另行寫入。當多個結帳請求併發搶購同一個庫存有限的 SKU 時，系統 MUST 保證已扣減的庫存總量不超過扣減前的庫存，`product_skus.stock_qty` MUST NOT 變為負數；庫存不足以滿足某個結帳請求的品項時，該次結帳 MUST 整筆失敗（不部分成功），已由本次結帳扣掉的其他品項庫存 MUST 一併回滾。

#### Scenario: 併發結帳搶購同一 SKU 不超賣
- **WHEN** 某 SKU `stock_qty = 5`，10 個不同會員（或同一會員的 10 個並發請求）同時各自對數量為 1 的該 SKU 品項發起結帳
- **THEN** 恰好 5 個結帳成功，5 個因庫存不足失敗（422），該 SKU 最終 `stock_qty = 0`（不為負數），成功建立的訂單數量恰為 5

#### Scenario: 結帳品項之一庫存不足導致整單失敗、其餘品項庫存不變
- **WHEN** 購物車內有兩個品項，品項 A 庫存充足、品項 B 庫存不足以滿足購物車內數量，會員呼叫結帳
- **THEN** 回 422 指出品項 B 庫存不足，不建立訂單，品項 A 對應 SKU 的 `stock_qty` 不變（未被扣減）

### Requirement: Order line items are denormalized snapshots

訂單品項（`order_items`）建立時 MUST 複製當下的商品標題、SKU code 等顯示用文字為自身欄位（不僅存商品/SKU 的外鍵），價格與幣別快照 MUST 直接沿用來源購物車品項已經固化的 `price_amount`/`currency`（不重新查詢 SKU 現價）。來源購物車品項附帶的贈禮訊息（卡片留言）MUST 一併複製為訂單品項自身的欄位。訂單品項建立後，其顯示內容 MUST NOT 因商品被刪除、改名，SKU 被刪除、改價，或來源購物車被清空而改變。

#### Scenario: 商品刪除後訂單品項顯示內容不受影響
- **WHEN** 訂單建立後，其中某品項對應的商品（含其 SKU）被商家刪除
- **THEN** 查詢該訂單詳情時，該品項的商品標題、SKU code、價格、數量與刪除前完全相同

#### Scenario: 商品改名或改價後既有訂單品項不受影響
- **WHEN** 訂單建立後，其中某品項對應的商品標題或 SKU 現價被商家修改
- **THEN** 查詢該訂單詳情時，該品項顯示的標題與價格仍是結帳當下的快照值

#### Scenario: 贈禮訊息隨品項快照進訂單
- **WHEN** 購物車內某品項附有贈禮訊息，會員或訪客完成結帳
- **THEN** 對應的訂單品項帶有與購物車品項完全相同的贈禮訊息

#### Scenario: 同一 SKU 的不同贈禮訊息在訂單中維持為獨立品項
- **WHEN** 購物車內含同一 SKU 的兩個品項，各帶不同的贈禮訊息
- **THEN** 訂單中對應產生兩個獨立的訂單品項，各自保有自己的贈禮訊息

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

結帳請求 MUST 附帶結構化收件資訊。必填欄位為收件人姓名、電話、地址行三者，系統 MUST 驗證其皆非空，缺漏 MUST 回 422。城市、郵遞區號、國別 MUST 為選填——地址結構因地區而異（單欄地址於部分地區為常態），系統 MUST NOT 為尚未有任何能力讀取的欄位強制要求值。選填欄位缺席時 MUST 以空值儲存，MUST NOT 由系統推測或填入預設值。

收件資訊的儲存格式 MUST 保留全部六個欄位鍵（無論是否有值），MUST 以訂單自身的欄位儲存（不透過其他表關聯），且 MUST 讓後續能力可直接讀取解析，不需額外轉換。

#### Scenario: 缺少必要收件欄位被拒
- **WHEN** 結帳請求的收件資訊缺少電話欄位
- **THEN** 回 422，不建立訂單

#### Scenario: 僅提供必填欄位的單欄地址結帳成功
- **WHEN** 結帳請求的收件資訊只有收件人姓名、電話與地址行（無城市、郵遞區號、國別）
- **THEN** 訂單建立成功，訂單詳情回應中該三個選填欄位為空值

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

訂單列表 SHALL 額外支援下列**選填**篩選，且每一項 MUST 作用於該商家的整個訂單集合（而非單一頁）：

- 依 `payment_status` 精確比對
- 依 `fulfillment_status` 精確比對
- 依 `requested_delivery_date` 的閉區間（以日曆日表達，區間端點皆為選填且可單邊給定）
- 依關鍵字：可解析為正整數時 MUST 比對訂單編號；同時 MUST 對收件人姓名與收件人電話做大小寫不敏感的子字串比對
- 依顧客識別碼精確比對（`member_id`）——「這位顧客買過什麼」MUST 由本列表回答，MUST NOT 由顧客端點另行回傳一份訂單內容

未提供任何上述選填參數時，列表的查詢與回應 MUST 與本需求變更前完全相同。

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

#### Scenario: 依付款與出貨狀態篩選出待出貨清單
- **WHEN** 以「已付款」的 `payment_status` 與「未出貨」的 `fulfillment_status` 併用查詢
- **THEN** 僅回傳同時符合兩者的訂單

#### Scenario: 依指定配送日區間篩選
- **WHEN** 以某一日曆日同時作為區間起訖查詢
- **THEN** 僅回傳 `requested_delivery_date` 等於該日的訂單；**未指定配送日的訂單 MUST NOT 被回傳**

#### Scenario: 關鍵字比對收件人姓名或電話
- **WHEN** 以收件人姓名或電話的一部分作為關鍵字查詢
- **THEN** 回傳收件資訊中姓名或電話包含該子字串的訂單，比對 MUST 大小寫不敏感

#### Scenario: 關鍵字為數字時亦比對訂單編號
- **WHEN** 以一個存在於該商家的訂單編號作為關鍵字查詢
- **THEN** 該訂單 MUST 出現在結果中

#### Scenario: 依顧客識別碼篩出該顧客的全部訂單
- **WHEN** 以某位顧客的識別碼作為 `member_id` 查詢
- **THEN** 僅回傳該商家中屬於該顧客的訂單，且該顧客的每一筆訂單 MUST 可經由分頁取得

#### Scenario: 顧客識別碼篩選受商家範圍約束
- **WHEN** 以一個屬於另一商家的顧客識別碼作為 `member_id` 查詢本商家的訂單列表
- **THEN** 回傳零筆，MUST NOT 回傳任何其他商家的訂單

#### Scenario: 不帶新參數時行為不變
- **WHEN** 呼叫端未提供任何新增的選填篩選參數
- **THEN** 回應 MUST 與這些參數存在之前完全相同

### Requirement: Tenant data isolation for order tables

`orders`、`order_items` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop。

#### Scenario: 跨店查詢看不到彼此的訂單資料
- **WHEN** shop A 的會員或商家查詢自己的訂單
- **THEN** 回應不含任何 shop B 的訂單或品項資料

### Requirement: 所有下單路徑共用單一結帳實作

系統中任何建立訂單的路徑（會員結帳、訪客結帳，以及未來任何新增的下單入口）MUST 呼叫同一個結帳服務方法完成扣庫存、建單與金額組成。系統 MUST NOT 存在第二段實作條件式扣庫存的程式碼，也 MUST NOT 存在第二段自行計算 `subtotal_amount`／`discount_amount`／`shipping_fee`／`total_amount` 的程式碼。

**重複送出的冪等保護亦 MUST 只有一份實作，且 MUST 對所有下單路徑一致適用**（spec checkout-idempotency）。系統 MUST NOT 讓某一條下單路徑擁有自己的一套冪等語意，也 MUST NOT 讓某一條下單路徑成為繞過冪等保護的捷徑。

**等級折扣的解析與計算亦 MUST 只有一份實作，且 MUST 落在該同一個結帳服務方法內**（spec tier-discount-application）。任何下單入口 MUST NOT 自行決定折扣率、自行計算折抵金額，也 MUST NOT 接受由請求主體指定的折扣率或折抵金額。

新增下單入口時，其與既有入口的差異 MUST 僅止於「如何取得顧客身分」與「如何取得品項與訂單層附加輸入」，MUST NOT 觸及庫存扣減、訂單品項快照、金額組成、折扣套用、購物車狀態轉換或冪等鍵定案的邏輯。

#### Scenario: 不同下單入口共用同一份庫存保護

- **WHEN** 兩個不同的下單入口併發搶購同一個庫存有限的 SKU
- **THEN** 成功總數不超過扣減前的庫存量，`product_skus.stock_qty` MUST NOT 為負數

#### Scenario: 新入口不重複實作扣庫存

- **WHEN** 檢視任一下單入口的實作
- **THEN** 其扣庫存與建單皆經由既有結帳服務方法完成，該入口自身 MUST NOT 直接寫入 `product_skus.stock_qty` 或建立 `orders`／`order_items`

#### Scenario: 不同下單入口對同一輸入產生相同的金額組成

- **WHEN** 會員結帳與訪客結帳以相同的品項、相同的運送方式、且顧客身分屬於同一個會員等級，各建立一筆訂單
- **THEN** 兩筆訂單的 `subtotal_amount`／`discount_amount`／`shipping_fee`／`total_amount` 完全相同

#### Scenario: 折扣不可由請求指定

- **WHEN** 任一結帳請求的主體中含有折扣率或折抵金額之類的鍵
- **THEN** 該值 MUST 被完全忽略，訂單的折扣 MUST 僅由伺服器端解析出的顧客等級決定

#### Scenario: 不同下單入口的冪等語意一致

- **WHEN** 分別對會員結帳與訪客結帳送出帶相同形式冪等鍵的重複請求
- **THEN** 兩者的回放語意、錯誤碼與狀態碼完全一致，且皆 MUST NOT 建立第二筆訂單

### Requirement: Order amounts are composed of an item subtotal and a shipping fee

訂單 MUST 記錄四個獨立的金額欄位，皆為 `int64`／`BIGINT` 整數最小貨幣單位（沿用 spec product-catalog 的金額表示法，MUST NOT 使用浮點數），並共用訂單自身的 `currency`：

- `subtotal_amount`：品項小計，等於 Σ(`order_items.price_amount` × `quantity`)。**折扣 MUST NOT 改變這個值**——它永遠是折扣前的品項小計。
- `discount_amount`：等級折扣的折抵金額（spec tier-discount-application），未套用任何折扣時 MUST 為 `0`。
- `shipping_fee`：本筆訂單的運費快照，未選定運送方式時 MUST 為 `0`。**折扣 MUST NOT 改變這個值**。
- `total_amount`：**應付總額**，MUST 恆等於 `subtotal_amount − discount_amount + shipping_fee`。

此組成關係 MUST 由資料庫層的約束強制，MUST NOT 僅依賴應用層計算。`subtotal_amount`、`discount_amount`、`shipping_fee`、`total_amount` 皆 MUST >= 0，且 `discount_amount` MUST <= `subtotal_amount`。任何向金流請款的金額 MUST 使用 `total_amount`，MUST NOT 使用 `subtotal_amount`。

金額 MUST 於結帳當下一次算定並永久快照，MUST NOT 因商品改價、運送方式改價、運送方式被刪除、會員等級改價或會員等級被刪除而重新計算。

#### Scenario: 未選定運送方式且無折扣的訂單

- **WHEN** 結帳請求未附帶運送方式，且顧客不屬於任何有折扣的會員等級
- **THEN** 訂單 `shipping_fee = 0`、`discount_amount = 0`、`subtotal_amount` 等於品項小計、`total_amount` 等於 `subtotal_amount`

#### Scenario: 選定運送方式的訂單其應付總額含運費

- **WHEN** 品項小計為 `3600`、選定的運送方式固定運費為 `200`、無折扣
- **THEN** 訂單 `subtotal_amount = 3600`、`discount_amount = 0`、`shipping_fee = 200`、`total_amount = 3800`

#### Scenario: 帶折扣的訂單其應付總額扣除折抵

- **WHEN** 品項小計為 `3600`、折抵 `360`、運費 `200`
- **THEN** 訂單 `subtotal_amount = 3600`、`discount_amount = 360`、`shipping_fee = 200`、`total_amount = 3440`

#### Scenario: 金額組成不可被破壞

- **WHEN** 任何路徑嘗試建立或寫入一筆 `total_amount != subtotal_amount - discount_amount + shipping_fee` 的訂單
- **THEN** 寫入 MUST 失敗（資料庫約束拒絕），MUST NOT 產生不一致的訂單

#### Scenario: 運費快照不因運送方式改價而變動

- **WHEN** 訂單建立後，其所選運送方式的固定運費被商家調整或該運送方式被刪除
- **THEN** 該訂單的 `shipping_fee` 與 `total_amount` 完全不變

#### Scenario: 折抵快照不因等級改價而變動

- **WHEN** 訂單建立後，顧客所屬會員等級的折扣率被商家調整、該等級被刪除、或顧客升降級
- **THEN** 該訂單的 `discount_amount` 與 `total_amount` 完全不變

### Requirement: Checkout accepts an optional shipping method and snapshots it onto the order

結帳請求 MAY 附帶運送方式識別碼。附帶時，系統 MUST 驗證該運送方式屬於請求所屬的 shop 且為啟用狀態（`is_active = true`）；不存在、屬於其他商家或已停用 MUST 回 422（不得回 404、不得靜默忽略），且 MUST NOT 建立訂單。

驗證通過時，系統 MUST 將該運送方式當下的固定運費複製為訂單的 `shipping_fee`，並將其名稱複製為訂單自身的欄位；系統 MUST 同時記錄所選運送方式的識別碼，該識別碼 MUST 為資訊性欄位，呼叫端 MUST NOT 倚賴它日後仍能解析到存在的運送方式。

未附帶運送方式時，訂單的運送方式識別碼與名稱 MUST 為空值，`shipping_fee` MUST 為 `0`，其餘行為 MUST 與本需求引入前完全相同。

#### Scenario: 選定啟用中的運送方式

- **WHEN** 結帳請求附帶一個屬於本店且 `is_active = true` 的運送方式識別碼
- **THEN** 訂單建立成功，其 `shipping_fee` 等於該運送方式的固定運費，訂單詳情回應包含該運送方式的識別碼與名稱

#### Scenario: 選定已停用的運送方式被拒

- **WHEN** 結帳請求附帶一個 `is_active = false` 的運送方式識別碼
- **THEN** 回 422，不建立訂單，不變更任何庫存

#### Scenario: 選定其他商家的運送方式被拒

- **WHEN** 結帳請求附帶一個屬於其他商家的運送方式識別碼
- **THEN** 回 422，不建立訂單

#### Scenario: 運送方式名稱在其被刪除後仍可讀回

- **WHEN** 訂單建立後，商家刪除該筆訂單所選的運送方式
- **THEN** 查詢該訂單詳情時，運送方式名稱與 `shipping_fee` 與建立當下完全相同

### Requirement: Checkout accepts an optional requested delivery date

結帳請求 MAY 附帶指定配送日。該值 MUST 以訂單自身的日期型別欄位儲存（僅日期、不含時刻與時區），MUST NOT 儲存於收件資訊的結構化欄位內。

附帶時，系統 MUST 驗證其為 `YYYY-MM-DD` 格式且為真實存在的日期，並 MUST 拒絕在任何時區都已成為過去的日期（即早於「UTC 今日的前一天」）；驗證失敗 MUST 回 422，且 MUST NOT 建立訂單。系統 MUST NOT 施加其他日期限制（例如商家前置天數或最遠可預訂日）。

未附帶時 MUST 以空值儲存，MUST NOT 由系統推測或填入預設值。

#### Scenario: 附帶合法的指定配送日

- **WHEN** 結帳請求附帶格式為 `YYYY-MM-DD` 的未來日期
- **THEN** 訂單建立成功，訂單詳情回應中的指定配送日與請求相同

#### Scenario: 格式錯誤或不存在的日期被拒

- **WHEN** 結帳請求附帶 `2026/08/05` 或 `2026-02-31`
- **THEN** 回 422，不建立訂單

#### Scenario: 明顯過去的日期被拒

- **WHEN** 結帳請求附帶一個早於 UTC 今日前一天的日期
- **THEN** 回 422，不建立訂單

#### Scenario: 未附帶指定配送日

- **WHEN** 結帳請求未附帶指定配送日
- **THEN** 訂單建立成功，訂單詳情回應中的指定配送日為空值

