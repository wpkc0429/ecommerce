# Design: admin-commerce-operations

## Context

`admin-commerce-catalog`（design D1）把後台從 616 行單檔改成原生 ES module 的多檔結構，並把「什麼時候該推翻這個決定」寫成四條可檢驗的條件。它同時明確點名了三件留給本 change 的事：read-mostly 列表視圖、前端路由、以及 `shop.manage_client_origins` 這個平台層權限在商家層畫面上的可見性來源。它還留下一條 tripwire：**「出貨畫面顯示跨多個品項重算的『已出貨／待出貨』」是它指認的跨視圖衍生狀態**，踩到就該重開框架評估。本文件第一節就回答那個問題。

三個既有事實限制了本 change 的選項空間：

1. **API 已經齊全到令人意外的程度**。訂單／出貨／運送方式／會員等級／點數／publishable key／origin 的後台端點在 `order-management`（2026-07-18）到 `storefront-client-auth`（2026-07-30）之間全部交付完畢，共 25 條路由，全部載於 `api/openapi/openapi.yaml`。本 change 幾乎是純前端。
2. **D13 的 v1 破例窗口已於 `order-extras` 用盡並關閉**。任何 API 變更只能是 additive，且必須先證明「真的缺」。
3. **admin 仍然沒有 CI job、沒有型別檢查、沒有單元測試**。唯一守著它的是 `e2e/happy-path.mjs` 的瀏覽器斷言。這條事實在本 change 沒有改變，因此「哪些東西必須有 E2E 斷言」仍然是一個設計決策而不是測試細節。

## Goals / Non-Goals

**Goals:**

- 回答 `admin-commerce-catalog` design D1 的 tripwire #1：出貨畫面是否真的產生跨視圖衍生狀態。
- 訂單的檢視（列表／篩選／詳情）與取消；出貨的建立與狀態推進；運送方式與會員等級的維護；會員點數的檢視與調整；publishable key 與 allowed origin 的維護。
- 讓「分頁列表無法在前端過濾」這件事以最小的 additive API 變更解決，而不是做一個會說謊的篩選器。
- 可分享的深層連結（訂單詳情、會員點數），且不引入框架。
- 平台層權限（`shop.manage_client_origins`）在商家層畫面上的可見性來源。
- 金額（運費、訂單三件組、等級門檻）與**點數**在顯示與輸入的單位紀律。

**Non-Goals:**

- 顧客名冊／會員管理畫面（D7）。
- 訂單的 `created_at` 日期區間篩選（D4）。
- 退款、部分出貨、多筆出貨、訂單匯出、批次操作（D8、D9）。
- `discount_percent` 的實際折扣計算（`member-tiers-and-points` 已定義它本階段只儲存）。
- 平台層的商家／站點／主題／角色管理畫面（那是平台管理員的後台，不是商家營運）。
- 任何資料庫 schema、migration、事件接線的變更。

## Decisions

### D1. Tripwire #1 未被踩到，理由是 shipment 是整筆訂單而非部分出貨——這件事必須先查證再設計

`admin-commerce-catalog` design D1 的推翻條件 #1 寫的是：

> 需要跨視圖的即時衍生狀態——例如一個畫面上多個小元件必須隨同一份資料同步重算（**出貨介面若要即時反映「已出貨數／剩餘數」跨多列連動**，就會踩到）。

**查證結果：這個前提在本系統不成立。** `shipping.Service.CreateShipment(ctx, shopID, orderID, carrier, trackingNumber)` 的簽名裡沒有品項、沒有數量；`shipments` 表沒有任何 order_item 的關聯欄位；`shipments.order_id` 上有唯一索引，`CreateShipment` 以它作為並發競賽的最終仲裁者（`shipping-logistics` design D4），且在 `ord.FulfillmentStatus != Unfulfilled` 時直接回 409「order already has a shipment」。`ListShipmentsAdmin` 的註解明說「0 or 1 row this phase」。

