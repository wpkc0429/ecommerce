## ADDED Requirements

### Requirement: Cart item carries an optional gift message

購物車品項 MAY 附帶一則贈禮訊息（卡片留言）：一段隨**該品項**（而非整筆訂單）遞送的自由文字。加入品項時 MAY 附帶，未附帶時 MUST 以空字串儲存，MUST NOT 以 null 表示「沒有留言」。

系統 MUST 限制其長度上限為 500 個字元（以字元計，非位元組——同一個上限對中文與英文 MUST 一致）；超過上限 MUST 回 422，MUST NOT 截斷後接受。系統 MUST NOT 將其內容解讀為任何標記語言或格式。

購物車讀取回應的每個品項 MUST 包含此欄位（無留言時為空字串）。

#### Scenario: 加入帶贈禮訊息的品項

- **WHEN** 會員以 `gift_message` 加入一個品項
- **THEN** 加入成功，`GET /api/v1/shop/cart` 回應中該品項的 `gift_message` 與送出值相同

#### Scenario: 未附贈禮訊息的品項

- **WHEN** 會員加入品項時未附 `gift_message`
- **THEN** 加入成功，該品項的 `gift_message` 為空字串

#### Scenario: 超過長度上限被拒

- **WHEN** 會員以超過 500 個字元的 `gift_message` 加入品項
- **THEN** 回 422，購物車內容不受影響，MUST NOT 建立被截斷的品項

#### Scenario: 長度上限以字元計而非位元組

- **WHEN** 會員以 500 個中文字元的 `gift_message` 加入品項
- **THEN** 加入成功（MUST NOT 因其位元組長度超過 500 而被拒）

## MODIFIED Requirements

### Requirement: Add-item accumulation semantics

重複對同一 SKU **且相同贈禮訊息**呼叫加入品項端點時，系統 MUST 將新的 `quantity` 累加到該購物車既有品項的數量上（而非拒絕、也非覆寫），並對累加後的總數量重新套用庫存驗證。

同一 SKU 但**贈禮訊息不同**時，系統 MUST 建立獨立的品項列，MUST NOT 合併數量，MUST NOT 以任一則訊息覆寫另一則。庫存驗證 MUST 以 SKU 為單位，跨該 SKU 的所有品項列加總後比對 `stock_qty`。

#### Scenario: 重複加入同一 SKU 累加數量
- **WHEN** 會員先加入 `quantity = 2` 的某 SKU（未附贈禮訊息），再次加入同一 SKU `quantity = 3`（同樣未附贈禮訊息）
- **THEN** 該品項最終數量為 5（單一品項列，非兩筆）

#### Scenario: 累加後超過庫存被拒
- **WHEN** SKU `stock_qty = 5`，購物車內該 SKU 已有 `quantity = 3`，會員再加入 `quantity = 3`
- **THEN** 回 422，既有品項數量維持 3 不變

#### Scenario: 同一 SKU 不同贈禮訊息成為兩個品項
- **WHEN** 會員先以 `gift_message = "生日快樂"` 加入某 SKU `quantity = 1`，再以 `gift_message = "母親節快樂"` 加入同一 SKU `quantity = 1`
- **THEN** 購物車內出現兩個獨立品項，各自 `quantity = 1` 並保有自己的訊息

#### Scenario: 同一 SKU 分屬不同贈禮訊息的數量合併計入庫存驗證
- **WHEN** SKU `stock_qty = 1`，購物車內該 SKU 已有一個 `quantity = 1` 且附訊息 A 的品項，會員以訊息 B 再加入 `quantity = 1`
- **THEN** 回 422，購物車內容不受影響
