## 1. 前置查證

- [x] 1.1 確認 `bouquet/public/` 仍為零 API 呼叫（design D9 動用破例窗口的事實前提）
- [x] 1.2 盤點 `orders.total_amount` 的全部讀取點，確認語意變更的實際半徑（`payment.Service.InitiatePayment` 請款、`points` 核發、`httpapi` 序列化，以及 `/web` `/admin` `/e2e` 是否讀取）
- [x] 1.3 確認 `cart_items` 目前沒有任何唯一約束會阻擋「同一 SKU 多列」（design D1 的前提）
- [x] 1.4 確認 ent 的 `field.Time` + Postgres `date` 在本專案的驅動下可正常寫入／讀回（design D3）

## 2. 資料模型（spec: order-management, shopping-cart）

- [x] 2.1 `ent/schema/cart.go`：`CartItem` 新增 `gift_message`（`varchar(500)`、`NOT NULL DEFAULT ''`）
- [x] 2.2 `ent/schema/order.go`：`OrderItem` 新增 `gift_message`（同上）
- [x] 2.3 `ent/schema/order.go`：`Order` 新增 `subtotal_amount`、`shipping_fee`、`shipping_method_id`（資訊性、無 edge 無 FK）、`shipping_method_name`、`requested_delivery_date`（`date`）
- [x] 2.4 `ent/schema/order.go`：新增三條 CHECK — `subtotal_amount >= 0`、`shipping_fee >= 0`、`total_amount = subtotal_amount + shipping_fee`（design D7）
- [x] 2.5 `go generate ./internal/ent`
- [x] 2.6 `make migrate-gen name=add_order_extras`
- [x] 2.7 手動調整產生的 SQL：`UPDATE orders SET subtotal_amount = total_amount` 必須排在 ADD COLUMN 之後、組成 CHECK 之前（design D10）；確認 diff 未提議 drop 手寫的 `UNIQUE NULLS NOT DISTINCT`
- [x] 2.8 重算 `atlas.sum`（`ariga.io/atlas/sql/migrate.WriteSumFile`；atlas CLI 未安裝）
- [x] 2.9 `make migrate` 套用並驗證既有訂單 `subtotal_amount = total_amount`、`shipping_fee = 0`、`total_amount` 值未變

## 3. 購物車：贈禮訊息與合併鍵（spec: shopping-cart）

- [x] 3.1 `internal/cart/service.go`：`AddItem` 新增 `giftMessage` 參數；長度以 rune 計、上限 500，超過回 422（design D2）
- [x] 3.2 同檔：既有品項查找述詞由 `(cart_id, sku_id)` 改為 `(cart_id, sku_id, gift_message)`，並由 `Only` 改為 `First`（design D1 的實作陷阱）
- [x] 3.3 同檔：庫存驗證改為以 SKU 為單位跨所有品項列加總後比對
- [x] 3.4 同檔：`ItemInput` 新增 `GiftMessage`；`ReplaceItems` 的分組鍵改為 `(sku_id, gift_message)`，庫存檢查仍以 SKU 加總
- [x] 3.5 同檔：`CartItemView` 新增 `GiftMessage`
- [x] 3.6 `internal/httpapi/cart.go`：加入品項請求接受 `gift_message`；`cartItemJSON` 輸出 `gift_message`（永遠是字串，不為 null）
- [x] 3.7 **不**讓 `PUT /cart/items/{itemID}` 接受 `gift_message`（design D2 的已評估並否決）

## 4. 結帳：運送方式、配送日與金額組成（spec: order-management, shipping-logistics）

- [x] 4.1 `internal/order/service.go`：新增 `CheckoutInput{ShippingAddress, ShippingMethodID *int, RequestedDeliveryDate *string}`，`Checkout` 改收此型別
- [x] 4.2 同檔：新增配送日解析／驗證（嚴格 `YYYY-MM-DD`、不得早於 UTC 今日前一天），以**原始字串**傳入服務層使兩條路徑共用單一定義（design D3/D11）
- [x] 4.3 同檔：新增運送方式解析／驗證（屬本店、`is_active`），失敗回 422 指標 `/shipping_method_id`；於交易**之前**完成（design D4/D7）
- [x] 4.4 同檔：累加變數更名為 `subtotal`，`total = subtotal + shippingFee`，建立訂單時寫入五個新欄位——**條件式扣庫存與受影響列數檢查一個字元不改**
- [x] 4.5 同檔：建立 `order_items` 時寫入 `gift_message`
- [x] 4.6 `internal/httpapi/order.go`：會員結帳請求以**平鋪**方式新增 `shipping_method_id` / `requested_delivery_date`（design D11）
- [x] 4.7 同檔：`orderJSON` 新增 `subtotal_amount`、`shipping_fee`、`shipping_method_id`、`shipping_method_name`、`requested_delivery_date`（`YYYY-MM-DD` 或 null）；`orderItemJSON` 新增 `gift_message`