也就是說：**一筆訂單至多一筆出貨，出貨的粒度是整筆訂單。**「已出貨 3 件、待出貨 2 件」在這個資料模型裡沒有對應的事實可以顯示——不是 UI 沒做，是它不存在。出貨畫面上唯一的衍生狀態是「這筆訂單有沒有出貨紀錄」，那是**一個布林值、由一次 API 回應直接給出、只在單一視圖內使用**。

因此：

- **tripwire #1 未觸發**，`admin-commerce-catalog` design D1 的 (c) 結論（無建置步驟、原生 ES module、最小詞彙）在本 change 繼續成立，不重開評估。
- 更重要的是，**UI 不得假裝支援部分出貨**。出貨表單不提供品項勾選、不提供數量輸入——那會讓商家以為可以只出一部分，然後在送出後發現整筆訂單都被標成已出貨。畫面上的字必須是「將**整筆訂單**標記為已出貨」。
- 若日後真的要做部分出貨，那是 `shipments` 需要 line items 的 schema 變更，屆時 tripwire #1 才真正到期——**那時要重新評估的是框架，而不是在前端手工同步四個地方**。這句話留給下一個人。

**四條推翻條件在本 change 結束時的狀態**（逐條檢查，不是感覺）：

| 條件 | 狀態 |
| --- | --- |
| #1 跨視圖即時衍生狀態 | 未觸發（本節） |
| #2 同一個小元件第三次重寫 | **觸發**，處置見 D2——但它指向 `lib/`，不指向框架 |
| #3 需要型別安全甚於 E2E | 未觸發（本 change 的危險處仍是語意而非型別，見 D10） |
| #4 admin 總行數 > ~3000 | 未觸發，但**只差一點**（實測 2,940 行：JS 2,361 ＋ HTML 395 ＋ CSS 184），見 Risks |

### D2. 第三次重複確實出現了，但它要的是 `lib/` 裡的兩個小函式，不是 `table()`，更不是框架

**先數清楚。** 本 change 新增 6 個列表（訂單、運送方式、會員等級、點數流水帳、publishable key、allowed origin），加上既有的分類與商品，共 8 個。每一個都在重複同一段：

```js
const tbody = clear($("#x-tbody"));
if (rows.length === 0) tbody.append(el("tr", {}, el("td", { colspan: "N", class: "empty" }, "…")));
for (const r of rows) tbody.append(el("tr", {}, el("td", {}, …), …));
```

分頁器則是 3 個（商品、訂單、點數流水帳）：

```js
const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
$("#x-pageinfo").textContent = `第 ${page} / ${pages} 頁 ｜ 共 ${total} 筆`;
$("#x-prev").disabled = page <= 1;
$("#x-next").disabled = page >= pages;
```

tripwire #2 說「第三次是缺少抽象的訊號，屆時先問能不能加進 `lib/`，答案是否定的（因為需要生命週期或反應性）時才是框架的訊號」。**這兩段都不需要生命週期，也不需要反應性**——它們是純函式：輸入資料、輸出 DOM。所以答案是「能加進 `lib/`」，本 change 就加，且**回頭改寫既有的分類與商品視圖**（否則「第三次重複」只是被搬到別處而不是被消除）。

**決定：`lib/dom.js` 新增 `rows(host, list, render, { empty, colspan })` 與 `renderPager(prefix, page, total, pageSize)`，各約 6 行。**

**為什麼不是 `table()`。** 一個接管整張表的 `table({ columns: [...] })` 必須同時擁有表頭、對齊、空狀態、以及每一格的產生方式，於是每一格從「一行 `el("td", {}, …)`」變成一個 config 物件裡的 `render` 欄位。那買到的是統一的表頭宣告，賣掉的是「儲存格就是程式碼」這個讓 `el()` 好讀的性質；而表頭在本專案是**靜態 HTML**（`index.html` 的 `<thead>`），本來就不重複。重複的只有 tbody 的迴圈與空狀態。抽掉重複的那一段，不多抽。

