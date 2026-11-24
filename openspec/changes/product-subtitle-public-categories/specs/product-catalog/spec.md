## ADDED Requirements

### Requirement: 商品副標欄位

商品 MUST 具有 `subtitle` 字串欄位（上限 200 rune，預設空字串）。後台建立與更新商品時 MAY 提供 `subtitle`；更新時未提供 MUST 保持原值。後台與公開的商品回應 MUST 包含 `subtitle`。

#### Scenario: 建立含副標的商品
- **WHEN** 具 `product.create` 者建立商品並提供 `"subtitle": "Seasonal Bouquet"`
- **THEN** 回應 201，回應與之後的公開詳情皆含 `"subtitle": "Seasonal Bouquet"`

#### Scenario: 更新時未帶副標不覆蓋
- **WHEN** 更新商品的請求未包含 `subtitle`
- **THEN** 原有 `subtitle` 不變

#### Scenario: 既有商品的副標為空字串
- **WHEN** 讀取本變更前建立的商品
- **THEN** `subtitle` 為 `""`

### Requirement: 公開分類端點

系統 SHALL 提供無需認證的 `GET /api/v1/shop/categories`，依前台租戶解析規則（publishable key 優先，否則 Host）回傳該商家的完整分類樹；每個節點含 `id`、`name`、`slug`、`parent_id`、`position`、`children`，同層 MUST 依 `position` 遞增、再依 `id` 遞增排序。MUST NOT 回傳其他商家的分類。

#### Scenario: 回傳本店分類樹
- **WHEN** shop A 有分類「花束」及其子分類「玫瑰」，前台以 shop A 的 Host 請求分類端點
- **THEN** 回應含「花束」節點，其 `children` 含「玫瑰」

#### Scenario: 不跨店
- **WHEN** shop B 有分類「蛋糕」，前台以 shop A 的 Host 請求分類端點
- **THEN** 回應不含「蛋糕」

### Requirement: 公開商品回應含分類摘要

公開商品列表與詳情的每個商品 MUST 額外包含 `categories` 陣列，元素為 `{id, slug, name}`，依分類 `position`、`id` 遞增排序；既有 `category_ids` MUST 保留且與 `categories` 的 id 集合相同。

#### Scenario: 商品帶分類名稱與 slug
- **WHEN** 已發佈商品屬於分類「本季新品」（slug `new-arrivals`），前台請求該商品詳情
- **THEN** 回應 `categories` 含 `{"slug": "new-arrivals", "name": "本季新品", ...}`，`category_ids` 含同一 id

### Requirement: 公開商品列表可依分類篩選（含子分類）

`GET /api/v1/shop/products` MUST 接受選填查詢參數 `category`（分類 slug）；提供時 MUST 回傳關聯於該分類**或其任一子孫分類**的已發佈商品，每個商品至多出現一次，分頁與 `total` 依篩選後集合計算。排序 MUST 為：先列直接關聯於所請求分類的商品（依該關聯的 `position` 遞增、再依商品 `id` 遞增），再列僅經由子孫分類關聯的商品（依 `id` 遞增）。slug 不存在於本商家時 MUST 回 200 與空列表。未提供 `category` 時行為 MUST 與本變更前相同。

#### Scenario: 依分類篩選
- **WHEN** 「本季新品」分類有 2 個已發佈商品與 1 個草稿商品，前台請求 `?category=new-arrivals`
- **THEN** 回應含 2 個商品，`total` 為 2

#### Scenario: 包含子分類且不重複
- **WHEN** 「花束」有子分類「玫瑰」；商品 P1 直接屬於「花束」、P2 只屬於「玫瑰」、P3 同時屬於兩者，前台請求 `?category=bouquets`
- **THEN** 回應恰含 P1、P2、P3 各一次，且 P1、P3（直接成員）排在 P2 之前

#### Scenario: 不存在的分類
- **WHEN** 前台請求 `?category=no-such-slug`
- **THEN** 回 200，`products` 為空陣列，`total` 為 0

#### Scenario: 他店分類 slug 不生效
- **WHEN** shop B 有 slug `cakes` 的分類，前台以 shop A 請求 `?category=cakes`
- **THEN** 回 200 與空列表

### Requirement: 分類內的商品可手動排序

商品與分類的每筆關聯 MUST 具有 `position`（非負整數）。系統 SHALL 提供 `GET /api/v1/admin/shops/{shopID}/categories/{categoryID}/product-order`（`category.view`，回傳直接關聯商品依 `position`、`product_id` 遞增）與 `PUT` 同路徑（`category.edit`，`{product_ids: [...]}`）。`PUT` 的 `product_ids` MUST 恰好是該分類目前直接關聯商品的一個排列，否則 MUST 回 422 且不做任何變更。新建立的關聯 MUST 排在該分類既有關聯之後。商品更新時，`category_ids` 中保留下來的分類，其關聯的 `position` MUST 不變。

#### Scenario: 手動排序反映於公開列表
- **WHEN** 商家把「本季新品」的商品順序設為 [P3, P1, P2]
- **THEN** `GET /shop/products?category=new-arrivals` 依序回傳 P3、P1、P2（皆為已發佈時）

#### Scenario: 不完整排列被拒
- **WHEN** 分類有直接成員 [P1, P2, P3]，送出 `{"product_ids": [P1, P2]}`
- **THEN** 回 422，順序不變

#### Scenario: 更新商品不重設排序
- **WHEN** P3 在「本季新品」排第一，商家更新 P3 的標題並送出相同的 `category_ids`
- **THEN** P3 在「本季新品」仍排第一
