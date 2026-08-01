## Context

`member_tiers.discount_percent` 今天的完整生命史：

```
member-tiers-and-points (2026-07-18)  欄位建立，CHECK 0–100，spec 明寫「本階段僅儲存不套用」
order-extras            (2026-07-31)  未觸及
admin-commerce-operations(2026-08-01) 後台可設定、可顯示，Non-Goals 再次寫明「不做實際折扣計算」
seed（demo 商家）                      銀卡 5%、金卡 10%，註解寫「populated so the back office
                                       has something to display」
```

也就是說：**平台已經把一個什麼都不做的旋鈕交到商家手上，而且還先幫他轉好了兩格。** 全樹搜尋 `DiscountPercent` 的讀取點，只有 CRUD 的驗證與序列化，沒有任何計價路徑。

結帳側的既有形狀（本變更必須繞開的東西）：

```
order.Service.Checkout(ctx, shopID, memberID, CheckoutInput)
  交易前   ShippingAddress.Validate / parseRequestedDeliveryDate / resolveShippingMethod
  交易內   cart CAS Active→Converted
           for each line:  UPDATE product_skus SET stock_qty -= q WHERE stock_qty >= q   ← 全系統唯一
                           affected == 0 → rollback                                        一份扣庫存
           subtotal 累加
           INSERT orders / order_items
           冪等預約定案（條件式 UPDATE ＋ 受影響列數）
  commit
```

三個 `-race` 並發測試（`TestCheckoutConcurrentOversellingProtection`、`TestGuestCheckoutConcurrentOversellingProtection`、`TestGuestAndMemberCheckoutShareOneStockGuard`）守著那個迴圈。**本變更所有新邏輯都落在迴圈之外**，且以那三個測試一行不改地通過作為「未被削弱」的證據——與 `order-extras`／`checkout-idempotency` 兩個姊妹 change 完全相同的證據義務。

金額的既有事實：`orders` 有 `subtotal_amount`／`shipping_fee`／`total_amount`，關係由 DB CHECK `orders_total_amount_composition_check` 強制為 `total = subtotal + fee`。點數核發基準是 `subtotal_amount`（`order-extras` design D5）。

約束：PostgreSQL 16；金額一律 int64 最小貨幣單位（**TWD 的最小貨幣單位是元**，`3400` 就是 NT$3,400），**禁用浮點數**；`/api/v1` 的 D13 破例窗口已於 `order-extras` 用盡並明文關閉，本變更必須純 additive；`api/openapi/openapi.yaml` 的四道契約檢查會因回應形狀改變而轉紅，必須同步更新。

## Goals / Non-Goals

**Goals:**

- 讓商家在後台設定的 `discount_percent` **真的**改變顧客付的錢與金流請款的金額。
- 折抵金額成為訂單上**可稽核的一級事實**：事後任何人都答得出「這一單當時折了多少、依據哪一級、幾 %」，且該答案不因商家日後改動等級設定或顧客升降級而改變。
- 折扣的算術是**整數、確定、可在 spec 上讀到唯一一種捨入方向**——不含糊，因為含糊會產生對不上帳的一分錢。
- 折扣進入之後，金額三者（現在是四者）的關係仍然是**資料庫層事實**，而不是一行只活在 Go 裡的加法。
- 點數核發基準在折扣存在後仍然有一個**經得起解釋**的定義，且**已核發的每一點在新規則下都仍然正確**。
- 兩條下單路徑（會員／訪客）行為一致，且**都只經由既有的 `order.Service.Checkout`**。
- 既有的並發安全扣庫存保證與冪等鍵機制**皆以測試證明未被削弱**。
- 純 additive：**不動用**已關閉的 D13 破例窗口。

**Non-Goals:**

- 不做優惠券／促銷碼／滿額折扣／限時活動（那是一個獨立的 promotions 能力，它會需要自己的資料表、疊加規則與有效期模型）。
- 不做**多重折扣疊加**：一筆訂單至多套用**一個**等級折扣。
- 不做「以點數折抵金額」的兌換機制——`member-tiers-and-points` 的這條 Non-Goal 本變更**不推翻**（見 D1 末段：本變更只推翻其中一條，並說明為何另一條的前提沒變）。
- 不做 per-shop 可設定的「折扣是否含運費」開關（D2 決定運費不打折，全平台一致）。
- 不做最低消費門檻、最高折抵上限、單品排除清單。
- 不做訂單建立後的折扣調整／重算端點（那與快照策略 D8 直接衝突）。
- 不做退款時的折扣拆分（`payment_status` 的退款值 `2`／`3` 至今沒有任何程式路徑會寫入；`order-extras` 已把同一個問題留給尚不存在的 returns/refunds 能力）。
- 不做訪客結帳前的折扣預估端點（D10 論證為何那是一個不該存在的端點）。
- 不做 per-shop 可設定的點數核發公式（`member-tiers-and-points` 的既有 Non-Goal，本變更不推翻）。

## Decisions

### D1. 正面處理 `member-tiers-and-points` 的 Non-Goal：它有三個支撐前提，兩個已經消失

該 change 的 design Non-Goals 原文是：

> No point redemption/spending mechanism (no checkout discount, no "pay with points") — this phase only earns and claws back points. `discount_percent` on `member_tiers` is a reserved, unused field for a future redemption/pricing feature.

spec 的對應句更直接：「MAY 包含 `discount_percent`（0–100 之間，**本階段僅儲存不套用任何折扣邏輯**）」。

**這不是一個被遺忘的欄位，是一個被寫下來的決定。** 所以要推翻它，必須說出當時支撐它的是什麼、以及那些東西今天還在不在。逐條：

**前提一：「它屬於一個更大的 redemption/pricing 特性，該特性尚未設計。」**
**部分成立，而且本變更刻意只切下已經完全確定的那一半。** 那句話把兩件不同的事綁在一起了：「以點數折抵金額」（redemption——需要決定兌換比率、是否可與其他優惠並用、退貨時如何回收已兌換的點數、以及一套顧客可見的兌換 UI）與「等級自帶折扣率」（pricing——只需要一個乘法）。前者的設計空間今天仍然是開的，本變更**不碰它**，`member-tiers-and-points` 的那條 Non-Goal 在 redemption 這一半上繼續有效。後者的設計空間早就被 `discount_percent` 這個欄位本身封死了：0–100 的整數、掛在等級上、每個顧客同時至多屬於一級。**要決定的只剩「乘在什麼上」與「怎麼捨入」——那正是本 design 的 D2 與 D4，兩節，不是一個階段。**

