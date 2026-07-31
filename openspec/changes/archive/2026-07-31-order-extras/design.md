## Context

三個缺口的共同來源是同一件事：**訂單目前只知道「買了什麼」，不知道「怎麼送、哪天送、附什麼話」**。

```
orders            shop_id member_id status payment_status fulfillment_status
                  currency total_amount shipping_address(jsonb) cancelled_at
order_items       product_id product_title sku_id sku_code quantity price_amount currency
cart_items        cart_id sku_id quantity price_amount currency
shipping_methods  name carrier flat_rate is_active     ← 存在，但 checkout 完全不認識它
```

`order.Service.Checkout` 是全系統唯一一段並發安全的條件式扣庫存（`UPDATE ... WHERE stock_qty >= ?` + 受影響列數檢查），由 `-race` 的 `TestCheckoutConcurrentOversellingProtection` 與 `TestGuestAndMemberCheckoutShareOneStockGuard` 兩個測試守著。**本變更所有金額邏輯都必須繞開那段程式的內部，只在它前後動手。**

`shipping-logistics` design D8 當初拒絕把運費併入 `total_amount`，理由有二：checkout 交易是最脆弱的區塊、以及沒有消費端。第一個理由本變更以「只碰交易的前後、不碰迴圈內部」正面處理；第二個理由已經不成立——ONDO 的結帳畫面就是消費端，而且它今天**必須自己算合計**（`money(sub + shipFee())`），而 `payment.Service.InitiatePayment` 向金流請款的是 `ord.TotalAmount`。顧客看到 3800、實際被請款 3600 不是理論風險，是把運費留在前端的必然結果。

ONDO（`bouquet`，零建置 vanilla JS，**目前仍零 API 呼叫**）的既有資料形狀：

```js
addToCart(p, qty, size, msg) → state.cart.push({ key, id, size, qty, msg })   // 卡片訊息在品項上
state.form = { name, phone, addr, date, card }                                 // date 在訂單層
SHIPS = [{k:'same', fee:200}, {k:'next', fee:150}, {k:'pick', fee:0}]           // 三種固定運費
```

約束：PostgreSQL 16；金額一律 int64 最小貨幣單位 + `currency CHAR(3)`，禁用浮點數；`orders` 的狀態欄位只能經 `order.Service` 的方法更新；公開 API 一律 `/api/v1`，design D13 規定 v1 僅接受 additive 變更，姊妹 change ⓪① 已各破例一次、② 未動用。

## Goals / Non-Goals

**Goals:**

- 卡片訊息隨每個品項從購物車一路快照到訂單品項，且同一款商品配不同卡片能成為兩個獨立品項。
- 指定配送日成為訂單上**可查詢、可驗證**的一級資料，而不是塞在 jsonb 裡的字串。
- 「應付總額」的權威回到伺服器端：前端顯示什麼、金流請款什麼、商家對帳什麼，都是同一個數字。
- 運費併入之後，點數核發的基準是一個**經得起解釋、且對既有已核發點數回溯成立**的定義。
- 兩條結帳路徑（會員／訪客）在這三項上行為完全一致，且都只經由既有的 `order.Service.Checkout`。
- 扣庫存的並發安全保證、訂單取消的庫存歸還保證，皆以測試證明未被削弱。

**Non-Goals:**

- 不做重量／地區／滿額免運等費率運算（`flat_rate` 維持單一固定值）。
- 不做商家可設定的配送前置天數、截單時刻或不可配送日。
- 不做 `shipping_methods.currency`（見 D4 的已知限制）。
- 不讓運送方式成為必填，也不做「商家要求必選」的設定。
- 不做購物車品項訊息的編輯端點（見 D2）。
- 不做退款金額的拆分（退貨要不要退運費是 returns/refunds 能力的問題，本變更只負責把 `shipping_fee` 記下來讓它日後有得算）。
- 不做 per-shop 可設定的點數核發公式（`member-tiers-and-points` 的既有 Non-Goal，本變更不推翻）。

## Decisions

### D1. 卡片訊息掛在品項上，`cart_items` 與 `order_items` 兩處都存

`gift_message varchar(500) NOT NULL DEFAULT ''`，兩張表都加。

**為何兩處都存。** 兩者的角色本來就不同，而且**訪客路徑逼著必須兩處都有**：訪客結帳的品項先經 `cart.Service.ReplaceItems` 物化進購物車，再由既有的 `Checkout` 讀購物車建單（姊妹 change `guest-checkout` design D1 的整個立足點）。若 `cart_items` 沒有這個欄位，訊息在中途就掉了，除非另開一條繞過購物車的下單路徑——那正是被明令禁止的東西。至於 `order_items`，它是**永久快照**（order-management design D4：建立後不因商品改名／刪除而變）；卡片訊息是顧客當下寫的字，本就該和 `product_title`、`sku_code` 一樣被凍結，而不是靠 join 一個日後會被清掉的購物車列。

