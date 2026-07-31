## MODIFIED Requirements

### Requirement: Order payment success automatically and idempotently awards points

當訂單 `payment_status` 透過 `order.Service.UpdatePaymentStatus` 成功轉為已付款時，系統 MUST 自動核發點數給該訂單所屬的 `members` 記錄：核發點數 = `orders.subtotal_amount`（**品項小計**）除以 100 取整數（無條件捨去），比例為平台統一常數，不因商家而異。

核發基準 MUST 為 `subtotal_amount` 而非 `total_amount`：運費（`orders.shipping_fee`）MUST NOT 產生點數。點數獎勵的是商品購買，MUST NOT 因顧客選擇較貴的配送方式而增加。

核發 MUST 是冪等的——同一筆訂單 MUST NOT 因付款成功的通知重複送達而被核發超過一次。

#### Scenario: 付款成功正確核發點數
- **WHEN** 一筆 `subtotal_amount = 1290`、`shipping_fee = 0` 的訂單付款成功（`payment_status` 變為已付款）
- **THEN** 該訂單所屬會員的 `points` 增加 12（`1290 / 100` 取整數），`point_transactions` 新增一筆對應該訂單的核發記錄

#### Scenario: 運費不計入核發點數
- **WHEN** 一筆 `subtotal_amount = 3600`、`shipping_fee = 200`（`total_amount = 3800`）的訂單付款成功
- **THEN** 核發點數為 36（依 `3600` 計算），MUST NOT 為 38

#### Scenario: 配送方式不影響核發點數
- **WHEN** 兩筆品項完全相同（`subtotal_amount` 相同）的訂單分別選定運費 `200` 與運費 `0` 的運送方式，兩者皆付款成功
- **THEN** 兩筆訂單核發的點數完全相同

#### Scenario: 重複觸發付款成功不重複核發
- **WHEN** 同一筆訂單的付款成功流程因重複投遞（例如 webhook 重送）被觸發第二次
- **THEN** 該會員的 `points` 不因第二次觸發而再次增加，`point_transactions` 內該訂單只存在一筆核發記錄

#### Scenario: 零金額訂單核發零點數但仍留下核發記錄
- **WHEN** 一筆 `subtotal_amount` 小於 100 最小貨幣單位的訂單付款成功
- **THEN** 核發點數為 0，`members.points` 不變，但 `point_transactions` 仍留下一筆該訂單的核發記錄，防止後續重複觸發被誤判為尚未處理

### Requirement: Order return claws back previously awarded points, capped at the current balance

訂單 `fulfillment_status` 透過 `order.Service.UpdateFulfillmentStatus` 轉為已退貨（`3`）時，系統 MUST 嘗試扣回該訂單先前核發的點數：扣回量 = `min(該訂單原本核發的點數, 會員目前的 points)`，MUST NOT 造成 `members.points` 變為負數。扣回的基準 MUST 為該訂單實際核發過的點數（即以品項小計計算的值），MUST NOT 重新以任何金額欄位推算——因此運費既未產生點數，退貨時也 MUST NOT 被扣回。

扣回 MUST 是冪等的——同一筆訂單的退貨轉換 MUST NOT 造成扣回被重複執行。訂單若從未核發過點數（例如訂單從未付款即被出貨後退貨），扣回量為 0，`members.points` 不變。

#### Scenario: 已核發點數的訂單退貨後扣回等量點數
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，且會員目前餘額 >= 12
- **THEN** 該會員 `points` 減少 12，`point_transactions` 新增一筆該訂單的扣回記錄

#### Scenario: 含運費的訂單退貨只扣回商品所核發的點數
- **WHEN** 一筆 `subtotal_amount = 3600`、`shipping_fee = 200` 且已核發 36 點的訂單轉為已退貨
- **THEN** 扣回 36 點（MUST NOT 為 38 點）

#### Scenario: 扣回量不超過會員目前餘額
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，但會員目前餘額因其他異動已降為 5
- **THEN** 該會員 `points` 最多扣回至 0（扣回 5 點，而非 12 點），MUST NOT 變為負數

#### Scenario: 重複觸發退貨轉換不重複扣回
- **WHEN** 同一筆訂單的已退貨轉換因重複觸發被處理第二次
- **THEN** 該會員的 `points` 不因第二次觸發而重複扣回
