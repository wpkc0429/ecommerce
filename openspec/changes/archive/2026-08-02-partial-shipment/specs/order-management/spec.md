## MODIFIED Requirements

### Requirement: Order line items are denormalized snapshots

訂單品項（`order_items`）建立時 MUST 複製當下的商品標題、SKU code 等顯示用文字為自身欄位（不僅存商品/SKU 的外鍵），價格與幣別快照 MUST 直接沿用來源購物車品項已經固化的 `price_amount`/`currency`（不重新查詢 SKU 現價）。來源購物車品項附帶的贈禮訊息（卡片留言）MUST 一併複製為訂單品項自身的欄位。訂單品項建立後，其顯示內容 MUST NOT 因商品被刪除、改名，SKU 被刪除、改價，或來源購物車被清空而改變。

訂單品項上**唯一**可在建立後變動的欄位是「已出貨數量」（spec shipping-logistics）。它不是快照而是狀態，MUST 只由出貨路徑在其自身交易內寫入，MUST NOT 由結帳、取消或任何後台端點寫入。其餘每一個欄位 MUST 維持不可變。

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

#### Scenario: 出貨只改變已出貨數量，不改變任何快照欄位
- **WHEN** 一筆訂單品項被部分出貨
- **THEN** 該品項的已出貨數量增加，其商品標題、SKU code、單價、幣別、訂購數量與贈禮訊息 MUST 全部不變

### Requirement: Order cancellation requires all three axes at their initial value, and restores stock

訂單取消（`POST .../orders/{id}/cancel`）僅在該訂單 `status = 0`（已建立）且 `payment_status = 0`（未付款）且 `fulfillment_status = 0`（未出貨）時允許；任一軸已偏離初始值，取消請求 MUST 被拒絕（409），訂單狀態與庫存 MUST NOT 變動。取消成功時，系統 MUST 在同一資料庫交易內將該訂單 `status` 更新為 `1`（已取消），並將該訂單每個仍可定位到存活 SKU 的品項數量歸還至該 SKU 的 `stock_qty`。

已有任何出貨（含只出了一部分）的訂單，其 `fulfillment_status` MUST NOT 為 `0`，因此依上述規則 MUST 被拒絕取消。這是必要的：取消所歸還的是每個品項的**訂購數量**而非未出貨數量，若允許取消一筆已部分出貨的訂單，已經離開商家的商品會在庫存上被重複計入。

#### Scenario: 取消未付款未出貨的訂單成功並歸還庫存
- **WHEN** 一筆 `payment_status = 0`、`fulfillment_status = 0` 的訂單被取消
- **THEN** 訂單 `status` 變為 `1`，其每個品項對應 SKU（若仍存在）的 `stock_qty` 增加該品項的訂購數量

#### Scenario: 已取消訂單不可重複取消
- **WHEN** 對一筆 `status = 1`（已取消）的訂單再次呼叫取消
- **THEN** 回 409，訂單狀態與庫存不再變動

#### Scenario: 部分出貨的訂單不可取消
- **WHEN** 對一筆只出了部分品項（`fulfillment_status = 4`）的訂單呼叫取消
- **THEN** 回 409，訂單狀態、已出貨數量與庫存皆不變動