**為什麼空狀態值得被抽進 helper**：它是最容易漏的一段，而漏掉的表現是「一張看起來正在載入的空表格」——商家分不清「沒有訂單」與「壞了」。放進 helper 之後，忘記寫空狀態這個失敗模式從結構上消失。

### D3. 訂單列表新增四類選填篩選；先確認真的缺，再確認不能在前端做

**先確認是否真的缺。** `order.AdminListParams` 只有 `Page`／`PageSize`／`Status`；`openapi.yaml` 的 `listOrdersAdmin` 只有這四個參數；`v1-surface.txt` 同樣只鎖了這四行。訂單回應本身有 `payment_status`、`fulfillment_status`、`requested_delivery_date`、`shipping_address`，但**沒有任何一條是可篩選的**。**確認缺。**

**為什麼不能在前端過濾。** `admin-commerce-catalog` design D6 對商品搜尋的論證在此逐字成立：列表是分頁的，前端只拿得到當頁，對當頁過濾會產生一個**主動誤導**的畫面。差別在後果——商品搜尋說謊，商家會多建一個商品然後撞 slug 422；訂單篩選說謊，商家會看到「今天沒有要出的貨」而其實有一筆在第 3 頁，**那是一束沒送到的花**。

**決定**：`GET /api/v1/admin/shops/{shopID}/orders` 新增四類選填參數。

| 參數 | 型別 | 語意 |
| --- | --- | --- |
| `payment_status` | 整數 | 精確比對 `orders.payment_status` |
| `fulfillment_status` | 整數 | 精確比對 `orders.fulfillment_status` |
| `delivery_from` / `delivery_to` | `YYYY-MM-DD` | `requested_delivery_date` 的閉區間（見 D4） |
| `q` | 字串 | 訂單編號精確比對（`q` 可解析為正整數時），或收件人姓名／電話的大小寫不敏感子字串 |

**為什麼是這四類而不是更多或更少。** 逐條對應商家每天實際會問的問題：

- 「今天／明天要出哪些貨」＝ `delivery_from=delivery_to=<日>` ＋ `fulfillment_status=0`。這是花店最核心的一句話，而 `orders.requested_delivery_date` 這根欄位在 ent schema 的註解裡就寫著它存在的理由是「the one query a florist actually wants（what ships tomorrow）」——那句話至今沒有任何端點兌現。
- 「已付款但還沒出貨的」＝ `payment_status=1` ＋ `fulfillment_status=0`。這就是揀貨清單。
- 「王小姐剛打電話來」＝ `q=王`。
- 「他說訂單編號是 1234」＝ `q=1234`。

`payment_status`／`fulfillment_status` 與既有的 `status` 是同一個形狀（`optionalInt16Query` + 一行 `Where`），**刻意不做值域驗證**：這兩根欄位的 CHECK 在 DB 是 `>= 0`，enumeration 由 payment／shipping 各自擁有（`order-management` design D5），前端硬編一組值等於在訂單套件裡複製別人的列舉。不認得的值只會篩出零筆，那是誠實的答案。

**`q` 的實作與已知限制。** 姓名與電話住在 `orders.shipping_address` 這個 jsonb 裡，所以是 `shipping_address->>'recipient_name' ILIKE '%…%'`（ent 的自訂 predicate，`sql.ExprP` 帶參數繫結，不做字串拼接）。與 design D6 的 `ILIKE '%…%'` 一樣走不到索引，理由與路徑也一樣：單一商家的訂單量級下不構成問題，真的需要時是 pg_trgm GIN 或全文檢索，屆時 API 契約不變。**`q` 不比對地址行**——商家找的是人，不是路名；把 `line1` 納入會讓「台北」這種輸入回傳半個城市的訂單。

