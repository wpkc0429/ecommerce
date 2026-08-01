## Context

使用者的原話是「**評估兼容部分出貨功能**」，所以這份 design 的第一節是評估，不是方案。

今天的系統事實是「**一筆訂單至多一筆出貨，粒度是整筆訂單**」，而這件事被三個地方各記了一次，因為每一個踩到它的人都得先知道它存在：`admin-commerce-operations` 的 tripwire #1、`order-notification-email` 的 design D12（那一節的標題就寫著「給 `partial-shipment` 的交棒」）、以及 `CLAUDE.md`。本 change 就是把它引爆的那一個。

---

## 相容性評估

### ① 現況的不變式，逐項查證

| # | 不變式 | 證據（查證過，非引述） |
| --- | --- | --- |
| 1 | 一筆訂單至多一筆出貨 | `shipments` 的 `index.Fields("order_id").Unique()`（`ent/schema/shipping.go`） |
| 2 | 出貨沒有品項概念 | `CreateShipment(ctx, shopID, orderID, carrier, trackingNumber)` — 簽名裡沒有品項也沒有數量 |
| 3 | 出貨清單至多一列 | `ListShipmentsAdmin` 的註解「0 or 1 row this phase」 |
| 4 | 訂單的出貨狀態就是那一筆出貨的狀態 | `AdvanceShipment` 直接 `UpdateFulfillmentStatus(…, target)`，不做任何推導 |
| 5 | 只有未出貨的訂單能建立出貨 | `CreateShipment` 的 `if ord.FulfillmentStatus != FulfillmentStatusUnfulfilled → 409` |
| 6 | 顧客端「這筆訂單的出貨」是單數 | `GetShipmentForMember` 以 `.Only(ctx)` 取一列；兩條 v1 端點的路徑就叫 `/shipment` |
| 7 | `fulfillment_status` 只有 0/1/2/3 | spec shipping-logistics「enumeration is finalized by this change」；DB CHECK 只有 `>= 0`（**寬鬆的**，見下） |
| 8 | 後台不得呈現「已出貨數／待出貨數」 | spec admin-operations-ui 的明文禁令 |

本 change 全部推翻 1–6 與 8，並**擴充**（不改寫）7。

### ② 三條會靜默壞掉的既有路徑——它們是本 change 的義務，不是新功能

這一節是評估的核心：光是「讓 `shipments` 可以有多列」，就有三處既有程式碼**不會編譯失敗、不會噴錯、只會給出錯的答案**。

1. **`GetShipmentForMember` 的 `.Only(ctx)`**。ent 的 `Only` 在查到 >1 列時回錯，而這裡的錯誤處理是 `return nil, ErrNotFound` ——所以一筆有兩個包裹的訂單，顧客查詢出貨會拿到 **404**。兩條 v1 端點（`GET /shop/orders/{orderID}/shipment`、`GET /shop/guest/order/shipment`）都吃這條路徑。**一個成功建立了兩筆出貨的商家，會讓他的顧客查不到任何一筆。**
2. **`AdvanceShipment` 把訂單的 `fulfillment_status` 直接設成該筆出貨的狀態**。三個品項分兩箱寄，第一箱送達 → 整筆訂單變成「已送達」，而第二箱還在路上。
3. **`CreateShipment` 的守衛**（不變式 5）。它會讓第二次出貨永遠回 409——功能在資料層可行之後，行為上仍然做不到。

還有第四條，性質不同但同樣要正面回答：

4. **`CancelOrder` 的三軸 CAS**。它要求 `fulfillment_status = 0`。如果部分出貨**停在 0**（一個看起來很吸引人的「不動既有列舉」方案），那麼一筆已經寄出兩箱的訂單**仍然可以被取消**，而取消會把**全部**品項的數量歸還庫存（`CancelOrder` 的 restock 迴圈走的是 `order_items.quantity`，不是未出貨數）。那不是顯示問題，是**帳面庫存憑空多出已經離開店裡的花**。這一條單獨就否決了「停在 0」（見 D1）。

### ③ 對既有消費端可見的變化

| 消費端 | 讀什麼 | 本 change 之後 | additive？ |
| --- | --- | --- | --- |
| ONDO（`bouquet/public/app.js:1038`） | `FULFILL_LABELS[o.fulfillment_status] \|\| '未知'` | 部分出貨的訂單顯示「**未知**」（`FULFILL_LABELS` 只有 4 個元素，索引 4 是 `undefined`） | 型別上是；**語意上不是**——正面承認，見 D1 |
| ONDO | `GET /shop/shipping-methods` | 不變 | — |
| ONDO | 訂單金額三件組 | 不變 | — |
| 任何呼叫 `POST .../shipments` 的既有客戶端 | body `{carrier, tracking_number?}` | **逐位元組不變**——省略 `items` ＝ 出貨全部未出品項（D5） | 是 |
| 任何呼叫 `GET .../shipments`（後台）的客戶端 | `{shipments: [...]}` | 陣列本來就是陣列，只是可能不只一列；新增 `items` 鍵 | 是 |
| 任何呼叫 `GET .../shipment`（單數，顧客）的客戶端 | 單一物件 | 單筆出貨的訂單**逐位元組不變**；多筆時回**最近一筆** | 是（相對於今天的 404，是修好而不是弄壞） |
| 資料庫 | `shipments.order_id` UNIQUE | 移除 | 否（但無外部可見面） |

**結論**：本 change 唯一對外可見的語意變化是 `orders.fulfillment_status` 多了一個值 `4`。所有其他變化要嘛是新增（新端點、新鍵、新選填參數），要嘛是修復一條本來就會壞的路徑。ONDO 需要的修改是**一行**（`FULFILL_LABELS` 補第五個元素），而在他們補之前，畫面顯示「未知」——難看，但不說謊。

**④ 上表已逐項在 `bouquet` 原始碼上覆驗（2026-08-01），三項修正**：