**前提二：「沒有後台 UI，商家看不到這個欄位。」**
**已消失。** `admin-commerce-operations` 把 `discount_percent` 做成會員等級表單的一個輸入框、列表的一欄，E2E 甚至有斷言驗證它「以 `%` 顯示而不是 `NT$`」。從商家的角度，這個欄位與 `min_points` 長得一模一樣——而 `min_points` 是**真的會作用**的。該 change 自己的 Non-Goals 寫著「`discount_percent` 的實際折扣計算（`member-tiers-and-points` 已定義它本階段只儲存）」，也就是說它**知道**自己在做這件事，並且把後果留給了本變更。留下來的後果是具體的：畫面上兩個並排、外觀相同的欄位，一個生效一個不生效，而且沒有任何提示。

**前提三：「沒有真實商家，所以沒有人會設定它然後期待它作用。」**
**已消失，而且消失得比前提二更徹底。** ONDO 溫度花室即將上線；更直接的是 **seed 自己已經把 demo 商家的銀卡設成 5%、金卡設成 10%**，註解理由是「so the back office has something to display」。也就是說平台交付的示範資料本身就在演示一個不存在的功能。

**因此：推翻。** 而且要注意這次推翻的性質與姊妹 change 不同——`checkout-idempotency` 推翻 `guest-checkout` D11 時，論證的是「其中一個理由當初就是誤判」。這裡沒有誤判：`member-tiers-and-points` 在它自己的時空裡是對的（沒有 UI、沒有商家、沒有消費端，做了也沒有人能驗證它是否符合期待）。**改變的是世界，不是判斷。** 誠實地記下這一點比宣稱前人想錯了更有用，因為它同時說明了**下一個**這種欄位該怎麼處理：不要在沒有消費端時提前把可設定項交到商家手上。

**已評估並否決：不做套用，改成把後台的 `discount_percent` 欄位拿掉或標成「未啟用」。** 這確實是一個誠實的選項，而且成本近乎零。否決的理由是它解決的是**症狀**（商家被誤導）而不是**需求**（商家想給 VIP 折扣）——而那個需求在一個做會員等級的電商平台上不會消失，只會變成下一個 change 的第一句話。把欄位藏起來還有一個具體代價：`member_tiers.discount_percent` 已經在 `/api/v1` 的公開回應（`MemberTier`）與 `v1-surface.txt` 裡，藏掉 UI 不會讓它從契約消失，只會讓契約與 UI 各說各話。

### D2. 折扣套在**品項小計**上，運費不打折

`discount_amount = f(subtotal_amount, discount_percent)`，`shipping_fee` 完全不參與。

理由，由弱到強：

1. **運費不是商家的營收。** `order-extras` design D5 為了決定點數基準已經完整論證過這件事：運費大部分是代收轉付給物流商的錢。對一筆自己沒賺到的錢打折，等於商家自掏腰包補貼物流商——那不是任何商家設定「VIP 九折」時的意圖。

2. **它會讓折扣金額取決於「怎麼送」而不是「買了什麼」。** 同一束 3800 的花，門市自取折 380、當日配送（運費 200）折 400。這對顧客說不通（我買的是同一束花），對商家更糟——**它獎勵顧客選最貴的配送方式**，與 `order-extras` D5 理由 2 指出的反效果是同一個，方向也相同。

3. **「免運」是一個獨立的、商家真正會要的產品功能，而它不是「運費打 100 折」。** 商家想給的是「VIP 免運」，那是一個布林值，不是一個百分比；把它擠進 `discount_percent` 會產生「VIP 九折＝運費也九折」這種沒有人要的中間物。等級免運是一個 additive 的後續欄位（`member_tiers.free_shipping`），本決定不擋它，而且正因為折扣不碰運費，那個欄位日後可以獨立地加。

4. **（決定性）它讓每一條既有的金額語句都不必重寫。** `subtotal_amount` 仍然是 Σ(`items[].line_total`)，`shipping_fee` 仍然是所選運送方式 `flat_rate` 的快照、**永不重算**（`order-extras` D4 的原話）。折扣只是在兩者之間插入一項。若折扣套在含運費的總額上，`shipping_fee` 就會變成一個「帳面上是這個數字，但實際上你只付了 90%」的欄位——而它是**對帳欄位**，商家要拿它跟物流商的請款單核對。一個對不上物流商帳單的 `shipping_fee` 比沒有這個欄位更糟。

**代價，誠實記錄：** 顧客看到「VIP 九折」而總額只少了 `subtotal × 10%`，若他把運費也算進「應該打折的錢」就會覺得少折了。交付給商家的說明必須寫明「折扣以商品金額計算，運費不折」——與點數的措辭刻意平行（`docs/storefront-integration.md` 第 9 節已經有一段同型的文字）。

### D3. 訂單記錄 `discount_amount`，而不是「用 `discount_percent` 事後重算」

**這一條不是可選的，是本變更能不能稽核的分水嶺。**

只存 `discount_percent`（甚至只存 `member_tier_id`）而在需要時重算，看似省一個欄位，實際上會產生一個**永遠算不回來的數字**：`discount_percent` 是商家隨時可以改的設定值，而 `member_tiers` 是可以被刪除的列（刪除時 `members.level_id` 由 FK `SET NULL`）。商家在 8 月把金卡從 10% 改成 15%、9 月刪掉金卡，那麼 7 月那筆訂單的折抵金額在事後就有三個互相矛盾的答案，而**沒有一個是當時真的發生的那個**。

同一個判斷在本專案已經做過三次，形狀完全相同：`order_items.price_amount`／`product_title`（商品改價改名不動已成立的訂單）、`orders.shipping_fee`／`shipping_method_name`（`order-extras` D4：「快照一旦寫進就永不重算」）。本決定是第四次，不是新發明。

因此 `orders` 新增：

| 欄位 | 型別 | 語意 |
| --- | --- | --- |
| `discount_amount` | `bigint NOT NULL DEFAULT 0` | **實際折抵的金額**，最小貨幣單位。恆等式的一項 |
| `discount_percent` | `smallint NULL` | 當時**實際套用**的折扣率。NULL ＝ 結帳當下顧客不屬於任何等級 |
| `member_tier_name` | `varchar(100) NULL` | 當時等級的名稱快照，**不因日後改名或刪除而變動** |