**additive 的論證**：五個新的**選填** query 參數，舊呼叫端不受任何影響，`v1-surface.txt` 只增 10 行（每個參數兩行：`… query x` 與 `… query x optional`），零刪除。不使用 D13 的破例窗口。**不帶任何新參數時，`ListOrdersAdmin` 走的是與變更前逐行相同的查詢。**

### D4. 日期區間篩的是「指定配送日」而不是「建立時間」，因為平台沒有商家時區

**決定**：`delivery_from`／`delivery_to` 篩 `requested_delivery_date`；**不**提供 `created_at` 的區間篩選。

**理由一（正確性）**：`requested_delivery_date` 的欄位型別是 Postgres `date`，它就是一個日曆日，沒有時刻、沒有偏移量。`delivery_from=2026-08-05` 這個查詢在任何時區的任何人手上都是同一個答案。`created_at` 是 `timestamptz`：「2026-08-05 這一天的訂單」需要先知道那是誰的 2026-08-05——而 `shops` 表**沒有時區欄位**（已查證）。要嘛憑空發明一個伺服器時區（會在跨日邊界靜默地少算或多算訂單，而且沒有任何測試會發現），要嘛要求呼叫端傳 RFC3339 瞬間（正確，但把時區語意推給每一個未來的呼叫端，包含 curl 使用者）。兩條路都不該在一個「補 UI」的 change 裡定案。

**理由二（有用性）**：花藝的營運節奏是「哪天要送」，不是「哪天下的單」。`created_at` 區間的真正用途是月結報表，那是報表功能，不是日常營運。

**已知取捨（必須寫進 spec 而不是只寫在這裡）**：`requested_delivery_date` 是**選填**欄位，未指定配送日的訂單在 `NULL` 上比較為假，因此**帶任一日期界限時會被排除**。這是正確語意（「8/5 要送的」本來就不包含「沒說要哪天送的」），但它會讓商家以為訂單消失，所以 UI 必須在套用日期篩選時明示這件事。

### D5. 前端路由：`lib/router.js`，約 40 行，hash-based，視圖名稱就是路由

**決定**：`location.hash` 形如 `#/<view>[/<param>]`。`#/orders`、`#/orders/1234`、`#/points/57`、`#/products`。

**為什麼現在做**（`admin-commerce-catalog` 刻意延後）：訂單詳情與會員點數是**帶參數的視圖**。沒有路由的話，「從訂單詳情跳到這位顧客的點數」得靠兩個模組互相持有狀態或由 shell 轉交參數——那是一個沒有名字的路由器。有了 hash，參數的來源只有一個地方，而且順帶得到商家真正會用的能力：把 `#/orders/1234` 貼給同事。

**為什麼是 hash 而不是 History API**：後台是靜態檔案由 nginx 服務（`admin/Dockerfile`）。History API 的深層連結需要伺服器把任意路徑 rewrite 回 `index.html`，那是一個部署設定變更；hash 不需要任何伺服器配合。這與「維持無建置步驟／同一份靜態資源服務任何環境」是同一個取捨方向。

**形狀**（`showTab` 從「切 panel」變成「導航」，避免兩份真相）：

- `showTab(name, ...params)` 只做一件事：寫入 `location.hash`。真正的畫面切換發生在 `hashchange` 的單一處理器裡，它切 panel、標 active、呼叫 `view.load(...params)`。
- hash 未變時 `hashchange` 不會觸發，因此導航函式在 hash 相同時直接呼叫一次渲染。
- 開機時：若 hash 指向的視圖存在**且呼叫者有該視圖的權限節點**，就路由過去；否則落到第一個有權限的視圖。權限不足時不得靜默停在空白畫面。
- 未知的 hash 一律回落到第一個有權限的視圖，不顯示錯誤——一個過期的書籤不該長得像故障。

**不做**：巢狀路由、query string 狀態（篩選條件不入 hash）、路由守衛。篩選條件不入 hash 是刻意的：它會讓「貼給同事」這件事變成貼一串連自己都看不懂的參數，而深層連結要解的問題是「這一筆」，不是「這個篩選」。

