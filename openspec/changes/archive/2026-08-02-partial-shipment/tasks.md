## 1. 資料模型與遷移

- [x] 1.1 `ent/schema/shipping.go`：新增 `ShipmentItem`（`shop_id`／`shipment_id` FK／`order_item_id` FK／`quantity`），`UNIQUE(shipment_id, order_item_id)`、`index(shop_id, shipment_id)`、CHECK `quantity > 0`
- [x] 1.2 `ent/schema/shipping.go`：把 `Shipment` 的 `index.Fields("order_id").Unique()` 改為非唯一，並改寫該型別的註解（今天寫著「at most one per order」與唯一索引是仲裁者）
- [x] 1.3 `ent/schema/order.go`：`OrderItem` 新增 `shipped_quantity`（int32，預設 0）＋ CHECK `order_items_shipped_quantity_check`：`shipped_quantity >= 0 AND shipped_quantity <= quantity`
- [x] 1.4 `tenant/enttenancy.go`：登記 `ShipmentItem`
- [x] 1.5 `cd api && go generate ./internal/ent`
- [x] 1.6 `make migrate-gen name=add_partial_shipment`，**逐行讀過** `.up.sql`：確認 `DROP INDEX`（唯一索引移除）與兩個 CHECK 真的產出來了；沒有就手寫（atlas 對 CHECK 以名稱比對、對索引不可假設）
- [x] 1.7 在該 migration 的 `.up.sql` 手動補上**回填**：為每一筆既有 `shipments` 插入涵蓋該訂單全部 `order_items` 的 `shipment_items`，並把有出貨的訂單其 `order_items.shipped_quantity` 設為 `quantity`；`.down.sql` 對稱回復
- [x] 1.8 `make migrate` 通過

## 2. 領域邏輯（order 套件）

- [x] 2.1 `order.Service` 新增 `UpdateFulfillmentStatusTx(ctx, tx, shopID, orderID, status)`；與既有 `UpdateFulfillmentStatus` 共用一個以 `*ent.OrderClient` 為參數的私有實作（`tx.Order` 與 `client.Order` 同型別），既有方法的簽名與行為原樣不動

## 3. 領域邏輯（shipping 套件）

- [x] 3.1 新增出貨品項輸入型別與 `deriveFulfillmentStatus` 純函式（design D2 的五個分支），並為該函式寫單元測試（無需 DB）涵蓋五個分支與「單筆全額出貨的值序列與今天相同」
- [x] 3.2 `CreateShipment` 改為單一 `ent.Tx`：交易開頭 `ForUpdate()` 鎖訂單列 → 驗證（已取消／已有退貨／無未出數量）→ 解析品項（省略＝全部未出、空陣列＝422、外訂單品項＝422、重複指名＝422）→ 每列條件式 UPDATE `shipped_quantity` ＋ 檢查影響列數（超量→422）→ 建立 `shipments` 與 `shipment_items` → 推導並經 `UpdateFulfillmentStatusTx` 更新 → commit
- [x] 3.3 `AdvanceShipment` 改為單一 `ent.Tx`：鎖訂單列 → 既有的 status CAS → 推導並更新訂單 `fulfillment_status`（**不再**直接寫入該筆出貨的目標狀態）→ commit → commit 之後才發佈 `events.OrderReturned`
- [x] 3.4 `ListShipmentsAdmin`：移除「0 or 1 row this phase」註解，載入 `shipment_items`，順序維持 id 遞減
- [x] 3.5 `GetShipmentForMember`：`.Only()` 改為 `Order(ByID desc).First()`（最近一筆），並載入 `shipment_items`
- [x] 3.6 新增 `ListShipmentsForMember`（顧客可讀的複數形），順序 id 遞減，尚無出貨回空集合而非 ErrNotFound
- [x] 3.7 更新 `shipping` 套件與 `Shipment` schema 的所有註解：不再有「一單至多一筆」，改述新的仲裁者（`order_items` 上的條件式 UPDATE ＋ CHECK）

## 4. HTTP 層與契約