**為何不快照 `member_tier_id`。** `order-extras` D4 決定快照 `shipping_method_name` 而不快照 `carrier` 時，判準寫得很清楚：「快照的判準是**顧客當時同意的是什麼**」。顧客同意的是「金卡會員，九折」——一個名字與一個率，不是一個資料列的 id。而 `shipping_method_id` 之所以被保留，是因為**前端要用 id 選運送方式**（`GET /shipping-methods` → `shipping_method_id`），有真實的往返需求；等級沒有這個往返，顧客從來不「選」一個等級。少一個永久的 v1 鍵、少一段「這個 id 可能指向不存在的列，呼叫端不得倚賴它解析」的說明。**重新評估的觸發條件明確**：出現一份需要**依等級列**（而不是依名字）分組的後台報表時，加 `member_tier_id` 是純 additive 的。

**為何 `discount_percent` 在「有等級但該等級沒設折扣」時是 `0` 而不是 NULL。** 兩者都要能表達，但它們是**不同的事實**：「這位顧客當時不屬於任何等級」與「這位顧客當時是一般會員，而一般會員沒有折扣」。前者 `member_tier_name IS NULL`，後者 `member_tier_name = '一般會員'` 且 `discount_percent = 0`。因此 `member_tiers.discount_percent` 為 NULL 時，訂單上快照的是 `0`——**訂單上的這個欄位回答的是「套用了幾 %」，而那在有等級時永遠是一個數字。**

### D4. 捨入：`discount_amount = subtotal_amount × discount_percent / 100`，整數運算、**無條件捨去**

```go
// order/service.go — 唯一一處計算折抵金額
discount := subtotal * int64(percent) / 100
```

**先把問題說清楚。** `3400 × 10 / 100 = 340` 剛好整除；`3333 × 10 / 100 = 333.3` 不整除。不整除時的處置有三種，而且必須挑一種寫進 spec：

| 方式 | 3333 @ 10% | 顧客付 | 誰受益 |
| --- | --- | --- | --- |
| **無條件捨去（採用）** | 折 333 | 3000 | 商家，**上限嚴格小於 1 個最小貨幣單位** |
| 四捨五入 | 折 333 | 3000 | 視個位數而定，兩邊各半 |
| 無條件進位 | 折 334 | 2999 | 顧客，上限同樣小於 1 個最小貨幣單位 |

三者的金額差距在任何情況下都**小於一個最小貨幣單位**（TWD 就是小於 1 元）。所以這一節要選的不是「哪個比較划算」，而是**哪一個規則最不容易在日後被寫錯**。

**採用無條件捨去，理由如下：**

1. **（決定性）系統裡已經有一條捨入規則，而它就是無條件捨去。** 點數核發是 `floor(subtotal / 100)`，寫在 spec、寫在 `points.Service`、寫在 `admin/lib/money.js` 的註解、也寫在交付商家的說明裡。再引入第二種捨入方向，代價不是一次選擇，而是**每一個日後碰金額的人都要先查是哪一種**。一個系統裡少一種要記的規則是有價值的（`checkout-idempotency` D8 為了 TTL 對齊 24 小時用過同一個論證）。

2. **它是 Go 整數除法的原生行為，因此無法被實作錯。** `a * p / 100`（`a >= 0`、`p >= 0`）就是無條件捨去，沒有 helper、沒有 `+50`、沒有 `math.Round`。四捨五入要寫成 `(a*p + 50) / 100`——那個 `+ 50` 是本專案裡最典型的「下一個人會順手『簡化』掉」的東西，而簡化掉之後**沒有任何測試會噴錯**（它只在不整除時差 1）。

3. **它讓「折抵永遠不會超過商家答應的比例」成為真的。** 商家設定 10%，顧客實際拿到的折扣率永遠 ≤ 10%。無條件進位會讓一筆 10% 的設定實際折出 10.03%——金額微小，但它讓「`discount_percent` 是上限」這句話變成假的，而那是商家對這個欄位唯一的直覺。

**乘除順序刻意是「先乘後除」。** `subtotal / 100 * percent` 會先丟掉小於 100 的尾數（3333 → 33 → 330，少折 3 元），那是一個大 100 倍的誤差。溢位的界限誠實記錄：`subtotal × 100` 要溢位 int64 需要 `subtotal > 9.2×10^16`，而 `subtotal` 本身在累加時就會先溢位——**這是既有的、與本變更無關的界限**，不是本決定引入的。

**`discount_percent = 100` 的邊界**：`subtotal × 100 / 100 = subtotal`，折抵恰好等於小計，`total = shipping_fee`。不需要特別處理，也不需要 clamp——這正是 D5 的 CHECK `discount_amount <= subtotal_amount` 所描述的邊界，而算術本身永遠落在界內。

**`subtotal = 0` 的邊界**：折抵 0。與現行的「零金額訂單核發零點數但仍留下記錄」同一種處置。

### D5. 新的金額恆等式，以及讓它成為資料庫事實的四條 CHECK

```
total_amount = subtotal_amount − discount_amount + shipping_fee
```

減項刻意夾在中間而不是寫成 `(subtotal − discount) + fee`，因為那個括號正是 D2 的內容：**折扣只吃 `subtotal`，運費在折扣之外**。這個順序在 spec、契約敘述與後台畫面上必須一字不差地一致。

`orders` 的約束變成：

```sql
orders_total_amount_check              total_amount    >= 0            （既有，不動）
orders_subtotal_amount_check           subtotal_amount >= 0            （既有，不動）
orders_shipping_fee_check              shipping_fee    >= 0            （既有，不動）
orders_discount_amount_check           discount_amount >= 0                          ← 新增
orders_discount_amount_bound_check     discount_amount <= subtotal_amount            ← 新增
orders_discount_percent_check          discount_percent IS NULL
                                        OR (discount_percent BETWEEN 0 AND 100)      ← 新增
orders_tier_snapshot_check             (member_tier_name IS NULL) = (discount_percent IS NULL)  ← 新增
orders_total_amount_composition_check  total_amount = subtotal_amount
                                        - discount_amount + shipping_fee             ← 取代既有
```

**`discount_amount <= subtotal_amount` 是這一組裡最重要的一條，值得單獨說。** 它把「折扣永遠不會把商品金額變成負的」變成資料庫事實。少了它，一個手寫的 `UPDATE orders SET discount_amount = 99999` 會產生一筆 `total_amount` 為負的訂單——雖然 `orders_total_amount_check` 會擋下 `total < 0`，但擋下的方式是把錯誤呈現成「總額為負」，而真正的錯是「折抵大於商品金額」。**約束應該在錯誤發生的那一層說話**，這樣下一個讀到 migration 的人就知道界線畫在哪裡。

**`orders_tier_snapshot_check` 讓半套快照不可能存在。** 「有等級名字但沒有率」或「有率但沒有名字」都是無法解釋的資料列，而它們只可能來自一次寫錯。這是 `checkout_idempotency_keys` 的 `(status = 1 AND order_id IS NOT NULL)` 那條 CHECK 的同型應用：把「一組欄位要嘛都在、要嘛都不在」交給資料庫，下一個寫入這張表的人就不必記得它。

