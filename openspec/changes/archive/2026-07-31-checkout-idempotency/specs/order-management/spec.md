## MODIFIED Requirements

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

### Requirement: 所有下單路徑共用單一結帳實作

系統中任何建立訂單的路徑（會員結帳、訪客結帳，以及未來任何新增的下單入口）MUST 呼叫同一個結帳服務方法完成扣庫存、建單與金額組成。系統 MUST NOT 存在第二段實作條件式扣庫存的程式碼，也 MUST NOT 存在第二段自行計算 `subtotal_amount`／`shipping_fee`／`total_amount` 的程式碼。

**重複送出的冪等保護亦 MUST 只有一份實作，且 MUST 對所有下單路徑一致適用**（spec checkout-idempotency）。系統 MUST NOT 讓某一條下單路徑擁有自己的一套冪等語意，也 MUST NOT 讓某一條下單路徑成為繞過冪等保護的捷徑。

新增下單入口時，其與既有入口的差異 MUST 僅止於「如何取得顧客身分」與「如何取得品項與訂單層附加輸入」，MUST NOT 觸及庫存扣減、訂單品項快照、金額組成、購物車狀態轉換或冪等鍵定案的邏輯。

#### Scenario: 不同下單入口共用同一份庫存保護

- **WHEN** 兩個不同的下單入口併發搶購同一個庫存有限的 SKU
- **THEN** 成功總數不超過扣減前的庫存量，`product_skus.stock_qty` MUST NOT 為負數

#### Scenario: 新入口不重複實作扣庫存

- **WHEN** 檢視任一下單入口的實作
- **THEN** 其扣庫存與建單皆經由既有結帳服務方法完成，該入口自身 MUST NOT 直接寫入 `product_skus.stock_qty` 或建立 `orders`／`order_items`

#### Scenario: 不同下單入口對同一輸入產生相同的金額組成

- **WHEN** 會員結帳與訪客結帳以相同的品項與相同的運送方式各建立一筆訂單
- **THEN** 兩筆訂單的 `subtotal_amount`／`shipping_fee`／`total_amount` 完全相同

#### Scenario: 不同下單入口的冪等語意一致

- **WHEN** 分別對會員結帳與訪客結帳送出帶相同形式冪等鍵的重複請求
- **THEN** 兩者的回放語意、錯誤碼與狀態碼完全一致，且皆 MUST NOT 建立第二筆訂單