**為何是 `NOT NULL DEFAULT ''` 而不是 nullable。** NULL 與 `''` 在這裡表達同一件事（沒有留言），留兩種表達方式只會讓每個讀取點都要寫一次三元判斷。既有慣例支持這個選擇：姊妹 change `guest-checkout` design D8 對 `shipping_address` 的選填欄位就是「鍵永遠存在、值可以是空字串」。回應中 `gift_message` 永遠是字串，不會是 `null`。

**為何叫 `gift_message` 而不是 `note` / `card_message`。** `note` 分不出是顧客寫的還是商家寫的（商家備註是日後另一個欄位，不該撞名）；`card_message` 把它綁死在「卡片」這個花店專有的載體上，但同一個欄位在別的商家就是禮物包裝的留言。`gift_message` 是這個東西在電商領域的通用名字。

**合併鍵由 `sku_id` 擴充為 `(sku_id, gift_message)`。** 這不是可選的：花店最典型的一單就是「同一款花束買兩束，一束給媽媽、一束給岳母」，兩張卡片內容不同。若合併鍵只看 SKU，這一單會被合併成數量 2 的單一品項，兩則訊息只會留下一則——**靜默的資料遺失**，而且是在最不該出錯的地方。`AddItem` 與 `ReplaceItems` 兩處同步改。

**隨之而來的一個實作陷阱，必須明寫。** `AddItem` 目前以 `Where(CartIDEQ, SkuIDEQ).Only(ctx)` 找既有品項；`Only` 在命中多列時回錯誤。一旦同一個 SKU 可以有多列（不同訊息），這行就是一顆定時 500。改為以完整的 `(cart_id, sku_id, gift_message)` 述詞配合 `First(ctx)`（ent 的 `First` 以 id 遞增取第一列，行為確定）。

**評估後否決：為 `(cart_id, sku_id, gift_message)` 加唯一索引。** 專案確實有「以 DB 唯一索引作最終仲裁者」的既有紀律（`shipments.order_id`、`point_transactions(order_id, kind)`），但那兩處守的是**不可逆的錯誤**（重複出貨、重複核發點數）。這裡守的是「併發兩次 AddItem 可能產生兩列而非累加」——一個結帳金額完全正確、顧客也看得到兩列的良性競態，而且它**今天就存在**（`AddItem` 本來就是交易內的 read-then-write，沒有任何約束擋它）。用一個鍵含 500 字元使用者自由文字的 btree 索引去換這個，代價與收益不成比例。

**庫存檢查仍以 SKU 為單位加總。** `ReplaceItems` 按 `(sku_id, gift_message)` 分組寫入，但把同一個 `sku_id` 的所有分組數量**加總後**才與 `stock_qty` 比對——庫存屬於 SKU，不屬於卡片訊息。漏掉這一步就會讓「兩束同款花、各一張卡」在只剩一件庫存時通過購物車驗證（雖然最終仍會被 checkout 的原子扣減擋下並整筆失敗，但那是在使用者按下送出之後才報錯，體驗與意圖都錯）。

### D2. 卡片訊息上限 500 字元；不提供編輯端點

**上限 500 個 rune（不是 byte）。** 以 `utf8.RuneCountInString` 在服務層檢查並回 422，欄位型別為 `varchar(500)`（Postgres 的 `varchar(n)` 本就以字元計，兩者一致，不會出現「Go 說可以、DB 說不行」）。理由：需要一個上限（這是未認證訪客也能寫入的自由文字，沒有上限就是一個 1MB body 限制以內的任意大小寫入面），但上限要對得起用途——一張手寫卡片頂多百來字，500 字元留了寬裕的餘地，同時遠小於任何濫用價值。以 rune 計是因為以 byte 計會讓「中文只能寫 166 字、英文可以寫 500 字」，那是把實作細節當成產品規則。

**不提供編輯端點（已評估並否決）。** `PUT /shop/cart/items/{itemID}` 目前只收 `quantity`；讓它同時收 `gift_message` 只要五行。否決的理由是它會打破 D1 剛剛建立的識別：修改訊息等於把這一列變成另一個 `(sku, message)` 身分，於是可以人工製造出兩列**完全相同** `(sku, message)` 的品項——那是 `AddItem` 的累加語意永遠不會產生的狀態，而系統裡沒有任何東西會去修復它。「移除再重新加入」已經完整覆蓋這個需求，且天然維持累加不變式。記為明確的取捨，不是遺漏。

