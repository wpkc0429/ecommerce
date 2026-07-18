# product-catalog Specification

## Purpose
TBD - created by archiving change product-catalog. Update Purpose after archive.
## Requirements
### Requirement: Shop-scoped category tree
分類（`categories`）MUST 屬於單一 shop，`name` 與 `slug` MUST 於同一 shop 內唯一；`slug` MUST 符合 `^[a-z0-9-]+$`（儲存前小寫正規化）。分類 MAY 透過 `parent_id` 指向同一 shop 內的另一分類，形成樹狀結構；`parent_id` 為 NULL 代表根分類。

#### Scenario: 建立根分類
- **WHEN** 於 shop A 建立 `name = "男鞋"`、`slug = "mens-shoes"`、無 `parent_id` 的分類
- **THEN** 分類建立成功，視為根分類

#### Scenario: 建立子分類
- **WHEN** 於 shop A 建立 `parent_id` 指向既有分類「男鞋」的子分類「跑鞋」
- **THEN** 分類建立成功，查詢分類樹時「跑鞋」出現在「男鞋」底下

#### Scenario: 同店名稱重複被拒
- **WHEN** 於 shop A 建立與既有分類相同 `name` 的分類
- **THEN** 回 422（不同商家間相同名稱允許）

#### Scenario: 同店 slug 重複被拒
- **WHEN** 於 shop A 建立與既有分類相同 `slug` 的分類
- **THEN** 回 422（不同商家間相同 slug 允許）

### Requirement: Category hierarchy cycle prevention
建立或更新分類的 `parent_id` 時，系統 MUST 拒絕會造成環狀（分類成為自己的祖先）的指派，回 422。

#### Scenario: 直接自我參考被拒
- **WHEN** 更新分類 A 的 `parent_id` 為 A 自己的 id
- **THEN** 回 422

#### Scenario: 間接環狀被拒
- **WHEN** 分類樹為 A → B → C（C 的 parent 是 B，B 的 parent 是 A），嘗試更新 A 的 `parent_id` 為 C
- **THEN** 回 422（此舉會形成 A → C → B → A 的環）

### Requirement: Category deletion guard
分類刪除 MUST 採 RESTRICT 語意：若該分類仍有子分類（`parent_id` 指向它），或仍有商品透過 `product_category` 掛在它底下，刪除 MUST 回 409 並拒絕刪除；僅當分類無子分類且無掛載商品時刪除才成功。

#### Scenario: 刪除有子分類的分類被拒
- **WHEN** 嘗試刪除分類「男鞋」，其底下仍有子分類「跑鞋」
- **THEN** 回 409，分類未被刪除

#### Scenario: 刪除有商品掛載的分類被拒
- **WHEN** 嘗試刪除仍有商品關聯的分類
- **THEN** 回 409，分類未被刪除

#### Scenario: 刪除空分類成功
- **WHEN** 刪除無子分類、無商品掛載的分類
- **THEN** 分類刪除成功

### Requirement: Product creation and identity
商品（`products`）MUST 屬於單一 shop，`slug` MUST 於同一 shop 內唯一且符合 `^[a-z0-9-]+$`（儲存前小寫正規化）；商品建立時 `status` MUST 預設為 0（草稿）。

#### Scenario: 建立商品成功
- **WHEN** 於 shop A 建立 `title = "經典跑鞋"`、`slug = "classic-runner"` 的商品
- **THEN** 商品以 `status = 0`（草稿）建立

#### Scenario: 同店 slug 重複被拒
- **WHEN** 於 shop A 建立與既有商品相同 slug 的商品
- **THEN** 回 422（不同商家間相同 slug 允許）

### Requirement: Product draft/publish status
商品 `status` MUST 僅為 0（草稿）或 1（已發佈）；商家 MUST 能透過更新操作在兩態間切換。此狀態語意獨立於分類/SKU 資料是否完整——商家可先建立草稿商品、逐步補齊 SKU 後再發佈。

#### Scenario: 草稿轉已發佈
- **WHEN** 更新商品 `status` 從 0 改為 1
- **THEN** 商品 `status = 1`

#### Scenario: 非法 status 值被拒
- **WHEN** 更新商品 `status` 為 2 或其他非 0/1 的值
- **THEN** 回 422

### Requirement: Product-category many-to-many association
商品與分類 MUST 為多對多關聯：一個商品 MAY 屬於零個、一個或多個分類；一個分類 MAY 掛有零個或多個商品。關聯 MUST 透過商品建立/更新時傳入的分類 id 集合維護（全量取代語意：更新後的集合即最終狀態）。

#### Scenario: 商品掛多個分類
- **WHEN** 建立商品時傳入分類 id 集合 `[男鞋, 新品上市]`
- **THEN** 該商品同時出現在「男鞋」與「新品上市」的商品列表查詢中

#### Scenario: 更新分類集合為全量取代
- **WHEN** 商品原掛 `[男鞋, 新品上市]`，更新時傳入 `[男鞋]`
- **THEN** 商品僅保留與「男鞋」的關聯，「新品上市」的關聯被移除

### Requirement: Product SKU nested management
每個商品 MAY 有多個 SKU；SKU MUST 於商品建立/更新請求中以巢狀陣列傳入（不提供獨立於商品 CRUD 之外的 SKU 端點）。更新時 MUST 支援 upsert 語意：帶 id 的項目更新既有 SKU，不帶 id 的項目新增 SKU，請求未包含的既有 SKU id MUST 被移除。`sku_code` MUST 於同一 shop 內唯一。

