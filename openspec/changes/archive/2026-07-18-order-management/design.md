## Context

`shopping-cart`（已歸檔）建立了會員自助購物車與價格快照慣例；`product-catalog`（已歸檔）建立了 `BIGINT`/`int64` 整數最小貨幣單位金額表示法（design D1）與 `product_skus.stock_qty` 單一數字庫存（無預留鎖定機制，design D6）。本 change 是 Phase 2 電商核心的第六個 proposal，讀者對象除了本 change 實作者，也包含下兩個 proposal：`payment-integration`（會更新 `payment_status`、需要讀訂單總金額）與 `shipping-logistics`（會更新 `fulfillment_status`、需要讀收件資訊）——因此本文件的 Decisions 特別詳盡交代狀態欄位語意與 service 層存取方式。

「結帳」是購物車與訂單之間唯一的轉換路徑：`shopping-cart` design D8 已預留 `carts.status = 1`(converted) 這個列舉值但明確聲明「轉換動作是 order-management 的職責」；`product-catalog` design D6 已聲明「本階段不做庫存預留鎖定，最終庫存驗證與扣庫存留給 order-management」。本 change 正是履行這兩個承諾。

## Goals / Non-Goals

**Goals:**

- `orders`/`order_items` 資料模型，shop-scoped，納入既有租戶隔離機制，`order_items` denormalize 商品標題/SKU code 等文字快照。
- 結帳流程：購物車 → 訂單的原子轉換，含**不可超賣**的並發安全庫存扣減。
- 訂單三軸狀態模型（`status`/`payment_status`/`fulfillment_status`）定案，並提供受控的 service 層更新方法供後續 proposal 呼叫。
- 訂單取消與庫存歸還（交易安全）。
- 會員自助 + 商家後台的訂單查詢/取消 API。
- 為 `payment-integration`/`shipping-logistics` 提供明確的資料存取路徑（package、函式、欄位語意）。

**Non-Goals（明確留給後續或更後期）：**

- **實際金流整合**——`payment_status` 本階段固定以 `unpaid`(0) 建立，不接任何金流服務商；付款成功/失敗/退款的流程與觸發時機是 `payment-integration` 的職責。
- **運費試算與物流方式選擇**——`shipping_address` 只是一個結構化 JSON 收件資訊欄位；實際運費計算、物流商整合、`fulfillment_status` 的細緻子狀態（已出貨/配送中/已送達等）是 `shipping-logistics` 的職責。
- **訂單金額調整**（部分退款、折扣、稅金、運費計入總額）——`total_amount` 在本階段就是 `order_items` 小計加總，不含稅金/運費/折扣。
- **訂單修改**（結帳後更改品項、數量、收件資訊）——訂單一經建立即不可變（`shipping_address` 除外的欄位；即使 `shipping_address` 本階段也不提供修改端點，留給 `shipping-logistics` 評估是否需要）。
- **多次部分取消**（僅取消部分品項）——取消是整筆訂單的操作。
- **訂單狀態通知**（email/webhook）——不在本階段範圍。
- **管理端建立/手動調整訂單**——本階段訂單只能透過會員結帳建立，商家後台不提供建立訂單的端點。

## Decisions

### D1. `orders`/`order_items` 資料表結構與租戶隔離：比照 `carts`/`cart_items` 慣例

**決定**：兩張表都直接持有 `shop_id` 欄位（不透過 join 推導），註冊進 `api/internal/tenant.tenantOwned`，讓既有 interceptor/hook 覆蓋。`orders.member_id` 直接 FK 到平台級 `Member.ID`（同 `carts.member_id`，design D1 of shopping-cart），不透過 `ShopMember` 間接關聯。

字段：

