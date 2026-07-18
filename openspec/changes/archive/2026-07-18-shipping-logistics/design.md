## Context

order-management（已歸檔）建立了訂單的三軸狀態模型，`fulfillment_status` 欄位刻意只留一個寬鬆的 `CHECK (fulfillment_status >= 0)` 與唯一初始值 `0`（未出貨），並提供受控入口 `order.Service.UpdateFulfillmentStatus(ctx, shopID, orderID, status int16) (*ent.Order, error)`（做租戶範圍檢查、存在性檢查、非負值檢查，直接覆寫欄位，不做狀態機檢查）給未來的物流能力使用。本變更是這條軸線的第一個真正寫入者。

本專案目前沒有任何真實物流商的 API 憑證，因此本變更比照 payment-integration 的先例：產出一個**可驗證的參考實作**——物流方式設定（CRUD）+ 出貨紀錄管理，商家在後台手動輸入追蹤資訊（比照信用卡以外「銀行轉帳」情境的手動確認模式），資料模型與服務層介面設計上為未來串接真實物流商 API（自動取號、狀態查詢 webhook 等）留餘地，但本階段不做任何外部整合。

## Goals / Non-Goals

**Goals:**
- `shipping_methods` 資料表：商家可設定的物流方式（CRUD），固定運費（不做重量/地區費率運算）。
- `shipments` 資料表：出貨紀錄，比照 `payments` 的稽核風格（真實 FK、無 `OnDelete` cascade），一張訂單一筆。
- 定案 `orders.fulfillment_status` 與 `shipments.status` 完整列舉值。
- 商家後台出貨（建立出貨紀錄）、推進狀態（送達/退貨）、查詢三個 API；會員自助查詢自己訂單出貨狀態的唯讀 API。
- 狀態轉換合法性檢查：不能對未出貨訂單標記已送達、不能重複出貨、不能對已取消訂單出貨。
- 嚴格遵守 order-management 交接的邊界：`orders.fulfillment_status` 只能透過 `order.Service.UpdateFulfillmentStatus` 寫入；不修改 `order.Service.Checkout` 的交易邏輯。

**Non-Goals:**
- 不串接任何真實物流商（沒有憑證，理由同 payment-integration）。
- 不支援部分出貨（訂單品項拆多筆出貨）——評估後認為這需要在 `order_items` 層級追蹤已出貨/未出貨數量，複雜度顯著增加且本階段沒有明確需求；一張訂單一筆出貨紀錄的簡單模型已足夠涵蓋「商家包一箱寄出」的主流情境。
- 不把運費併入 `orders.total_amount`——`total_amount` 是結帳當下的商品小計快照（order-management design D1），且 checkout 交易邏輯是全系統最脆弱、已用 `-race` 驗證過的並發安全區塊，本次刻意不去動它。`shipping_methods.flat_rate` 本階段純粹是商家後台的設定資訊，不參與任何金額計算、不寫入訂單。
- 不做公開唯讀 `shipping_methods` 端點——評估後決定不做（見 D7）。
- 不做「送達後又退貨」（post-delivery return）——本階段 `delivered`/`returned` 皆為終態，只能從 `shipped` 轉入；送達後的退貨/退款屬於未來 returns/refunds 能力的範圍。
- 不做物流方式的地區/重量分級費率——`flat_rate` 是單一固定值。

## Decisions

### D1: `shipping_methods` 資料表

比照 `Category` 的 shop-scoped CRUD 慣例（直接 `shop_id` 欄位、註冊進 `tenant.tenantOwned`、`TimeMixin`）。欄位：

```go
field.Int("shop_id")
field.String("name").MaxLen(100)...NotEmpty()
field.String("carrier").MaxLen(100)...NotEmpty()
field.Int64("flat_rate")          // minor units, >= 0
field.Bool("is_active").Default(true)
```

`carrier` 是純文字（不是 enum/FK）——物流商名稱本階段只作顯示用途，不驅動任何程式邏輯分支（與 `payment.Provider.Name()` 不同：那是驅動 provider 選路的程式碼鍵值，這裡沒有對應的「carrier 抽象介面」，因為本階段沒有任何 carrier 的程式邏輯需要分派）。索引：`(shop_id, is_active)` 方便未來公開端點/下拉選單只撈 active 的。DB CHECK：`flat_rate >= 0`。

沒有唯一約束在 `name`——不同商家可能想建立同名但不同 carrier 的物流方式（例如兩種「常溫配送」對應不同物流商），不強加不必要的限制。

### D2: `orders.fulfillment_status` 與 `shipments.status` 列舉值定案——共用同一組數字