- [x] 4.1 `httpapi/shipping.go`：`createShipmentRequest` 新增選填 `items`（`*[]…` 以分辨省略與空陣列）；`shipmentJSON` 新增 `items`；新增 `GET /orders/{orderID}/shipments`（會員）handler
- [x] 4.2 `httpapi/order.go`：`orderItemJSON` 新增 `shipped_quantity`
- [x] 4.3 `httpapi/guest.go`：新增 `GET /guest/order/shipments`
- [x] 4.4 `api/openapi/openapi.yaml`：新增 `ShipmentItem` schema；`Shipment` 新增 `items` 並移除「一張訂單最多一筆」的敘述；`OrderItem` 新增 `shipped_quantity`；`createShipment` 請求主體新增選填 `items`；新增兩條路徑；更新受影響端點的敘述
- [x] 4.5 `api/openapi/v1-surface.txt`：**只增行**（新路由、其參數行與 optional 行、`Shipment.items`、`ShipmentItem.*`、`OrderItem.shipped_quantity`）。確認 diff 中**沒有任何刪除行**

## 5. 測試（Go）

- [x] 5.1 `shipping/service_integration_test.go`：部分出貨、補出剩餘、超量被拒（422）、空陣列被拒、外訂單品項被拒、重複指名被拒、已退貨後不得再出貨（409）、全部出完後再出貨（409）
- [x] 5.2 同上：`Σ shipment_items.quantity == order_items.shipped_quantity`（跨多筆出貨）
- [x] 5.3 同上：多筆出貨下 `fulfillment_status` 的推導（4 / 1 / 2 / 3 各一）與「單一出貨送達不使訂單變已送達」
- [x] 5.4 同上：既有的單筆全額出貨測試**一條都不改**、全部仍綠（D5 的相容性證明）
- [x] 5.5 `httpapi/shipping_integration_test.go`：省略 `items` 的既有請求逐字不變仍為 201；新的複數端點（會員）；單數端點在多筆出貨下回最近一筆（**不是** 404）
- [x] 5.6 `httpapi/guest_integration_test.go`：訪客複數端點
- [x] 5.7 `httpapi/order_integration_test.go`：部分出貨的訂單取消回 409（`CancelOrder` 一行未改）
- [x] 5.8 `points/service_integration_test.go`：同一訂單第二筆出貨退貨不重複扣回
- [x] 5.9 遷移回填的驗證：一支整合測試建立「舊形狀」的出貨（無 `shipment_items`、`shipped_quantity=0`）後斷言推導值會偏離，並以回填後的資料斷言推導值 == 儲存值

## 6. 後台介面（不換框架）

- [x] 6.1 `admin/index.html`：訂單品項表格新增「已出貨」欄；訂單列表的出貨狀態篩選新增「部分出貨」選項
- [x] 6.2 `admin/views/orders.js`：`FULFILLMENT_STATUS` 新增 `4: "部分出貨"`
- [x] 6.3 `admin/views/orders.js`：品項表格顯示 `shipped_quantity / quantity`
- [x] 6.4 出貨區塊改為「多筆出貨清單（各自帶裝載品項與各自的終態按鈕）＋ 建立出貨表單」；表單以 `repeater()` 逐項列出可出數量（預設與 max 皆為未出數量），**每列 `read()` 閉包在自己的 input 上**，收集時不做任何 DOM 查詢
- [x] 6.5 表單提供「整筆出貨」路徑（不送 `items`）與「指定品項出貨」路徑；已退貨的訂單不提供建立出貨並說明原因；標記退貨的確認文案補上「該訂單之後無法再建立出貨」
- [x] 6.6 若 `orders.js` 超過約 400 行（tripwire #5），把出貨區塊拆成 `admin/views/order-shipments.js` 由 `orders.js` 匯入——**檔案切分，不得新增 `lib/` 抽象**

## 7. E2E

- [x] 7.1 `e2e/commerce-path.mjs`：部分出貨鏈（兩品項訂單 → 只出一項 → 訂單 `4` ＋ `shipped_quantity` 正確 → 顧客複數端點看到一箱 → 出第二項 → 訂單 `1` → 送達 → `2`），外加超量被拒（422）與單數端點回最近一筆
- [x] 7.2 `e2e/happy-path.mjs`：既有 ⑰ 節整筆出貨的斷言**一條都不刪**；新增後台部分出貨的畫面斷言（品項表格顯示已出貨數、出貨狀態徽章為「部分出貨」）

## 8. 文件與紀錄