```
orders
  id, shop_id, member_id
  status            int16 default 0   -- 訂單生命週期軸（D5）
  payment_status    int16 default 0   -- 付款軸（D5），payment-integration 擁有的列舉空間
  fulfillment_status int16 default 0  -- 出貨軸（D5），shipping-logistics 擁有的列舉空間
  currency          varchar(3)
  total_amount      int64             -- order_items 小計加總的快照（建立時計算一次，之後不變）
  shipping_address  jsonb NOT NULL    -- 結構化收件資訊（D8）
  cancelled_at      timestamptz NULL  -- 取消時間戳，NULL 表示未取消
  created_at, updated_at

order_items
  id, shop_id, order_id
  product_id   int NULL   -- 純資訊性欄位，見 D4（無 FK/edge）
  product_title varchar(200) NOT NULL  -- denormalized 快照
  sku_id       int NULL   -- 純資訊性欄位，見 D4（無 FK/edge）
  sku_code     varchar(64) NOT NULL    -- denormalized 快照
  quantity     int32 NOT NULL CHECK > 0
  price_amount int64 NOT NULL CHECK >= 0  -- 快照，沿用 cart_items 已快照的值
  currency     varchar(3) NOT NULL
  created_at, updated_at
```

索引：`orders(shop_id, member_id)`、`orders(shop_id, status)`、`order_items(order_id)`、`order_items(shop_id)`。

**理由**：與 `cart`/`cart_items`、`Category`/`Product`/`ProductSKU` 的既有慣例完全一致（直接 `shop_id` 欄位是租戶隔離機制的硬性要求，design D7 of product-catalog）。`orders.member_id` 直接存放而非透過 `ShopMember` 的理由與 `carts.member_id`（shopping-cart design D1）相同：訂單查詢天生是「這個 shop、這個會員」的複合查詢，直接欄位比多一趟 join 更直接，且維持與 `carts` 同一種資料形狀，降低下一個 agent 的認知負擔。

### D2. 並發安全扣庫存：ent 原生條件式 `UPDATE`（`AddStockQty` + `Where(StockQtyGTE)` + 檢查受影響列數），不用顯式 `SELECT ... FOR UPDATE`

**決定**：對每個結帳品項，在同一交易內執行：

```go
affected, err := tx.ProductSKU.Update().
    Where(entproductsku.IDEQ(skuID), entproductsku.ShopIDEQ(shopID), entproductsku.StockQtyGTE(qty)).
    AddStockQty(-qty).
    Save(ctx)
if err != nil { /* real error */ }
if affected == 0 { /* insufficient stock or SKU vanished — fail this line */ }
```

`ent` 為數值欄位生成的 `Add<Field>` mutation 方法會編譯成 SQL `SET stock_qty = stock_qty - $1`（不是「先讀再算再寫」），`Save(ctx)` 回傳的 `int` 是資料庫回報的實際受影響列數。`Where(StockQtyGTE(qty))` 讓「庫存是否足夠」與「扣減」在同一條 SQL 陳述式內完成，不存在讀取與寫入之間的競態窗口。

**理由（取捨）**：
1. **為何條件式 `UPDATE` 天生就對並發安全，即使不顯式鎖列**：PostgreSQL 的 `UPDATE ... WHERE` 陳述式本身會對每一列匹配候選在寫入前取得列鎖；若另一個交易已鎖住同一列（尚未 commit/rollback），本交易的 `UPDATE` 會阻塞直到對方結束，接著**在取得鎖之後、以最新已提交的資料重新評估 `WHERE` 子句**（PostgreSQL Read Committed 模式下 `UPDATE`/`DELETE` 的既定行為，非本 change 額外實作的邏輯）。這代表兩個併發結帳搶同一庫存有限的 SKU 時，第二個交易永遠是拿到「扣完前一筆之後的最新庫存」去做判斷，不會發生 lost update／超賣。
2. **為何不用顯式 `SELECT ... FOR UPDATE`**：`SELECT FOR UPDATE` 需要多一趟往返（先鎖列讀值、應用層判斷、再送 `UPDATE`），且要求呼叫端手動記得每次都要在同一顆交易內鎖對列、鎖對順序（多品項結帳時有 lock ordering / deadlock 的額外心智負擔）。條件式 `UPDATE` 把「檢查 + 扣減」收斂成一條原子陳述式，用 `ent` 原生 `AddStockQty`/`Where` 生成器就能表達，不需要下 raw SQL，語意更貼近「這本來就是一個原子操作」而非「先鎖、再改」的兩步驟心智模型，程式碼也更短、更難寫錯。
3. **前提假設**：此模式正確性依賴交易隔離等級為 PostgreSQL 預設的 **Read Committed**（`api/internal/database` 與既有 `cart`/`catalog` package 皆未覆寫隔離等級，維持預設）。若隔離等級被改為 `Serializable`，並發 `UPDATE` 會改為其中一個交易收到序列化失敗（`40001`）而非阻塞等待，屆時需要應用層重試迴圈——本 change 不需要，因為現況全庫共用預設 Read Committed，記錄在 Risks 供未來若有人調整隔離等級時知悉。

