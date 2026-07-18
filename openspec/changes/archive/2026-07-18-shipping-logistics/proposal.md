## Why

訂單建立後目前沒有任何方式把 `fulfillment_status` 從「未出貨」推進——order-management 刻意把這條軸線留空（`CHECK (fulfillment_status >= 0)`），交給本次變更定案。商家需要能設定自己的物流方式（宅配/超商取貨等），並在實際出貨時手動登記追蹤資訊；本專案目前沒有任何真實物流商的 API 憑證，比照 payment-integration 的 mock provider 先例，本次只做「可驗證的參考實作」——商家在後台手動輸入追蹤資訊（比照銀行轉帳等線下付款的手動確認模式），但資料模型與介面設計要為未來串接真實物流商 API 留餘地。

## What Changes

- 新增 `shipping_methods` 資料表（ent schema）：shop-scoped tenant-owned，商家設定的物流方式（`name`、`carrier`、`flat_rate` 固定運費、`is_active`），供商家後台 CRUD。
- 新增 `shipments` 資料表（ent schema）：shop-scoped tenant-owned，比照 `payments` 的稽核風格，`order_id` 是真實 FK（無 `OnDelete` cascade），記錄 `carrier`、`tracking_number`（nullable）、`status`、`shipped_at`/`delivered_at`（nullable）。一張訂單本階段只支援一筆出貨紀錄（不做部分出貨拆單）。
- 定案 `orders.fulfillment_status` 完整列舉值（0=未出貨、1=已出貨、2=已送達、3=已退貨——見 design.md），以及新表 `shipments.status` 的列舉值（與 `fulfillment_status` 非零值共用同一組數字，見 design.md D2）。
- 新增商家後台 API：`shipping_methods` CRUD（RBAC `shipping_method.*`）；`POST /api/v1/admin/shops/{shopID}/orders/{id}/shipments`（建立出貨紀錄，狀態轉為已出貨）；`PUT .../shipments/{shipmentID}`（推進狀態為已送達/已退貨）；`GET .../shipments`（查詢，RBAC `shipment.*`）。
- 新增會員自助 API：`GET /api/v1/shop/orders/{id}/shipment`（查看自己訂單的出貨狀態，唯讀，無 RBAC，比照既有訂單自助查詢模式）。
- RBAC permission catalog 新增 `shipping_method.*`、`shipment.*` 節點並加進既有角色定義（`merchant_owner`、`editor`）。
- `api/internal/tenant/enttenancy.go` 的 `tenantOwned` 註冊 `ShippingMethod`、`Shipment`。

**不做**（評估後縮小範圍，理由見 design.md）：
- 不串接任何真實物流商 API（沒有憑證，比照 payment-integration 先例）。
- 不將運費併入 `orders.total_amount`——那是結帳當下的商品小計快照，不動 order-management 的 checkout 交易邏輯；`flat_rate` 本階段僅供顯示/未來使用，不影響任何金額計算。
- 不支援部分出貨（訂單品項拆多筆出貨）——一張訂單一筆出貨紀錄，足以涵蓋本階段情境。
- 不做公開唯讀 `shipping_methods` 端點（供未來 storefront 結帳頁使用）——目前沒有消費端，評估後決定不多產出未經使用/未經測試的公開介面，留給有實際 storefront 結帳需求的未來變更。

## Capabilities

### New Capabilities
- `shipping-logistics`: 物流方式設定（CRUD）、出貨紀錄資料模型、商家出貨/推進狀態 API、會員自助查看出貨狀態 API，`orders.fulfillment_status` 完整列舉值定案。

### Modified Capabilities
（無——`fulfillment_status` 的更新沿用 order-management 已提供的 `order.Service.UpdateFulfillmentStatus` 服務層方法，不改變其既有 requirements。）

## Impact

- 新增 Go package `api/internal/shipping`（ent schema 對應的 Service，管理 `shipping_methods` 與 `shipments`）。
- 新增 ent schema `ShippingMethod`、`Shipment`（新表）與對應 migration（`make migrate-gen name=add_shipping_logistics`）。
- 新增 `api/internal/httpapi/shipping.go`（handler），修改 `router.go`（新增商家後台路由群組、會員自助路由）。
- 修改 `api/internal/tenant/enttenancy.go`（註冊 `ShippingMethod`、`Shipment` 為 tenant-owned）。
- 修改 `api/internal/seed/seed.go`（新增 `shipping_method.*`、`shipment.*` permission catalog 與角色授權）。
- 修改 `api/internal/app/wire.go`（組裝 `shipping.Service`、`httpapi.ShippingHandler`）。
- 依賴既有 `order.Service`（`GetOrder`/`GetOrderAdmin`/`UpdateFulfillmentStatus`），不修改其程式碼，尤其不動 `Checkout` 交易邏輯。