`orders.fulfillment_status` 完整列舉值本階段定案為：`0`=未出貨（unfulfilled，order-management 既有初始值）、`1`=已出貨（shipped）、`2`=已送達（delivered）、`3`=已退貨（returned）。

`shipments.status` **不另外發明一組獨立列舉**，而是直接沿用 `fulfillment_status` 的非零值（`1`/`2`/`3`）：因為本階段一張訂單只有一筆出貨紀錄，且這筆紀錄的存在本身就代表「已出貨」（建立出貨紀錄的動作 = 出貨事件本身，不是先建立一筆 pending 草稿、之後再標記出貨——見 D3 API 設計），所以 `shipments.status` 與 `orders.fulfillment_status` 在任何時刻都應該相等（`shipments.status` 有值時）。共用同一組常數（定義在 `api/internal/shipping` package，`ShippedStatus/DeliveredStatus/ReturnedStatus int16 = 1/2/3`）避免兩條軸線的列舉值意外分岔，也讓「建立/更新 shipment 之後呼叫 `UpdateFulfillmentStatus` 帶同一個值」這件事在程式碼裡是顯而易見的一行對應，不是兩套需要人工保持同步的常數表。

DB CHECK：`orders.fulfillment_status >= 0`（既有，order-management 定義，本變更不修改該 CHECK 字串本身——它本來就是寬鬆的 `>= 0`，定案的是語意而非 schema）；`shipments.status IN (1, 2, 3)`（`shipments` 表本身沒有 `0`/未出貨的狀態可以表示——不存在的 shipment 列就是「未出貨」，見 D4）。

### D3: Shipment 建立/狀態轉換的 API 設計與合法性規則

**建立出貨紀錄** `POST /api/v1/admin/shops/{shopID}/orders/{id}/shipments`：body 帶 `carrier`、`tracking_number`（可空字串——見下）。這個動作本身就是「出貨」事件，不是先建草稿再標記——所以：
- 建立時 `status` 固定為 `ShippedStatus(1)`，`shipped_at` 設為 `time.Now()`。
- 前置條件（皆檢查，任一不符回 409，訂單不存在於該商家回 404）：訂單 `status != order.StatusCancelled`（不能對已取消訂單出貨）；訂單 `fulfillment_status == order.FulfillmentStatusUnfulfilled`（尚未出貨過——見 D4 的並發安全實作）。
- 成功後在同一次操作內呼叫 `order.Service.UpdateFulfillmentStatus(ctx, shopID, orderID, ShippedStatus)`。

**推進狀態** `PUT /api/v1/admin/shops/{shopID}/orders/{id}/shipments/{shipmentID}`：body 帶目標狀態（`"delivered"` 或 `"returned"`，服務層轉換為 `2`/`3`）。合法轉換只有 `shipped(1) → delivered(2)` 與 `shipped(1) → returned(3)`；`delivered`/`returned` 皆為終態，對終態的 shipment 再次呼叫一律 409（不允許 `delivered → returned`、不允許同一終態重複呼叫視為 no-op——比照 order 現有 `CancelOrder` 的「非法就整段拒絕」慣例，而非 payment webhook 那種「重複投遞需要安全 no-op」慣例，因為這裡的呼叫方是商家後台的人為操作，不是可能重送的外部系統，重複點擊「標記已送達」應該讓使用者看到明確的錯誤而非靜默吞掉）。成功後同樣呼叫 `order.Service.UpdateFulfillmentStatus`，並設定 `delivered_at`（`delivered` 時）。`returned` 不特別記錄額外時間戳欄位——`updated_at`（`TimeMixin`）已足夠標示狀態變更時間，不為單一分支多開一個 nullable 欄位。

`tracking_number` 允許空字串（部分超商取貨模式可能商家會先出貨、稍後才補登追蹤碼），但**不允許整個 body 缺少 `carrier`**——出貨紀錄至少要知道「交給哪家物流商」。

### D4: 一張訂單一筆出貨紀錄的並發安全實作

`shipments` 表 **`order_id` 加資料庫唯一索引**（`index.Fields("order_id").Unique()`）作為權威防線，而不是只靠應用層「先查詢再判斷」的競態窗口。`UpdateFulfillmentStatus` 是純覆寫、沒有 CAS 語意，不能單獨承擔「防止併發重複出貨」的責任，所以真正的並發安全來自 DB 唯一索引 + 服務層對衝突的正確處理，而不是加大交易範圍去跨 package 直接操作 `ent.Order`（那會違反交接鐵律：`fulfillment_status` 只能透過 `UpdateFulfillmentStatus` 寫入）。