- [x] 8.1 `CLAUDE.md`：改寫「一單至多一筆出貨」那條記錄（在 `admin-commerce-operations` 段落內）為現況，並記錄 tripwire #1 已引爆、已重評、結論為不換框架與其理由
- [x] 8.2 `CLAUDE.md`：更新 `order-notification-email` D12 那條記錄——假設已被推翻，出貨通知信的預設答案原樣保留為後續項
- [x] 8.3 `openspec/changes/archive/2026-08-01-order-notification-email/design.md`：在 D12 末尾附加**標明日期的修訂**（原文一字不動），寫明假設已被 `partial-shipment` 推翻
- [x] 8.4 `docs/storefront-integration.md`：若其中提及出貨端點或 `fulfillment_status` 列舉，補上 `4` 與複數端點

## 9. 閘門與量測

- [x] 9.1 `make test`（單元 ＋ 契約 ①②④）全綠
- [x] 9.2 `make test-int`（整合 ＋ 契約 ③ ＋ 接線 ⑤⑥⑦）全綠
- [x] 9.3 `make openapi` 四道檢查全綠
- [x] 9.4 `cd api && golangci-lint run`（若 PATH 有）
- [x] 9.5 `make seed-demo` ＋ 三個服務，`node happy-path.mjs` 與 `node commerce-path.mjs` 兩支全綠（先確認 `:8080` 沒有殘留的舊 server）
- [x] 9.6 量測並記錄 tripwire **#4′**（本次 `lib/` ＋ `app.js` 淨增 vs `views/` 淨增的 1/3）與 **#5**（最大 `views/*.js` 行數），把實測數字寫回 design D11

## 10. 實作備註（落地時與計畫的差異，以及計畫外必須做的事）

計畫本身沒有被推翻，但有三項偏差與三項計畫外的工作，記在這裡而不是默默做掉。

**偏差（做了，位置與計畫不同）**

- 5.7／5.8 的測試落在 `httpapi/shipping_integration_test.go` 與 `shipping/service_integration_test.go`，不是計畫寫的 `order_`／`points_` 檔。理由是 fixture：兩者都需要「一張訂單、多筆出貨」的裝置，而那套裝置在 shipping 的檔案裡。
- 5.9 的回填驗證改寫成 **A/B 且兩邊都走真正的服務路徑**（`AdvanceShipment`），並且**從 migration 檔案讀出那段 SQL** 而不是抄一份進測試。抄一份的版本在有人改了 migration 之後會永遠是綠的，而那正是這支測試唯一想抓的事。
- 6.6 的拆檔**做了**（`views/order-shipments.js`）。#5 因此沒有響——不拆的話 `orders.js` 會是約 539 行。數字見 design D11。

**計畫外（不做就會出事）**

- [x] **`order_items` 的排序修正（四處）**。`shipped_quantity` 是這張表第一個會被 UPDATE 的欄位，而 Postgres 把更新後的 tuple 寫在 heap 尾端，所以無排序的 `WithItems()` 會在每次出貨後把剛出貨的那一行排到最後——顧客的訂單品項順序會每出一箱洗一次牌。修法是 `order.orderItemsByID`（3 處）＋ `notify`（1 處）。**這是跑測試時才發現的，不是設計時預見的**；完整記述見 design 相容性評估 ⑤。整合測試與 e2e 各加一條順序斷言。
- [x] **並發正確性的兩支測試 ＋ 一次反事實**。`TestConcurrentShipmentsNeverExceedTheOrderedQuantity`（12 併發打一條 5 件的品項，`-race -count=5`）與 `TestConcurrentShipmentsOfDisjointLinesStillCompleteTheOrder`（鎖自己的測試）。拿掉 `ForUpdate()` 重跑，後者 6 次紅 2 次、前者全綠——與 D4 的預測逐字相符。
- [x] **租戶隔離的結構性檢查**（design D14）。登記 `ShipmentItem` 只解決這一次；`enttenancy_internal_test.go` 讓「下一張帶 `shop_id` 的表忘了登記」直接轉紅。已實測拿掉那一行會紅。另補一支整合測試驗真實行為，含 `WithItems()` eager-load 的範圍化。

**修掉的既有缺陷**：`shipping/service_integration_test.go` 在接手時**無法編譯**（缺 `io`／`points`／`entpointtransaction` 三個 import，且回填測試裡有一段不存在的 `testutil.ExecSQL` 與一個空轉的 no-op guard）。`go build ./...` 會過而 `go vet ./...` 不會——測試在接手前一次都沒跑過。
