## Why

第一個外部前端 ONDO 溫度花室的結帳畫面有三樣東西在本 API 沒有對應，因此**組不完整**：每束花附的手寫卡片訊息（掛在購物車品項上）、整筆訂單的指定配送日，以及畫面上「小計 ＋ 運費 ＝ 合計」裡的運費——`orders.total_amount` 目前只含品項小計，運費不在其中。

前兩者是缺欄位；第三者是缺**權威**。`shipping_methods`（固定運費）資料表在姊妹 change `shipping-logistics` 就已存在，但它的 design D8 刻意讓 `flat_rate` 停在「純設定資訊」，不參與任何金額計算，把決定留給後續。結果是：前端只能自己把運費加上去顯示，而 `payment.Service.InitiatePayment` 向金流請款的是 `orders.total_amount`——**顧客看到 3800，實際被請款 3600**。兩邊各算各的不是遲早不一致，是現在就不一致。這是 ONDO 上線前的最後一批 API 缺口，也是把「應付總額」收回伺服器端的正確時機。

## What Changes

- **購物車品項與訂單品項新增卡片訊息**（`cart_items.gift_message` / `order_items.gift_message`，選填、上限 500 字元）。購物車是暫存、訂單是永久快照，兩處都存，沿用 `order_items` 既有的「denormalize 為文字快照、無 FK」紀律。購物車品項的合併鍵由 `sku_id` 擴充為 `(sku_id, gift_message)`——同一款花配兩張不同的卡片是兩個品項，不是數量 2。
- **訂單新增指定配送日**（`orders.requested_delivery_date`，選填，Postgres `DATE`）。**不**塞進 `shipping_address` jsonb：那是「送到哪」，這是「哪天送」；而且 jsonb 裡的日期不可驗證、不可索引，商家最想做的「明天要出哪幾單」就永遠做不到。
- **結帳可選定運送方式，運費併入訂單金額**。`orders` 新增 `subtotal_amount`（品項小計）、`shipping_fee`（運費快照）、`shipping_method_id` + `shipping_method_name`（所選方式的資訊性快照，供對帳與退款計算），並以資料庫 CHECK 固定三者關係為 `total_amount = subtotal_amount + shipping_fee`。運送方式**選填**：不帶時運費為 0、`total_amount` 與今日完全相同。
- **BREAKING**：`orders.total_amount` 的語意由「品項小計」變更為「**應付總額**（品項小計 ＋ 運費）」。這是本變更動用 D13 破例窗口的唯一一項，理由與邊界見 design D6/D9。**沒有任何欄位被移除或改名，沒有任何既有資料列的值改變**（回填後 `subtotal_amount = total_amount`、`shipping_fee = 0`，既有列在新語意下本來就正確）。實際改變的只有一件事：**日後**帶運送方式的訂單，其 `total_amount` 會大於 Σ(items)。
- **點數核發基準由 `total_amount` 改為 `subtotal_amount`**（品項小計）。運費不計點——理由完整寫在 design D5，這是本變更最需要正面論證的產品決策，不是實作細節。連帶效果：`payment.Service` 發佈的 `events.OrderPaymentSucceeded` 的 `TotalAmount` 欄位**更名**為 `SubtotalAmount`（Go 內部型別，更名而非新增，是為了讓每個建構點在編譯期被強制重新檢視，而不是靜默地拿到零值）。
- **新增公開唯讀端點 `GET /api/v1/shop/shipping-methods`**（僅回傳 `is_active` 的方式）。`shipping-logistics` design D7 當初以「沒有任何消費端」為由拒絕做這個端點；ONDO 就是那個消費端，前提消失了。
- 兩條結帳路徑（會員 `POST /api/v1/shop/checkout`、訪客 `POST /api/v1/shop/guest/checkout`）**同時**支援上述三項，且**共用同一個 `order.Service.Checkout`**。運費快照與金額組成全部發生在那一個方法裡，扣庫存的交易區塊一行不改。

本變更**不新增任何下單入口**，也不新增第二條扣庫存路徑（order-management spec 的「所有下單路徑共用單一結帳實作」不變式原樣成立）。

不在本次範圍內（明確記錄，非遺漏）：

- **不做運費費率運算**（重量／地區／滿額免運）。`shipping_methods.flat_rate` 維持單一固定值，這是 `shipping-logistics` 就定下的形狀；分級費率是它自己的 change。
- **不做商家可設定的配送前置天數／截單時間**。ONDO 只需要一個日期；前置天數需要商家時區、國定假日、每個運送方式各自的截單時刻——那是一整套模型，不是一個欄位。
- **不做 `shipping_methods.currency`**。運費一律以訂單幣別計價（見 design D4 的已知限制）。
- **不讓運送方式變成必填**，也不做「商家可要求必選運送方式」的設定。
- **不做購物車品項訊息的編輯端點**（見 design D2 的已評估並否決）。
- **不動 `cart` 的 `total`**：購物車不知道顧客會選哪個運送方式，`CartView.Total` 維持等於 `Subtotal`。

## Capabilities

### New Capabilities