### D3. 指定配送日是 `orders` 的獨立 `DATE` 欄位，不是 `shipping_address` 的選填鍵

`orders.requested_delivery_date date NULL`。

`order.ShippingAddress` 的原始註解確實寫著「shipping-logistics may add further *optional* fields to it later」——那是一個**允許**，不是一個指示，而且它談的是**地址欄位**。配送日不是地址：`shipping_address` 回答「送到哪」，配送日回答「哪天送」。把後者塞進前者會付出三樣代價：

1. **不可查詢。** 商家最想做的第一件事就是「明天要出哪幾單」。jsonb 裡的一個字串鍵要做到這件事需要表達式索引與一致的格式假設；一個 `date` 欄位加一個索引就結束了（本變更先不加索引——後台還沒有這個查詢端點；但欄位型別決定了它日後加得成，jsonb 則不然）。
2. **不可驗證。** jsonb 的值是什麼形狀由寫入者決定。今天寫 `"2026-08-05"`、明天寫 `"2026/8/5"`、後天寫 `"下週三"`，資料庫全部接受。
3. **時區與型別的誤差會固化在資料裡。** 見下。

**為何是 `DATE` 而不是 `timestamptz`。** 顧客選的是**日曆上的一天**，不是一個瞬間。存成 timestamp 就必須決定那一天的哪一刻，而任何選擇都是錯的：`2026-08-05T00:00:00Z` 在台北是 08-05 早上八點，看起來沒事；但同一個值在任何 UTC 以西的時區就是 08-04。這是經典的差一天 bug，而且會**寫進資料**、事後無法分辨。`DATE` 沒有這個問題，因為它根本不承載時刻。

**驗證只有一條規則，刻意如此。** 格式必須嚴格是 `YYYY-MM-DD`（`time.Parse("2006-01-02")`，因此 `2026-02-31` 這種不存在的日期也會被擋），且日期**不得早於 UTC 今日的前一天**。

那個「前一天」的鬆弛不是隨便給的：系統目前**沒有商家時區**這個資料，所以伺服器無法知道「今天」對這家店是哪一天。UTC+8 的台北在當地 08-05 早上七點時，UTC 還是 08-04；UTC−7 的洛杉磯在當地 08-04 傍晚時，UTC 已經是 08-05。以 `UTC今日 − 1日` 為下界，等於只擋掉「在地球上任何時區都已經成為過去」的日期——它抓得住 `2025-…` 這種打錯的年份（真正要防的東西），而不會誤殺任何一位在自己時區裡合法下單的顧客。

**評估後否決：上界（例如不得超過一年後）、商家可設定的前置天數、不可配送日。** 前置天數需要商家時區 + 每個運送方式各自的截單時刻 + 國定假日表，那是一整套模型；上界則會擋掉「一年後的婚禮先訂花」這種完全合理的訂單，用一個猜測的數字換不到什麼。**ONDO 只需要一個日期**，而過度設計的成本會由每一個接入的商家承擔。

**選填。** 不是每家店的結帳都收這個欄位；缺席即 NULL，回應中為 `null`。ONDO 自己的表單要不要必填是 ONDO 的事。

### D4. 運送方式在結帳時以 id 選定，費用當場快照；`shipping_methods` 新增公開唯讀端點

**流程。** 前端先 `GET /api/v1/shop/shipping-methods` 取得該商家的 active 運送方式（`{id, name, carrier, flat_rate}`），結帳時把選中的 `shipping_method_id` 送回來。`Checkout` 驗證它屬於本店且 `is_active`，把 `flat_rate` 複製成 `orders.shipping_fee`、把 `name` 複製成 `orders.shipping_method_name`，並保留 `shipping_method_id`。

**選填，而非必填。** 這一條讓整個變更幾乎全部落在 additive 範圍內：不帶 `shipping_method_id` 的請求（也就是今天所有的請求）行為與今天**逐位元組相同**——`shipping_fee = 0`、`total_amount = subtotal_amount`。必填則會同時打斷會員與訪客兩個既有端點，而且會讓一家還沒設定任何運送方式的商家連結帳都做不到。「商家可要求必選」是一個設定項，不是本變更的預設。

**不合法的 `shipping_method_id` 回 422，不是 404。** 它是請求 body 裡的一個值，與 `/items/{i}/sku_id` 指到不存在／非本店的 SKU 完全同型，沿用同一個既有處置（`ValidationError`，pointer `/shipping_method_id`）。已停用（`is_active = false`）的方式同樣回 422 而不是「照收費用」：商家把它下架就是不想再賣這個選項，用它的價錢成立一筆訂單違背那個意圖。