**取代既有的組成 CHECK 是一次 DROP＋ADD，不是資料變更。** 既有列的 `discount_amount` 由 `DEFAULT 0` 填入，於是每一列既有訂單在新恆等式下**本來就成立**（`total = subtotal − 0 + fee`），**不需要任何回填**——這與 `order-extras` D10 必須手動插入 `UPDATE orders SET subtotal_amount = total_amount` 的處境不同，那次是因為新欄位沒有能讓既有列成立的預設值。本次有。驗收條件因此可以寫得很硬：**migration 前後 `SELECT count(*) FROM orders WHERE total_amount <> subtotal_amount - discount_amount + shipping_fee` 恆為 0，且沒有任何一列的任何一個既有欄位的值改變。**

### D6. 點數核發基準改為**折後**品項小計——上一把尺這次判不出勝負，所以必須換一把

**先把上一把尺拿出來用，不能跳過。** `order-extras` design D5 的決定性理由（理由 5）是：

> 它是唯一對既有已核發點數回溯成立的選擇。既有訂單回填後 `subtotal_amount = total_amount`、`shipping_fee = 0`，所以每一筆已經發出去的點數，在新規則下都仍然完全正確。

把同一把尺套在本變更的兩個候選上：

| 候選 | 對既有已核發點數是否仍然正確？ |
| --- | --- |
| 基準 ＝ `subtotal_amount`（原價） | **是**——既有列 `discount_amount = 0` |
| 基準 ＝ `subtotal_amount − discount_amount`（折後） | **是**——既有列 `discount_amount = 0`，兩式相等 |

**兩個都通過。** 這一次那把尺是一個**必要條件而不是判準**——因為本變更的回填讓 `discount_amount` 對每一筆既有訂單都是 0，兩個候選在歷史資料上**逐筆相同**。誠實地說出這件事很重要：沿用上一次的措辭宣稱「這是唯一回溯成立的選擇」會是一句假話。所以：**回溯一致性是門檻，兩個候選都跨過了；真正的決定要用別的理由做。**

**決定：核發基準為 `subtotal_amount − discount_amount`（折後商品金額）。**

理由，由弱到強：

1. **`order-extras` design D5 自己預告了這個答案。** 它的理由 4 原文：「日後若出現手續費、包裝加購、稅，『以品項小計為基準』這條線已經畫在正確的位置，**最多只需細化為「折扣後的品項小計」**」。那不是本變更事後找到的支持，是上一個決定在做的時候就寫下的預期。照著它做，等於兌現一個已經公開的承諾；反過來做，等於推翻一個沒有人反對過的預測，而且沒有新事實可以拿來當理由。

2. **折扣與點數是同一份忠誠預算的兩種形式。** 以原價計點等於對同一筆交易同時給折扣與**全額**回饋，於是回饋成本隨等級提升而**相乘**放大——而商家在後台只看得到一個 `discount_percent` 旋鈕，看不到那個乘數。一個會在商家背後自己加倍的成本結構是錯的，即使每一步的算術都對。

3. **它讓顧客手上的數字可以自己驗算。** 折後基準下，顧客看到「商品 3800 − 折扣 190 ＝ 3610」與「+36 點」，兩者之間是他做得出來的除法。以原價計點則會出現「實付 3610、拿到 38 點」，而 38 這個數字**在他的收據上不存在任何來源**——客服唯一能說的是「點數是照原價算的」，那是一句沒有人會預期的話。

4. **它讓「100% 折扣」不會核發點數。** 商家用一筆全額折扣的訂單處理補寄或贈品時，以原價計點會核發 38 點給一筆商家沒有收到任何商品收入的訂單。以折後計點則自然是 0。

**已評估並否決：原價計點（「獎勵忠誠不該懲罰折扣」）。** 這個論點的道德直覺是真的：顧客會覺得「我升級了，反而每一單賺的點數變少」。但它的實際後果比直覺弱得多——等級只會**降低**單筆的累積速率，不會讓餘額下降（`min_points` 比的是當前餘額，而餘額只在退貨扣回與手動調整時減少），所以顧客不會因為使用折扣而掉級；他只是升級得慢一點，而他已經拿到了升級的實質回報（折扣本身）。**用一個顧客看得見、當場生效的折扣，換一個累積速率上的差額，是這個等級制度本來就在做的交易。** 真正需要處理的是溝通，不是規則：交付說明必須寫「點數以**實付商品金額**計算」。

**退貨扣回不受影響。** `points.Service.HandleOrderReturned` 的扣回量取自該訂單**原本那一列 award 的 `points_delta`**，不重算基準。折扣訂單退貨時扣回的就是它當初核發的那個數字，恆等成立，不需要任何改動。

### D7. 事件欄位**更名**為 `AwardBasisAmount`，而不是新增 `DiscountAmount`

`events.OrderPaymentSucceeded` 今天帶 `SubtotalAmount`，而 `points.Service.HandleOrderPaid` 就是拿它除以 100。兩種改法：

- **新增** `DiscountAmount` 並保留 `SubtotalAmount`，由 `points` 自己相減：任何忘了設定新欄位的建構點會拿到 Go 零值，於是**所有折扣訂單都以原價核發點數**——多發、靜默、沒有編譯錯誤、沒有執行期錯誤。
- **更名** `SubtotalAmount` → `AwardBasisAmount`：每一個建構點與讀取點在**編譯期**被強制重新檢視。

選更名。這正是 `order-extras` design D8 做過的同一個判斷（它把 `TotalAmount` 更名為 `SubtotalAmount`，理由一字不差：「忘了設定新欄位會靜默地拿到零值」），而那一節的結尾寫著「沒有必要再種一顆同樣的雷」。這是一個 Go 內部型別、不是公開契約，更名是免費的。

**名字刻意描述角色而不是算術。** `AwardBasisAmount` 說的是「這個數字是點數核發的基準」，於是它的定義只有一個地方（`payment.Service.HandleWebhook` 的那一行 `ord.SubtotalAmount - ord.DiscountAmount`），而 `points.Service` 永遠不重新推導它。若命名為 `DiscountedSubtotalAmount`，日後任何一個「除了折扣還要扣掉 X」的變更都會讓名字說謊；`AwardBasisAmount` 不會。

