## MODIFIED Requirements

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