1. **`FULFILL_LABELS[4]` 顯示「未知」已證實**（`bouquet/public/app.js:1017-1018` 的字面陣列長度 4，唯一讀取點在 `:1038`，寫法是 `FULFILL_LABELS[o.fulfillment_status] || '未知'`，且 `esc()` 對 `undefined` 是 null-safe）。**不會崩**。附帶一個沒人注意過的脆弱處：那個 `||` 之所以安全，純粹因為沒有任何標籤是空字串——哪天有人把某個標籤改成 `''`，合法的 `0` 就會變成「未知」。
2. **ONDO 完全沒有讀任何出貨端點。** 全站只有五個 API 呼叫（`products`／`shipping-methods`／`guest/checkout`／`guest/order/payments`／`guest/order`），`shipment` 這個字在整個 repo 一次都沒出現。所以 D6「單數端點在多筆出貨下回 404」修的是一條**目前沒有外部消費者**的路徑——它仍然值得修（它是真的壞的，而且我們自己的 e2e 讀它），但上表把它列為「既有消費端」是**不準確的**，這裡更正。
3. **ONDO 也完全沒有讀 `order.items[]`**（訂單頁只讀 `id`／`payment_status`／`fulfillment_status`／`total_amount`）。這一點在下面的 ⑤ 變得重要。

### ⑤ 一條設計時沒有預見的相容性問題：`order_items` 變成可變的表

**這是本 change 唯一一個「不是新增、也不是修好一條壞路徑」的既有行為變化，而它是在跑測試時才發現的，不是推理出來的。**

`shipped_quantity` 是 `order_items` 上**第一個會被 UPDATE 的欄位**（在此之前這張表是純快照，寫一次就不再動）。而 `order.Service` 的三處與 `notify` 的一處都用 `WithItems()` 載入品項——**沒有 `Order()`**。在此之前這看起來一直是對的，因為 heap 的物理順序恰好等於插入順序。

Postgres 的 MVCC 會把更新後的 tuple 寫成一個新版本、放在 heap 尾端，所以一次部分出貨之後，seq scan 會把**剛出貨的那一行排到最後**。實測：

```
INSERT (1,2,3) → SELECT 回 1,2,3
UPDATE id=1    → SELECT 回 2,3,1
```

具體症狀：一張三列的訂單，出貨第二列之後顧客看到的品項順序變成 1,3,2；再出第一列變成 3,2,1。**不會噴錯，只會每出一箱就洗一次牌。**

修法是四處明訂 `Order(entorderitem.ByID())`（`order.orderItemsByID` ＋ `notify`）。**這不是防禦性整理**：專案早就寫下「回傳集合的端點必須明訂順序，不得交給資料庫決定」，本 change 只是讓資料庫第一次對這張表有了意見。整合測試（`TestShipmentPartialFlowOverHTTP`）與 e2e 各有一條斷言釘住順序。

**受影響範圍**：後台訂單詳情的品項表格、訂單確認信的品項列。**ONDO 不受影響**——它不讀 `order.items`（見 ④.3）。

---

## Goals / Non-Goals

**Goals:**

- 一筆訂單可以分多次出貨，每次指名品項與數量。
- **不可能出超過訂購量**，而且防護是並發安全的，形狀與系統裡唯一那份並發安全的扣減（`order.Service.Checkout` 的扣庫存迴圈）相同。
- **既有的單筆全額出貨流程逐位元組不變**：既有請求、既有回應、既有的 `fulfillment_status` 值序列（0→1→2／0→1→3）全部原樣。
- 既有資料在 migration 後，其**推導出的** `fulfillment_status` 等於它今天**儲存的**值——這是一個可以被測試斷言的性質，不是一句保證。
- 顧客看得到「哪些東西在哪一箱、到哪裡了」。
- 後台的部分出貨介面**不引入前端框架**。

**Non-Goals:**

- **部分退貨／部分退款**（D7）。
- **出貨通知信**（D9）——`order-notification-email` D12 的到期條件在本 change 成立，但那是一個獨立決定，本 change 只負責把它變成可搜尋的後續項。
- 出貨層級的運費、運送方式或包裹尺寸（`orders.shipping_fee` 仍是訂單層的單一快照）。
- 出貨紀錄的刪除、作廢、更正（沒有端點，本 change 也不加）。
- 「這張訂單全部出完了」的**事件**（沒有訂閱者；沒有消費者的事件不新增，D13）。
- `/web` 主題模式的前台呈現（該模式今天完全沒有電商 UI）。

---

## Decisions

### D1: `fulfillment_status` 新增 `4`＝部分出貨——三個候選逐一過篩，這一題正面回答

現行列舉：`0` 未出貨／`1` 已出貨／`2` 已送達／`3` 已退貨。三個品項出了兩個，訂單層是什麼？

**候選 A：停在 `0`（未出貨），全部出完才變 `1`。**
**否決，而且理由不是美感，是一個真的會發生的 bug**：`CancelOrder` 的 CAS 條件包含 `fulfillment_status = 0`，所以部分出貨的訂單**仍然可以被取消**，而取消會把 `order_items.quantity`（訂購量，不是未出量）全額歸還庫存。已經寄出去的兩箱花會在庫存上復活。要救它就得去改 `CancelOrder` 的 CAS——那是全系統第二危險的條件式 UPDATE，而改它的唯一理由是為了讓一個假的狀態值看起來沒事。**不合算，也不誠實。**

**候選 B：沿用 `1`（已出貨）。**
**否決。** 這是三個候選裡唯一**會說謊而且無法被偵測**的：ONDO 今天顯示「已出貨」，本 change 之後對一筆只出了三分之二的訂單**仍然**顯示「已出貨」，而它讀到的位元組完全沒變——它沒有任何辦法知道語意變了。這正是 `order-notification-email` D12 拒絕先寄出貨信的同一條理由（「一封排定要變成錯的信」），只是換成了一個排定要變成錯的欄位值。