#### Scenario: 建立商品時一併建立多個 SKU
- **WHEN** 建立商品並帶入兩個 SKU（`sku_code = "SHOE-M-RED"`、`sku_code = "SHOE-L-RED"`）
- **THEN** 商品與兩個 SKU 一併建立成功，皆可查詢

#### Scenario: 更新時新增與移除 SKU
- **WHEN** 商品原有 SKU `[A, B]`，更新請求帶入 `[A（更新其庫存）, C（新 SKU）]`
- **THEN** SKU A 更新、SKU C 新增、SKU B 被移除

#### Scenario: 同店 sku_code 重複被拒
- **WHEN** 新增的 SKU `sku_code` 與同一 shop 內既有 SKU 重複（不論是否同一商品）
- **THEN** 回 422（不同商家間相同 sku_code 允許）

### Requirement: SKU price stored as integer minor units
SKU 的價格 MUST 儲存為整數最小貨幣單位（`price_amount`，資料庫型別 `BIGINT`），並搭配 ISO 4217 幣別代碼欄位（`currency`）；系統 MUST NOT 以浮點數或非整數型別儲存金額。往返讀寫 MUST 精確保留輸入值，不因型別轉換產生誤差。

#### Scenario: 大額整數往返精確
- **WHEN** 建立 SKU 時 `price_amount = 999999999`
- **THEN** 讀取該 SKU 時 `price_amount` 精確等於 `999999999`，無精度損失

#### Scenario: 負數價格被拒
- **WHEN** 建立或更新 SKU 時 `price_amount` 為負數
- **THEN** 回 422

### Requirement: SKU single-quantity inventory
SKU 的庫存 MUST 以單一非負整數（`stock_qty`）表示可售數量；本階段 MUST NOT 實作多倉儲拆分或預留鎖定邏輯。

#### Scenario: 建立 SKU 帶初始庫存
- **WHEN** 建立 SKU 時 `stock_qty = 50`
- **THEN** 讀取該 SKU 時 `stock_qty = 50`

#### Scenario: 負數庫存被拒
- **WHEN** 建立或更新 SKU 時 `stock_qty` 為負數
- **THEN** 回 422

### Requirement: Merchant-scoped catalog management authorization
分類與商品（含其 SKU）的管理操作 MUST 依既有三層 RBAC 判定（spec rbac）：檢視/建立/編輯需 `category.view`/`category.create`/`category.edit` 或 `product.view`/`product.create`/`product.edit`；刪除需 `category.delete`/`product.delete`。跨商家操作 MUST 依既有 cross-shop access guard 回 403。

#### Scenario: merchant_owner 可完整管理
- **WHEN** 持有 shop A `merchant_owner` 角色的使用者對 shop A 執行分類/商品的建立、編輯、刪除
- **THEN** 皆被允許

#### Scenario: editor 不可刪除
- **WHEN** 持有 shop A `editor` 角色（無 `category.delete`/`product.delete`）的使用者嘗試刪除 shop A 的分類或商品
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的分類/商品管理 API
- **THEN** 回 403

### Requirement: Product listing pagination and filters
商品列表 API MUST 支援分頁（`page`/`page_size`），MAY 依 `category_id` 篩選（僅回傳該分類下商品）與依 `status` 篩選（草稿/已發佈）。

#### Scenario: 分頁列表
- **WHEN** shop A 有 30 個商品，以 `page=1&page_size=10` 查詢
- **THEN** 回傳 10 筆商品與 `total = 30`

#### Scenario: 依分類篩選
- **WHEN** 以 `category_id` 查詢屬於「男鞋」分類的商品
- **THEN** 僅回傳掛有該分類的商品

#### Scenario: 依狀態篩選
- **WHEN** 以 `status=1` 查詢已發佈商品
- **THEN** 僅回傳 `status = 1` 的商品，不含草稿

### Requirement: Tenant data isolation for catalog tables
`categories`、`products`、`product_skus`、`product_category` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop，不因用戶端傳入的 shop 識別值而變更範圍。

#### Scenario: 跨店查詢看不到彼此資料
- **WHEN** shop A 的管理者查詢自己商家的商品列表
- **THEN** 回應不含 shop B 的任何商品或分類

### Requirement: Published-only public catalog endpoint
系統 SHALL 提供無需認證的公開端點，依請求解析出的 shop 上下文列出已發佈商品（`status = 1`）與取得單一已發佈商品詳情（含其 SKU 列表）；草稿商品（`status = 0`）在此端點 MUST 回 404。

#### Scenario: 公開端點僅列出已發佈商品
- **WHEN** shop A 有 3 個已發佈商品與 2 個草稿商品，前台請求商品列表
- **THEN** 回應僅含 3 個已發佈商品

#### Scenario: 草稿商品詳情不可公開存取
- **WHEN** 前台請求草稿商品的 slug 詳情
- **THEN** 回 404

#### Scenario: 已發佈商品詳情含 SKU
- **WHEN** 前台請求已發佈商品的詳情
- **THEN** 回應含該商品的 SKU 列表（含 `price_amount`、`currency`、`stock_qty`、`options`）

