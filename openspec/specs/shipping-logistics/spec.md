# shipping-logistics Specification

## Purpose
TBD - created by archiving change shipping-logistics. Update Purpose after archive.
## Requirements
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

`orders.fulfillment_status` 的列舉值為：`0`=未出貨（unfulfilled）、`1`=已出貨（shipped）、`2`=已送達（delivered）、`3`=已退貨（returned）、`4`=部分出貨（partially shipped）。`shipments.status` 沿用其中的 `1`/`2`/`3`，不另外定義獨立列舉，且 MUST NOT 取用 `4`——部分出貨是**訂單**的狀態，單一出貨紀錄永遠是完整的一箱。

`0`–`3` 的語意 MUST 與新增 `4` 之前完全相同；既有的值序列（`0`→`1`→`2` 與 `0`→`1`→`3`）MUST NOT 改變。

`orders.fulfillment_status` MUST 由該訂單當下所有出貨紀錄**推導**而得，且該推導 MUST 為下列的全函式，依序判定：

1. 沒有任何出貨紀錄 → `0`
2. 任一筆出貨紀錄為已退貨 → `3`
3. 該訂單已出貨數量總和小於訂購數量總和 → `4`
4. 所有出貨紀錄皆為已送達 → `2`
5. 其餘 → `1`

`orders.fulfillment_status` 的寫入 MUST 只能透過 `order.Service` 的受控入口，MUST NOT 由本能力的任何程式碼直接寫入 `ent.Order`。

本能力引入品項層級出貨之前既已存在的出貨紀錄，其對應訂單的**推導值 MUST 等於其當時儲存的值**（由資料遷移的回填保證）。

#### Scenario: 建立涵蓋整筆訂單的出貨後訂單標記已出貨
- **WHEN** 一筆 `fulfillment_status = 0` 訂單被商家建立涵蓋全部品項的出貨紀錄
- **THEN** 該訂單 `fulfillment_status` 變為 `1`（已出貨）

#### Scenario: 只出一部分時訂單標記部分出貨
- **WHEN** 一筆含兩個品項的訂單只有其中一個品項被出貨
- **THEN** 該訂單 `fulfillment_status` 變為 `4`（部分出貨）

#### Scenario: 補出剩餘品項後訂單標記已出貨
- **WHEN** 一筆 `fulfillment_status = 4` 的訂單，其剩餘未出貨品項全部被出貨
- **THEN** 該訂單 `fulfillment_status` 變為 `1`（已出貨）

#### Scenario: 部分出貨時單一出貨送達不使訂單變為已送達
- **WHEN** 一筆含兩個品項的訂單只出了其中一個，且該筆出貨被標記為已送達
- **THEN** 該訂單 `fulfillment_status` 仍為 `4`（部分出貨），MUST NOT 為 `2`

#### Scenario: 所有出貨皆送達後訂單標記已送達
- **WHEN** 一筆分兩次出貨且已全部出完的訂單，其兩筆出貨皆被標記為已送達
- **THEN** 該訂單 `fulfillment_status` 變為 `2`（已送達）

### Requirement: Merchant can create a shipment record that advances an order to shipped

商家後台 API `POST /api/v1/admin/shops/{shopID}/orders/{id}/shipments` MUST 依既有三層 RBAC 判定，需要 `shipment.create` 權限。請求 MUST 附帶 `carrier`（非空），MAY 附帶 `tracking_number`，MAY 附帶品項清單（省略即為出貨全部未出品項）。

建立成功時，系統 MUST 在**單一資料庫交易**內完成：建立一筆 `shipments` 記錄（`status` 為已出貨、`shipped_at` 設為當下時間）、建立其對應的 `shipment_items`、遞增相關訂單品項的已出貨數量，並依推導規則更新該訂單的 `fulfillment_status`。任一步失敗 MUST 使整筆交易回滾，MUST NOT 留下部分完成的出貨。