**候選 C（採用）：新增 `4`＝部分出貨。**
既有四個值的語意**逐字不變**，既有的值序列一個都沒動。新值對 ONDO 是可偵測的：`FULFILL_LABELS[4]` 是 `undefined`，畫面顯示「未知」。**「未知」比「錯」好**，這是整個決定的一句話版本。

**「新增列舉值算不算破壞 v1？」——正面回答。**
D13 的破例窗口已經關閉，所以這個問題不能含糊。答案是：**在型別上是 additive（無鍵消失、無請求被拒、`v1-surface.txt` 一行都沒少、DB CHECK 是 `fulfillment_status >= 0` 本來就容得下）；在語意上它是一個既有消費端必須被告知的變化，因此本 change 把它當成後者處理**——寫進 spec、寫進本節、並在下面列出 ONDO 那一行的具體修法。

同時要說清楚**為什麼它不需要走 `/api/v2` 或新端點**（D13 對破壞性變更的規定）：破壞性變更的定義是「今天成立的呼叫在明天不成立」。這裡沒有任何呼叫變得不成立——變的是**現實**（訂單真的可以只出一部分了），而一個回報現實的欄位必須有能力回報新的現實。把它塞進 `/api/v2` 只會產生兩個 API 各說一半的真話。`payment_status` 的列舉已經有一個同構的先例：`order-management` 建立時只寫 `0`，`payment-integration` 之後往同一欄加值，DB CHECK 從第一天起就是寬鬆的 `>= 0`，**正是為了這件事**。

**ONDO 那一行的修法**（寫在這裡，因為它是這個決定的成本，不是附註）：

```js
var FULFILL_LABELS = ['未出貨', '已出貨', '已送達', '已退回', '部分出貨'];
```

`4` 排在最後而不是插在 `1` 之前，因為既有值的**數值**不可以動；列舉的閱讀順序與陣列順序不再一致，這是新增列舉值到既有欄位的固有代價。

**為什麼不是「部分送達」也給一個值**：沒有消費者，而且它會逼出一個真正的組合爆炸（部分出貨×部分送達×部分退貨）。訂單層的軸是**粗粒度摘要**；精確的事實在 `shipments` 清單裡，顧客與後台都讀得到。

### D2: `orders.fulfillment_status` 改為**推導值**，而推導規則的第一要求是重現歷史

寫入仍然只經 `order.Service` 的受控入口（spec order-management design D6 不變），但**寫什麼值**改由一個純函式決定：

```
derive(shipments, orderedQty, shippedQty):
  1. 沒有任何出貨               → 0 未出貨
  2. 任一筆出貨為已退貨          → 3 已退貨
  3. shippedQty < orderedQty    → 4 部分出貨      ← 新增的唯一分支
  4. 全部出貨皆為已送達          → 2 已送達
  5. 其餘                       → 1 已出貨
```

其中 `orderedQty = Σ order_items.quantity`，`shippedQty = Σ order_items.shipped_quantity`。

**這個規則的第一個驗收條件是「單筆全額出貨時，它產生的值序列與今天逐位元組相同」**，逐條驗算：一筆涵蓋全訂單的出貨建立後（未退、shipped==ordered、非全送達）→ `1` ✓；標記送達後 → `2` ✓；標記退貨後 → `3` ✓。**沒有出貨的訂單走分支 1 → `0`** ✓。也就是說：**今天的行為是這個函式的一個特例**，不是「大致相容」。

**分支 2（退貨壓過一切）的理由**：訂單層沒有「部分退貨」的詞彙（D7 是 Non-Goal），而 `events.OrderReturned` 的點數扣回本來就是整筆訂單的全有全無。讓一筆退貨把整筆訂單標成已退貨，是**與既有機制一致**的粗答案；精確的事實在出貨清單上。它的代價寫在 Risks。

**分支 3 在分支 4 之前的理由**：「出了三分之二，而那三分之二已送達」的訂單，對顧客而言是**還沒收齊**。先報「部分出貨」比先報「已送達」誠實。

**歷史資料**：migration 會為每一筆既有出貨回填涵蓋整張訂單的 `shipment_items`，並把 `order_items.shipped_quantity` 設為 `quantity`（見 Migration Plan）。回填之後，**每一筆既有訂單的推導值等於它儲存的值**——這是一條被整合測試斷言的性質，不是一句話。沒有這個回填，一筆歷史上已送達的訂單在下一次推導時會掉進分支 3 變成「部分出貨」。

### D3: `order_items.shipped_quantity` 是一個欄位，不是每次從 `shipment_items` 加總——**因為只有欄位能做成條件式 UPDATE**

問題是「不能出超過訂購量，而且要並發安全」。系統裡唯一一份並發安全的條件式扣減長這樣（`order.Service.Checkout`）：

```go
affected, _ := tx.ProductSKU.Update().
    Where(entproductsku.IDEQ(id), entproductsku.ShopIDEQ(shopID), entproductsku.StockQtyGTE(qty)).
    AddStockQty(-qty).Save(ctx)
if affected == 0 { /* 庫存不足 */ }
```

要照這個形狀做，**謂詞必須落在一個欄位上**。於是：

```go
affected, _ := tx.OrderItem.Update().
    Where(entorderitem.IDEQ(itemID), entorderitem.OrderIDEQ(orderID), entorderitem.ShopIDEQ(shopID),
          entorderitem.ShippedQuantityLTE(it.Quantity-n)).   // shipped + n <= quantity
    AddShippedQuantity(n).Save(ctx)
if affected == 0 { /* 超出訂購量 */ }
```

**「每次從 `shipment_items` 加總」這個替代方案不是比較慢，是做不到**：一個 `SUM()` 進不了 `UPDATE … WHERE` 的謂詞而不引入 `SELECT … FOR UPDATE` 或應用層 read-then-write，而後者正是本專案反覆拒絕的東西（`checkout-idempotency` D3、`shipments(order_id)`、`point_transactions(order_id, kind)` 都是同一條紀律：**由資料庫仲裁**）。