**代價，誠實記錄：** 計算基準的那一行落在 `payment` 而不是 `points`，也就是說發佈者知道了一點訂閱者的政策。這是刻意的取捨——它換到的是「基準只有一個定義、且該定義緊貼著它讀取的那一列訂單」。替代方案（事件只帶原始事實、由 `points` 自行相減）把政策放回正確的套件，但代價正是上面第一項的靜默零值。**在「政策位置優雅」與「錯了會不會噴錯」之間，本專案一貫選後者。**

### D8. 快照策略：解析在交易之外、寫入在訂單建立時、之後永不重算

**解析時機。** `resolveTierDiscount(ctx, shopID, memberID)` 與既有的 `resolveShippingMethod` 同一階段——**在 `s.Client.Tx(ctx)` 之前**。它讀 `members`（`WHERE id = ? AND shop_id = ?`，顯式帶 shop_id，因為結帳路徑可能不在租戶上下文）並 join 出 `member_tier`，回傳 `(tierName *string, percent *int16)`。

**為何在交易之外。** 與 `order-extras` D4 對運費的判斷逐字相同：商家在這個窗口內改動等級設定是**良性競態**——顧客拿到的是他送出請求當下那個折扣率，這比讓一筆結帳因為後台改了一次設定而失敗更合理。而且它把一次額外查詢留在交易之外，不延長交易持有時間、不引入新的鎖等待。

**折抵金額的計算落在哪裡。** `subtotal` 是在扣庫存迴圈裡累加出來的，所以乘法只能在迴圈**之後**、`tx.Order.Create()` 之前。改動邊界（比照 `order-extras` D7 的同一張表，這樣兩次變更的證據義務可以直接對照）：

| 位置 | 改動 |
| --- | --- |
| 交易前（與既有可購買性／運送方式驗證同階段） | **新增** 解析會員等級與其 `discount_percent` |
| 交易內：cart CAS `Active→Converted` | **不動** |
| 交易內：逐行條件式扣庫存 ＋ 受影響列數檢查 | **不動一個字元** |
| 交易內：迴圈之後、建立 `orders` 之前 | **新增** 一行整數乘除 |
| 交易內：建立 `orders` | 多寫三個欄位、`SetTotalAmount` 的算式多一個減項 |
| 交易內：建立 `order_items` | **不動** |
| 交易內：冪等預約定案 | **不動** |

**為何讀 `members.level_id` 而不是當場以 `points` 重新評估等級。** `level_id` 是由 `points.Service` 在每一次點數異動時於同一個交易內維護的快取（`member-tiers-and-points` design D6/D7）——它**就是**「這位顧客現在是哪一級」的權威答案。在結帳路徑上重跑一次 `recomputeLevel` 只會多一次查詢，並且製造第二個對「顧客是哪一級」的判定，而兩個判定遲早會不一致。

**快照之後永不重算，這是本節的全部重點。** 訂單建立後：商家把金卡改成 15%、把金卡刪掉、顧客退貨掉回銀卡、顧客手動被扣點——**既有訂單的 `discount_amount`／`discount_percent`／`member_tier_name`／`total_amount` 一個字都不變**。系統中沒有任何路徑會重算它們（`orders` 的寫入路徑只有 `Checkout`、`CancelOrder`、`UpdatePaymentStatus`、`UpdateFulfillmentStatus`，後三者只碰狀態欄位）。這一點以整合測試逐格覆蓋，並在 E2E 以「改動等級設定後重讀訂單」的方式再證一次。

### D9. 兩條路徑：訪客**會**拿到等級折扣，而這是本變更唯一的實質新揭露

**先查證行為，再決定。** `guest.Service.resolveIdentity` 以 `(shop_id, email)` 找出**或建立**身分，然後把該 `member_id` 交給既有的 `order.Service.Checkout`。因此把等級解析放進 `Checkout` 的直接後果是：

| 訪客結帳的 email 對應到 | `members.level_id` | 折扣 |
| --- | --- | --- |
| 全新 email（本次才建立無密碼身分） | NULL（0 點，從未核發） | **無** |
| 回頭客（同 email 曾以訪客身分下單並付款） | 已由 `points.Service` 維護 | **有** |
| 已註冊會員（有密碼）的 email | 同上 | **有** |

**決定：照上表，訪客路徑與會員路徑一視同仁。**

**這不是順手為之，是這個功能能不能成立的前提。** ONDO 溫度花室**全站沒有登入 UI**（`guest-checkout` design 的 Context 原話），它的每一筆訂單都是訪客結帳。若折扣只在已認證的會員結帳上生效，那麼對第一個外部商家而言本變更**等於什麼都沒做**——商家仍然設了旋鈕而顧客仍然付原價，只是這次連平台自己都知道。「只給會員」聽起來比較安全，但它安全的方式是讓功能不存在。

**隨之而來的揭露，逐項寫清楚：**

1. **知道某個 email 的人，可以用它取得該顧客的折扣。** 半徑：他拿到的是折扣本身，而訂單寄到**他自己填的地址**、由**他自己付款**、點數記到**受害者**頭上。他得不到受害者的訂單歷史、點數餘額、個資或任何 token（`guest-checkout` D3/D4 的既有邊界完全未變）。實質上這是「一張優惠券被別人用了」，而優惠幅度是商家自己設定的那個數字。`guest-checkout` design D6 已經就同一條路徑的更嚴重版本（替受害者製造訂單紀錄、付錢送他點數）做過同型的判斷並接受。

2. **回應會揭露「這個 email 在本商家屬於哪一級」。** 這一條比第 1 條更需要正面處理，因為它與 `guest-checkout` design D6 的原句有張力——那裡的決定是「照常建單，**回應與其他情形完全同構**」，理由是不要把註冊表單的存在性洩漏複製到一個未認證端點上。本變更讓回應**不再同構**：`discount_amount`／`member_tier_name` 會依 email 而異。

   **這個揭露無法用設計繞開，只能被承認。** 顧客必須看得到自己付了多少、折了多少——那正是本變更的全部目的。任何「套用折扣但不告訴請求者」的方案都是自相矛盾的（金額本身就會說出來）。所以問題不是「要不要揭露」，而是「揭露的成本是否可接受」：

   - **探測不是免費的。** 要看到答案必須**真的建立一筆訂單**：扣庫存、產生一列商家看得到的訂單、並受既有的兩條限流規則約束（`shop+IP` 20 次／10 分鐘、`shop+IP+email` 5 次／10 分鐘）。這與「送一個請求就得到一個 yes/no」的存在性預言機不是同一個量級。
   - **洩漏面比既有的更窄還是更寬？誠實回答：更寬。** 先前訪客結帳只洩漏「這個 email 沒有被商家停用」（`checkout_unavailable` 的必然語意），現在多洩漏「它屬於哪一個等級」。這是一個**真實的擴張**，不是措辭問題，所以 `guest-checkout` 的 spec 必須跟著改，而不是靠解釋帶過。
   - **正確的修法已經有名字。** `guest-checkout` design D6 已經把 `email-verification` 列為關閉「未經驗證的 email 關聯」的那個 change。本變更**不設計它**，但為它多添一條驅動理由，並在 spec 與交付說明中明寫此關聯未經驗證。