一張訂單 MAY 有多筆 `shipments` 記錄。同一張訂單的並發出貨操作 MUST 被序列化，使推導出的 `fulfillment_status` 不會因為交錯而停留在錯誤的值。

目標訂單 `status` 為已取消 MUST 拒絕（409）。該訂單已無未出貨數量 MUST 拒絕（409）。該訂單已有任一筆已退貨的出貨 MUST 拒絕（409）。目標訂單不存在於該商家 MUST 回 404。

#### Scenario: 對未出貨且未取消的訂單建立出貨紀錄成功
- **WHEN** 持有 `shipment.create` 權限的使用者對 shop A 一張 `status=0`（已建立）、`fulfillment_status=0`（未出貨）的訂單呼叫建立出貨紀錄 API，附帶 `carrier` 與 `tracking_number`
- **THEN** 回應包含新建立的出貨紀錄（`status` 為已出貨），該訂單 `fulfillment_status` 變為 `1`

#### Scenario: 對已取消訂單建立出貨紀錄被拒
- **WHEN** 對一張 `status=1`（已取消）的訂單呼叫建立出貨紀錄 API
- **THEN** 回 409，不建立出貨紀錄，訂單 `fulfillment_status` 不變

#### Scenario: 對已部分出貨的訂單建立第二筆出貨成功
- **WHEN** 對一張 `fulfillment_status` 為 `4`（部分出貨）的訂單，指名其剩餘品項再次呼叫建立出貨紀錄 API
- **THEN** 回 201，該訂單擁有兩筆出貨紀錄

#### Scenario: 缺少 carrier 的建立請求被拒
- **WHEN** 呼叫建立出貨紀錄 API 但請求缺少 `carrier` 欄位
- **THEN** 回 422，不建立出貨紀錄

### Requirement: Merchant can advance a shipment to delivered or returned with a restricted state machine

商家後台 API `PUT /api/v1/admin/shops/{shopID}/orders/{id}/shipments/{shipmentID}` MUST 依既有三層 RBAC 判定，需要 `shipment.update` 權限。合法的狀態轉換僅有「已出貨 → 已送達」與「已出貨 → 已退貨」；`delivered`、`returned` 皆為終態，對非「已出貨」狀態的出貨紀錄（含已是終態的紀錄）呼叫本 API MUST 拒絕（409），不得就地把 `returned` 改回 `delivered` 或反向。

轉換成功時，系統 MUST 依推導規則更新對應訂單的 `fulfillment_status`——MUST NOT 直接把該筆出貨的目標狀態當成訂單的狀態，因為一張訂單可能有多筆出貨。標記為已送達時 MUST 記錄 `delivered_at`。同一張訂單的並發狀態推進 MUST 被序列化。

#### Scenario: 涵蓋整筆訂單的出貨標記為已送達
- **WHEN** 持有 `shipment.update` 權限的使用者對一筆涵蓋整筆訂單、`status` 為已出貨的出貨紀錄呼叫更新 API，目標狀態為已送達
- **THEN** 該出貨紀錄 `status` 變為已送達且 `delivered_at` 有值，對應訂單 `fulfillment_status` 變為 `2`

#### Scenario: 涵蓋整筆訂單的出貨標記為已退貨
- **WHEN** 持有 `shipment.update` 權限的使用者對一筆涵蓋整筆訂單、`status` 為已出貨的出貨紀錄呼叫更新 API，目標狀態為已退貨
- **THEN** 該出貨紀錄 `status` 變為已退貨，對應訂單 `fulfillment_status` 變為 `3`

#### Scenario: 多筆出貨中僅其一送達時訂單不變為已送達
- **WHEN** 一張已全部出完、含兩筆出貨的訂單，其中一筆被標記為已送達
- **THEN** 該筆出貨為已送達，對應訂單 `fulfillment_status` 仍為 `1`（已出貨）

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