衍生欄位會漂移，這是真的。三道防線，其中兩道在資料庫裡：

1. **唯一寫入路徑**：只有 `shipping.Service` 在建立出貨的那個交易裡寫它。`order` 套件不碰、後台沒有端點、`CancelOrder` 不碰（它只在 `fulfillment_status = 0` 時成立，那時 `shipped_quantity` 恆為 0）。
2. **DB CHECK `order_items_shipped_quantity_check`：`shipped_quantity >= 0 AND shipped_quantity <= quantity`**。就算謂詞哪天被寫錯，交易會**中止**而不是寫進一個不可能的數字——與 `orders_total_amount_composition_check` 是同一個思路（把不變式放進資料庫，而不是放進一段別人可能忘記重現的 Go 算術）。
3. **整合測試斷言 `Σ shipment_items.quantity == order_items.shipped_quantity`**，跨多筆出貨。這是那道「衍生欄位會漂移」的直接反制。

**`shipped_quantity` 不因退貨而回減**。理由有二：退貨在本 change 之後不允許再出貨（D7），所以回減沒有消費者；而它一旦可回減，第 2 條的 CHECK 就不再是一個單調不變式，「已出過多少」與「現在手上有多少」會變成兩個概念——那是部分退貨要處理的事。

### D4: 出貨操作改為交易式，並在交易開頭鎖住訂單列

`CreateShipment` 今天**完全不是交易**（先推進訂單狀態、再 insert，靠唯一索引仲裁，輸掉的那一邊留下一個「已經正確」的狀態）。有了品項扣減之後這行不通：品項扣減與出貨紀錄必須同生共死。

所以兩個方法都改成單一 `ent.Tx`，並在交易的第一件事就是：

```go
tx.Order.Query().Where(IDEQ(orderID), ShopIDEQ(shopID)).ForUpdate().Only(ctx)
```

**為什麼需要這個鎖，而條件式 UPDATE 不夠**：條件式 UPDATE 保證的是「不會超出訂購量」，它是對的且不需要鎖。但**推導出來的訂單層狀態**不是。實際會發生的競態：訂單有 X、Y 兩個品項各 1 件，交易 A 出 X、交易 B 出 Y，兩者鎖的是**不相交**的 `order_items` 列，因此可以完全平行；在 READ COMMITTED 下，A 看不到 B 未提交的 `shipped_quantity`，反之亦然，於是**兩邊都推導出「部分出貨」並各自寫入**——最終訂單全部出完了，狀態卻停在 `4`。這不是理論：它只需要商家開兩個分頁。

訂單列鎖把「同一張訂單上的出貨操作」序列化，代價是同訂單的並發出貨會排隊——那是每天個位數次的操作。**兩道防護都保留**：條件式 UPDATE ＋ 影響列數是「不會超出訂購量」的仲裁者（即使鎖被誰拿掉了它仍然成立），鎖是「推導值正確」的條件。

**上面這兩句話已經分別用測試證明，不是靠讀程式碼相信的**（2026-08-01）：

- `TestConcurrentShipmentsNeverExceedTheOrderedQuantity`——12 個並發請求各出 1 件、打一條訂購 5 件的品項：**恰好 5 個 201、7 個被拒**，`shipped_quantity` 收在 5、`Σ shipment_items.quantity` ＝ 5、`shipments` 恰好 5 列，最終 `fulfillment_status` ＝ 1（不是 4）。`-race` ＋ `-count=5` 全綠。
- `TestConcurrentShipmentsOfDisjointLinesStillCompleteTheOrder`——兩個 goroutine 出**不同**品項，兩者都必須 201，且最終訂單必須是 `1`。**這一條是鎖自己的測試**，因為它與超量無關（兩邊不爭用同一列 `order_items`，條件式 UPDATE 根本沒被考驗到）。
- **反事實已實測**：把 `ForUpdate()` 拿掉重跑 `-count=6`，上面第二條 **6 次中紅 2 次**，訊息正是「兩條都出完了，訂單卻停在 4」；第一條（超量）**仍然全綠**——與 D4 的預測逐字相符：條件式 UPDATE 沒有鎖也成立，鎖保護的是別的東西。

鎖的順序：本路徑是 `orders` → `order_items` → `shipments`／`shipment_items`，不觸碰 `product_skus`，因此不與 `Checkout`（`product_skus` → `orders`）或 `CancelOrder`（`orders` → `product_skus`）之間製造新的環——`CancelOrder` 與本路徑都先鎖 `orders`，順序一致。

**`order.Service` 因此新增一個交易式的受控入口** `UpdateFulfillmentStatusTx(ctx, tx, shopID, orderID, status)`。既有的 `UpdateFulfillmentStatus` 簽名與行為原樣保留（它仍是 spec 指名的入口，也仍有測試直接驗它），兩者共用同一個以 `*ent.OrderClient` 為參數的私有實作——`tx.Order` 與 `client.Order` 在 ent 裡是同一個型別，所以這不是抽象層，是一個參數。**`shipping` 套件依然一行都不寫 `ent.Order`。**

### D5: **省略 `items` ＝ 出貨全部未出的品項**——這是整個相容性的支點

`POST .../orders/{orderID}/shipments` 的請求主體今天是 `{carrier, tracking_number?}`。v1 只做 additive 變更，所以這個請求**必須繼續成立**。它繼續成立的條件是「省略 `items` 有一個定義好的意思」，而那個意思只有一個候選是對的：**出貨全部尚未出貨的品項**。

於是對一筆從未出貨的訂單，省略 `items` 的請求會建立一筆涵蓋整張訂單的出貨、把訂單推進到 `1`——**與本 change 之前逐位元組相同**。既有的 e2e、既有的後台、任何既有客戶端一個字都不用改。

而它同時是產品上最常用的那顆按鈕（花藝訂單多數是一次出完），所以這不是為了相容性硬湊的語意。

