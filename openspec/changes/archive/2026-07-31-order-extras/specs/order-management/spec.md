## ADDED Requirements

### Requirement: Order amounts are composed of an item subtotal and a shipping fee

訂單 MUST 記錄三個獨立的金額欄位，皆為 `int64`／`BIGINT` 整數最小貨幣單位（沿用 spec product-catalog 的金額表示法，MUST NOT 使用浮點數），並共用訂單自身的 `currency`：

- `subtotal_amount`：品項小計，等於 Σ(`order_items.price_amount` × `quantity`)。
- `shipping_fee`：本筆訂單的運費快照，未選定運送方式時 MUST 為 `0`。
- `total_amount`：**應付總額**，MUST 恆等於 `subtotal_amount + shipping_fee`。

此組成關係 MUST 由資料庫層的約束強制，MUST NOT 僅依賴應用層計算。三者皆 MUST >= 0。任何向金流請款的金額 MUST 使用 `total_amount`，MUST NOT 使用 `subtotal_amount`。

金額 MUST 於結帳當下一次算定並永久快照，MUST NOT 因商品改價、運送方式改價或運送方式被刪除而重新計算。

#### Scenario: 未選定運送方式的訂單

- **WHEN** 結帳請求未附帶運送方式
- **THEN** 訂單 `shipping_fee = 0`、`subtotal_amount` 等於品項小計、`total_amount` 等於 `subtotal_amount`

#### Scenario: 選定運送方式的訂單其應付總額含運費

- **WHEN** 品項小計為 `3600`、選定的運送方式固定運費為 `200`
- **THEN** 訂單 `subtotal_amount = 3600`、`shipping_fee = 200`、`total_amount = 3800`

#### Scenario: 金額組成不可被破壞

- **WHEN** 任何路徑嘗試建立或寫入一筆 `total_amount != subtotal_amount + shipping_fee` 的訂單
- **THEN** 寫入 MUST 失敗（資料庫約束拒絕），MUST NOT 產生不一致的訂單

#### Scenario: 運費快照不因運送方式改價而變動

- **WHEN** 訂單建立後，其所選運送方式的固定運費被商家調整或該運送方式被刪除
- **THEN** 該訂單的 `shipping_fee` 與 `total_amount` 完全不變

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

## MODIFIED Requirements

### Requirement: Checkout converts an active cart into an order

會員對自己的 active 購物車發起結帳（`POST /api/v1/shop/checkout`）時，系統 MUST 在單一資料庫交易內完成：驗證每個購物車品項可購買、對每個品項的 SKU 做原子扣庫存、建立訂單與訂單品項、將來源購物車 `status` 更新為 `1`（converted）。任一步驟失敗，整筆交易 MUST 回滾（不建立訂單、不變更庫存、不變更購物車狀態）。

結帳請求 MAY 附帶運送方式與指定配送日；這兩者的驗證 MUST 在交易開始之前完成，任一驗證失敗 MUST 回 422 且 MUST NOT 觸及庫存或購物車狀態。訂單的三個金額欄位（`subtotal_amount`／`shipping_fee`／`total_amount`）MUST 於同一交易內算定並寫入。

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

### Requirement: 所有下單路徑共用單一結帳實作

系統中任何建立訂單的路徑（會員結帳、訪客結帳，以及未來任何新增的下單入口）MUST 呼叫同一個結帳服務方法完成扣庫存、建單與金額組成。系統 MUST NOT 存在第二段實作條件式扣庫存的程式碼，也 MUST NOT 存在第二段自行計算 `subtotal_amount`／`shipping_fee`／`total_amount` 的程式碼。

新增下單入口時，其與既有入口的差異 MUST 僅止於「如何取得顧客身分」與「如何取得品項與訂單層附加輸入」，MUST NOT 觸及庫存扣減、訂單品項快照、金額組成或購物車狀態轉換的邏輯。

#### Scenario: 不同下單入口共用同一份庫存保護

- **WHEN** 兩個不同的下單入口併發搶購同一個庫存有限的 SKU
- **THEN** 成功總數不超過扣減前的庫存量，`product_skus.stock_qty` MUST NOT 為負數

#### Scenario: 新入口不重複實作扣庫存

- **WHEN** 檢視任一下單入口的實作
- **THEN** 其扣庫存與建單皆經由既有結帳服務方法完成，該入口自身 MUST NOT 直接寫入 `product_skus.stock_qty` 或建立 `orders`／`order_items`

#### Scenario: 不同下單入口對同一輸入產生相同的金額組成

- **WHEN** 會員結帳與訪客結帳以相同的品項與相同的運送方式各建立一筆訂單
- **THEN** 兩筆訂單的 `subtotal_amount`／`shipping_fee`／`total_amount` 完全相同
