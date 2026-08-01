## MODIFIED Requirements

### Requirement: 所有下單路徑共用單一結帳實作

系統中任何建立訂單的路徑（會員結帳、訪客結帳，以及未來任何新增的下單入口）MUST 呼叫同一個結帳服務方法完成扣庫存、建單與金額組成。系統 MUST NOT 存在第二段實作條件式扣庫存的程式碼，也 MUST NOT 存在第二段自行計算 `subtotal_amount`／`discount_amount`／`shipping_fee`／`total_amount` 的程式碼。

**重複送出的冪等保護亦 MUST 只有一份實作，且 MUST 對所有下單路徑一致適用**（spec checkout-idempotency）。系統 MUST NOT 讓某一條下單路徑擁有自己的一套冪等語意，也 MUST NOT 讓某一條下單路徑成為繞過冪等保護的捷徑。

**等級折扣的解析與計算亦 MUST 只有一份實作，且 MUST 落在該同一個結帳服務方法內**（spec tier-discount-application）。任何下單入口 MUST NOT 自行決定折扣率、自行計算折抵金額，也 MUST NOT 接受由請求主體指定的折扣率或折抵金額。

**「訂單已成立」領域事件的發佈亦 MUST 只有一處，且 MUST 落在該同一個結帳服務方法內**（spec order-notification-email）。發佈 MUST 發生在建單交易成功提交**之後**，且 MUST 對每一張成功建立的訂單恰好發佈一次。任何未建立訂單的路徑——輸入驗證失敗、庫存不足、購物車狀態衝突、冪等鍵回放——MUST NOT 發佈該事件。任何下單入口 MUST NOT 自行發佈該事件，因為那會使「一張訂單一個事件」退化為各入口自行遵守的紀律，而非結構性質。

新增下單入口時，其與既有入口的差異 MUST 僅止於「如何取得顧客身分」與「如何取得品項與訂單層附加輸入」，MUST NOT 觸及庫存扣減、訂單品項快照、金額組成、折扣套用、購物車狀態轉換、冪等鍵定案或訂單成立事件發佈的邏輯。

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

#### Scenario: 每一張建立出來的訂單恰好發佈一個訂單成立事件

- **WHEN** 任一下單入口成功建立一筆訂單
- **THEN** 系統恰好發佈一次「訂單已成立」領域事件，且發佈時該訂單已可被其他連線讀取

#### Scenario: 未建立訂單的路徑不發佈訂單成立事件

- **WHEN** 一次結帳請求因輸入驗證失敗、庫存不足或購物車狀態衝突而未建立訂單
- **THEN** 系統 MUST NOT 發佈「訂單已成立」領域事件

#### Scenario: 冪等回放不發佈第二個訂單成立事件

- **WHEN** 一次帶冪等鍵的結帳請求以同一把鍵重送並取得回放回應
- **THEN** 系統 MUST NOT 發佈第二次「訂單已成立」領域事件