**放棄的替代方案**：
- **應用層先 `SELECT` 讀庫存、if 判斷、再 `UPDATE`（無 `WHERE` 條件式保護）**——這是 shopping-cart `AddItem` 目前對「加入購物車時的庫存提示」採用的模式（design 明確聲明「僅為提示性檢查」），在結帳這種「最終且不可逆」的場景下絕對不能重用同一模式，會直接超賣。
- **應用層層級的互斥鎖（如 Redis 分散式鎖）**——需要額外的失敗處理（鎖過期、持有者當機）與新的基礎設施耦合，且資料庫本身已經能原子地做到同樣的事，屬於不必要的複雜度。

### D3. Checkout 交易邊界與流程順序

**決定**：`Service.Checkout` 全程在**單一 `ent.Tx`** 內完成，任何一步失敗即整個 `Rollback`，不留部分扣庫存或部分建立的訂單：

1. 載入該會員的 active 購物車（`shop_id`+`member_id`+`status=Active`），`WithItems(WithSku(WithProduct()))`。
2. 購物車不存在或 `len(items) == 0` → rollback，`ValidationError`（「購物車是空的」）。
3. 逐一驗證每個品項的**可購買性**（SKU 非 nil、`is_active`、所屬商品非 nil 且 `status = 1`）——**此步驟刻意不檢查庫存數量**，庫存的最終判斷收斂到步驟 5 的原子條件式 `UPDATE`，避免「這裡先查了一次庫存,通過了,但到了扣減那一刻又不夠」的 TOCTOU 窗口誤導成兩套不同的驗證邏輯。任一品項不可購買 → rollback，`ValidationError` 列出所有違規品項（`/items/{index}`）。
4. **原子翻轉來源購物車狀態**：`tx.Cart.Update().Where(IDEQ(cartID), StatusEQ(Active)).SetStatus(Converted).Save(ctx)`，若受影響列數為 0 → rollback，回傳「購物車已結帳或狀態已變更」的衝突錯誤。這一步刻意放在庫存扣減**之前**，作為防止「同一購物車被重複送出結帳」（例如使用者連點兩次）的並發守門——用跟 D2 相同的條件式 `UPDATE` 手法，額外保護了「同一顆購物車不會被兩個並發的結帳請求各自建立一張訂單」這個超出「庫存超賣」但同樣重要的正確性要求。
5. 逐一對每個品項執行 D2 的條件式扣庫存；任一品項受影響列數為 0 → rollback（含步驟 4 的購物車狀態翻轉也一併復原，因為在同一交易內），回傳 `ValidationError` 指出哪個品項庫存不足。
6. 計算 `total_amount`（`Σ price_amount * quantity`，全部品項幣別已由購物車幣別鎖定機制保證一致，見 shopping-cart design D4），建立 `orders` 列（`status=Created`、`payment_status=Unpaid`、`fulfillment_status=Unfulfilled`、`shipping_address` 取自請求輸入）。
7. 逐一建立 `order_items`（denormalize `product_title`/`sku_code`，`price_amount`/`currency`/`quantity` 直接複製 `cart_items` 已有的快照值——不重新查 SKU 現價，沿用 shopping-cart design D2 的既有快照）。
8. Commit；回傳建立好的訂單（含品項）。

