## Why

花藝類商家（第一個是 ONDO 形狀的店）的商品卡片是「中文品名 ＋ 一行英文副標」，今天商品只有 `title`／`description`，副標只能塞進 `meta` 這個無 schema 的 jsonb，前台與後台都沒有它的位置。另外前台需要依分類呈現商品（含「本季新品」區塊），但公開 API 今天：

- 商品回應只有 `category_ids`（整數），沒有名稱與 slug，前台無法顯示「所屬分類」；
- **沒有公開的分類端點**——分類樹只在後台 `GET /admin/shops/{shopID}/categories`；
- 公開商品列表 `GET /api/v1/shop/products` 只接受 `page`／`page_size`，無法依分類篩選。

後續 phase 的靜態店面會在建置時呼叫這些公開端點產生分類頁，所以它們現在就要以穩定的 v1 形狀存在。

## What Changes

- `products` 新增 `subtitle`（`varchar(200)`，NOT NULL DEFAULT `''`，零回填）。後台商品建立／更新接受 `subtitle`；後台與公開回應皆回傳它。
- 公開商品回應（列表與詳情）additive 新增 `categories: [{id, slug, name}]`（依 `position`、`id` 排序）；既有 `category_ids` 保留不變。
- 新增公開端點 `GET /api/v1/shop/categories`：回傳該商家的分類樹（`id`／`name`／`slug`／`parent_id`／`position`／`children`），租戶解析規則同 `/shop/products`（publishable key 優先，否則 Host），無需認證。
- 公開商品列表新增選填查詢參數 `category`（分類 slug）：回傳屬於該分類**或其子孫分類**的已發佈商品；直接成員依商家手排順序在前，子分類的商品依 id 在後；slug 不存在時回 200 ＋ 空列表（與「不帶篩選」一樣是集合查詢，不是資源查找）。
- **分類內的商品手動排序**：`product_category` 新增 `position`；新增 `GET`／`PUT /api/v1/admin/shops/{shopID}/categories/{categoryID}/product-order`；商品 PUT 對 `category_ids` 改為差集更新，保留下來的關聯不會被重設順序。
- **「本季新品」以分類表達，不新增旗標欄位**（理由見 design D3）；demo seed 新增一個 `new-arrivals`／「本季新品」分類並掛上兩個商品。
- 後台商品編輯器新增「副標」欄位（例如英文品名）；分類視圖新增「商品排序」。

## Capabilities

### New Capabilities
（無）

### Modified Capabilities
- `product-catalog`：商品新增 `subtitle`；新增公開分類端點；公開商品回應含分類摘要；公開列表可依分類 slug 篩選。
- `admin-catalog-ui`：商品編輯器提供副標欄位。
- `demo-seed`：demo 目錄含「本季新品」分類。

## Impact

- **資料**：一個 migration（`products.subtitle`、`product_category.position`）。
- **程式碼**：`api/internal/ent/schema/catalog.go`、`api/internal/catalog/service.go`（Input/Update/PublicListParams、列表查詢）、`api/internal/httpapi/products.go`、`api/internal/httpapi/categories.go`（新 `MountPublic`）、`api/internal/httpapi/router.go`、`api/internal/seed/seed.go`、`admin/views/products.js`、`admin/views/categories.js`。
- **API 契約**：`openapi.yaml` 新增 3 條路由、1 個查詢參數、`Product.subtitle`、`Product.categories`、`CategorySummary` schema；`v1-surface.txt` 只新增行。
- **相依**：無。與 `product-images-r2` 都會改 `productJSON` 與 `Product` schema，建議本 change 先上線以減少衝突。
