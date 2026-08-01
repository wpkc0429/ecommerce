## MODIFIED Requirements

### Requirement: Product listing pagination and filters
商品列表 API MUST 支援分頁（`page`/`page_size`），MAY 依 `category_id` 篩選（僅回傳該分類下商品）與依 `status` 篩選（草稿/已發佈）。

商品列表 API MAY 額外接受一個**選填**的關鍵字參數 `q`。提供時，MUST 以大小寫不敏感的子字串比對，回傳商品標題、slug 或其任一 SKU 的 `sku_code` 命中該關鍵字的商品；比對 MUST 在該商家的**整個目錄**上進行，而非僅限當頁。`q` 與 `category_id`／`status` 同時提供時 MUST 一併滿足。未提供 `q` 時，端點行為 MUST 與本參數引入前完全相同。

#### Scenario: 分頁列表
- **WHEN** shop A 有 30 個商品，以 `page=1&page_size=10` 查詢
- **THEN** 回傳 10 筆商品與 `total = 30`

#### Scenario: 依分類篩選
- **WHEN** 以 `category_id` 查詢屬於「男鞋」分類的商品
- **THEN** 僅回傳掛有該分類的商品

#### Scenario: 依狀態篩選
- **WHEN** 以 `status=1` 查詢已發佈商品
- **THEN** 僅回傳 `status = 1` 的商品，不含草稿

#### Scenario: 依關鍵字搜尋標題
- **WHEN** 以 `q` 帶入某商品標題的一段子字串（大小寫不同）查詢
- **THEN** 該商品出現在結果中，`total` 反映符合條件的總數而非全目錄總數

#### Scenario: 依關鍵字搜尋 SKU 代碼
- **WHEN** 以 `q` 帶入某 SKU 的 `sku_code` 查詢
- **THEN** 擁有該 SKU 的商品出現在結果中

#### Scenario: 關鍵字與其他篩選並用
- **WHEN** 同時以 `q` 與 `status=0` 查詢
- **THEN** 僅回傳同時命中關鍵字且為草稿的商品

#### Scenario: 未帶關鍵字時行為不變
- **WHEN** 不提供 `q` 查詢商品列表
- **THEN** 結果與本參數引入前完全相同