## 5. 訪客路徑（spec: guest-checkout）

- [x] 5.1 `internal/guest/service.go`：`CheckoutInput` 新增 `ShippingMethodID`、`RequestedDeliveryDate`，`items[]` 透過 `cart.ItemInput` 帶入 `gift_message`
- [x] 5.2 同檔：三項輸入原樣轉交 `order.Service.Checkout`，**不**在 guest 套件重新驗證或重新命名錯誤（沿用既有慣例）

## 6. 公開運送方式端點（spec: shipping-logistics）

- [x] 6.1 `internal/shipping/service.go`：新增 `ListActiveShippingMethods(ctx, shopID)`
- [x] 6.2 `internal/httpapi/shipping.go`：新增 `MountPublic`，`GET /shipping-methods`，回應僅 `{id, name, carrier, flat_rate}`（design D4：公開最小集合）
- [x] 6.3 `internal/httpapi/router.go`：掛在 `/api/v1/shop` 的 TenantMW 群組（比照 `Products.MountPublic`），不加任何認證

## 7. 點數基準（spec: member-tiers-and-points）

- [x] 7.1 `internal/events/events.go`：`OrderPaymentSucceeded.TotalAmount` **更名**為 `SubtotalAmount`（design D8：更名而非新增，讓遺漏在編譯期爆炸）
- [x] 7.2 `internal/payment/service.go`：事件建構點改帶 `ord.SubtotalAmount`；請款金額 `ord.TotalAmount` 維持不變
- [x] 7.3 `internal/points/service.go`：核發計算改讀 `ev.SubtotalAmount`；扣回邏輯不變（以原核發量為準）
- [x] 7.4 更新 `payment`／`points` 既有測試中的事件建構點與斷言

## 8. 測試

- [x] 8.1 既有 `TestCheckoutConcurrentOversellingProtection` 一行不改地保留並以 `-race` 通過
- [x] 8.2 既有訪客並發測試（訪客版、訪客＋會員混合版）一行不改地保留並以 `-race` 通過
- [x] 8.3 新增「帶運送方式的並發結帳」測試：確認多出的金額邏輯未在交易內引入新的競態
- [x] 8.4 既有訂單取消歸還庫存測試通過（運費不涉庫存，`CancelOrder` 未改動）
- [x] 8.5 金額組成測試：不帶運送方式 → `total = subtotal`、`shipping_fee = 0`；帶運送方式 → `total = subtotal + fee`
- [x] 8.6 資料庫組成 CHECK 測試：直接嘗試寫入 `total != subtotal + fee` 的訂單必須失敗
- [x] 8.7 運送方式驗證測試：不存在／跨店／已停用皆 422 且不觸及庫存與購物車狀態
- [x] 8.8 運費快照測試：訂單建立後改價／刪除運送方式，訂單金額與名稱不變
- [x] 8.9 配送日測試：合法值可讀回、`2026/08/05` 與 `2026-02-31` 與過去日期皆 422、未附帶為 null
- [x] 8.10 贈禮訊息測試：購物車加入／讀回、超過 500 字元 422、500 個中文字元可通過、快照進 `order_items`
- [x] 8.11 合併鍵測試：同 SKU 同訊息累加、同 SKU 不同訊息成為兩列、跨訊息的庫存加總驗證
- [x] 8.12 點數測試：`shipping_fee = 200` 的訂單核發依 `subtotal_amount` 計算；兩種運費的相同品項訂單核發相同；退貨扣回等於原核發量
- [x] 8.13 公開運送方式端點測試：未認證可讀、只回 active、跨店隔離
- [x] 8.14 訪客路徑測試：三項輸入在訪客端點成立、與會員路徑產生相同金額組成、含運費訂單的請款金額為 `total_amount`
- [x] 8.15 `make test` 全綠
- [x] 8.16 `make test-int` 全綠（含 `-race`）
- [x] 8.17 `make lint` 無新增問題

## 9. 收尾

- [x] 9.1 `make seed-demo` 後 `cd e2e && node happy-path.mjs` 通過
- [x] 9.2 對執行中的 dev 服務做一次端到端手動煙霧測試（會員與訪客各一筆含運費訂單 + 付款 + 點數）
- [x] 9.3 更新 `CLAUDE.md` 架構註記與 `openspec/project.md` 的約束條目（含 D13 破例窗口就此關閉）
- [x] 9.4 整理 ONDO 接入這三項所需的前端作法作為交付說明

### 實作過程中的補充決定（與 design 的差異）