**費用讀取在交易之外。** 運送方式的解析與驗證發生在 `s.Client.Tx(ctx)` 之前（與 `Checkout` 既有的可購買性驗證同一階段）。商家在這個窗口內改價是良性競態：顧客被收的是他送出請求當下看到的那個價格，這比讓一筆結帳因為後台改了一次設定而失敗更合理。快照一旦寫進 `orders.shipping_fee` 就永不重算——與 `order_items.price_amount` 同一條紀律。

**`shipping_method_id` 是資訊性欄位，無 FK、無 edge。** 完全比照 `order_items.product_id`／`sku_id`（order-management design D4）：訂單需要的顯示與對帳資訊（`shipping_method_name`、`shipping_fee`）已經快照在自己身上，沒有什麼需要 join。更決定性的理由是：加 FK 會改變一個既有端點的行為——`DELETE /shipping-methods/{id}` 今天永遠成功，加了 FK 之後不是被 RESTRICT 擋下（商家再也刪不掉用過的方式），就是被 SET NULL 悄悄改寫歷史訂單。兩個都比「id 可能指向不存在的列，呼叫端不得倚賴它解析」更糟。

**只快照 `name`，不快照 `carrier`。** 快照的判準是「顧客當時同意的是什麼」——他選的是「台北市當日配送」這個選項與它的價錢，不是「黑貓宅急便」這家公司。而後台真正需要 carrier 的地方是建立出貨紀錄，`shipping.Service.CreateShipment` 本來就強制要求呼叫端明確傳入 `carrier`，不倚賴訂單上有沒有這個值。若日後證明後台想要預填，補一個 `shipping_method_carrier` 快照欄位是純 additive 的。

**新增公開唯讀端點，推翻 `shipping-logistics` design D7。** 該決定的理由原文是「目前沒有任何消費端……公開一個沒有真實呼叫方的唯讀端點，測試只能驗證回傳資料形狀正確」。ONDO 就是那個呼叫方，前提已消失。端點掛在 `/api/v1/shop` 的 TenantMW 群組下、無認證（比照 `GET /shop/products`），**只回傳 `is_active = true` 的列**，且回應刻意小於後台的序列化形狀——只有 `{id, name, carrier, flat_rate}`，不含 `is_active`（回傳的全是 active，這個鍵沒有資訊量）與時間戳。D13 規定 v1 只能 additive，所以每一個公開出去的鍵都是一份永久承諾；公開最小集合是唯一能日後再加的方向。

**已知限制：運費沒有自己的幣別。** `shipping_methods.flat_rate` 是一個裸的最小貨幣單位整數；本設計規定它以**訂單幣別**計價（訂單幣別由購物車鎖定，購物車幣別由第一個品項鎖定）。因此一家真的做多幣別的商家，其運送方式無法對不同幣別報不同價。加 `shipping_methods.currency` 需要：既有列的預設值從哪來（商家沒有「主要幣別」這個欄位）、後台 CRUD 的形狀、以及結帳時幣別不符的處置——那是一個獨立的決定，而本階段全系統沒有任何多幣別路徑（每張購物車鎖單一幣別、每筆訂單單一幣別）。明確記錄為限制而非疏漏。

### D5. 點數核發基準改為 `subtotal_amount`（品項小計），運費不計點

**這是本變更最需要正面論證的一節。** 運費一旦併入 `total_amount`，`member-tiers-and-points` 的規則「核發點數 = `orders.total_amount` / 100 無條件捨去」就會自動變成「運費也計點」——不是因為有人決定要這樣，而是因為沒有人決定。所以必須明確地決定。

**決定：核發基準為 `orders.subtotal_amount`（品項小計）。退貨扣回的基準維持「原本核發了多少就扣回多少」，不受影響。**

理由，由弱到強：

1. **忠誠點數獎勵的是購買，不是物流成本。** 運費對商家而言大部分是代收轉付給物流商的錢，不是營收也不是毛利。以它為基礎發放平台的忠誠貨幣，等於商家為一筆自己沒賺到的錢付出回饋成本。

2. **它會讓回饋取決於「怎麼送」而不是「買了什麼」，而且方向是錯的。** 同一束 3600 的花，門市自取拿 36 點、當日配送（運費 200）拿 38 點。從顧客的角度這說不通（我買的是同一束花）；從平台的角度更糟——它**獎勵顧客選最貴的配送方式**，也就是用忠誠點數補貼最低效的物流選擇。一條會誘導出反效果的規則，即使算術正確也是錯的。