`shipping.Service.CreateShipment` 流程：
1. 查詢訂單（`entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)`），不存在回 `ErrNotFound`；`status == StatusCancelled` 或 `fulfillment_status != FulfillmentStatusUnfulfilled` 回 `ConflictError`（409）——這一步是及早回饋清楚的錯誤訊息，不是並發安全的保證。
2. 呼叫 `Orders.UpdateFulfillmentStatus(ctx, shopID, orderID, ShippedStatus)`，把 `fulfillment_status` 推進為已出貨。
3. 在 `shipments` 表插入一筆列（`order_id` 唯一索引）。若插入因唯一索引衝突失敗，代表發生了併發競態（兩個請求都通過了步驟 1 的檢查）——回 409，不回滾步驟 2 的 `fulfillment_status`：並發的另一個請求已經成功建立了合法的 shipment，此時訂單狀態與那筆合法紀錄是一致的，只是「輸掉競態的那個請求」正確地拿到 409，不是資料損毀。

這個順序下，`orders.fulfillment_status` 全程只透過受控入口 `UpdateFulfillmentStatus` 寫入，`shipping` package 不曾直接操作 `ent.Order`；`shipments.order_id` 的唯一索引是擋「同一訂單併發建立兩筆出貨紀錄」的最終仲裁者。這與 payment-integration D6 webhook idempotency 面對「多步驟非原子操作」時的設計哲學一致：不是靠一個巨大的跨 package 交易，而是靠冪等/唯一約束讓每一步失敗都收斂到安全狀態。

### D5: `shipping` package 結構與錯誤慣例

新增 `api/internal/shipping`，比照 `order`/`payment`/`catalog` 的既有慣例：`Service{Client *ent.Client; Orders *order.Service}`（唯一寫 `orders.fulfillment_status` 的路徑是 `Orders.UpdateFulfillmentStatus`），`ValidationError`/`ConflictError`/`ErrNotFound` 三種錯誤型別，`writeShippingError` 對應 `httpapi` 的 4xx 對應表（422/409/404，其餘 500）。

`ShippingMethod` 的 CRUD 方法（`CreateShippingMethod`/`GetShippingMethod`/`ListShippingMethods`/`UpdateShippingMethod`/`DeleteShippingMethod`）與 `Shipment` 的方法（`CreateShipment`/`AdvanceShipment`/`ListShipmentsAdmin`/`GetShipmentForMember`）都放在同一個 `Service`（不像 `payment`/`order` 各自獨立成 package，因為兩個資料表在領域上緊密相關——`shipping_methods` 是設定，`shipments` 是這個設定驅動的操作紀錄，拆成兩個 package 只會增加 import 複雜度，沒有既有先例要求 1 package = 1 table，`catalog` package 本身就同時管理 `Category`/`Product`/`ProductSKU` 三張表）。

### D6: 商家後台 API 形狀與 RBAC

比照 `CategoriesHandler`（CRUD）與 `PaymentHandler`（訂單子資源）的組合：

- `shipping_methods` CRUD：`GET/POST /shipping-methods`、`GET/PUT/DELETE /shipping-methods/{shippingMethodID}`，RBAC `shipping_method.view`/`shipping_method.create`/`shipping_method.edit`/`shipping_method.delete`（完全比照 `category.*` 四個節點的命名慣例）。
- 出貨紀錄：`POST /orders/{orderID}/shipments`（`shipment.create`）、`PUT /orders/{orderID}/shipments/{shipmentID}`（`shipment.update`）、`GET /orders/{orderID}/shipments`（`shipment.view`，回傳陣列——本階段恆為 0 或 1 筆，但比照 `payments` 列表端點的既有慣例回傳陣列形狀，兩者語意一致：「這個訂單底下的出貨/付款紀錄」都是概念上的一對多關係，只是出貨這邊目前業務規則把「多」收斂成「至多一」）。

`merchant_owner`/`editor` 兩個既有角色都授予 `shipping_method.*` 全部節點與 `shipment.view`/`shipment.create`/`shipment.update`（比照它們現有對 `order.*`/`payment.*` 的授權方式，不特別為出貨紀錄設計比訂單管理更嚴格的角色分野）。

### D7: 不做公開唯讀 `shipping_methods` 端點