（無。本變更是把三個既有能力各自缺的欄位補齊並重新定義金額組成，每一項都落在既有 spec 的範圍內；憑空立一個 `order-extras` 能力只會讓「訂單金額怎麼算」散在兩份 spec 裡。）

### Modified Capabilities

- `order-management`: 結帳新增「可選定運送方式並快照其費用」與「可附指定配送日」兩項需求；`total_amount` 的定義由品項小計改為應付總額，並新增 `subtotal_amount` / `shipping_fee` 與三者的組成不變式；訂單品項的快照集合納入卡片訊息。
- `shopping-cart`: 購物車品項新增卡片訊息，並隨之改變「同一 SKU 重複加入即累加數量」的合併鍵定義。
- `shipping-logistics`: `shipping_methods.flat_rate` 由「純設定資訊、不參與任何金額計算」改為「結帳選定時成為訂單運費的來源」；新增公開唯讀的 active 運送方式查詢需求（推翻該 spec 原本明列的兩項 non-goal）。
- `member-tiers-and-points`: 點數核發基準由 `orders.total_amount` 改為 `orders.subtotal_amount`；退貨扣回的基準（以原核發量為準）不變，但其文字需隨之精確化。
- `guest-checkout`: 訪客結帳請求新增 `items[].gift_message`、`shipping_method_id`、`requested_delivery_date` 三項選填輸入，且其金額組成與點數基準 MUST 與會員路徑完全相同。

## Impact

**Schema / migration**

- `cart_items` + `gift_message varchar(500) NOT NULL DEFAULT ''`
- `order_items` + `gift_message varchar(500) NOT NULL DEFAULT ''`
- `orders` + `subtotal_amount bigint NOT NULL`、`shipping_fee bigint NOT NULL DEFAULT 0`、`shipping_method_id bigint NULL`（資訊性，無 FK）、`shipping_method_name varchar(100) NULL`、`requested_delivery_date date NULL`
- 新 CHECK：`orders_subtotal_amount_check`（>= 0）、`orders_shipping_fee_check`（>= 0）、`orders_total_amount_composition_check`（`total_amount = subtotal_amount + shipping_fee`）
- **需要回填**：`UPDATE orders SET subtotal_amount = total_amount`，且必須排在組成 CHECK 之前——Atlas diff 不產生回填，故本次 migration 需依既有慣例手動調整順序並重算 `atlas.sum`（precedent：`20260730152723_member_identity_per_shop`）。

**Go packages**

- `api/internal/ent/schema/cart.go`、`order.go`：新欄位與 CHECK。
- `api/internal/cart/service.go`：`ItemInput` 與 `AddItem` 帶入 `gift_message`；合併鍵改為 `(sku_id, gift_message)`；`ReplaceItems` 分組規則隨之調整（庫存檢查仍以 SKU 為單位加總）。
- `api/internal/order/service.go`：`Checkout` 改收 `CheckoutInput`（地址 + 運送方式 + 配送日）；新增運送方式解析／驗證與配送日解析；金額組成集中於此。**扣庫存區塊不改**。
- `api/internal/shipping/service.go`：新增 `ListActiveShippingMethods`。
- `api/internal/points/service.go`：核發基準改讀 `SubtotalAmount`。
- `api/internal/events/events.go`：`OrderPaymentSucceeded.TotalAmount` → `SubtotalAmount`。
- `api/internal/payment/service.go`：事件建構點隨之更新（請款金額 `ord.TotalAmount` 不變，且因語意變更自動變成正確的應付總額）。
- `api/internal/guest/service.go`：`CheckoutInput` 新增三項選填輸入並轉交。
- `api/internal/httpapi/`：`cart.go`、`order.go`、`guest.go`、`shipping.go`、`router.go` 的請求／回應形狀與新路由。

**API 契約**

- 新增 `GET /api/v1/shop/shipping-methods`（新路徑）。
- `POST /api/v1/shop/checkout`：請求 body **新增**兩個平鋪的選填鍵 `shipping_method_id` / `requested_delivery_date`（既有請求形狀完全不變）；回應新增 `subtotal_amount`、`shipping_fee`、`shipping_method_id`、`shipping_method_name`、`requested_delivery_date`，`items[]` 新增 `gift_message`；`total_amount` 語意變更（見 BREAKING）。
- `POST /api/v1/shop/guest/checkout`：請求新增 `shipping_method_id`、`requested_delivery_date`、`items[].gift_message`。
- `POST /api/v1/shop/cart/items`：請求新增選填 `gift_message`；購物車回應的 `items[]` 新增 `gift_message`。
- `GET /api/v1/shop/orders`、`.../orders/{id}`、`GET /api/v1/shop/guest/order`、後台訂單端點：回應同步新增上述欄位。

**下游相依**

- ONDO 接入面：卡片訊息隨每個品項送、配送日與運送方式送在訂單層、總額**直接顯示 API 回傳的 `total_amount`**（不要自己加）。接入說明見 tasks.md 末。
- 未來的 returns/refunds 能力：`shipping_fee` 是它區分「退貨品但不退運費」所需的最小資訊，本變更把它記在訂單上正是為此。