3. **它讓退貨扣回變成一句說不清楚的話。** 顧客退的是花，不是已經跑完的那趟配送。以品項小計為基準時，「扣回 = 把退掉的商品所賺的點數收回」是一句完整、可以印在條款上的話；以應付總額為基準則變成「連已經履行的服務所賺的點數也一併收回」，而系統無法向顧客解釋為什麼。

4. **未來擴充下的穩定性。** 日後若出現手續費、包裝加購、稅，「以品項小計為基準」這條線已經畫在正確的位置，最多只需細化為「折扣後的品項小計」；「以應付總額為基準」則會把每一種新出現的費用自動吸進點數，每加一個費用就要重新吵一次。

5. **（決定性）它是唯一對既有已核發點數回溯成立的選擇。** 既有訂單回填後 `subtotal_amount = total_amount`、`shipping_fee = 0`，所以**每一筆已經發出去的點數，在新規則下都仍然完全正確**。反過來若選「運費計點」，新規則的真實內容會是「運費計點，但本次變更之前的每一筆訂單除外」——一條從第一天就帶著例外的規則，而那個例外永遠不會消失。

**因此可觀察的行為改變是零。** 舊系統能產生的任何一筆訂單（必然 `shipping_fee = 0`），在新規則下核發的點數與舊規則完全相同。`member-tiers-and-points` 的 spec 仍然**必須**改——因為它的文字現在指向了一個語意已經不同的欄位，照舊解讀會得出錯誤結果。這是一個必要的 MODIFIED delta，不是可以迴避的措辭問題。

**代價，誠實記錄：**

- `orders` 必須同時保留兩個金額。這本來就要（對帳與日後的退款拆分都需要 `shipping_fee`），所以不算本決定額外付出的成本，但它確實是「不能只留一個 `total_amount`」的原因之一。
- 想要「連運費一起回饋」的商家現在沒有開關。per-shop 可設定的核發公式在 `member-tiers-and-points` 的 Non-Goals 裡本來就是非目標，本變更不推翻它；若日後要做，那個 change 會同時決定基準與比例，屆時本決定是它的預設值而不是它的障礙。
- 顧客付了 3800 只拿到 36 點。交付給商家的說明必須寫清楚「點數以商品金額計算，運費不計」，否則客服會被問。

**已評估並否決的中間方案：以應付總額為基準，但對既有訂單以 `subtotal_amount` 回溯補正。** 它需要一個一次性的補發批次去修正歷史點數餘額與等級，而點數是**已經呈現給顧客看過**的數字——事後單方面調整它，換到的只是「運費也能計點」這個本身就有問題（見理由 2）的性質。

### D6. `total_amount` 語意變更的精確邊界

新定義：`orders.total_amount` = 應付總額 = `subtotal_amount + shipping_fee`。

**沒有改變的東西（逐項列出，因為這決定了破壞半徑）：**

- 沒有任何欄位被移除或改名。`total_amount` 這個鍵仍在，型別仍是 int64 最小貨幣單位。
- **沒有任何既有資料列的值改變。** 回填令 `subtotal_amount = total_amount`、`shipping_fee = 0`，於是每一列既有訂單在新定義下本來就成立。
- 沒有任何端點的路徑、狀態碼或授權規則改變。
- `payment.Service.InitiatePayment` 向金流請款的仍然是 `ord.TotalAmount`——**這一行不必改，而且因為語意變更自動變成正確的**（先前它是本變更要修的 bug 本身）。
- 既有的整合測試不需要為了配合本變更修改任何金額斷言（它們的訂單都沒有運送方式）。

**唯一改變的東西：** 日後帶運送方式的訂單，其 `total_amount` 會**大於** Σ(`items[].price_amount × quantity`)。任何自己以品項重算總額的消費端，從那一刻起會與 API 不一致。

**目前 v1 的外部消費端：零。** 已查證 `bouquet/public/` 全樹 `fetch(` / `api/v1` / `axios` / `XMLHttpRequest` 零命中；`/web`、`/admin`、`/e2e` 三個第一方前端沒有任何一處讀取訂單金額。

**已評估並否決：新增 `grand_total_amount`，讓 `total_amount` 維持品項小計。** 這是唯一能讓本變更 100% additive 的做法，但它的代價是永久的：`total_amount` 會成為一個**名字說謊的欄位**，而 v1 的 additive-only 規則會讓這個謊言再也改不掉。之後每一個讀這份 API 的人都要先學會「total 不是 total」，而第一個沒學會的人會寫出一個少收運費的請款。用一次窗口尚開時的破壞性變更，換掉一個永久的命名錯誤，方向是對的。

