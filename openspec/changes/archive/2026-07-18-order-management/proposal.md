## Why

Phase 2 電商核心目前止步於「購物車」——會員可以累積品項，但沒有任何方式把購物車轉換為一筆不可變的交易紀錄，商家也沒有訂單可以查看或處理。缺少訂單模型，`payment-integration`（收款）與 `shipping-logistics`（出貨）两個後續 proposal 都沒有依附的對象。本 change 補上「結帳」這個關鍵轉換動作，並建立訂單資料模型與三軸狀態欄位，作為後續兩個 proposal 的地基。

## What Changes

- 新增 `orders` / `order_items` 資料表（shop-scoped，比照 `carts`/`cart_items` 的租戶隔離慣例），`order_items` denormalize 商品標題與 SKU code 等文字快照，價格快照沿用 `cart_items` 既有的快照值。
- 新增結帳流程：讀取會員 active 購物車 → 驗證每個品項 purchasable 且庫存足夠 → 同一交易內對每個 SKU 做原子扣庫存（條件式 UPDATE，檢查影響列數）防止超賣 → 建立 `orders`/`order_items` → 把來源購物車 `status` 設為 `converted`。任一驗證失敗則整個結帳失敗、不建立訂單、不動庫存。
- 新增訂單三軸狀態欄位：`status`（訂單本身：`created`/`cancelled`）、`payment_status`（本次固定為初始值 `unpaid`，`payment-integration` 之後更新）、`fulfillment_status`（本次固定為初始值 `unfulfilled`，`shipping-logistics` 之後更新）。
- 新增訂單取消：僅在未付款且未出貨時可取消，取消時交易安全地歸還庫存。
- 新增收件資訊 JSON 欄位（`shipping_address`），儲存收件人姓名/電話/地址/郵遞區號等結構化欄位，供 `shipping-logistics` 直接讀取。
- 新增會員自助 API（`/api/v1/shop/...`，比照 cart 的 JWT member_id 比對，無 RBAC）：`POST /checkout`、`GET /orders`、`GET /orders/{id}`、`POST /orders/{id}/cancel`。
- 新增商家後台 API（`/api/v1/admin/shops/{shopID}/...`，RBAC `order.*` 權限節點）：`GET /orders`（可依狀態篩選）、`GET /orders/{id}`、`POST /orders/{id}/cancel`。
- 新增 `order.view`/`order.cancel` 權限節點至 `PermissionCatalog`/`roleDefs`。
- Service 層提供受控的狀態更新方法（供 `payment-integration`/`shipping-logistics` 呼叫，而非讓它們直接寫 ent）。

## Capabilities

### New Capabilities
- `order-management`: 購物車結帳轉換為訂單、並發安全的庫存扣減與歸還、訂單三軸狀態模型、會員自助訂單查詢與取消、商家後台訂單查詢與取消。

### Modified Capabilities
(none — 不修改既有 capability 的 requirements；本 change 會讀取但不變更 `shopping-cart`/`product-catalog` 既有規格所定義的行為，僅新增「結帳」這個新的外部操作把 cart.status 轉為 converted，這件事本身已被 shopping-cart spec 的 Cart status enumeration 要求預留給後續 proposal，不算修改既有 requirement)

## Impact

- 新增資料表：`orders`、`order_items`（ent schema + migration）。
- 新增 Go package `api/internal/order`（Service、ValidationError、ErrNotFound、狀態常數）。
- 新增 HTTP handler `api/internal/httpapi/order.go`，掛載於 `router.go` 的會員 `/shop` MemberMW 群組與商家 `/admin/shops/{shopID}` RBAC 群組。
- `api/internal/tenant/enttenancy.go` 的 `tenantOwned` 新增 `Order`/`OrderItem`。
- `api/internal/seed/seed.go` 的 `PermissionCatalog`/`roleDefs` 新增 `order.view`/`order.cancel`。
- `api/internal/app/wire.go` 組裝新 Service/Handler。
- `product_skus.stock_qty` 的更新路徑新增「結帳原子扣庫存」與「取消歸還庫存」兩種寫入來源，皆需交易安全；不變更 `product-catalog`/`cart` 既有欄位或既有 API 行為。