**理由**：把「購物車狀態翻轉」與「庫存扣減」放在同一交易內、且翻轉在前，讓兩種不同的並發風險（重複送出 vs 超賣）都用同一種「條件式 UPDATE + 檢查受影響列數」機制解決，不必引入兩套不同的鎖定策略。

### D4. `order_items.product_id`/`sku_id`：純資訊性欄位，不建立 FK/edge

**決定**：`order_items.product_id`/`sku_id` 是**普通可空整數欄位**，**不**建立 ent edge、**不**建立資料庫外鍵約束。所有顯示所需的資訊（標題、SKU code、價格、幣別、數量）已经在建立當下 denormalize 進 `order_items` 自己的欄位（D1）。這兩個 id 欄位純粹是「這行品項曾經對應到哪個 product/SKU」的參考線索（例如未來後台想提供「查看商品」的跳轉連結，商品還存在的話），服務層讀取時**不得**假設它們一定指向存活的資料列。

**理由（取捨）**：
- 這與 `cart_items.sku_id`（shopping-cart design D7：`Optional().Nillable()` + `entsql.OnDelete(entsql.SetNull)`）刻意不同——購物車品項需要**即時**判斷可購買性（要 join 回 SKU 讀 `is_active`/`stock_qty`），所以需要一個「被刪除時自動斷開但品項留存」的機制。訂單品項則完全相反：一旦建立就是永久歷史記錄，**不需要、也不應該**再 join 回 SKU/Product 取任何即時資訊——所有需要顯示的內容都已經是自己的欄位。用一個沒有 FK 約束的純資訊性整數欄位，完全迴避了「商品/SKU 被刪除時要不要 cascade／SET NULL」這整個問題：不管上游資料表發生什麼事（刪除、重建、id 重用），`order_items` 的顯示內容永遠不受影響，這正是「訂單是永久記錄」這個需求最直接的實現方式。
- 放棄「跟 cart_items 一樣用 `SET NULL` edge」的理由：那個機制的存在意義是「品項需要能查到當前 SKU 狀態」，訂單品項沒有這個需求；套用同一機制只是徒增一個 FK 約束與 cascade 規則要維護，卻沒有對應的好處。

### D5. 訂單三軸狀態欄位：確切列舉值與 CHECK 約束範圍

**決定**：

| 欄位 | 型別 | 本 change 定義的列舉值 | CHECK 約束 |
|---|---|---|---|
| `status` | `int16` default `0` | `0` = Created（已建立）／`1` = Cancelled（已取消） | `CHECK (status IN (0, 1))` |
| `payment_status` | `int16` default `0` | `0` = Unpaid（未付款）——本 change **唯一**會設定的值 | `CHECK (payment_status >= 0)` |
| `fulfillment_status` | `int16` default `0` | `0` = Unfulfilled（未出貨）——本 change **唯一**會設定的值 | `CHECK (fulfillment_status >= 0)` |

Go 常數（`api/internal/order`）：

```go
const (
    StatusCreated   int16 = 0
    StatusCancelled int16 = 1
)
const PaymentStatusUnpaid int16 = 0           // 其餘值由 payment-integration 定義
const FulfillmentStatusUnfulfilled int16 = 0  // 其餘值由 shipping-logistics 定義
```

**理由（為何 `status` 有完整 `IN (0,1)` CHECK，`payment_status`/`fulfillment_status` 只有寬鬆的 `>= 0` CHECK）**：
- `status` 這個軸完全由本 change 擁有語意（「訂單本身的生命週期」——已建立/已取消），未來 `payment-integration`/`shipping-logistics` 不會、也不應該往這個軸加新值（它們該用自己的軸），比照 `Product.status`/`Cart.status` 的既有慣例給出完整列舉的 CHECK 約束是安全的。
- `payment_status`/`fulfillment_status` 的**完整**列舉空間本 change 無從得知——`payment-integration` 需要哪些付款狀態（paid/failed/refunded/partially_refunded...）、`shipping-logistics` 需要哪些出貨狀態（shipped/delivered/returned...），是那兩個尚未規劃的 proposal 的職責。若本 change 現在就「猜」一組完整列舉並寫死 CHECK，猜錯的代價是那兩個 proposal 一開工就要先發一個 migration 改 CHECK 約束才能新增自己需要的值，這比「本 change 只約束型別/非負」更糟——後者讓那兩個 proposal 有完全的自由度定義自己的列舉並各自視需要加上更嚴格的 CHECK（他們有完整的上下文可以做這個決定，本 change 沒有）。寬鬆的 `>= 0` 只防止負值/型別誤用等低階錯誤，不假裝擁有不屬於本 change 的知識。