### D7. 金額組成由資料庫 CHECK 固定；扣庫存區塊逐字不動

新增 CHECK：

```sql
orders_subtotal_amount_check            subtotal_amount >= 0
orders_shipping_fee_check               shipping_fee    >= 0
orders_total_amount_composition_check   total_amount = subtotal_amount + shipping_fee
```

第三條是本變更把 D6 的新定義變成**資料庫層事實**的方式。三個金額欄位之間的關係若只活在 `Checkout` 的一行 Go 加法裡，下一個新增下單路徑或補資料的人就能製造出不一致的訂單，而且沒有任何東西會發現。同一行 CHECK 也順帶封死「有人日後想直接 `UPDATE orders SET shipping_fee`」這種繞過服務層的補丁。這與專案既有紀律一致（`orders_total_amount_check`、`cart_items_quantity_check` 都是同樣的想法，只是本條約束的是欄位**之間**的關係）。

**`Checkout` 的改動邊界。** 新的邏輯全部落在既有交易的**前後**：

| 位置 | 改動 |
| --- | --- |
| 交易前（與既有可購買性驗證同階段） | 解析／驗證配送日；解析／驗證運送方式並取得 `flat_rate` 快照 |
| 交易內：cart CAS `Active→Converted` | **不動** |
| 交易內：逐行條件式扣庫存 + 受影響列數檢查 | **不動一個字元**（僅累加變數由 `total` 更名為 `subtotal`） |
| 交易內：建立 `orders` | 多寫五個欄位 |
| 交易內：建立 `order_items` | 多寫 `gift_message` |

**驗證方式：** 既有的 `TestCheckoutConcurrentOversellingProtection`、`TestGuestCheckoutConcurrent...`、`TestGuestAndMemberCheckoutShareOneStockGuard` 三個 `-race` 並發測試**一行不改**地保留並通過；另新增一個「帶運送方式的並發結帳」版本，確認多出來的金額邏輯沒有在交易內引入新的等待點。訂單取消歸還庫存的既有測試同樣不改——運費不涉庫存，`CancelOrder` 完全不需要改動。

### D8. `events.OrderPaymentSucceeded.TotalAmount` **更名**為 `SubtotalAmount`，而非新增欄位

`points.Service.HandleOrderPaid` 需要的是點數基準（D5），也就是品項小計。做法有二：

- **新增** `SubtotalAmount` 並保留 `TotalAmount`：任何忘了設定新欄位的建構點會靜默地拿到 Go 零值，於是**所有點數都變成 0**，而且不會有任何編譯錯誤或執行期錯誤——只有顧客會發現。
- **更名**：每一個建構點與讀取點在編譯期被強制重新檢視。

選更名。這是一個 Go 內部型別，不是公開契約，更名是免費的；而 `member-tiers-and-points` 的 design Risks 已經記載過一個同型的教訓（「`Dispatcher` 忘了接線，points 就靜默不核發」），沒有必要再種一顆同樣的雷。事件本身不再攜帶應付總額，因為目前沒有任何訂閱者需要它；日後需要時再加是 additive 的。

### D9. 本變更動用 D13 破例窗口的第三次，也是最後一次

破壞性面向**恰好一項**：D6 的 `total_amount` 語意變更。其餘全部是 additive（新欄位、新端點、新的選填請求鍵；既有請求一律照常成功且回應相同）。

姊妹 change `guest-checkout` design D12 寫過一句本變更必須正面回應的話：「ONDO 即將接入，這個窗口本來就該在它接入時關閉，而不是每次覺得方便就再開一次。」

本變更仍然開它，理由有三：

1. **窗口尚未關閉，且這是最後一個機會。** 已重新查證 `bouquet/public/` 仍為零 API 呼叫（fetch/axios/XMLHttpRequest/`api/v1` 全樹零命中）。ONDO 接入之後，`total_amount` 的語意就會被寫進一個真實前端；此刻不改，就是永遠不改。
2. **不改的代價是永久的，改的代價是一次性的。** D6 已論證：唯一的 additive 替代方案會留下一個名字說謊的欄位，而 additive-only 規則會讓它再也改不掉。
3. **破壞半徑經過實測而非推測。** 沒有任何資料列的值改變、沒有任何鍵被移除、既有測試零修改、零外部消費端。這是一次語意上的破壞，不是形狀上的破壞。

**窗口就此關閉。** 本 design 明文記錄：ONDO 接入後，`/api/v1` 的任何破壞性變更都必須改為新增 `/api/v2` 或新端點；後續 change 不得再引用「尚無外部消費者」這個理由。

