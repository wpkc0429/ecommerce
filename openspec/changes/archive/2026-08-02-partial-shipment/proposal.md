## Why

出貨的粒度今天是**整筆訂單、至多一筆出貨**——`shipments.order_id` 上有唯一索引、`CreateShipment` 的簽名裡沒有品項也沒有數量。花藝訂單常態是「花瓶今天走、鮮花週五走」，商家只能二選一：整筆押到最後一天一起出，或先標記整筆已出貨然後對顧客說謊。

這條不變式是被刻意記下來、並被寫進三個地方的（`admin-commerce-operations` 的 tripwire #1、`order-notification-email` 的 design D12、`CLAUDE.md`），因為每一個踩到它的人都必須先知道它存在。本 change 的內容就是打破它，因此它的第一項工作是**評估相容性**：哪些既有承諾會變、既有消費端（ONDO 讀 `orders.fulfillment_status`）看得見什麼、哪些是 additive 哪些不是。

## What Changes

- 新增 `shipment_items`（`shipment_id` × `order_item_id` × `quantity`），一筆出貨自此可以只裝訂單的一部分。
- `order_items` 新增 `shipped_quantity`（預設 0，DB CHECK `0 <= shipped_quantity <= quantity`）。它是**超出訂購量的並發防護所需的那一欄**：唯一能以「條件式 UPDATE ＋ 檢查影響列數」表達的形狀，與 `order.Service.Checkout` 的扣庫存迴圈同構。
- **BREAKING（資料庫層，非 API 層）**：移除 `shipments.order_id` 的唯一索引。一筆訂單自此可有 N 筆出貨。
- `orders.fulfillment_status` **新增列舉值 `4`＝部分出貨**，並改為由該訂單所有出貨紀錄**推導**而得（既有的四個值語意逐字不變；單筆全額出貨的既有流程行為逐位元組相同）。
- `POST .../orders/{orderID}/shipments` 的請求主體新增**選填** `items`。**省略 `items` ＝ 出貨全部未出的品項**——今天每一個既有呼叫端因此行為完全不變。
- 新增兩條顧客可讀的複數端點：`GET /api/v1/shop/orders/{orderID}/shipments`、`GET /api/v1/shop/guest/order/shipments`。既有的**單數**端點（`.../shipment`）維持回傳單一物件，語意收斂為「最近一筆出貨」——它今天的 `.Only()` 在多筆出貨下會直接壞掉，這是本 change 必須修的既有路徑。
- 後台訂單詳情長出品項層級的出貨表單（每列數量、預設為未出數量）與多筆出貨清單；品項表格新增「已出貨／訂購」欄。
- **推翻** `admin-commerce-operations` 的「介面 MUST NOT 呈現任何已出貨數／待出貨數」——該禁令的理由是「系統裡沒有對應的事實」，本 change 建立了那個事實。
- **Non-Goal（明列，理由見 design）**：部分退貨／部分退款、出貨通知信、出貨層級的運費或運送方式、出貨紀錄的刪除與作廢。

## Capabilities

### New Capabilities

（無。本 change 全部落在既有能力的既有邊界內。）

### Modified Capabilities

- `shipping-logistics`：出貨粒度由「整筆訂單、至多一筆」改為「品項層級、可多筆」；新增超出訂購量的並發安全防護；訂單 `fulfillment_status` 改為推導值並新增 `4`＝部分出貨；新增顧客可讀的複數出貨端點。
- `order-management`：`order_items` 不再是完全不可變的快照——`shipped_quantity` 是其上唯一可變的欄位，且只能由出貨路徑寫入；取消訂單的三軸初始值規則新增「部分出貨的訂單不可取消」這個直接後果。
- `admin-operations-ui`：出貨介面由「整筆訂單、至多一筆」改為品項層級的部分出貨，並如實呈現已出貨／未出數量。
- `member-tiers-and-points`：退貨扣回的**觸發點**由「訂單被標記退貨」明確化為「任一筆出貨被標記退貨」，並明列其已知的過寬之處（部分退貨仍扣回整筆訂單的點數）。

## Impact

- **資料庫**：新表 `shipment_items`；`order_items` 新增一欄；`shipments` 移除一個唯一索引。一支 versioned migration，無資料回填（既有出貨紀錄的 `shipment_items` 為空＝「整筆出貨」的歷史語意，由推導函式吸收）。
- **API**：`api/openapi/openapi.yaml` 與 `api/openapi/v1-surface.txt` 新增兩條路由與若干 schema 屬性（**只增行**）。`Shipment` schema 新增 `items`，`OrderItem` schema 新增 `shipped_quantity`。
- **程式碼**：`api/internal/ent/schema/{shipping,order}.go`、`api/internal/shipping/service.go`（`CreateShipment`／`AdvanceShipment` 改為交易式）、`api/internal/order/service.go`（新增交易式的 fulfillment 更新入口）、`api/internal/httpapi/{shipping,guest,order}.go`、`api/internal/tenant/enttenancy.go`。
- **後台**：`admin/views/orders.js`、`admin/index.html`。**不換框架**（見 design 的 tripwire 小節）。
- **既有消費端**：ONDO 讀 `orders.fulfillment_status` 並以陣列索引取標籤，遇到 `4` 會顯示「未知」——不會崩、但也不會正確，需要對方加一個標籤。此為本 change 唯一對外可見的語意變化，design 正面處理。
- **文件**：`CLAUDE.md` 中「一單至多一筆出貨」那條記錄與 `order-notification-email` design D12 的交棒條件必須同步更新為現況。