三個邊界：

- `items` **存在但為空陣列** → 422。「我要出貨，但一件都不出」不是省略，是矛盾。省略（`null`）與空陣列必須分辨，因此實作用 `*[]shipmentItemInput`。
- 沒有任何未出品項時省略 `items` → 409「訂單已全部出貨」。這取代了今天的「訂單已有出貨紀錄」。
- `items` 指名的 `order_item_id` 不屬於這張訂單 → 422（請求主體上的值，比照 `/items/{i}/sku_id` 對外店 SKU 的既有處置），不是 404。

### D6: 顧客端新增**複數**端點，單數端點收斂為「最近一筆」

`member-directory` 定過調：**沒有消費者的端點不新增**。所以要先回答有沒有消費者——有，而且是本 change 自己製造的：一筆訂單有兩個包裹時，單數端點只能回一個，顧客沒有任何管道看到另一個。這與 `shipping-logistics` D7 當初拒絕開放 `GET /shop/shipping-methods`、後來被 `order-extras` 推翻的形狀完全一樣：**前提變了**（那次是「商家自帶前端出現了」，這次是「一筆訂單可以有兩個包裹了」）。

- 新增 `GET /api/v1/shop/orders/{orderID}/shipments`（會員）
- 新增 `GET /api/v1/shop/guest/order/shipments`（訂單存取權杖）
- 兩者回 `{shipments: [...]}`，**依 id 遞減**（與後台端點同序，且「回傳集合必須明訂順序」）。尚未出貨時回 **200 ＋ 空陣列**，不是 404——一個清單端點的空集合是 200，這與單數端點的 404 語意不同而且都是對的。

**單數端點**（`.../shipment`）維持回傳單一物件，改以「最近一筆」定義（`ORDER BY id DESC LIMIT 1`）。單筆出貨的訂單逐位元組不變；多筆時它回最新的那一箱，並在契約裡寫明「要看全部請用複數端點」。**它今天在多筆下回 404**，所以這是修好一條會壞的路徑，不是弄壞一條好的。

**為什麼不把單數端點改成回陣列**：那會讓每一個既有呼叫端當場壞掉，正是 D13 禁止的事。

### D7: 部分退貨是 Non-Goal，而「整筆退貨」的語意確實變了——把它講完

**部分退貨不做。** 它需要的不只是一個 `returned_quantity` 欄位：點數的扣回量取自**那一筆訂單的核發列的 `points_delta`**（`member-tiers-and-points` 的明文要求，而且是刻意的——它讓改過折扣率的訂單退貨時扣回的仍是當初核發的數字）。要按品項退，就得能把那個 `points_delta` 分攤到品項，而分攤的口徑（含不含運費、折扣怎麼攤、無條件捨去往哪邊）是一整套新規則。金流那邊還有一份對應的部分退款。**那是一個 change，不是一個欄位。**

**「整筆退貨」的語意變了嗎？變了，而且要明說**：本 change 之前，「那一筆出貨退回」＝「整張訂單退回」，因為兩者是同一件事。之後，**一筆出貨退回會把整張訂單標成已退貨，並扣回整張訂單的點數**——即使那一箱只裝了三個品項中的兩個。這是過寬的，而它過寬的原因是點數扣回本來就是訂單粒度的。

因此本 change 加上一條配套限制：**任一筆出貨已退貨的訂單，不得再建立新的出貨**（409）。理由是狀態序列的可解釋性——若允許，訂單會走 `3`（已退貨、點數已扣回）→ `4`（部分出貨）→ `1`，而那條序列沒有任何下游能解釋，點數也已經扣光了。商家遇到「退了一箱、剩下的還是要出」的情況，正解是開一張新訂單；本 change 讓伺服器明確地說不，而不是安靜地產生一筆無法解釋的紀錄。

**觸發次數**：第二筆出貨被標記退貨時仍會發佈 `events.OrderReturned`，但 `point_transactions` 的 `UNIQUE(order_id, kind)` 讓它是 no-op（`HandleOrderReturned` 既有的冪等分支）。**這是既有機制免費提供的正確行為**，本 change 只補一條測試把它釘住。

### D8: 訂單取消——查證結果

現行 CAS 是 `status=0 AND payment_status=0 AND fulfillment_status=0` 的條件式 UPDATE ＋ 影響列數檢查。查證結論：

- 部分出貨的訂單 `fulfillment_status = 4`，CAS 匹配 0 列 → **409「order can no longer be cancelled」**。
- **`CancelOrder` 一個字都不用改**，而且這正是想要的行為：已經寄出去的東西不該被一鍵歸還庫存。
- **已跑過不是推理**（`TestPartiallyShippedOrderCannotBeCancelled`，2026-08-01）：兩列訂單出掉其中一列，會員打 `POST /orders/{id}/cancel` 回 **409**，訂單維持 `status=0`（未取消）。
- 這個結果**完全依賴 D1 的選擇**。若選了候選 A（停在 0），同一段程式碼會回 200 並多還庫存。**這是 D1 從「品味問題」變成「正確性問題」的地方**，也是本評估最有價值的一項發現。

（`order-management` 的 spec 因此只需要一條新場景，不需要改規則本身。）

### D9: 出貨通知信仍然不做，但 D12 到期了——把它變成可搜尋的後續項

`order-notification-email` design D12 的整節標題是「本 change 依賴的出貨粒度假設（給 `partial-shipment` 的交棒）」，它的到期條件是「`partial-shipment` 讓 `shipments` 長出品項的那一刻」。**那一刻就是現在。**

**本 change 仍然不寄出貨信**，理由是範圍紀律：寄不寄一封顧客可見的信是一個獨立決定（收件人、時機、內容、額度都要重新評估，而 D9 的 Gmail 額度天花板是**全平台共用**的），把它夾帶在一個資料模型 change 裡，只會讓兩件事都審不清楚。