### D10. 舊訂單的處置：回填，而不是遷就

`UPDATE orders SET subtotal_amount = total_amount`（`shipping_fee` 由 `DEFAULT 0` 覆蓋）。

這一步**必須排在組成 CHECK 建立之前**，否則 `subtotal_amount` 的 `DEFAULT 0` 會讓每一筆金額非零的既有訂單違反 `total_amount = subtotal_amount + shipping_fee`，migration 當場失敗。Atlas 的 diff 引擎只產生 schema 形狀、從不產生資料搬移（姊妹 change `member-identity-per-shop` 的 migration 檔頭已經記載過這件事），所以本次 migration 需要依既有慣例手動調整順序，並重算 `atlas.sum`。

**已評估並否決：讓 `subtotal_amount` 可為 NULL，用「NULL 表示舊訂單」。** 那會讓每一個讀取點永遠帶著一個三元判斷，而且點數規則會變成「以 `subtotal_amount` 為準，若為 NULL 則以 `total_amount` 為準」——D5 好不容易換到的回溯一致性又被丟掉了。回填一次，之後所有程式碼只看一種形狀。

### D11. 訪客與會員兩條路徑一致，且都不新增下單入口

三項新輸入在兩條路徑上都成立，但**各自沿用該端點既有的 body 形狀**：

| | 會員 `POST /shop/checkout` | 訪客 `POST /shop/guest/checkout` |
| --- | --- | --- |
| 既有形狀 | 平鋪（body 本身就是收件資訊） | 巢狀（`{email, items, shipping_address}`） |
| 卡片訊息 | 在購物車品項上（`POST /shop/cart/items` 的 `gift_message`） | `items[].gift_message` |
| 運送方式 | 平鋪的 `shipping_method_id` | 頂層 `shipping_method_id` |
| 配送日 | 平鋪的 `requested_delivery_date` | 頂層 `requested_delivery_date` |

**為何不順手把會員端點改成巢狀。** 兩個端點形狀不一致確實是個疤，但它是**既有的**疤（`guest-checkout` 新增時就是這樣）。把會員端點改成 `{shipping_address: {...}, ...}` 會破壞一個既有端點的請求形狀，換到的只是美觀；平鋪新增兩個鍵則是純 additive——先前會成功的請求全部照常成功。用一個真實的破壞換一個外觀上的整齊，方向是錯的。（另一個選項是同時接受兩種形狀，那是把一個疤換成一段沒人說得清楚的解析邏輯。）

**共用的是 `order.Service.Checkout` 的新輸入型別 `CheckoutInput{ShippingAddress, ShippingMethodID *int, RequestedDeliveryDate *string}`。** 配送日以**原始字串**傳進服務層而不是在 handler 先解析：這樣「什麼是合法日期」只有一份定義，兩條路徑的 422 回應逐位元組相同，handler 不需要知道任何日期規則。order-management spec 的「所有下單路徑共用單一結帳實作」不變式因此不但未被削弱，還多了兩項共用內容。

## Risks / Trade-offs

- **[`total_amount` 語意變更打到某個未被發現的消費端]** → 已實測外部消費端為零（ONDO 零 API 呼叫、`/web` `/admin` `/e2e` 不讀訂單金額）；沒有任何既有資料列的值改變，所以即使有讀取端，它讀到的**既有訂單**數字仍完全相同——只有新產生的、帶運送方式的訂單才會不同。D9 明文關閉窗口，此類變更不會再有第四次。

- **[點數基準改變被誤讀為「點數變少了」]** → 可觀察的行為改變是零：舊系統能產生的任何訂單（`shipping_fee = 0`）核發的點數與先前完全相同（D5 理由 5）。真正需要溝通的是**日後**的規則，故交付給商家的說明 MUST 寫明「點數以商品金額計算，運費不計」。

- **[`shipping_method_id` 是無 FK 的資訊性欄位，商家刪除運送方式後它會指向不存在的列]** → 與 `order_items.product_id`／`sku_id` 完全同型（order-management design D4 已建立此慣例）：對帳與顯示所需的 `shipping_method_name`／`shipping_fee` 已快照在訂單上，呼叫端 MUST NOT 倚賴 `shipping_method_id` 能解析。加 FK 的代價（刪不掉用過的方式，或 SET NULL 悄悄改寫歷史）更大（D4）。

- **[運費沒有幣別，多幣別商家無法為不同幣別報不同運費]** → D4 的已知限制。本階段全系統無任何多幣別路徑（購物車鎖單一幣別、訂單單一幣別），故無法在今天驗證任何多幣別設計是否正確；記為限制，加 `shipping_methods.currency` 是純 additive 的後續變更。