3. **已評估並否決：只有「有密碼的已註冊會員」才拿得到折扣。** 這看似能同時保住功能與安全，實際上兩邊都保不住：ONDO 的顧客永遠是無密碼身分，所以功能仍然對第一個商家不存在；而且它會製造一條說不出口的規則——「你累積了 100 點升上金卡，但因為你沒設過密碼所以折扣不算數」。等級是**已經核發給顧客的東西**，用一個他從未被要求做的動作去限制它的兌現，是把安全成本轉嫁給無辜的常見情境。

4. **已評估並否決：折扣只在回應中呈現、不真的扣款（「顯示但不生效」）。** 荒謬，但值得記下否決它的理由：那正是本變更要修的 bug 的鏡像版本。

### D10. 結帳前的顯示：會員已經有了，訪客刻意沒有

**會員：不需要任何新東西。** `GET /api/v1/shop/points` 的回應已經包含 `level`（完整的 `MemberTier`，含 `discount_percent`）。一個做會員體驗的前端在結帳前呼叫它一次，就能以**與伺服器完全相同的整數規則**（D4）預估折抵金額。所以本變更**不新增端點、不新增欄位**，只在 `docs/storefront-integration.md` 寫明那條公式必須逐字照抄——這比新增一個「預估」端點好：預估端點會產生第二份折扣算術，而兩份算術遲早會分歧，分歧的那一刻顧客看到的數字與被請款的數字就不一樣了。

**訪客：沒有結帳前預估，而且刻意不做。**

要為訪客做預估，端點必須接受一個 email 並回答「這個 email 折多少」——那正是 D9 第 2 點費力避開的那個**廉價**預言機：一個請求、一個答案、不建立訂單、不扣庫存、不留下商家看得見的痕跡。用一次往返把「必須真的下一單才看得到」降級成「打一個 API 就知道」，是把本變更唯一的新揭露放大好幾個數量級，換到的只是一個畫面上的預覽。

**那顧客會不會「在付款頁才發現金額變了」？** 會，但**方向是向下的**。這一點值得說清楚，因為它與 `order-extras` 修掉的那個 bug 恰好相反：那次的問題是顧客看到 3800、實際被請款 3600 之外的**更多**（運費在前端算、請款用後端的 `total_amount`）——顧客付得比看到的多，那是不可接受的。本變更的落差是顧客付得比看到的**少**。一個向下的意外不需要用一個新的攻擊面去消除它。

交付說明給 ONDO 的建議因此很簡單：結帳畫面照舊以「自算小計 ＋ `flat_rate`」預估，**不承諾折扣**；訂單成立後的每一個畫面（確認頁、查詢、通知）**一律讀回應的 `total_amount`／`discount_amount`**。若 ONDO 日後做了會員登入，會員路徑的預估就自動可用。

### D11. 冪等鍵：回放語意不但仍然正確，而且正好是快照策略要的那個答案

`checkout-idempotency` 的三個保證逐項檢查：

- **指紋比對**：指紋 ＝ `SHA256(呼叫者範圍 || "\n" || 原始 body)`。本變更**不新增任何請求鍵**，所以同一個客戶端送出同樣的位元組，前後產生的指紋逐位元組相同。無影響。
- **同 key 重放**：回放路徑（`GuestHandler.replayCheckout` / `OrderHandler.checkout` 的 replay 分支）是**當下從資料庫讀出那一筆訂單**再序列化，不是重跑結帳。因此回放**不會**重新解析等級、不會重新計算折扣——它回傳的就是快照。這正是 D8 要的行為，而且是免費得到的。
- **同 key 不同 body 偵測**：不變。

有一個推論值得寫進 spec，因為它是「回放」與「新訂單」的界線：**顧客的等級在第一次請求與回放之間變了，回放仍然回傳原本的折扣**（同一把 key ＝ 同一筆訂單 ＝ 同一份快照）；而同一把 key 在 TTL（預設 24h）過期之後再送，那是一次**全新的結帳**，會依**當下**的等級重新計算折扣。兩者都正確，而且是同一條規則的兩面：折扣屬於訂單，不屬於 key。

**回應 body 變了（多三個鍵），對冪等有影響嗎？沒有。** 儲存的從來只是「這把 key 對應哪一筆訂單」，不是一份回應快照——`checkout-idempotency` design D5 明文選擇了這一點，理由是「回應快照會隨時間變成謊言」。本變更是那個選擇的又一次紅利。

### D12. 純 additive，不動用已關閉的 D13 破例窗口

逐項檢查（比照 `checkout-idempotency` D12 的做法，這一節的存在是為了讓這件事被**檢查過**而不是被假設）：

- **無新增／移除路由** → 契約檢查 ①（路由覆蓋）不受影響。
- **無新增／移除請求鍵、請求參數或標頭** → 表面鎖的參數行完全不動；既有請求逐位元組照常成功。
- **回應新增三個鍵**（`Order.discount_amount`／`discount_percent`／`member_tier_name`）→ 表面鎖**只增三行，零刪除**；契約檢查 ②（金額 `$ref`）會抓到 `discount_amount` 這個新的 `_amount` 結尾欄位並要求它 `$ref` `MinorUnitAmount`——那正是它的用途，照做。
- **無鍵被移除或改名，無型別改變。**
- **`total_amount` 的語意未改**——它仍然是「應付總額，也是向金流請款的金額」。這是本變更與 `order-extras` D6 的關鍵差異：那次改的是一個既有欄位的**意思**（品項小計 → 應付總額），所以是破壞；這次改的是**它與其他欄位之間的算式**，而它自己代表什麼一個字都沒動。
- **唯一可觀察的新行為**：帶折扣的訂單其 `total_amount` 會**小於** `subtotal_amount + shipping_fee`。這只可能出現在本變更上線**之後**建立、且顧客持有非零折扣等級的訂單上；既有的每一列訂單其值與關係都完全不變。
- **對「自己重算合計」的消費端的影響**：契約與接入說明從 `order-extras` 起就明寫「**不要自己算合計**，用 `total_amount`」，而且那句話當時就是為了同一類問題寫的。一個照做的消費端不受任何影響；一個沒照做的消費端在 `order-extras` 上線時就已經算錯運費了。

