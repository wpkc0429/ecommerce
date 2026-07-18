## ADDED Requirements

### Requirement: Merchant-configured shipping methods are a shop-scoped CRUD resource

商家後台 API（`/api/v1/admin/shops/{shopID}/shipping-methods`）MUST 提供物流方式的完整 CRUD（列表、建立、檢視、更新、刪除），依既有三層 RBAC 判定（spec rbac）：檢視需要 `shipping_method.view`，建立需要 `shipping_method.create`，更新需要 `shipping_method.edit`，刪除需要 `shipping_method.delete`。每筆物流方式 MUST 包含 `name`、`carrier`（物流商名稱，純文字）、`flat_rate`（BIGINT 整數最小貨幣單位，固定運費）、`is_active`。商家 MUST 只能操作 URL 中 `shopID` 所屬的物流方式；跨商家操作 MUST 依既有 cross-shop access guard 回 403。

#### Scenario: 具權限商家建立物流方式
- **WHEN** 持有 shop A `shipping_method.create` 權限的使用者以合法 `name`/`carrier`/`flat_rate` 呼叫建立物流方式 API
- **THEN** 建立成功，回應包含該物流方式的完整欄位

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `shipping_method.edit` 權限，嘗試更新 shop A 的物流方式
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的物流方式管理 API
- **THEN** 回 403

### Requirement: orders.fulfillment_status enumeration is finalized by this change

`orders.fulfillment_status` 的完整列舉值本階段定案為：`0`=未出貨（unfulfilled）、`1`=已出貨（shipped）、`2`=已送達（delivered）、`3`=已退貨（returned）。`shipments.status` 沿用同一組非零數值（`1`/`2`/`3`），不另外定義獨立列舉。`orders.fulfillment_status` 的寫入 MUST 只能透過 `order.Service.UpdateFulfillmentStatus`，MUST NOT 由本能力的任何程式碼直接寫入 `ent.Order`。

#### Scenario: 建立出貨紀錄後訂單標記已出貨
- **WHEN** 一筆 `fulfillment_status = 0` 訂單被商家建立出貨紀錄
- **THEN** 該訂單 `fulfillment_status` 變為 `1`（已出貨）

### Requirement: Merchant can create a shipment record that advances an order to shipped

商家後台 API `POST /api/v1/admin/shops/{shopID}/orders/{id}/shipments` MUST 依既有三層 RBAC 判定，需要 `shipment.create` 權限。請求 MUST 附帶 `carrier`（非空），MAY 附帶 `tracking_number`。建立成功時，系統 MUST 建立一筆 `shipments` 記錄（`status` 為已出貨、`shipped_at` 設為當下時間），並透過 `order.Service.UpdateFulfillmentStatus` 將該訂單 `fulfillment_status` 推進為 `1`（已出貨）。目標訂單 `status` 為已取消，或 `fulfillment_status` 已不是未出貨（該訂單已有出貨紀錄），MUST 拒絕（409），不建立任何 `shipments` 記錄，不變更訂單狀態。目標訂單不存在於該商家 MUST 回 404。一張訂單 MUST 至多只能有一筆 `shipments` 記錄。

#### Scenario: 對未出貨且未取消的訂單建立出貨紀錄成功
- **WHEN** 持有 `shipment.create` 權限的使用者對 shop A 一張 `status=0`（已建立）、`fulfillment_status=0`（未出貨）的訂單呼叫建立出貨紀錄 API，附帶 `carrier` 與 `tracking_number`
- **THEN** 回應包含新建立的出貨紀錄（`status` 為已出貨），該訂單 `fulfillment_status` 變為 `1`

#### Scenario: 對已取消訂單建立出貨紀錄被拒
- **WHEN** 對一張 `status=1`（已取消）的訂單呼叫建立出貨紀錄 API
- **THEN** 回 409，不建立出貨紀錄，訂單 `fulfillment_status` 不變

#### Scenario: 對已出貨訂單重複建立出貨紀錄被拒
- **WHEN** 對一張 `fulfillment_status` 已是 `1`（已出貨）的訂單再次呼叫建立出貨紀錄 API
- **THEN** 回 409，不建立第二筆出貨紀錄，訂單狀態不變

#### Scenario: 缺少 carrier 的建立請求被拒
- **WHEN** 呼叫建立出貨紀錄 API 但請求缺少 `carrier` 欄位
- **THEN** 回 422，不建立出貨紀錄

### Requirement: Merchant can advance a shipment to delivered or returned with a restricted state machine