- **[配送日的「不得早於 UTC 昨日」在極端時區下仍有一天的鬆弛]** → 這是刻意的（D3）：沒有商家時區資料時，任何更嚴的下界都會誤殺某些時區裡完全合法的當日下單。它要防的是打錯年份，不是防止顧客選昨天——後者由商家的實際作業流程與 ONDO 自己的表單驗證處理。

- **[購物車品項合併鍵改變，使既有前端的「同一 SKU 再加一次會累加」預期在帶訊息時不成立]** → 這正是意圖（D1）：不帶訊息時（`gift_message` 為空字串）行為與今天完全相同，只有明確附了不同訊息才會分成兩列。既有的購物車整合測試不需修改即可通過，本身就是這一點的證據。

- **[卡片訊息是未認證訪客也能寫入的自由文字]** → 500 rune 上限（D2）+ 訪客結帳既有的兩條限流規則 + 1MB body 上限。內容不被系統解讀為任何格式（不做 Markdown、不做 HTML），輸出時由消費端負責跳脫——與 `order_items.product_title` 同級的處置。

- **[trade-off：`Checkout` 在交易前多一次 `shipping_methods` 查詢]** → 每筆結帳一次主鍵查詢，發生在交易之外，不延長交易持有時間。換到的是「運費快照與扣庫存在同一個方法裡完成，沒有第二條路徑」。

- **[trade-off：多三個金額欄位，讀取端要知道哪個是哪個]** → 由 `orders_total_amount_composition_check` 在資料庫層固定關係（D7），並在 spec 明文定義三者。這比只留一個含義模糊的 `total_amount` 好，也比留一個名字說謊的 `total_amount` 好（D6）。

## Migration Plan

1. ent schema：`CartItem`／`OrderItem` 加 `gift_message`；`Order` 加 `subtotal_amount`／`shipping_fee`／`shipping_method_id`／`shipping_method_name`／`requested_delivery_date` 與三條 CHECK。`go generate ./internal/ent`。
2. `make migrate-gen name=add_order_extras` 產生 up/down。
3. **手動調整產生的 SQL**（依 `cmd/migrategen` 檔頭已載明的慣例）：把 `UPDATE orders SET subtotal_amount = total_amount` 插在 ADD COLUMN 之後、`orders_total_amount_composition_check` 之前；確認 diff 沒有順手提議 drop 掉手寫的 `UNIQUE NULLS NOT DISTINCT` 約束（姊妹 change 踩過）。
4. 重算 `atlas.sum`（以 `ariga.io/atlas/sql/migrate` 的 `WriteSumFile` 對 `migrations/` 重新雜湊；atlas CLI 未安裝於本環境）。
5. `make migrate` 套用；驗收條件：既有訂單 `subtotal_amount = total_amount`、`shipping_fee = 0`、`total_amount` 值未變。
6. 無新增設定值（不像姊妹 change 需要新的 TTL 或限流門檻）。`shipping_methods` 的權限節點與角色授權早已由 `shipping-logistics` 的 seed 建立，公開端點無 RBAC，seed 不需改動。

**Rollback**：`make migrate-down` 移除五個欄位、兩個品項欄位與三條 CHECK。`total_amount` 的值本身從未被改寫，因此回滾後每一筆訂單的 `total_amount` 仍是一個合法的值——**但帶運費的訂單回滾後會退化成只含品項小計的舊語意，那筆運費資訊會隨欄位一起消失**。這是唯一不可逆的部分，明確記錄於此：若已有帶運費的訂單，回滾前應先匯出 `shipping_fee`。

**部署**：無正式環境，程式部署即生效。與 ONDO 的接入沒有時序耦合——平台先上線（既有行為不變），ONDO 再改前端。

## Open Questions

- **後台是否需要「依配送日查訂單」的端點？** 本變更把 `requested_delivery_date` 做成可索引的 `date` 欄位正是為了讓它日後做得成，但現在不做：後台訂單列表目前只支援分頁與 `status` 篩選，加一個日期篩選需要同時決定區間語意與排序，而且沒有 UI 消費它。等後台出貨作業成為一個 change 時，這個查詢是它的一部分。
- **退貨時運費要不要退？** 本變更只負責把 `shipping_fee` 記在訂單上，讓這個問題日後**有得算**。答案本身屬於尚不存在的 returns/refunds 能力（目前 `payment_status` 的退款值 `2`／`3` 沒有任何程式路徑會寫入），在那個 change 裡連同部分退款一起決定。