### D6. `shop.manage_client_origins` 的可見性來源就是 `/shops/{shopID}/me`——因為 shop 快照本來就聯集了平台角色

**先查證，而不是先假設。** `rbac.Engine.load(ctx, userID, shopID)` 在 shop ctx 下取的角色指派是 `roleuser.ShopIDEQ(shopID) OR roleuser.ShopIDIsNil()`——**平台層角色的權限會被聯集進商家層快照**。`meInShop` 對整份權限目錄逐一跑 `snap.Allows(name)`，所以平台管理員在 `/shops/1/me` 會拿到 `shop.manage_client_origins`，商家 owner 不會。

實測（seed-demo 後的真實伺服器）：

```
平台 admin  /shops/1/me → 含 shop.manage_client_origins；GET /shops/1/allowed-origins → 200
商家 owner  /shops/1/me → 不含                          ；GET /shops/1/allowed-origins → 403
```

**決定：origin 管理不需要第二個可見性來源，`can("shop.manage_client_origins")` 就是答案**，與其他每一顆按鈕用同一個函式。這也解釋了為什麼 UI 上 publishable key 與 allowed origin 放在同一個分頁卻是兩個區塊：它們的 API 相鄰（都是自帶前端的接入設定），權限層級卻不同，所以 origin 區塊在商家 owner 眼中整塊不存在，而 key 區塊照常。

**已知的不精確（寫下來，因為它是 fail-open 的方向）**：伺服器的 `RequirePlatformPermission` 判的是「平台層快照的 `Allows` **且** `HasPlatformRole`」，而 `can()` 判的只有前者。若有人把 `shop.manage_client_origins` 授予一個 **merchant scope** 的角色（seed 不會這麼做，需要平台管理員刻意操作），UI 會畫出一顆按下去回 403 的按鈕。這與 `admin-commerce-catalog` design D5 已接受的性質相同——**這是體驗層不是安全層，伺服器永遠是唯一權威**——只是這一條的失誤方向是多畫一顆按鈕，而不是少畫。以 `HasPlatformRole` 修正它需要 `/me` 回應新增一個欄位，那是為了一個不存在於 seed 的組態擴張 v1 表面，不划算。

### D7. 點數視圖以 member id 定址；**不**順手新增顧客名冊端點

**先確認是否真的缺。** 點數的後台端點全部是會員範圍的：`GET/POST /admin/shops/{shopID}/members/{memberID}/points…`。平台**沒有任何列出會員的端點**（`GET /shops/{shopID}/users` 是 RBAC 的平台使用者，不是顧客）。所以「檢視某會員的點數」在 UI 上必須先有一個 member id。

**決定**：點數視圖接受一個 member id（來自深層連結 `#/points/{memberID}`，或訂單詳情的「檢視此顧客點數」按鈕，或一個手動輸入框）。**不新增顧客列表端點。**

**為什麼不順手加。** 一份顧客名冊不是一個 `GET /members`：它要分頁、要搜尋、要決定回應裡放不放 email／電話／消費金額（那是 PII 揭露決策，而 `members` 是 `member-identity-per-shop` 之後每家商家各自獨立的身分表），還要決定它算不算一個新的權限節點（`member.view` 今天不存在）。把這些塞進一個「補營運 UI」的 change 裡，做出來的會是一個看起來夠用、實際上沒想清楚 PII 邊界的端點，而且它一旦進 v1 就永遠拔不掉。留給獨立 change。

**代價要誠實**：本 change 之後，商家仍然無法「瀏覽全部顧客」，只能沿著訂單找到人。這是本 change 最大的已知缺口，寫進 proposal 的 Non-Goals 與回報清單。

### D8. UI 只呼叫既有端點，狀態一律經擁有者的 Service；出貨表單只提供伺服器接受的兩個目標狀態