本端點回傳**單一**出貨紀錄。訂單有多筆出貨時，MUST 回傳其中**最近的一筆**（依識別碼遞減取第一筆），MUST NOT 因為出貨紀錄不只一筆而失敗。要取得全部出貨紀錄的呼叫端 MUST 改用複數端點 `GET /api/v1/shop/orders/{id}/shipments`。訪客以訂單存取權杖存取的 `GET /api/v1/shop/guest/order/shipment` 適用完全相同的規則。

#### Scenario: 會員查詢自己已出貨訂單的出貨狀態
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/orders/{id}/shipment`，該訂單已有出貨紀錄
- **THEN** 回傳該筆出貨紀錄（carrier、tracking_number、status、shipped_at/delivered_at、裝載品項）

#### Scenario: 會員查詢自己尚未出貨訂單的出貨狀態
- **WHEN** 會員查詢自己名下一張尚無出貨紀錄的訂單的出貨狀態
- **THEN** 回 404

#### Scenario: 多筆出貨時單數端點回最近一筆
- **WHEN** 會員的一筆訂單有兩筆出貨紀錄，該會員呼叫單數出貨端點
- **THEN** 回 200，回傳識別碼較大的那一筆出貨紀錄，MUST NOT 回 404 或 500

#### Scenario: 跨會員查詢出貨狀態被拒
- **WHEN** 會員 A 嘗試以 `GET /api/v1/shop/orders/{id}/shipment` 查詢屬於會員 B 的訂單的出貨狀態
- **THEN** 回 404

### Requirement: Tenant data isolation for shipping_methods and shipments tables

`shipping_methods`、`shipments`、`shipment_items` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop。

#### Scenario: 跨店查詢看不到彼此的物流資料
- **WHEN** shop A 的商家或會員查詢自己的物流方式或出貨紀錄
- **THEN** 回應不含任何 shop B 的物流方式、出貨紀錄或出貨品項資料

### Requirement: Storefront can read the shop's active shipping methods

系統 MUST 提供公開唯讀端點 `GET /api/v1/shop/shipping-methods`，供商家自帶前端在結帳前呈現可選的運送方式。商家上下文 MUST 依既有前台租戶解析規則決定（spec multi-tenancy：publishable key 優先、否則 `Host`），該端點 MUST NOT 要求任何認證，MUST NOT 套用 RBAC 角色權限節點系統。

回應 MUST 僅包含 `is_active = true` 的運送方式；已停用者 MUST NOT 出現。每筆 MUST 包含識別碼、名稱、物流商名稱與固定運費，MUST NOT 包含 `is_active`（回傳的全部皆為啟用）與其他後台專用欄位。運費 MUST 以整數最小貨幣單位表達，其幣別 MUST 與該筆訂單的幣別相同（本階段每張購物車與每筆訂單皆為單一幣別）。

#### Scenario: 前台取得可選的運送方式

- **WHEN** 未認證的前台以已解析的商家上下文呼叫 `GET /api/v1/shop/shipping-methods`
- **THEN** 回 200，回傳該商家所有 `is_active = true` 的運送方式（識別碼、名稱、物流商、固定運費）

#### Scenario: 已停用的運送方式不出現在公開清單

- **WHEN** 商家將某運送方式設為 `is_active = false` 後，前台再次呼叫該端點
- **THEN** 回應中 MUST NOT 包含該運送方式

#### Scenario: 跨店查詢看不到彼此的運送方式

- **WHEN** 以 shop A 的商家上下文呼叫該端點
- **THEN** 回應 MUST NOT 包含任何屬於 shop B 的運送方式

### Requirement: A shipping method's flat rate becomes the order's shipping fee when selected at checkout

`shipping_methods.flat_rate` MUST 為結帳時運費的唯一來源。結帳選定某運送方式時，系統 MUST 將該筆的 `flat_rate` 與 `name` 於當下複製為訂單自身的欄位（spec order-management：`shipping_fee` 與運送方式名稱），此後 MUST NOT 因該運送方式改價、停用或被刪除而重新計算既有訂單的任何金額。

商家後台對 `shipping_methods` 的更新或刪除 MUST NOT 因為某些訂單曾經選用該筆而被阻擋，MUST NOT 連帶修改任何既有訂單。

#### Scenario: 選定的運送方式決定訂單運費

- **WHEN** 某運送方式 `flat_rate = 200`，結帳時選定該方式
- **THEN** 訂單 `shipping_fee = 200`，訂單應付總額為品項小計加 `200`

#### Scenario: 改價不影響既有訂單

- **WHEN** 訂單建立後商家將該運送方式的 `flat_rate` 由 `200` 改為 `300`
- **THEN** 既有訂單的 `shipping_fee` 仍為 `200`，其應付總額不變

#### Scenario: 刪除曾被選用的運送方式仍然成功

- **WHEN** 商家刪除一個已被既有訂單選用過的運送方式
- **THEN** 刪除成功，既有訂單的運送方式名稱與 `shipping_fee` 完全不變

### Requirement: A shipment records which order line items it contains, and in what quantity

系統 MUST 以 `shipment_items` 記錄一筆出貨所裝載的訂單品項與數量：每一列 MUST 指向一筆 `shipments` 與一筆 `order_items`，並帶一個大於 `0` 的 `quantity`。同一筆出貨對同一筆訂單品項 MUST 至多只有一列（由資料庫唯一約束保證），重複指名同一品項的請求 MUST 被拒絕（422）。

一筆出貨所指名的訂單品項 MUST 全部屬於該筆出貨所屬的訂單；指名不屬於該訂單的品項 MUST 回 422（請求主體上的值有誤），MUST NOT 回 404。

`shipment_items` MUST 納入既有租戶隔離機制（spec multi-tenancy）。

出貨紀錄的回應 MUST 包含其裝載的品項（訂單品項識別碼與數量），使呼叫端能與訂單品項對應。出貨層級 MUST NOT 帶有任何金額欄位——運費仍然只存在於訂單層（spec order-management）。

#### Scenario: 指名品項與數量建立出貨
- **WHEN** 商家對一筆含兩個品項（各 1 件）的訂單建立出貨，並只指名其中一個品項 1 件
- **THEN** 建立成功，該筆出貨的品項清單只含被指名的那一個品項與數量 `1`

#### Scenario: 指名不屬於該訂單的品項被拒
- **WHEN** 建立出貨的請求指名一筆屬於其他訂單的訂單品項識別碼
- **THEN** 回 422，不建立任何出貨紀錄，訂單狀態與已出貨數量皆不變

#### Scenario: 同一請求重複指名同一品項被拒
- **WHEN** 建立出貨的請求在品項清單中兩次指名同一筆訂單品項
- **THEN** 回 422，不建立任何出貨紀錄

### Requirement: Shipped quantity per order line item is authoritative and can never exceed the ordered quantity

系統 MUST 於每一筆訂單品項上維護「已出貨數量」，其值 MUST 恆滿足 `0 <= 已出貨數量 <= 訂購數量`，且該不變式 MUST 由資料庫約束強制，MUST NOT 只由應用層邏輯保證。

已出貨數量的遞增 MUST 以單一條件式 UPDATE ＋ 受影響列數檢查完成（與結帳扣庫存同一形狀），MUST NOT 以應用層的「先讀後寫」實作。任一品項的出貨數量會使其超出訂購數量時，整筆出貨請求 MUST 被拒絕（422），且 MUST NOT 產生任何出貨紀錄、任何品項數量變更或任何訂單狀態變更。

已出貨數量 MUST NOT 因出貨被標記為已退貨而回減。

訂單品項的回應 MUST 包含已出貨數量，使呼叫端不必自行加總出貨紀錄即可得知尚未出貨的數量。

#### Scenario: 超出訂購數量的出貨被拒
- **WHEN** 一筆品項訂購 2 件、已出貨 1 件，商家嘗試對該品項再出貨 2 件
- **THEN** 回 422，不建立出貨紀錄，該品項的已出貨數量仍為 1，訂單出貨狀態不變

#### Scenario: 分兩次出貨後已出貨數量等於訂購數量
- **WHEN** 一筆訂購 3 件的品項先出貨 1 件、再出貨 2 件
- **THEN** 該品項的已出貨數量為 3，且等於其兩筆出貨紀錄所載數量之和

#### Scenario: 退貨不使已出貨數量回減
- **WHEN** 一筆已出貨的出貨紀錄被標記為已退貨
- **THEN** 其所載品項的已出貨數量不變

### Requirement: Omitting the item list ships every not-yet-shipped item

建立出貨的請求中，品項清單 MUST 為選填。省略時，系統 MUST 將該訂單**所有尚未出貨的數量**納入本次出貨——因此對一筆從未出貨的訂單省略品項清單，其結果 MUST 與本能力引入品項概念之前完全相同。

品項清單若出現但為空陣列，MUST 被拒絕（422）：省略與「一件都不出」是兩件不同的事。

該訂單已無任何未出貨數量時，建立出貨的請求 MUST 被拒絕（409）。

#### Scenario: 省略品項清單即整筆出貨
- **WHEN** 商家對一筆從未出貨的訂單建立出貨，請求中只帶 `carrier`（不帶品項清單）
- **THEN** 建立成功，該筆出貨涵蓋訂單全部品項的全部數量，訂單出貨狀態變為已出貨

#### Scenario: 空品項清單被拒
- **WHEN** 建立出貨的請求帶一個空的品項清單
- **THEN** 回 422，不建立出貨紀錄

#### Scenario: 全部出貨完畢後再建立出貨被拒
- **WHEN** 一筆訂單所有品項的已出貨數量皆等於訂購數量，商家再次呼叫建立出貨
- **THEN** 回 409，不建立第二筆出貨紀錄

### Requirement: An order with a returned shipment accepts no further shipments

任一筆出貨已被標記為已退貨的訂單，MUST NOT 再建立新的出貨紀錄（409）。

理由是狀態序列的可解釋性：標記退貨會扣回**整筆訂單**已核發的點數（spec member-tiers-and-points），而訂單層的出貨狀態沒有「部分退貨」的表達方式；若允許續出，訂單會產生一段「已退貨 → 部分出貨 → 已出貨」的序列，而該序列沒有任何下游能夠正確解釋。

#### Scenario: 已退貨訂單不得再出貨
- **WHEN** 一筆訂單的某筆出貨已被標記為已退貨，商家嘗試對該訂單剩餘未出貨的品項建立新的出貨
- **THEN** 回 409，不建立出貨紀錄，訂單狀態與已出貨數量皆不變

### Requirement: Customers can read every shipment of an order

系統 MUST 提供顧客可讀的**複數**出貨端點，使一筆訂單的所有出貨紀錄都能被查得：會員自助 `GET /api/v1/shop/orders/{orderID}/shipments`（依既有會員 JWT 判定範圍），訪客 `GET /api/v1/shop/guest/order/shipments`（依既有訂單存取權杖判定範圍）。兩者 MUST NOT 套用 RBAC 角色權限節點系統。

回應 MUST 為集合，其順序 MUST 明訂（依識別碼遞減，與後台端點一致），MUST NOT 交由資料庫決定。訂單尚無任何出貨紀錄時 MUST 回 200 與空集合（清單端點的空集合不是 404）。

存取不屬於自己的訂單 MUST 回 404（不得洩漏該訂單屬於其他顧客）。

#### Scenario: 會員查得自己訂單的所有出貨
- **WHEN** 會員的一筆訂單分兩次出貨，該會員呼叫複數出貨端點
- **THEN** 回 200，集合含兩筆出貨紀錄，且各自帶有其裝載的品項與數量

#### Scenario: 尚未出貨時回空集合
- **WHEN** 顧客查詢一筆尚無出貨紀錄的自有訂單
- **THEN** 回 200，出貨集合為空

#### Scenario: 跨顧客查詢被拒
- **WHEN** 會員 A 以複數出貨端點查詢屬於會員 B 的訂單
- **THEN** 回 404