| 項目 | 決定 | 理由 |
| --- | --- | --- |
| `gift_message` 不加 ent `MaxLen` 驗證器 | 只留 `SchemaType: varchar(500)` + 服務層 rune 計數 | ent 的 `MaxLen` 以 **byte** 計，Postgres `varchar(n)` 以 **字元** 計。兩者並存會讓 500 個中文字（1500 bytes）被 ent 擋下卻能塞進資料庫欄位——實測就是一個 500。移除後兩道檢查對同一個值的判斷完全一致。 |
| `UpdateItemQuantity` 的庫存檢查 | 改為跨同 SKU 的所有品項列加總 | 合併鍵改變後，同一 SKU 可能有多列；沿用舊的單列比對會讓「兩張卡片各 1 件」在庫存只剩 1 時通過。與 `AddItem` 用同一條規則。 |
| `AddItem` 的既有品項查找 | 改為載入同 SKU 全部列，以 id 遞增找第一個訊息相同者 | 原本的 `Only()` 在同一 SKU 有多列時直接回錯誤（500）。design D1 已標記此陷阱，實作上以「查全部 + 選第一個」同時取得庫存加總與合併目標，一次查詢解決兩件事。 |

### 手動煙霧測試紀錄（對執行中的 dev 服務，DB 重置 + `make seed-demo`）

| 步驟 | 結果 |
| --- | --- |
| 後台建立三個運送方式（200／0／已停用 999） | 201 ×3 |
| `GET /api/v1/shop/shipping-methods`（`Host: demo.localhost`、**無任何憑證**） | 200，只回兩個 active，欄位僅 `{id, name, carrier, flat_rate}` |
| `POST /shop/guest/checkout`（同一 SKU ×2、兩張不同卡片、運送方式 1、配送日 2026-08-05） | 201，`subtotal=2580 fee=200 total=2780`，`items` 為**兩列**各帶自己的訊息，`requested_delivery_date=2026-08-05` |
| `POST /shop/guest/order/payments` | 201，`amount=2780`（**請款金額＝應付總額**，這正是本變更要修的 bug） |
| 付款成功 webhook | 200，`payment_status=1` |
| 點數 | **25**（`2580/100`），**不是 27**（`2780/100`）——運費不計點 |
| 會員路徑：`POST /shop/cart/items` 帶 `gift_message` | 201，購物車回應含該訊息 |
| 會員路徑：`POST /shop/checkout`（平鋪 body 帶 `shipping_method_id=2`、`requested_delivery_date`） | 201，`fee=0 total=1290`，`shipping_method_name=門市自取`，品項帶回卡片訊息 |
| 已停用的運送方式 / 不存在的運送方式 | 422，`/shipping_method_id` |
| `2026/08/05` / `2020-01-01` | 422，`/requested_delivery_date` |
| `make migrate` 於**含既有訂單**的資料庫 | 回填後 `subtotal_amount = total_amount = 123456`、`shipping_fee = 0`，`total_amount` 值未變；手寫的 `uq_members_shop_email/phone` 未被 diff 刪掉 |
| `make migrate-down` → `make migrate` | 往返成功 |
| 再跑一次 `make migrate-gen` | 殘差 diff **只剩**既有的手寫約束誤報，本變更的欄位與 CHECK 零殘差 |

## ONDO 接入說明（交付給 bouquet 專案）

接續 `guest-checkout` 的接入說明。以下是**卡片訊息、指定配送日、運費**這三項前端要做的事。

### 一句話總結

**不要自己算合計。** `total_amount` 就是應付總額（小計 ＋ 運費），也是金流實際請款的金額。ONDO 目前的 `money(sub + shipFee())` 要改成直接顯示 API 回傳的值，否則兩邊遲早不一致——而不一致的那一邊會是顧客看到的那一邊。

### ① 運送方式：從 API 取，不要硬編碼

ONDO 現在的 `SHIPS` 是三筆硬編碼（`same`/`next`/`pick`，運費 200/150/0）。改為進結帳流程前抓一次：

```js
const res = await fetch(`${API}/api/v1/shop/shipping-methods`, {
  headers: { "X-Publishable-Key": PUBLISHABLE_KEY },   // 不需要任何 Authorization
});
const { shipping_methods } = await res.json();
// [{ id: 1, name: "台北市當日配送", carrier: "自有車隊", flat_rate: 200 }, ...]
```

- 回傳的**只有啟用中**的方式；商家在後台把某個方式停用，前端下一次抓就看不到它了（不必自己過濾）。
- `flat_rate` 是**最小貨幣單位整數**，幣別同商品（TWD 即元）。顯示用 `money(flat_rate)`。
- ONDO 原本的 `note`（「12:00 前下單，當日 19:00 前送達」）API 沒有對應欄位，那是文案，留在前端即可；`carrier` 可以拿來顯示「黑貓冷藏」這類資訊。
- `state.ship` 要從 `'same'|'next'|'pick'` 改成存 API 的 `id`，送出時送 `shipping_method_id`。