### D6. 受控狀態更新方法：`payment-integration`/`shipping-logistics` 必須呼叫 service 方法，禁止直接寫 ent

**決定**：`api/internal/order` package 匯出以下兩個方法作為外部 proposal 更新這兩個軸的**唯一**入口：

```go
// UpdatePaymentStatus sets orders.payment_status. Callers outside this
// package (payment-integration) MUST use this method rather than writing the
// ent client directly — it enforces tenant scoping (shopID) and existence
// (ErrNotFound), and is the single place order-management can later add
// invariants (e.g. rejecting a transition on a cancelled order) without every
// caller having to change. status must be >= 0 (ValidationError otherwise);
// order-management does not validate the specific value against a payment
// state machine — that machine belongs to payment-integration.
func (s *Service) UpdatePaymentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error)

// UpdateFulfillmentStatus is the fulfillment-status analogue, owned by
// shipping-logistics.
func (s *Service) UpdateFulfillmentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error)
```

兩者都以 `shopID`（租戶範圍）+ `orderID` 定位訂單、`ErrNotFound` 表示訂單不存在或不屬於該 shop、`status < 0` 回 `ValidationError`。**刻意不**在這兩個方法內加上「訂單已取消就拒絕更新」這類跨軸業務規則——那個規則屬於呼叫方（`payment-integration`/`shipping-logistics`）該不該允許在已取消訂單上繼續走它們自己的流程（例如已取消訂單仍可能需要標記退款），本 change 沒有足夠上下文替它們決定，硬加會是本 change 猜錯風險最高的地方之一，因此保持這兩個方法是純粹的「租戶範圍 + 存在性檢查 + 型別檢查」的欄位設值器，業務規則留給呼叫方在自己的 service 層決定。

**理由**：直接開放 `payment-integration`/`shipping-logistics` 用 `ent.Order.Update()` 寫欄位雖然技術上可行（`ent.Client`/生成的 model 是 exported 的），但會讓「誰負責保證 `shopID`/`orderID` 範圍正確」「未來若要在更新這兩個欄位時順帶觸發事件（例如出貨後發通知）要改幾個呼叫點」這類職責散落在多個 package 裡。集中成兩個方法，讓 `order-management` 保留唯一的「以後想加東西」擴充點，呼叫方只需要知道這兩個函式簽名。

### D7. 訂單取消：可取消條件與庫存歸還

**決定**：訂單可取消的條件是**三軸皆處於初始值**：`status = Created AND payment_status = Unpaid AND fulfillment_status = Unfulfilled`。任一軸已被 `payment-integration`/`shipping-logistics` 推進（付款中/已付款/已出貨等）即不可透過本 change 的取消端點取消，回 409（`ConflictError`，本 change 需要引入此型別——不同於 shopping-cart 沒有 409 情境，訂單取消確實有「狀態不允許」的 RESTRICT 語意）。

取消流程（同一 `ent.Tx`）：

