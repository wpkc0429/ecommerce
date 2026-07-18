## Why

Phase 1 交付了多租戶基礎、shop 範圍化 RBAC 與 CMS 引擎，但完全沒有商品資料模型——電商核心（購物車、訂單、金流、物流、會員點數）皆需要「商品」與「SKU」作為地基。本 change 是 Phase 2 電商核心的第一個 proposal，優先建立商品目錄的資料層與管理 API，讓後續 5 個 proposal（shopping-cart、order-management、payment-integration、shipping-logistics、member-tiers-and-points）有穩定的 products/skus 表與金額表示法慣例可以直接依賴，避免各自另訂、互不相容。

## What Changes

- 新增商品分類（`categories`）：shop 內樹狀結構（`parent_id` 自參考），名稱與 slug 於 shop 內唯一。
- 新增商品（`products`）：標題、slug（shop 內唯一、SEO 友善格式）、描述、草稿/發佈狀態（語意比照 `pages`）、SEO meta；與分類為多對多關聯（`product_category` join table）。
- 新增商品 SKU（`product_skus`）：每個商品可有多個 SKU，各自持有 `sku_code`（shop 內唯一）、選項值（JSON 物件，如 `{"size":"M"}`）、價格（`price_amount BIGINT` 最小貨幣單位整數 + `currency` ISO 4217 代碼）、單一數字庫存量（`stock_qty`，不做多倉/預留鎖定）、可停用旗標（`is_active`）。
- **金額表示法定案**：所有金額欄位一律 `BIGINT` 儲存最小貨幣單位整數（零小數貨幣如 TWD 直接代表「元」；未來若需支援有小數貨幣則代表「分」），不使用浮點數或 `numeric`。此慣例為後續 cart/order/payment 的強制契約。
- 新增租戶隔離註冊（`api/internal/tenant`）：`categories`、`products`、`product_skus`、`product_category` 均掛上既有的 shop_id 強制隔離機制。
- 新增 RBAC 權限節點：`category.view/create/edit/delete`、`product.view/create/edit/delete`（沿用 `page.*` 命名慣例），指派給 `merchant_owner`（全權限）與 `editor`（缺 delete）。
- 新增商家範圍管理 API（`/api/v1/admin/shops/{shopID}/categories`、`/api/v1/admin/shops/{shopID}/products`）：分類 CRUD、商品 CRUD（含 SKU 巢狀建立/更新）、列表支援分頁/分類篩選/狀態篩選。
- 新增一個公開唯讀端點（`/api/v1/shop/products`、`/api/v1/shop/products/{slug}`）：依 tenant 上下文列出已發佈商品，比照 `content-rendering` 的 published-only 語意，草稿商品回 404。

## Capabilities

### New Capabilities
- `product-catalog`: 商品分類、商品、SKU 的資料模型與 shop 範圍管理 API，以及公開的已發佈商品唯讀端點。

### Modified Capabilities
(無 — 本次不變更既有 capability 的 requirements；租戶隔離與 RBAC 的既有機制被擴充套用到新表，但既有 spec 的 requirements 本身不變。)

## Impact

- 新 ent schema：`api/internal/ent/schema/catalog.go`（Category、Product、ProductSKU、ProductCategory）。
- 新 package：`api/internal/catalog`（service 層，比照 `api/internal/cms` 結構）。
- 新 HTTP handler：`api/internal/httpapi/categories.go`、`api/internal/httpapi/products.go`（merchant CRUD + 公開唯讀端點）。
- 修改：`api/internal/tenant/enttenancy.go`（`tenantOwned` map 新增四張表）、`api/internal/seed/seed.go`（`PermissionCatalog`/`roleDefs` 新增權限節點）、`api/internal/httpapi/router.go`（掛載新路由）、`api/internal/app/wire.go`（組裝新 service/handler）。
- 新 migration：`api/migrations/`（Atlas diff 產生）。
- 不影響既有 pages/shops/rbac/multi-tenancy/theme-system/content-rendering/authentication capability 的既有行為。