但**留著過期的記錄比沒有記錄更糟**，所以本 change 負責：

1. 在 `order-notification-email` 的 design D12 末尾附上一段**標明日期的修訂**（原文一字不動），寫明假設已被推翻。
2. 更新 `CLAUDE.md` 裡「一單至多一筆出貨」與 D12 的那兩條記錄。
3. 把 D12 已經寫好的預設答案原樣轉成後續項：**每次出貨各寄一封、只列該次品項、`UNIQUE(order_id, kind)` → `UNIQUE(shipment_id, kind)`；`Sender`／分派模型／防重機制不動**。這三句話今天仍然成立，而且因為 `shipment_items` 已經存在，「只列該次品項」現在有對應的資料了。

### D10: admin tripwire #1——實測，結論是**不換框架**，而且理由不是紀律是資料模型

`admin-commerce-catalog` design D1 的推翻條件 #1 是「跨視圖衍生狀態」，而 `admin-commerce-operations` 明文寫著它「未觸發，理由是資料模型而不是克制……要做部分出貨得先讓 `shipments` 長出 line items，屆時 tripwire #1 才真正到期」。

**前提條件成立了，所以重新評估在寫任何 admin 程式碼之前執行。實測結論：#1 描述的那個危害不成立，而原因同樣是資料模型。**

當初的擔憂具體是什麼：「已出貨數／待出貨數」這種數字若要在瀏覽器裡維持，就得在**四個地方**手工同步（品項表格的欄位、出貨表單每列的預設值與上限、送出後的重繪、以及訂單層的狀態徽章），而那正是框架的反應式綁定在解的問題。

本 change 之後這件事的形狀變了：

- **「已出貨數」現在是伺服器上的一個欄位**（`order_items.shipped_quantity`，D3），由 `GET /orders/{id}` 一起回來。瀏覽器**不計算**它。
- 唯一在瀏覽器裡算的是 `remaining = quantity - shipped_quantity`，那是**一列之內的純函式**，`repeater` 的 `renderRow` 閉包裡一行。
- 出貨成功後的更新策略是 `loadDetail(o.id)` **整份重讀**（既有做法，本 change 不改）。所以不存在「四個地方要同步」——只有一個地方（伺服器）持有真相，畫面是它的一次性投影。

**反事實，寫下來因為它才是這一節的價值**：如果 D3 選了「每次從 `shipment_items` 加總」而不開那個欄位，後台就會被迫在瀏覽器裡把出貨清單攤平成「每個品項已出幾件」——那才是 tripwire #1 描述的東西，而且它會同時讓 D3 的並發防護做不成。**同一個決定同時解掉了並發問題與前端衍生狀態問題，這不是巧合：兩者都是「這個數字的權威在哪裡」的同一個問題。**

**落地後的實測證據**（不是設計時的推理）：全 `admin/` 只有**一行**瀏覽器端的數量算術——`order-shipments.js` 的 `remainingOf`。`shipped_quantity` 在 `orders.js` 與 `order-shipments.js` 各出現一次，兩次都是**直接顯示伺服器給的值**。沒有任何地方把出貨清單攤平成每品項已出數，也沒有任何地方在送出後就地修補畫面。E2E 有一條斷言專門守著 repeater 的配對：一張兩列的表單填 A=1、B=0，若兩列的 `read()` 沒有各自閉包在自己的 input 上（例如改用 `schemaform.js` 的 label-找-DOM 路徑），出貨的會是 B 而不是 A，而且**兩種結果的 HTTP 狀態碼完全一樣**。

**無論結論如何，本 change 都不換框架**——`CLAUDE.md` 明文：「若哪天真的要換框架，那必須是一個『內容就是換框架』的獨立 change，不是夾帶在功能裡。」本 change 用既有詞彙（`el()`／`repeater()`／`rows()`／`renderPager()`／`formatMoney()`／`can()`）交付，巢狀的出貨品項表單走 `repeater()`（每列 `read()` 閉包在自己的 input 上，**一次 DOM 查詢都不做**），**刻意不走** `lib/schemaform.js` 的 label-找-DOM 收集路徑——出貨品項正是「N 列 × 每列 M 個同名子欄位」的形狀，那條路徑在這裡會靜默配錯值。

### D11: tripwire #4′ 與 #5 的實測數字

基準（本 change 之前，實測）：

| | 行數 |
| --- | --- |
| `admin/lib/*.js` ＋ `admin/app.js` | **873** |
| `admin/views/*.js` | **1,720** |
| JS 小計 | 2,593 |
| `index.html` 449 ＋ `styles.css` 187 | 636 |
| **總計** | **3,229** |
| 最大單一視圖 `views/orders.js` | **375** |

**#5 響了要做什麼——事先定調，免得事後找理由**：#5 的意思是「這個檔案該拆了」，不是「該換框架了」。訂單詳情裡的出貨區塊已經是一個自足的東西（自己的資料、自己的表單、自己的權限判定），拆成 `admin/views/order-shipments.js` 並由 `orders.js` 匯入，是**檔案切分**而不是新的抽象層——它不會增加 `lib/`，因此也不會觸發 #4′。若拆完之後 `orders.js` 仍超過 400 行，就在 tasks 裡如實記錄並留給下一個 change，**不得**為了讓數字好看而把邏輯塞進 `lib/`（那會把 #5 的壓力轉移到 #4′，兩個 tripwire 都失去意義）。

#### 落地後的實測（2026-08-01）

| | 之前 | 之後 | 淨增 |
| --- | --- | --- | --- |
| `admin/lib/*.js` ＋ `admin/app.js` | 873 | **873** | **0** |
| `admin/views/*.js` | 1,720 | **1,909** | **+189** |
| `index.html` | 449 | 457 | +8 |
| `styles.css` | 187 | 201 | +14 |
| **總計** | 3,229 | **3,440** | +211 |

