## Why

`product-catalog`（已歸檔）交付了商品與 SKU 的資料模型與 `BIGINT` 整數最小貨幣單位金額慣例，但會員無法把商品放進購物車——電商轉換路徑（瀏覽 → 加入購物車 → 結帳）缺了中間這一步。下一個 proposal（`order-management`）需要一個穩定的「購物車內容 + 總金額」來源才能建立訂單，因此本 change 優先把購物車資料模型與自助 API 落地，讓 `order-management` 有明確的資料可讀，不必自己另訂一套。

## What Changes

- 新增購物車（`carts`）：shop-scoped，`member_id` 指向平台級 `Member`（比照 `ShopMember` 直接存 `shop_id`+`member_id`，不透過 `ShopMember` 間接關聯——理由見 design.md）。`status` 三態（`active`/`converted`/`abandoned`），`currency` 由第一件加入的品項固定。同一 (shop, member) 至多一個 `active` 購物車（partial unique index）。
- 新增購物車品項（`cart_items`）：`cart_id`、`sku_id`、`quantity`（正整數）、加入當下的**價格快照**（`price_amount`/`currency` 複製自 SKU，不即時查詢）。
- 新增會員自助購物車 API（`/api/v1/shop/cart/...`，member JWT 認證，**不**套用 RBAC 權限節點——存取控制僅為「JWT member_id 等於購物車擁有者」）：取得目前購物車（含小計/總計）、加入品項、更新品項數量、移除品項、清空購物車。
- 幣別限制：同一購物車禁止混不同幣別的 SKU，加入時幣別不符回 422；不做多幣別換算。
- 加入品項時驗證 SKU 屬於同一 shop、`is_active=true`、所屬商品已發佈、`quantity` 不超過目前 `stock_qty`（僅檢查，不扣庫存不鎖定——結帳扣庫存留給 `order-management`）。
- 新增租戶隔離註冊：`Cart`、`CartItem` 納入 `api/internal/tenant.tenantOwned`。
- **不做**（Non-Goals，見 design.md）：訪客購物車、庫存預留鎖定、多幣別換算、storefront SSR 購物車頁面。

## Capabilities

### New Capabilities
- `shopping-cart`：已登入會員的自助購物車資料模型（`carts`/`cart_items`）與 API（取得/加入/更新數量/移除/清空），含價格快照、幣別一致性、庫存驗證、下架商品呈現等行為規則。

### Modified Capabilities
(無 — 本次不變更既有 capability 的 requirements；租戶隔離機制被擴充套用到新表，但既有 spec 的 requirements 本身不變。)

## Impact

- 新 ent schema：`api/internal/ent/schema/cart.go`（`Cart`、`CartItem`）。
- 新 package：`api/internal/cart`（service 層，比照 `api/internal/catalog` 結構：`Service{Client *ent.Client}`、`ValidationError`/`ConflictError`/`ErrNotFound`）。
- 新 HTTP handler：`api/internal/httpapi/cart.go`（會員自助 CRUD，掛於既有 `/shop` tenant-scoped 群組，`d.MemberMW` 認證）。
- 修改：`api/internal/tenant/enttenancy.go`（`tenantOwned` 新增 `Cart`/`CartItem`）、`api/internal/httpapi/router.go`（掛載新路由）、`api/internal/app/wire.go`（組裝新 service/handler）。
- 新 migration：`api/migrations/`（Atlas diff 產生）。
- 不影響既有 pages/shops/rbac/multi-tenancy/theme-system/content-rendering/authentication/product-catalog capability 的既有行為。