1. 原子條件式翻轉：`tx.Order.Update().Where(IDEQ(orderID), ShopIDEQ(shopID), StatusEQ(Created), PaymentStatusEQ(Unpaid), FulfillmentStatusEQ(Unfulfilled)).SetStatus(Cancelled).SetCancelledAt(now).Save(ctx)`；受影響列數為 0 → 若訂單存在但狀態不符 → `ConflictError`（409）；若訂單根本不存在或不屬於該 shop/member → `ErrNotFound`（404）。這個條件式 `UPDATE` 同時是「取消」的執行與「可取消性」的判斷，用跟 D2/D3 相同的手法防止「同時間有 payment-integration 正在把訂單標記為已付款、使用者又點了取消」的競態。
2. 翻轉成功後，查出該訂單所有 `order_items`，對每個 `sku_id != nil` 的品項執行 `tx.ProductSKU.Update().Where(IDEQ(*skuID)).AddStockQty(quantity).Save(ctx)`（純遞增，不需要條件式保護——歸還庫存不會產生負數風險）。`sku_id == nil`（該 SKU 已被刪除，D4）的品項**無法歸還**——沒有存活的 SKU 列可以加回庫存，這是可接受的已知限制（見 Risks）。
3. Commit。

會員自助取消（`POST /shop/.../orders/{id}/cancel`）額外要求 `member_id` 相符（否則 `ErrNotFound`，比照 cart 的「不洩漏歸屬」慣例）；商家後台取消（`POST /admin/.../orders/{id}/cancel`）只要求 `shop_id` 相符（RBAC 已在 middleware 層檢查 `order.cancel` 權限）。兩者共用同一個 `Service.CancelOrder(ctx, shopID, orderID int, memberID *int)` 方法，`memberID` 為 `nil` 時跳過會員擁有權檢查。

**理由**：把「可取消」限縮到三軸皆為初始值，是最保守、最不會與未來 proposal 衝突的邊界——一旦 `payment-integration`/`shipping-logistics` 開始動這筆訂單，它們的狀態機比 `order-management` 更清楚「這個狀態下能不能被取消/要不要退款」，此時繼续讓本 change 的通用取消端點介入是危險的（可能繞過它們的業務規則,例如已出貨訂單被取消但沒有觸發退貨流程）。那兩個 proposal 之後若需要「已付款訂單的取消」，應該是它們自己的專屬取消/退款流程,而不是重用這個端點。

### D8. 收件資訊 `shipping_address`：結構化 JSON，欄位形狀為 `shipping-logistics` 設計

**決定**：`shipping_address` 儲存為 `jsonb`（ent `field.JSON(..., json.RawMessage{})`，比照 `Product.meta`/`ProductSKU.options` 既有慣例——欄位型別在 DB/ent 層是不透明 blob，具體形狀由應用層的 Go struct 定義與驗證）。`api/internal/order` 定義：

```go
type ShippingAddress struct {
    RecipientName string `json:"recipient_name"`
    Phone         string `json:"phone"`
    Line1         string `json:"line1"`
    Line2         string `json:"line2,omitempty"`
    City          string `json:"city"`
    PostalCode    string `json:"postal_code"`
    Country       string `json:"country"`
}
```

`Checkout` 驗證 `RecipientName`/`Phone`/`Line1`/`City`/`PostalCode`/`Country` 皆非空字串（`Line2` 選填），任一缺漏回 `ValidationError`（422，`/shipping_address/<field>`）。

**理由**：欄位涵蓋台灣/多數地區常見收件資訊需要的最小集合（收件人、電話、地址兩行、城市、郵遞區號、國別），`shipping-logistics` 之後若要串接物流商 API，多數物流商的收件資訊表單就是這幾個欄位的排列組合，不需要重新設計整個欄位形狀（頂多新增可選欄位，向後相容）。存成 `jsonb` 而非拆成多個資料表欄位的理由與 `Product.meta` 相同：這是「一份跟著訂單走的資訊快照」，不需要被關聯式查詢（沒有「依城市搜尋訂單」這種需求），拆欄位只會讓 schema migration 的心智負擔上升卻沒有對應的查詢效益。

### D9. `ConflictError` 型別：本 change 需要（跟 catalog 一樣，跟 cart 不一樣）

**決定**：`api/internal/order` 定義 `ConflictError{Message string}`（`Error()` 回傳 `Message`），對應 D3 的「購物車已結帳」與 D7 的「訂單狀態不允許取消」兩種情境，HTTP handler 映射到 409。