**#4′：未響。** `lib/` ＋ `app.js` 淨增 **0**，門檻是 `views/` 淨增 189 的 1/3 ＝ **63**。`admin/lib/` 一行未改，`app.js` 一行未改——後者是因為 `order-shipments.js` **不是一個視圖**（它沒有 tab、沒有路由、不進註冊表），只是 `orders.js` 匯入的一個模組。預測命中。

**#5：未響，但這是拆檔的結果不是壓力不存在。** 最大 `views/*.js` 是 `products.js` 的 **338**（本 change 未動它）；`orders.js` 從 375 **降到 306**，新檔 `order-shipments.js` **258**。**若不拆，`orders.js` 會是約 539 行**（306 ＋ 258 − 約 25 行的模組標頭與 deps 接線），遠超 400——所以 D11 的預測「#5 幾乎確定會響」是**對的**，只是處置在事前就決定好了，於是它以「已處理」而不是「已觸發」的形式收場。這正是事先定調的用途。

**共用詞彙一個都沒加。** 出貨品項表單用既有的 `repeater()`、清單用既有的 `el()`／`clear()`、權限用既有的 `can()`。`badge`／`formatTime` 由 `orders.js` **注入**而不是搬進 `lib/`——它們是訂單詳情那一頁的格式，而把它們搬進 `lib/` 會讓 #4′ 量到一筆本 change 並沒有產生的壓力。

**誠實記錄一筆刻意沒還的債**：`formatTime` 在 `views/` 已經有**四份**拷貝（`members`／`orders`／`points`／`storefront-clients`），這早就過了 tripwire #2 的「第三次重寫」門檻，按規則該進 `lib/`。本 change **刻意不順手做**——它與部分出貨無關，而且做了之後 #4′ 量到的就不是本 change 自己的壓力。留給下一個 admin change，連同 `label()`（兩份）一起。

### D12: 契約、鎖檔與檢查

- `openapi.yaml`：新增兩條路徑；`Shipment` schema 新增 `items`（`additionalProperties: false`，不加就會讓**檢查 ③** 對真實回應轉紅）；`OrderItem` schema 新增 `shipped_quantity`；`createShipment` 的請求主體新增選填 `items`；`Shipment` 的 description 移除「一張訂單最多一筆」。
- `v1-surface.txt`：**只增行**。新增 2 條路由行、其參數行與 optional 行、`Shipment.items`、`ShipmentItem.*`、`OrderItem.shipped_quantity`。**一行都不刪。**
- **檢查 ②（金額必須 `$ref` `MinorUnitAmount`）**：`shipment_items` 沒有任何金額欄位（出貨不帶錢，運費仍是訂單層的單一快照），所以不觸發。這是刻意的——一旦出貨長出金額，運費就會有兩個權威。
- **檢查 ①／⑥**：新增路由掛在既有的 `ShippingHandler`／`GuestHandler` 上，`Deps` 不變，所以路由自動被列舉；忘了寫進 `openapi.yaml` 會自動轉紅。
- **檢查 ⑤**：無新增 `Deps` 欄位。
- **檢查 ⑦**：**無新增訂閱者、無新增事件型別**，`expectedSubscribers` 一行不動。這是刻意的（D13）。

### D13: 不新增領域事件——以及它逼出的「e2e 要不要補斷言」

出貨是一件值得廣播的事，但**今天沒有訂閱者**：`notify` 的出貨信是 Non-Goal（D9），`points` 不對出貨反應，快取與出貨無關。`member-directory` 已經定調「沒有消費者的節點不新增」，同一條規則適用於事件。加了會有兩個具體代價：檢查 ⑦ 轉紅要有人改（而改它的意義是「請回答誰要用它」——今天沒有答案），以及一個沒有訂閱者的事件會讓下一個人以為出貨鏈已經接好了。

**e2e 要不要補斷言？要，而且與 D9 的情況相反**——本 change 的行為**完全在 e2e 的可觀測範圍內**（全是 HTTP API ＋ 後台畫面）。所以：

- `commerce-path.mjs` 補一段部分出貨鏈：兩個品項的訂單 → 只出一個 → 訂單為 `4`、`shipped_quantity` 正確 → 顧客的複數端點看得到一箱 → 出第二個 → 訂單為 `1` → 標記送達 → `2`；外加一條「出超過訂購量被拒（422）」。
- `happy-path.mjs` 既有的 ⑰ 節（整筆出貨）**一條斷言都不刪**——它現在同時是 D5「省略 `items` 逐位元組不變」的活證明。另補後台部分出貨的畫面斷言。

### D14: 新表的租戶隔離不靠記得，靠一道會轉紅的檢查

`shipment_items` 是租戶資料——它說的是「某位指名顧客的包裹裡有什麼」。它當然被登記進 `tenant.tenantOwned` 了，但**「有沒有記得登記」本身就是一個沒有任何東西在看的問題**：漏掉不會編譯失敗、不會噴錯、每一條既有測試都照樣綠，症狀只有在跨店讀取時才出現，而沒有任何測試在做跨店讀取。這與 `contract-router-completeness` 診斷過的「忘了把路由加進清單＝兩條零文件路由全綠通過」是同一種病。

所以本 change 除了登記，還加了兩道**純單元測試**（自動進 CI，不需要 DB）：

1. **每一張帶 `shop_id` 欄位的表都必須被分類**——要嘛在 `tenantOwned`，要嘛在新的 `notTenantOwned` 並寫下理由。今天後者有四張：`role_user`／`user_permission`／`shop_user`／`site_shop`。它們**不是**遺漏，登記它們會是主動的錯誤：`rbac.Engine.load` 讀的是 `shop_id = ? OR shop_id IS NULL`（平台層角色要被聯集進商家層快照，`admin-commerce-operations` D6），interceptor 會把它改寫成 `shop_id = ?` 而**靜默丟掉所有平台層授權**。
2. **`tenantOwned` 的每個 key 都必須是真的 ent 型別**（反射 `ent.Client` 的欄位取得）。key 是 `Query.Type()` 回傳的字串，打錯字不是編譯錯誤、不是執行期錯誤，只是那張表**永遠**不被範圍化。