**因此本變更不需要破例窗口。** 已重新確認 `bouquet/public/` 仍為零 API 呼叫（`fetch` / `axios` / `XMLHttpRequest` / `api/v1` 全樹零命中），但**本變更不依賴這個事實**——它在有外部消費端的世界裡同樣成立。

### D13. 後台：把說謊的那句話改掉，並在訂單詳情呈現折抵；tripwire #4 必須先量再寫

**兩處，都很小，但都是本變更的一部分——因為「商家以為在給折扣」正是本變更要修的問題，而後台就是他形成那個以為的地方。**

1. `admin/views/member-tiers.js` 的檔頭與畫面說明目前寫著「discount_percent 本階段只儲存、不參與任何計價……畫面必須說出這件事，否則商家會以為設了就會打折」。這句話現在**倒過來成立**：必須說出「設定後會在結帳時自動套用於商品金額（運費不折）」。**不改它，本變更就修好了功能卻留下一個告訴商家功能不存在的畫面。**
2. `admin/views/orders.js` 的金額組合由三件組變四件組。`admin-commerce-operations` design D10 的原則不變（組合式呈現，不是並列的數字），加上一條新的：**折抵列只在 `discount_amount > 0` 時出現**。理由是「− NT$0」出現在每一筆訂單上會教會商家忽略那一列，而它恰恰是最需要被看見的一列；沒有折扣時 `小計 ＋ 運費 ＝ 應付總額` 依然逐字成立，商家該核對的算式沒有變。有折扣時該列顯示折抵金額與其來源（`金卡會員 10%`）。

**`admin/lib/money.js` 的紀律原樣適用**：`discount_amount` 是金額，走 `formatMoney`；`discount_percent` 是百分比，走 `formatPercent`；**兩者絕不互換**（design D10 的那條規則存在的理由——點數與金額之間有真實換算——在這裡以另一種形式重現：`10` 這個數字同時可以是 10% 與 NT$10）。

**tripwire #4，必須先量。** `admin-commerce-catalog` design D1 的第四條推翻條件是「admin 總行數 > ~3000」，而 `admin-commerce-operations` 實測 **2,940 行**（JS 2,361 ＋ HTML 395 ＋ CSS 184）並明寫「**下一個視圖就會越過**——下一個 admin change 開工前先重評 D1，不要先寫程式」。

本變更**不新增任何視圖**：它改的是兩個既有視圖裡的文案與一列金額，預估增量 20 行以內。**處置：實作前後各量一次，把數字寫進 tasks 的驗收與交付回報。** 若實測跨過 ~3000，則依 tripwire 的要求**停下來先重新評估 D1**，而不是繼續寫。（記錄立場：本變更不認為「為了不跨線而把程式寫得更短」是誠實的做法——tripwire 量的是累積複雜度，不是單次 diff。）

### D14. seed 不改；e2e 是唯一能證明「折扣真的生效」的地方

**`internal/seed` 一行不改。** demo 的三級等級（一般會員 0／銀卡 30 ＋5%／金卡 100 ＋10%）本來就帶著那兩個百分比，註解理由是「so the back office has something to display」。**本變更讓那兩個數字第一次真的生效**，這正是 demo 資料應該演示的東西。

需要驗證但不需要改動的耦合：

- `seed` 的整合測試守著「一筆單品訂單必須跨得過 30 點門檻」。第一次購買的顧客**沒有等級**（`level_id` 為 NULL，從未核發過點數），所以那筆訂單的核發基準仍是完整的 `subtotal_amount`，門檻判定完全不受影響。
- 升上銀卡（5%）之後每束花核發 36 點（`floor(3800 × 0.95 / 100)`）而不是 38 點，金卡（10%）是 34 點——階梯仍然可攀，只是慢一點。這是 D6 的預期效果，不是回歸。

**E2E 是唯一能證明整條路徑真的接上的地方**（沿用 `commerce-demo-seed` design 對 `wire.go` 零測試覆蓋的實測結論）。兩處，各有不可替代的理由：

- `e2e/commerce-path.mjs`（純 API）：在會員因為前面幾節的購買而**真的升上銀卡**之後下第二筆單，斷言折扣率、折抵金額、四件組恆等式、**改動等級設定後訂單不變**（快照證明），以及付款後點數以**折後**基準核發（`floor(net/100) ≠ floor(subtotal/100)`，用與既有「運費不計點」斷言完全相同的「這個檢查有意義」手法把兩個答案分開）。該訂單付款後出貨並退貨，讓該區段對點數餘額**淨零**、與後續區段互不干擾；代價是每次執行永久消耗 1 個庫存單位（退貨不歸還庫存），`STOCK_PER_RUN` 由 3 調為 4，註解同步更新。
- `e2e/happy-path.mjs`（瀏覽器）：營運後台區段的那位訪客顧客在付款後拿到 34 點，**恰好越過銀卡的 30 點門檻**。因此用**同一個 email** 再下一筆訂單就會自然帶折扣——不需要任何人工佈置。這一筆用來斷言後台訂單詳情**真的畫出**折抵列與其來源等級，以及第一筆（無折扣）**沒有**畫出那一列。它同時是 D9「訪客回頭客會拿到折扣」在真實伺服器上的證據。

## Risks / Trade-offs

- **[訪客結帳的回應揭露了「這個 email 屬於哪一級」]** → D9 第 2 點完整記錄。無法以設計繞開（金額本身就會說出來）；成本由「必須真的建立一筆訂單」與既有兩條限流規則界定；正確修法是 `email-verification`（`guest-checkout` design D6 已命名），本變更為它多添一條驅動理由並在 spec 與交付說明明寫此關聯未經驗證。**不宣稱這個揭露為零。**

- **[知道某 email 的人可以用它取得該顧客的折扣]** → D9 第 1 點。半徑限於折扣本身（他自付、自收、點數記到對方頭上），不觸及訂單歷史、點數餘額、個資或任何 token。與 `guest-checkout` design D6 已接受的同一條路徑上更嚴重的行為同級。

- **[捨入方向被日後「順手改成四捨五入」]** → D4 的第 2 點正是針對這個：無條件捨去是整數除法的原生行為，沒有可以被刪掉的 `+ 50`。防線是 spec 的明文規則、以不整除金額為輸入的邊界測試，以及註解裡的一句「這是全系統唯一一種捨入方向」。**不宣稱測試涵蓋了每一種百分比**，只涵蓋整除、不整除、0%、100%、無等級五種。