評估後決定本次不做。理由（比照 payment-integration D8 的「評估後拒絕」模式）：(1) 目前沒有任何消費端——storefront（`/web`）還沒有結帳頁 UI 會呼叫這個端點，公開一個沒有真實呼叫方、也就沒有真實使用情境驗證的唯讀端點，測試只能驗證「回傳資料形狀正確」，無法驗證它真的滿足未來 storefront 的需求；(2) 保持範圍精簡——之後真的要做 storefront 結帳頁串接物流方式選擇時，那次改動本來就需要重新盤點回應形狀（例如是否要附加 estimated delivery、是否要依會員地址篩選可用物流方式），現在先做只是提前鎖定一個可能要重改的介面形狀。

### D8: 為何運費不進 `orders.total_amount`——與 checkout 交易邏輯的邊界

`order.Service.Checkout` 的庫存扣減是全系統唯一經過 `-race` 驗證的並發關鍵路徑（order-management 交接明確標註為最脆弱區塊），`total_amount` 在那個交易內一次性計算為 `Σ price_amount * quantity` 並永久快照，之後任何程式碼都不重新計算它（order-management design：`order_items` 建立後不再變動，快照永不過期）。本變更完全不修改 `Checkout`；`shipping_methods.flat_rate` 本階段是純粹的商家後台設定資訊，不出現在 `orders` 表的任何欄位、不影響任何既有金額計算路徑。未來若要讓運費影響訂單應付總額，那將是一個需要重新設計「訂單建立時如何選擇物流方式並計入金額」的獨立變更（可能牽動 checkout 流程本身，屬於高風險改動，不適合在本次順手夾帶）。

### D9: Config / wiring

不需要新增任何 config 值——不像 payment-integration 的 mock provider 需要一把 HMAC 密鑰，本變更沒有簽章驗證機制（商家後台操作全部透過既有 AdminMW JWT + RBAC 驗證身分，不是像 webhook 那樣沒有 ambient 身分的路徑），所以沒有類似 `PAYMENT_MOCK_WEBHOOK_SECRET` 的新增環境變數。

`app/wire.go` 組裝：
```go
shippingService := &shipping.Service{Client: client, Orders: orderService}
deps.Shipping = &httpapi.ShippingHandler{Client: client, Service: shippingService, Authz: authz, Log: a.log}
```

### D10: `tenantOwned` 註冊與 migration

`api/internal/tenant/enttenancy.go` 的 `tenantOwned` map 新增 `"ShippingMethod": true, "Shipment": true`（兩者皆直接帶 `shop_id` 欄位，比照 `Payment`/`Order` 的既有模式）。Migration 透過既有 `make migrate-gen name=add_shipping_logistics` 流程（Atlas diff engine，對照本機開發用 Postgres，本環境 docker compose 已啟動可直接執行）產生 up/down SQL，不手寫。

## Risks / Trade-offs

- **[D4 的步驟 2/3 之間如果服務重啟或崩潰]** → `fulfillment_status` 已被推進為 `shipped`，但 `shipments` 列尚未寫入——訂單看起來「已出貨」但查不到追蹤資訊。這是示範性參考實作接受的已知風險（與 payment-integration D6 承認的「CAS 贏家呼叫 `UpdatePaymentStatus` 後又崩潰」風險同一等級）；正式環境應加背景對帳或改用真正的分散式交易，不在本變更範圍。實務上這個窗口極短（兩個 API 呼叫之間沒有外部 I/O 或使用者互動），且商家後台介面上可以人工重試（重試會因為 `fulfillment_status` 已非 `Unfulfilled` 而收斂到明確的 409，不會產生第二筆 shipment）。
- **[`shipments.status` 與 `orders.fulfillment_status` 共用同一組數字（D2）]** → 若未來 `orders.fulfillment_status` 需要新增一個 `shipments` 表無法對應的中間狀態（例如「已通知物流商但尚未實際攬收」），這個共用假設會被打破，需要屆時拆成兩組獨立列舉並補上映射層。本階段的「建立出貨紀錄=出貨事件本身」模型不需要這個中間狀態，暫不預先設計。
- **[不支援部分出貨]** → 大宗訂單商家若想分批出貨，本階段無法表達，只能等全部品項備齊後一次出貨，或商家自行在 `tracking_number` 欄位用文字註記多個追蹤碼（權宜作法，不是本變更設計的一部分）。

## Migration Plan

新增 `shipping_methods`、`shipments` 兩張表為純新增（無既有資料遷移），走既有 `make migrate-gen name=add_shipping_logistics` 流程從 ent schema diff 產生 up/down SQL。Rollback：`down` migration drop 兩張新表；`shipping_method.*`/`shipment.*` permission 節點與角色授權是 seed 資料，重跑 `make seed` 冪等更新，回滾只需回退程式碼版本（seed 不會主動刪除已授權的 permission，符合既有 seed 慣例）。