**理由**：shopping-cart 的 service doc 特別記錄「沒有 `ConflictError`，因為那個 change 沒有 RESTRICT 語意」；本 change 確實有兩處真正的「操作在目前狀態下不被允許」情境（跟 `product-catalog` 的分類刪除 RESTRICT 語意同一類），因此比照 `catalog.Service` 定義 `ConflictError`，不是憑空引入新概念。

### D10. Package 結構與 API 摘要

**決定**：新 package `api/internal/order`（比照 `cart`/`catalog` 的結構）：

```go
package order // api/internal/order

type Service struct { Client *ent.Client }

var ErrNotFound = errors.New("order: not found")
type ValidationError struct { Message string; Details []Detail }
type ConflictError struct { Message string }

// Checkout converts the member's active cart into an order (D3): validates
// purchasability, atomically flips cart.status Active->Converted, atomically
// decrements stock per line (D2), computes total_amount, creates
// orders+order_items. All-or-nothing in one ent.Tx.
func (s *Service) Checkout(ctx context.Context, shopID, memberID int, addr ShippingAddress) (*ent.Order, error)

// GetOrder returns one order owned by memberID in shopID, with items loaded.
func (s *Service) GetOrder(ctx context.Context, shopID, memberID, orderID int) (*ent.Order, error)

// ListOrders lists memberID's orders in shopID, paginated.
func (s *Service) ListOrders(ctx context.Context, shopID, memberID int, params ListParams) (*OrderPage, error)

// GetOrderAdmin / ListOrdersAdmin: shop-scoped only (no memberID check — RBAC
// middleware already enforced admin's shop access). ListOrdersAdmin filters
// optionally by Status/PaymentStatus/FulfillmentStatus.
func (s *Service) GetOrderAdmin(ctx context.Context, shopID, orderID int) (*ent.Order, error)
func (s *Service) ListOrdersAdmin(ctx context.Context, shopID int, params AdminListParams) (*OrderPage, error)

// CancelOrder cancels (D7). memberID == nil means the admin path (shop-scope
// only); memberID != nil additionally enforces ownership (member path).
func (s *Service) CancelOrder(ctx context.Context, shopID, orderID int, memberID *int) (*ent.Order, error)

// UpdatePaymentStatus / UpdateFulfillmentStatus: see D6.
func (s *Service) UpdatePaymentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error)
func (s *Service) UpdateFulfillmentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error)
```

**理由**：與 `cart.Service`/`catalog.Service` 相同的形狀（explicit `shopID`/`memberID` 參數、不依賴 ambient context，讓 package 保持可測試、handler 是唯一從 request 解出身分的地方——同 cart.Service 的既有慣例）。

### D11. 給 `payment-integration`/`shipping-logistics` 的存取指南

**查詢訂單總金額與幣別**（`payment-integration` 建立付款單需要）：`orders.total_amount`（`int64`）與 `orders.currency` 是建立訂單時算好的快照欄位，不需要每次重新加總 `order_items`：

```go
o, err := client.Order.Query().Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)).Only(ctx)
// o.TotalAmount, o.Currency
```

或呼叫 `order.Service{Client: client}.GetOrderAdmin(ctx, shopID, orderID)`（回傳同一個 `*ent.Order`，含 `Edges.Items` 若需要逐行明細）。

**更新付款狀態**：`order.Service{Client: client}.UpdatePaymentStatus(ctx, shopID, orderID, newStatus)`（D6）——**禁止**直接 `client.Order.UpdateOneID(orderID).SetPaymentStatus(...)`，繞過會失去 `order-management` 未來可能加上的不變量檢查點。`payment-integration` 需要自行定義 `newStatus` 的完整列舉值（`0` 已被本 change 用作 Unpaid，之後的付款狀態建議從 `1` 開始編號），並自行決定是否要對 `orders.payment_status` 加上更嚴格的 CHECK 約束（透過新的 migration）。