檢查 1 刻意用**數量**比對而不是名稱比對：ent 的產生碼裡沒有「型別名 ↔ 表名」的對照，自己用 snake_case ＋ 複數化推一份出來會是第二個可能出錯的真相來源。留下的盲點很窄而且寫在測試註解裡（同一個 change 裡既加一張 `shop_id` 表、又加一個指向沒有 `shop_id` 的真實型別的 `tenantOwned` 條目，數量會抵銷）。

**已實測**：拿掉 `"ShipmentItem": true` 那一行 → 兩道都轉紅，訊息直接列出 25 張表並說「去登記，或去寫下不登記的理由」。另有一支整合測試驗真正的行為，包含**經由 `WithItems()` 的 eager-load 也被範圍化**——那是生產路徑實際走的查詢，而 ent 為 edge 另發一次 SQL，所以它與直接查詢是兩件要分別確認的事。

---

## Risks / Trade-offs

- **ONDO 會顯示「未知」直到他們補一個標籤** → 這是 D1 明知並接受的成本；替代方案（沿用 `1`）會顯示「已出貨」而那是錯的。修法一行，已寫在 D1。
- **一筆出貨退回會扣回整張訂單的點數，即使那箱只裝了一部分** → 過寬，已在 D7 明說。緩解：本 change 禁止「已退貨的訂單再出貨」，讓這個狀態不會與後續出貨疊加。正解是部分退貨 change。
- **`order_items.shipped_quantity` 是衍生欄位，會漂移** → 三道防線（唯一寫入路徑、DB CHECK、跨多筆出貨的加總測試），其中兩道在資料庫裡。D3。
- **訂單列鎖讓同一張訂單的並發出貨排隊** → 每天個位數次的操作，且鎖範圍是單一訂單。沒有它，兩個分頁就能讓訂單卡在「部分出貨」。D4。
- **單數顧客端點在多筆出貨下只回最近一筆** → 相對於今天的 404 是修復；要看全部有新的複數端點，契約寫明。D6。
- **回填不做就會讓歷史訂單的推導值變錯** → Migration Plan 的第 2 步就是回填，並有一條整合測試斷言「回填後每筆既有訂單的推導值 == 儲存值」。
- **`shipments.order_id` 的唯一索引被移除後，「重複出貨」不再有資料庫仲裁者** → 這是刻意的（一張訂單本來就要能有多筆），取而代之的仲裁者是 `order_items` 上的條件式 UPDATE ＋ CHECK：出不出得了，由「還剩幾件」決定，而不是由「已經有沒有出過」決定。**這是本 change 唯一移除的資料庫約束，atlas 對索引移除必須人工確認產出**（見 Migration Plan）。
- **`orders.js` 會變得更大（#5）** → 事先定調為檔案切分而非框架，見 D11。

---

## Migration Plan

一支 versioned migration，**三段，順序不可調換**：

1. **新增**：`shipment_items` 表（`shop_id`／`shipment_id` FK／`order_item_id` FK／`quantity`，`UNIQUE(shipment_id, order_item_id)`，CHECK `quantity > 0`）；`order_items.shipped_quantity`（`NOT NULL DEFAULT 0`）＋ CHECK `shipped_quantity >= 0 AND shipped_quantity <= quantity`。
2. **回填**（純 SQL，無應用程式參與）：為每一筆既有 `shipments` 插入涵蓋該訂單全部 `order_items` 的 `shipment_items`；把有出貨的訂單其 `order_items.shipped_quantity` 設為 `quantity`。**這一步讓「既有列的推導值 == 儲存值」成立**，也就是既有資料的 `fulfillment_status` 一個字都不會變。
3. **移除**：`shipments.order_id` 的唯一索引，改為一般索引。**atlas 對索引的處理必須人工確認**——本專案已知 atlas 以名稱比對 CHECK 且改運算式時完全沉默，索引移除同樣不能假設它會自己產出來。產生 migration 後**逐行讀過** `.up.sql`，確認 `DROP INDEX` 真的在裡面；沒有就手寫。

`.down.sql` 對稱：加回唯一索引、丟掉欄位與表。**注意 down 的可行性邊界**：若正式環境已經產生了真正的多筆出貨，加回唯一索引會失敗——這是誠實的行為（回滾一個已經被使用的功能本來就會失敗），不是缺陷；真正的回滾窗口是「部署後、商家開始用部分出貨之前」。

回滾策略（不需要 migrate-down）：本 change 沒有任何開關可以關掉，因為它改的是資料模型而不是行為分支。但**省略 `items` 的既有路徑逐位元組不變**（D5），所以「不使用新功能」本身就是一個完整的回退姿勢——商家只要繼續按那顆「整筆出貨」就與今天完全相同。

---

## Open Questions

- **「這張訂單全部出完了」要不要成為一個事件？** 今天不需要（D13 沒有訂閱者）。它會在出貨通知信落地時被重新問一次，而屆時的問題是「每箱一封還是出完一封」——`order-notification-email` D12 已經給了預設答案（每箱一封），所以這個事件很可能永遠不需要。記在這裡以免它被當成遺漏。
- **部分退貨的入口在哪一層？** 是 `shipment_items` 長出 `returned_quantity`，還是 `order_items` 長出？前者能表達「哪一箱退的」，後者能直接接上點數的分攤。本 change 不預先決定，但註明：那個 change 的第一項工作是決定點數 `points_delta` 的分攤口徑，不是加欄位。
- **`shipped_quantity` 之外要不要有 `packed_quantity`（已包裝未寄出）？** 今天「建立出貨紀錄」就是「寄出」這個事件本身（`shipping-logistics` D3），沒有草稿階段。部分出貨沒有改變這一點，但它讓「先揀貨、晚點才交給物流」的需求變得比較容易被提出來。不在本 change 解決。