`orders.payment_status` 只由 `payment.Service` 經 `order.Service.UpdatePaymentStatus` 寫；`orders.fulfillment_status` 只由 `shipping.Service` 經 `order.Service.UpdateFulfillmentStatus` 寫（`order-management` design D6）。UI 沒有任何繞過的路徑，也不得發明一個。

具體約束：

- **出貨狀態**：`PUT …/shipments/{id}` 只接受 `"delivered"` 與 `"returned"`，兩者皆為終態（`shipping.Service.AdvanceShipment` 的 CAS 只在 `status = shipped` 時成立）。UI 因此**只在 shipment 為「已出貨」時顯示這兩顆按鈕**，一旦進入終態就只剩狀態文字。不提供「改回已出貨」——伺服器會 409，而一顆保證失敗的按鈕比沒有按鈕更糟。
- **付款狀態**：後台沒有標記付款的端點（付款由 provider webhook 推進）。UI 顯示付款紀錄（`GET …/orders/{id}/payments`）與 `payment_status`，**不提供任何手動改付款狀態的操作**。
- **訂單取消**：只呼叫 `POST …/orders/{id}/cancel`；已付款或已出貨會回 409，UI 原樣顯示伺服器訊息（那則訊息本來就寫成給人看的），不自行預判。
- **「退貨」會扣回點數**：`AdvanceShipment` 在 `returned` 時發 `events.OrderReturned`，points 訂閱後扣回該訂單已核發的點數。UI 的確認對話框必須說出這件事，否則商家會以為那只是一個標籤。

### D9. 出貨是整筆訂單、至多一筆；UI 如實呈現這個限制

承 D1。訂單詳情的出貨區塊有三種狀態，畫面必須分辨得出來：

1. **未出貨**（`fulfillment_status = 0`，無 shipment）：顯示建立出貨表單（carrier 必填、追蹤號選填），按鈕文字明示「將整筆訂單標記為已出貨」。
2. **已出貨**（有 shipment 且為 shipped）：顯示 carrier／追蹤號／出貨時間，加上「標記已送達」「標記退貨」兩顆按鈕。
3. **終態**（delivered／returned）：只顯示紀錄與狀態，無按鈕。

已取消的訂單不顯示建立出貨表單（`CreateShipment` 會回 409「order is cancelled」）。

### D10. 金額不縮放沿用 `lib/money.js`；**點數不是金額，不得經過 `formatMoney`**

金額側沿用 `admin-commerce-catalog` design D4，一字不改：整個 `admin/` 不存在 `/100` 或 `*100`。本 change 新增的金額顯示點是運費（`flat_rate`、`shipping_fee`）與訂單三件組（`subtotal_amount`／`shipping_fee`／`total_amount`），輸入點是運送方式的 `flat_rate`。

**本 change 新增的陷阱是相反方向的**：畫面上會同時出現三種看起來都像數字的東西——

| 值 | 單位 | 顯示方式 |
| --- | --- | --- |
| `flat_rate`、`total_amount`… | 最小貨幣單位整數 | `formatMoney()` → `NT$150` |
| `points`、`points_delta`、`min_points` | **點**，不是錢 | 純整數 + 「點」 |
| `discount_percent` | 百分比 | 整數 + `%` |

**點數與金額之間存在一個真實的換算**（`points.Service` 以 `floor(subtotal_amount / 100)` 核發），這正是危險所在：一個把 `points` 丟進 `formatMoney` 的人會得到 `NT$34`，看起來完全合理。因此規則寫死：**`formatMoney()` 只接受帶 `currency` 的欄位**；點數與百分比一律不經過它。訂單詳情同時顯示 `subtotal_amount`（金額）與該訂單核發的點數時，兩者必須有明顯不同的呈現。

金額三件組的顯示必須是**組合式**的（`小計 + 運費 = 應付總額`），不是三個並列的數字：`order-extras` design D6 把 `total_amount` 的語意從「品項小計」改成「應付總額」，而唯一能讓商家察覺這件事的地方就是這個畫面。