商家後台 API `PUT /api/v1/admin/shops/{shopID}/orders/{id}/shipments/{shipmentID}` MUST 依既有三層 RBAC 判定，需要 `shipment.update` 權限。合法的狀態轉換僅有「已出貨 → 已送達」與「已出貨 → 已退貨」；`delivered`、`returned` 皆為終態，對非「已出貨」狀態的出貨紀錄（含已是終態的紀錄）呼叫本 API MUST 拒絕（409），不得就地把 `returned` 改回 `delivered` 或反向。轉換成功時，系統 MUST 透過 `order.Service.UpdateFulfillmentStatus` 將對應訂單 `fulfillment_status` 更新為相符的值（`2` 或 `3`），標記為已送達時 MUST 記錄 `delivered_at`。

#### Scenario: 已出貨的出貨紀錄可標記為已送達
- **WHEN** 持有 `shipment.update` 權限的使用者對一筆 `status` 為已出貨的出貨紀錄呼叫更新 API，目標狀態為已送達
- **THEN** 該出貨紀錄 `status` 變為已送達且 `delivered_at` 有值，對應訂單 `fulfillment_status` 變為 `2`

#### Scenario: 已出貨的出貨紀錄可標記為已退貨
- **WHEN** 持有 `shipment.update` 權限的使用者對一筆 `status` 為已出貨的出貨紀錄呼叫更新 API，目標狀態為已退貨
- **THEN** 該出貨紀錄 `status` 變為已退貨，對應訂單 `fulfillment_status` 變為 `3`

#### Scenario: 對已送達的出貨紀錄重複標記被拒
- **WHEN** 對一筆 `status` 已是已送達的出貨紀錄再次呼叫更新 API
- **THEN** 回 409，出貨紀錄與訂單狀態皆不變

#### Scenario: 對已退貨的出貨紀錄標記為已送達被拒
- **WHEN** 對一筆 `status` 已是已退貨的出貨紀錄呼叫更新 API，目標狀態為已送達
- **THEN** 回 409，出貨紀錄與訂單狀態皆不變

### Requirement: Merchant back-office can list an order's shipments

商家後台 API `GET /api/v1/admin/shops/{shopID}/orders/{id}/shipments` MUST 依既有三層 RBAC 判定，需要 `shipment.view` 權限。商家 MUST 只能查詢 URL 中 `shopID` 所屬訂單的出貨紀錄；跨商家操作 MUST 依既有 cross-shop access guard 回 403。目標訂單不存在於該商家 MUST 回 404。

#### Scenario: 具權限商家檢視自己商家訂單的出貨紀錄
- **WHEN** 持有 shop A `shipment.view` 權限的使用者查詢 shop A 一張已出貨訂單的出貨紀錄
- **THEN** 回傳該訂單底下的出貨紀錄

#### Scenario: 跨店查詢被拒
- **WHEN** 僅屬 shop A 的使用者查詢 shop B 一張訂單的出貨紀錄
- **THEN** 回 403

### Requirement: Member self-service shipment access is scoped by member identity

會員自助 API `GET /api/v1/shop/orders/{id}/shipment` MUST 僅依賴已驗證會員 JWT 中的 `member_id` 判斷存取範圍（比照既有訂單自助 API 慣例，spec order-management），MUST NOT 套用 RBAC 角色權限節點系統。會員 MUST 只能查詢自己名下訂單的出貨狀態；存取不屬於自己的訂單 id MUST 回 404（不得洩漏該訂單屬於其他會員）。訂單尚無出貨紀錄時 MUST 回 404。

#### Scenario: 會員查詢自己已出貨訂單的出貨狀態
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/orders/{id}/shipment`，該訂單已有出貨紀錄
- **THEN** 回傳該筆出貨紀錄（carrier、tracking_number、status、shipped_at/delivered_at）

#### Scenario: 會員查詢自己尚未出貨訂單的出貨狀態
- **WHEN** 會員查詢自己名下一張尚無出貨紀錄的訂單的出貨狀態
- **THEN** 回 404

#### Scenario: 跨會員查詢出貨狀態被拒
- **WHEN** 會員 A 嘗試以 `GET /api/v1/shop/orders/{id}/shipment` 查詢屬於會員 B 的訂單的出貨狀態
- **THEN** 回 404

### Requirement: Tenant data isolation for shipping_methods and shipments tables

`shipping_methods`、`shipments` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop。

#### Scenario: 跨店查詢看不到彼此的物流資料
- **WHEN** shop A 的商家或會員查詢自己的物流方式或出貨紀錄
- **THEN** 回應不含任何 shop B 的物流方式或出貨紀錄資料