**讀取收件資訊**（`shipping-logistics` 建立出貨單需要）：`orders.shipping_address` 是 `jsonb`，形狀見 D8 的 `order.ShippingAddress` struct（`json.Unmarshal(o.ShippingAddress, &addr)`）。若 `shipping-logistics` 需要更多欄位（例如收件時段偏好），可以在 `ShippingAddress` struct 新增**選填**欄位並在自己的 change 更新這個型別定義——既有訂單的 JSON 不會因為 struct 新增欄位而失效（`omitempty`/零值即可）。

**更新出貨狀態**：`order.Service{Client: client}.UpdateFulfillmentStatus(ctx, shopID, orderID, newStatus)`（D6），約定同付款狀態——`0` 已被用作 Unfulfilled，`shipping-logistics` 自行編號後續值。

**訂單取消的交集**：若 `shipping-logistics`/`payment-integration` 需要在自己的流程中取消訂單（例如付款逾時自動取消），**不要**呼叫本 change 的 `CancelOrder`（D7 的可取消條件是「三軸皆初始值」，一旦它們自己已經把 `payment_status`/`fulfillment_status` 推進過，`CancelOrder` 會回 409 拒絕）——它們應該直接操作 `orders.status`（比照 D6 的模式，若這類需求出現，建議在該 proposal 自己的 design 中新增一個類似 `UpdatePaymentStatus` 的受控方法，而不是重用會員/商家取消端點背後的 `CancelOrder`）。

## Risks / Trade-offs

- **[Risk] 隔離等級假設** → D2 的並發安全性依賴 PostgreSQL 預設 Read Committed 隔離等級。若未來任何 change 把交易隔離等級改為 Serializable，條件式 `UPDATE` 會開始遇到序列化失敗（`40001`）而非阻塞排隊，屆時本 change 的 `Checkout`/`CancelOrder` 需要加上重試迴圈。緩解：本 change 的整合測試（並發超賣測試）會在 CI 的真實 PostgreSQL 16 上跑，若隔離等級被意外更動，測試會直接失敗曝光問題。
- **[Risk] `order_items.sku_id`/`product_id` 是純資訊性欄位（D4），無 FK 完整性** → 若上游資料被刪除，這兩個欄位會變成「懸空」的 id（不會是 NULL，因為沒有 SET NULL 機制，是建立當下的原始值）。緩解：後台/前台任何讀取都不應該依賴這兩個欄位反查 SKU/Product，只作為選填的「查看商品」跳轉線索，找不到就不顯示連結；已在 D4 的方法註解中明確警告。
- **[Risk] SKU 被刪除後的訂單無法透過取消歸還其庫存（D7 步驟 2）** → 已知限制，發生機率低（SKU 通常在還有未完成訂單時不會被商家刪除，且刪除 SKU 目前只會由商品刪除的 cascade 觸發，商家主動刪除有未結案訂單的商品是邊緣情境）。緩解：不歸還的庫存不會造成資料不一致（只是那一份庫存「消失」而非「多出來」），比錯誤地加回一個可能已經被其他用途占用的庫存數字更安全。
- **[Trade-off] `payment_status`/`fulfillment_status` 沒有完整 CHECK 約束（D5）** → 換取 `payment-integration`/`shipping-logistics` 不必在開工第一步就先發 migration 改約束的彈性，代價是資料庫層面不會擋下「型別正確但語意錯誤」的值（例如寫入一個尚未定義的付款狀態數字）；業務層級的合法值範圍由呼叫方（透過 D6 的受控方法）自行負責。

## Migration Plan

- 新增 ent schema（`api/internal/ent/schema/order.go`）→ `go run ./cmd/migrategen -name add_order_management` 產生版本化 migration（比照既有 `add_product_catalog`/`add_shopping_cart` 命名慣例）→ `make migrate` 套用。
- 無資料回填需求（全新表，無既有資料）。
- Rollback：`make migrate-down` 還原（down migration 隨 `migrategen` 自動產生，需人工確認 drop 順序正確——`order_items` 先於 `orders`）。

## Open Questions

無阻塞性未決問題。`payment_status`/`fulfillment_status` 的完整列舉值由後續 proposal 自行決定（D5/D11 已預留擴充空間），不在本 change 範圍內解決。