### D11. E2E：掛在既有 `happy-path.mjs` 的瀏覽器區段，接在商品鏈之後，用商品鏈自己建的商品下單

`commerce-demo-seed` design D2 已定案不開第三個入口。本 change 的營運鏈需要瀏覽器（它驗的是 UI），所以接在 `happy-path.mjs` 第 ⑫ 節（商品發佈）之後、`runCommercePath()` 之前。

**覆蓋路徑**：後台建立運送方式（斷言運費顯示未被縮放）→ 以**該商品鏈剛發佈的商品**與**該運送方式**經公開 API 訪客下單 → 後台以深層連結 `#/orders/{id}` 開啟詳情（斷言三件組金額、卡片訊息、指定配送日、收件人）→ 付款（API + 已簽章 webhook）→ 後台建立出貨並標記已送達 → 後台點數頁看到餘額與流水帳 → 產生 publishable key → 會員等級 CRUD → allowed origin 登記與移除。

**幾個刻意的選擇：**

- **用商品鏈自建的商品下單，不用 seed 的花束**。`commerce-path.mjs` 的 `STOCK_PER_RUN` 是對 seed 花束庫存的預算，本區段若也吃它就會縮短「一份 seed 能跑幾次」，而且是靜默地縮短（下一個人只會看到庫存耗盡）。自建商品自帶庫存，互不干擾。
- **沿用瀏覽器裡已登入的平台管理員 session，不另外登入**。登入限流是每 (shop, IP, email) 5 分鐘 5 次；`happy-path` 已花掉 2 次平台管理員登入、`commerce-path` 花掉 1 次商家 owner 登入。多一次登入會讓連跑兩輪撞上限流，而那看起來會像功能壞了。
- **會被其他測試看見的變動要復原**：新建的會員等級以極高的 `min_points` 建立並在區段結束前刪除（否則會擾動 `commerce-path` 的等級階梯斷言）；allowed origin 帶 `runID` 且用完刪除（它是全域唯一的，不刪第二輪就 422）。運送方式不刪——它只會讓 `commerce-path` 的「至少 3 個啟用中的運送方式」更容易成立。
- **權限可見性的斷言放在 `commerce-path.mjs`（純 API）**：商家 owner 的 `/shops/{id}/me` 必須含 `publishable_key.create` 而**不含** `shop.manage_client_origins`，且 `GET /allowed-origins` 對他回 403。這是 D6 的機械化證據，而且它必須以**商家 owner** 的身分驗——瀏覽器區段是平台管理員，看不到這個差異。

### D12. 契約與表面鎖

`openapi.yaml` 的 `listOrdersAdmin` 新增五個選填參數（`payment_status`、`fulfillment_status`、`delivery_from`、`delivery_to`、`q`）。`v1-surface.txt` 以 `-openapi.update-surface` 重生後**逐行檢查 diff 只有新增**：預期新增 10 行（每個參數兩行），零刪除。四道檢查中的 ①（路由覆蓋）不受影響（無新路由）；②（金額 `$ref`）不受影響（無新金額欄位）；③（回應驗證）不受影響（回應形狀未變）；④ 是本次唯一會動的鎖。

## Risks / Trade-offs