### ② 送出訂單：三項新輸入的位置

```js
await fetch(`${API}/api/v1/shop/guest/checkout`, {
  method: "POST",
  headers: { "Content-Type": "application/json", "X-Publishable-Key": PUBLISHABLE_KEY },
  body: JSON.stringify({
    email: form.email,
    items: cart.map(l => ({
      sku_id: l.skuId,
      quantity: l.qty,
      gift_message: l.msg || "",      // ← 卡片訊息，掛在「每個品項」上
    })),
    shipping_address: {
      recipient_name: form.name,
      phone: form.phone,
      line1: form.addr,
    },
    shipping_method_id: state.ship,               // ← 訂單層，選填
    requested_delivery_date: toISODate(form.date), // ← 訂單層，選填，"YYYY-MM-DD"
  }),
});
```

**卡片訊息（`items[].gift_message`）**

- 位置就是 ONDO 現在放它的地方：`state.cart[].msg`。直接映射。
- 上限 **500 個字元**（以字元計，中文英文一樣算 500），超過回 422 指向 `/items/{i}/gift_message`。
- **同一款花配兩張不同卡片＝兩個品項**，不要在前端先合併數量。API 的合併鍵是 `(sku_id, gift_message)`：訊息相同才會累加數量，訊息不同就是兩列，訂單裡也會是兩列。ONDO 的 `state.cart` 目前以 `key` 去重而 `key` 不含 `msg`——**這是要改的一點**：`key` 要把 `msg` 算進去，否則第二張卡片會覆蓋第一張。
- 沒有卡片就送空字串或整個省略，回應裡永遠是字串（不會是 `null`）。

**指定配送日（`requested_delivery_date`）**

- 必須是 `YYYY-MM-DD`。ONDO 現在的欄位是自由文字、placeholder 寫 `2026 / 08 / 05`、驗證只有 `/\d/`——**要改成 `<input type="date">` 或自行正規化**，否則會被 422。
- 只有一條伺服器端規則：不得是「在任何時區都已過去」的日期（即早於 UTC 昨日）。沒有前置天數、沒有截單時間、沒有最遠可預訂日——那些是 ONDO 自己的營業規則，要擋請在前端擋。
- 選填。不送就是 `null`。

**運送方式（`shipping_method_id`）**

- 送 ①拿到的 `id`。不送＝運費 0（`total_amount` 等於小計）。
- 送到不存在／別家商家／已停用的 id → 422 指向 `/shipping_method_id`，**訂單不會建立、庫存不會動**。

### ③ 金額怎麼顯示才會跟 API 一致

結帳成功的回應（以及之後的 `GET /shop/guest/order`）帶三個金額：

```json
{
  "subtotal_amount": 3600,   // 小計：Σ(單價 × 數量)
  "shipping_fee": 200,       // 運費
  "total_amount": 3800,      // 合計＝應付總額＝金流請款金額
  "shipping_method_id": 1,
  "shipping_method_name": "台北市當日配送",
  "requested_delivery_date": "2026-08-05",
  "items": [{ "gift_message": "給媽媽", "...": "..." }]
}
```

- **結帳畫面（送出前）**：API 還不知道這張單，此時只能由前端預估——用 `subtotal`（自己算）＋ 從 `/shipping-methods` 拿到的 `flat_rate`。這是「顯示」而非「權威」。
- **送出後的任何畫面**（訂單確認頁、訂單查詢、寄給顧客的訊息）：**一律用回應的 `total_amount`**，不要重算。
- 三者的關係由資料庫約束保證恆等（`total = subtotal + fee`），所以不會出現 API 自己對不起來的情況。

### ④ 點數要怎麼跟顧客說

**點數以商品金額（`subtotal_amount`）計算，運費不計點。** 3600 的花＋200 運費，拿到的是 36 點不是 38 點；門市自取和當日配送拿到的點數一樣多。

文案不要寫成「消費 100 元 1 點」——那會被理解成含運費。寫「商品金額每 100 元 1 點（運費不計）」。

### ⑤ 錯誤排查對照（本變更新增的部分）

| 現象 | 意義 |
| --- | --- |
| 422，details 指向 `/shipping_method_id` | 運送方式不存在／屬於別家商家／已被商家停用（重新抓一次 `/shipping-methods`） |
| 422，details 指向 `/requested_delivery_date` | 不是 `YYYY-MM-DD`、不是真實存在的日期（如 `2026-02-31`），或明顯是過去的日期 |
| 422，details 指向 `/items/{i}/gift_message` | 卡片訊息超過 500 個字元 |
| 合計跟預期差了運費 | 忘了送 `shipping_method_id`（不送＝免運） |