- **[商家把 `discount_percent` 從 0 改成 90，下一秒起所有訂單九折而沒有任何確認]** → 這是既有的產品行為（`min_points` 同樣即時生效），本變更不引入審核流程。緩解是後台文案明說「儲存後立即對新訂單生效」，而**既有訂單完全不受影響**（D8）——也就是說誤操作的半徑是「改回來之前的那些訂單」，不是全部歷史。

- **[點數變少被顧客誤讀為「升級反而吃虧」]** → D6 已論證這是等級制度本來就在做的交易（折扣換累積速率），且不會造成掉級。真正的處置是溝通：交付說明必須寫「點數以**實付商品金額**計算」，與既有「運費不計點」那句並列。**這個誤解無法靠設計消除。**

- **[新增一條 DB CHECK 取代既有的組成 CHECK，migration 在非空資料庫上失敗]** → 既有列的 `discount_amount` 由 `DEFAULT 0` 填入，新恆等式對每一列既有訂單本來就成立，**不需要回填**。驗收條件寫成一句可執行的 SQL（D5 末段）。這比 `order-extras` D10 的處境簡單，但仍然要親手檢查 atlas 產生的語句順序（ADD COLUMN 必須在 ADD CONSTRAINT 之前，且必須確認 diff 沒有順手提議 drop 掉手寫的 `UNIQUE NULLS NOT DISTINCT` 約束——姊妹 change 踩過兩次）。

- **[`AwardBasisAmount` 更名漏改某個建構點]** → 編譯期就會停下來，這正是選更名的理由（D7）。代價是所有既有測試的建構點都要改一次，接受。

- **[trade-off：`Checkout` 在交易前多一次 `members` 查詢]** → 每筆結帳一次主鍵查詢（附帶一次 `member_tiers` 的 join），發生在交易之外，不延長交易持有時間。不持有等級的顧客同樣付這一次查詢——**刻意不做「先看 `level_id` 再決定要不要 join」的最佳化**，因為那會讓程式有兩條路徑而只有一條被測到。

- **[trade-off：`orders` 多三個欄位，讀取端要知道哪個是哪個]** → 由四條 CHECK 在資料庫層固定關係（D5），由 spec 明文定義四者，由後台的組合式呈現讓商家看得見。這與 `order-extras` D7 為三件組做的處置完全同型。

- **[折扣不含運費，想給「VIP 免運」的商家沒有開關]** → D2 理由 3 已論證那是一個獨立的布林欄位而不是一個百分比。加 `member_tiers.free_shipping` 是純 additive 的後續變更，且正因為折扣不碰運費，它日後可以獨立地加。

- **[e2e 每次執行多消耗 1 個庫存單位]** → `STOCK_PER_RUN` 3 → 4，`BQ-ANNI-M` 的 36 庫存由「12 次」降為「9 次」重新 seed 的間隔。註解同步更新為實際數字，不留下一句過期的「well over a dozen」。

## Migration Plan

1. ent schema：`Order` 新增 `discount_amount`（`Int64`，`Default(0)`）、`discount_percent`（`Int16`，`Optional().Nillable()`）、`member_tier_name`（`String`，`MaxLen(100)`，`Optional().Nillable()`）；`Annotations` 的 `Checks` 新增四條、取代 `orders_total_amount_composition_check`。`go generate ./internal/ent`。
2. `make migrate-gen name=add_tier_discount` 產生 up/down。**手動檢查產生的 SQL**：ADD COLUMN 必須在 ADD CONSTRAINT 之前；確認 diff 沒有順手提議 drop 掉手寫的 `UNIQUE NULLS NOT DISTINCT`／`UNIQUE(shop_id, email|phone)` 約束（兩個姊妹 change 都踩過，處置是把那些語句從 up 與 down 兩邊一併刪掉）。
3. 重算 `atlas.sum`（以 `ariga.io/atlas/sql/migrate` 的 `WriteSumFile` 對 `migrations/` 重新雜湊；atlas CLI 未安裝於本環境）。
4. `make migrate` 套用。**驗收**：新欄位存在；`SELECT count(*) FROM orders WHERE total_amount <> subtotal_amount - discount_amount + shipping_fee` ＝ 0；既有列的 `total_amount`／`subtotal_amount`／`shipping_fee` 一個值都沒有改變。
5. **無新增設定值。** 沒有 TTL、沒有門檻、沒有開關——折扣率完全由商家在 `member_tiers` 上決定。
6. `seed` 不改（D14）。RBAC 不改（設定折扣走既有的 `member_tier.edit`，套用折扣不是一個需要權限的動作）。

**Rollback**：`make migrate-down` 移除三個欄位與四條 CHECK，並把組成 CHECK 還原為 `total_amount = subtotal_amount + shipping_fee`。**唯一不可逆的部分明寫**：若回滾時已存在帶折扣的訂單（`discount_amount > 0`），還原後的組成 CHECK **會被那些列違反**，`ALTER TABLE` 當場失敗。這是刻意的——安靜地放行會留下一批 `total_amount` 與其組成不符的訂單。回滾前必須先決定那些訂單怎麼辦（把 `subtotal_amount` 改寫成折後值以保住實付金額，或匯出後刪除）；down migration 的檔頭必須寫下這句話。

**部署**：無正式環境，程式部署即生效。與 ONDO 的接入沒有時序耦合——平台先上線（沒有等級的顧客行為逐位元組不變），ONDO 再更新它的顯示。

## Open Questions

- **等級免運（`member_tiers.free_shipping`）要不要做？** D2 理由 3 已論證它是一個布林值而不是折扣率的延伸，且本變更讓它日後可以獨立地加。真正該決定它的時機是有商家提出來的時候——現在做等於猜一個沒有人要求過的形狀。
- **退貨／退款時折扣怎麼拆？** 本變更把 `discount_amount` 記在訂單上，讓這個問題日後**有得算**。答案本身屬於尚不存在的 returns/refunds 能力（`payment_status` 的退款值 `2`／`3` 至今沒有任何程式路徑會寫入），與部分退款一起決定。`order-extras` 對 `shipping_fee` 留下的是同一句話。
- **以點數折抵金額（redemption）。** `member-tiers-and-points` 的這條 Non-Goal **仍然成立**（D1 前提一）。它一旦開工，會同時需要處理 `min_points` 的語意（該 design D2 已記載：加入兌換之後「當前餘額」會讓兌換點數導致降級）與「兌換折抵能否與等級折扣並用」——那是本變更刻意不預先猜測的疊加規則。
- **後台要不要能看到「這一級目前折出了多少錢」的彙總？** 這是商家評估回饋成本時真正會問的問題，但它是一份報表（需要期間、需要分組、需要決定含不含已取消訂單），不是一個欄位。`orders.discount_percent` ＋ `member_tier_name` 已經讓它日後做得成。