- [本 change 讓 `admin/` 從 1,496 行長到 **2,940 行**（JS 2,361 ＋ HTML 395 ＋ CSS 184），逼近 tripwire #4 的 ~3000 行] → 誠實記錄：**#4 是本 change 之後最接近觸發的一條，而且下一個視圖就會越過它**。下一個 admin change 開工前應先重新評估 `admin-commerce-catalog` design D1，而不是先寫程式。D2 的兩個 helper 與視圖註冊表讓每個新視圖的固定成本維持在常數，但那只是延緩不是免除。
- [`q` 走 jsonb 的 `->>` 加 `ILIKE '%…%'`，無索引] → 與 design D6 相同的規模判斷與相同的日後路徑（pg_trgm GIN／全文檢索），API 契約屆時不變。單一商家的訂單量級下不構成問題。
- [日期篩選會排除沒有指定配送日的訂單，商家可能以為訂單不見了] → UI 在套用日期篩選時明示「僅顯示有指定配送日的訂單」；spec 以 scenario 鎖住這句話必須出現。
- [沒有顧客名冊，商家只能沿著訂單找到人] → D7 的明確取捨，寫進 proposal Non-Goals 與交付回報。獨立 change 處理。
- [出貨是不可逆的：標記已送達／退貨之後沒有回頭路，且退貨會扣回點數] → 這是伺服器既有的狀態機，UI 不能也不該軟化它；處置是確認對話框說清楚後果（D8），而不是在前端做一個假的「復原」。
- [平台管理員在 UI 上看得到 origin 區塊而商家 owner 看不到，可能被誤讀為 bug] → 這正是正確行為（`storefront-client-auth` design D11：origin 是全域競爭命名空間）；UI 在該區塊標明「平台層設定」，並由 E2E 的 API 斷言鎖住兩種身分的差異。
- [`can()` 對 `shop.manage_client_origins` 的預測在一個非 seed 的組態下會 fail-open（多畫一顆按鈕）] → D6 已記錄；伺服器的 `RequirePlatformPermission` 是唯一權威，最壞後果是一顆按下去回 403 的按鈕。
- [新增 5 個選填 query 參數會讓 `listOrdersAdmin` 的參數面變寬，日後想收緊就會破壞 v1] → 選填參數的移除／收緊確實會讓表面鎖轉紅，那正是它存在的目的。五個參數各自對應一句商家每天會說的話（D3），不是預備性擴充。

## Migration Plan

純前端 + 一處 additive API 參數擴充，無資料庫變更、無資料遷移。實作順序（對應 tasks.md）：

1. API additive：`order.AdminListParams` 的四類欄位、`ListOrdersAdmin` 的條件式 `Where`、handler 的 query 讀取；整合測試（含「不帶參數時結果與過去相同」）。
2. `openapi.yaml` + `v1-surface.txt` 重生，確認 diff 零刪除；`make openapi` 四道全綠。
3. `lib/dom.js` 的 `rows()`／`renderPager()`，並回頭改寫 `views/categories.js`、`views/products.js`（否則第三次重複只是被搬家）。
4. `lib/router.js` 與 `app.js` 的導航改造；跑既有 E2E 確認 CMS 與商品流程未壞。
5. 六個新視圖 + `index.html` 的 panel + `styles.css`。
6. E2E 營運鏈（瀏覽器）與權限可見性斷言（API）。
7. 全套驗證（`make test`、`make test-int`、`make lint`、`make openapi`、重置 DB 後跑完整 E2E）。

回滾：admin 為靜態檔，回退 commit 即回滾；API 變更為選填參數，回退不影響任何既有呼叫端（`v1-surface.txt` 的 10 行會一起消失，那是回滾而非破壞）。

## Open Questions

- **顧客名冊／會員管理**（D7）——本 change 之後最明顯的缺口。它的前置是一份 PII 揭露決策與一個新的權限節點，不是一個列表端點。
- **訂單的 `created_at` 區間與月結報表**（D4）——前置是 `shops` 的時區欄位。在那之前，任何「某一天的訂單」都是一個沒有定義的問句。
- **部分出貨／多筆出貨**——前置是 `shipments` 長出 line items（schema 變更）。屆時 tripwire #1 才真正到期，見 D1 最後一段。
- **退款**——`payment` 領域今天沒有退款端點；訂單取消與退貨都不觸發任何金流動作。這在真實營運上遲早會痛，但它是後端領域的缺口，不是前端問題。
- **`admin/` 的 CI job**——admin 至今沒有型別檢查也沒有單元測試，而它現在有 2700 行。tripwire #3 尚未觸發（E2E 仍抓得住已知的錯誤類型），但下一個 change 值得重新問一次。
