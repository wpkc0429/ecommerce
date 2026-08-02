## Context

使用者的原話是「**報表可以評估看看**」。所以這份文件以評估開場，而不是以方案開場。

### ① 現在缺什麼——一句話量得出來

平台今天有 **117 條路由**，其中**零條**回傳彙總值。每一條讀取端點回的都是列（`orders`、`order_items`、`members`、`point_transactions`…），連 `total` 都是分頁的筆數而不是金額——契約檢查 ② 的 `moneyExemptions` 裡那六行「pagination count」正是這件事的化石。

於是商家要回答「這個月做了多少生意」，只有一條路：把 `GET /admin/shops/{id}/orders` 翻完，自己加。分頁上限 100，一個月 300 筆就是三頁，而且他得自己決定要不要把已取消的那幾筆扣掉。

### ② 為什麼過去兩個 change 刻意不做——兩次都留下同一份帳

**`member-directory` design D5**（回應欄位的取捨）逐字寫著：

> order counts / lifetime spend: OMITTED. 口徑未定——含不含已取消／未付款／運費／折抵，而一個看起來權威的錯數字比沒有數字更糟。**那是報表不是欄位。**

它甚至在 Open Questions 裡指名這件事與 `tier-discount-application` 留下的「這一級目前折出了多少錢」是同一份東西。

**`admin-commerce-operations` design D3/D4**（訂單列表的日期篩選）逐字寫著：

> 日期區間篩的是 `requested_delivery_date`（Postgres `date`，無時區歧義）而不是 `created_at`——`shops` 沒有時區欄位，「某一天的訂單」在伺服器上無法無歧義定義。

這兩條的性質不同，而分清楚它們是本 change 能否成立的關鍵：

- D3/D4 是**硬阻塞**：沒有時區欄位，這份報表連第一個數字都算不出來。它是一個資料模型缺口，可以用一支 migration 補上。
- member-directory D5 是**本 change 的實質內容**：它不是「還沒做」，是「還沒決定」。補一個欄位解決不了它——每一個數字都必須先回答「它算了什麼、沒算什麼」，而那個答案必須放在螢幕上，不是只放在這份文件裡。

### ③ 評估結論

**做得了，而且成本不在查詢。** 一支 migration（一個 nullable 欄位、零回填）解掉硬阻塞；兩條 SQL 聚合查詢產出全部數字；`internal/report` 是一個純讀取套件，不新增事件、不新增訂閱者、不碰任何寫入路徑。工程風險低到接近零。

**真正的成本全部在口徑，而口徑的失敗方式不會噴錯。** 報表最容易失敗的方式不是做不出來，是做出一個看起來權威的錯數字——它每天被看、被信、被拿去做決策，而系統裡沒有任何東西會說它錯。這與 admin design D4（金額縮放）是同一類風險：「寫錯不會噴錯」。因此本 change 的重心是：

1. 每個口徑決定都有寫下來的理由（D3）；
2. 每個口徑決定都在 **API 回應本身**與**畫面上**明示（D3、D12）；
3. 報表附一份**對帳分割**：計入的、已取消的、未付款的、已退貨的，四者相加等於期間內建立的全部訂單（D4）——這是唯一能讓商家自己驗證數字的東西。

### ④ 解掉之後能做什麼

`shops.timezone` 一旦存在，它就是全系統「某一天」語意的唯一權威。今天只有報表消費它，但它同時解鎖了：訂單列表改以下單日篩選（破壞性，記為後續，D10）、`member-directory` 那筆「這位顧客買過多少」的舊帳（有了口徑就有了答案）、以及未來任何以日／月為單位的排程。

---

## Goals / Non-Goals

**Goals:**

- `shops` 長出 `timezone`（IANA 名稱，可為 NULL），**零回填**；未設定時回落到平台預設，且**回應與畫面都如實標示這是回落而不是選擇**。
- 商家可自行設定時區（`PUT /admin/shops/{shopID}/settings`，商家層 `shop.update`）。
- 兩條報表端點（`report.view`）：期間營業額（摘要 ＋ 依商家當地日／月分組）、同期間的商品銷售排行。
- **口徑逐項定案，寫進契約的 description、寫進 spec、寫進回應本身、寫在畫面上。**
- 多幣別期間**在結構上**無法被加總成一個數字。
- admin 新增「報表」視圖；`formatTime` 由 `views/` 的四份拷貝收進 `admin/lib/format.js`（tripwire #2 的舊債）。

**Non-Goals:**

- **平均客單價、成長率、任何需要除法的衍生數字**（D11）。
- **圖表**。本 change 交付數字與表格；`admin/` 沒有圖表函式庫，而引入一個是「換框架」等級的決定。
- **匯出（CSV/Excel）**。理由與 `member-directory` 拒絕匯出鍵相同：前置是後台讀取的審計軌跡，今天沒有。
- **顧客層級的彙總**（訂單數／終身消費額）。本 change 建立了口徑，所以它從「未決定」變成「未實作」——記為後續，不夾帶。
- **修改既有訂單列表的 `delivery_from`/`delivery_to` 語意**（D10）。
- **快取**。報表是低頻的後台查詢，兩條查詢都是單表聚合；先加快取等於先建立一個會過期的第二權威。
- **`GET` 版本的 settings 端點**（D7）。

---

## Decisions

### D1: `shops.timezone` 可為 NULL，且既有列一列都不回填

**NULL ≠ `'Asia/Taipei'`。** 前者是「這家商家從來沒有設定過時區」，後者是「他選了台北」。今天這兩件事的**行為**相同（都用台北算），但它們是不同的事實，而把兩者壓成同一個值會在兩個地方造成傷害：

1. 商家無法知道自己是不是該去設定它。畫面上「時區：Asia/Taipei」與「時區：Asia/Taipei（平台預設，尚未設定）」是兩句不同的話，而只有後者會讓一個在東京的商家停下來。
2. 平台無法改預設值。回填之後，`REPORT_DEFAULT_TIMEZONE` 就成了一個對既有商家永遠不生效的設定——它會在某一天被人改動，然後發現什麼都沒發生。

代價明寫：多一個 nullable 欄位就多一個 `IS NULL` 分支，而回落邏輯是**兩處共用一份**（`internal/shoptz.Resolve`），不是每個呼叫端各判一次。

**為什麼是欄位而不是 `shops.meta` 的一個 jsonb 鍵**：與 `order-extras` design D3 把 `requested_delivery_date` 拉成獨立欄位的理由逐字相同——jsonb 字串既不可驗證也不可索引。這個值會進入 SQL 的 `AT TIME ZONE`，一個沒有型別約束的來源進到那個位置，錯誤會以「查詢整個炸掉」的形式出現。

**為什麼不存 UTC offset（`+08:00`）**：DST。台灣今天沒有日光節約時間，但這個欄位不是為台灣一家店設計的，而 offset 與時區的差別恰好在跨越切換日的那一天顯現——那天的報表會靜默錯一小時，而且只有那天。IANA 名稱是唯一能表達「這個地方的時間規則」的東西。

`varchar(64)`：最長的 IANA 名稱是 `America/Argentina/ComodRivadavia`（32）；64 有充分餘裕，且不像 `text` 那樣把「隨便什麼字串」變成合法輸入。

### D2: 時區名稱的驗證——`import _ "time/tzdata"`，因為「在你的機器上能跑」不是證據

`time.LoadLocation("Asia/Taipei")` 讀的是**系統的** tzdata（`/usr/share/zoneinfo`）。開發機一定有；容器不一定。本專案的 `api/Dockerfile` 目前確實有 `apk add --no-cache ca-certificates tzdata`——但那一行是為別的東西加的，**沒有任何測試守著它**，刪掉它不會有任何東西轉紅，而症狀是「時區驗證在生產環境永遠回 422」。

處置：`internal/shoptz` 匯入 `_ "time/tzdata"`，把 IANA 資料庫嵌進二進位檔（約 450KB）。它是 Go 標準庫的機制，只在系統 tzdata 找不到時作為後備使用，所以：

- 有系統 tzdata 的環境行為完全不變；
- 沒有的環境（scratch/distroless/被人刪掉 `apk` 那一行）仍然正確；
- 而且**任何連結 `internal/shoptz` 的二進位檔都自動得到它**——不必記得在每個 `main` 補一行。

`Dockerfile` 的 `tzdata` 保留不動（它服務其他用途，且移除它是一個與本 change 無關的變更）。

**驗證這件事真的成立**：`internal/shoptz` 的單元測試斷言 `LoadLocation` 對三個代表性時區成功，且——關鍵——斷言 `Asia/Taipei` 在 **`GOROOT` 之外**也可解析。單元測試無法真的模擬「系統沒有 tzdata」，所以另加一條：測試以 `time.LoadLocation` 解析後比對**已知的固定偏移**（`Asia/Taipei` 在 2026-08-02 是 UTC+8，`America/New_York` 在同日是 UTC−4，`Asia/Kolkata` 是 UTC+5:30——半小時偏移是嵌入資料庫真的被讀到的證據，而不是某個 fallback 湊巧回了 UTC）。若嵌入沒生效而系統也沒有 tzdata，`LoadLocation` 會回 error，測試紅。

**Postgres 有它自己的 tzdata，而 `AT TIME ZONE` 用的是它的**（D5）。這是一個真實的殘留風險：Go 認得而 PG 不認得的名字會讓查詢回 `invalid_parameter_value`，表現為 500。實測 PG16 的 `pg_timezone_names` 涵蓋完整 IANA 集合，所以今天不會發生；`internal/report` 的整合測試以三個代表性時區跑真實查詢，因此這個假設有一道會轉紅的檢查。**明寫不做的事**：不在寫入時同時向 PG 驗證一次（那會讓一個設定端點依賴資料庫的 tz 目錄，且兩邊都驗仍然無法保證讀取時 PG 沒被降級）。

### D3: 口徑——五個決定，逐項給理由

| # | 決定 | 理由 |
| --- | --- | --- |
| 1 | 只計 `status = 0`（未取消） | 取消會歸還庫存並扣回點數，系統已經把它當成沒發生過。算進營業額會讓報表與庫存互相矛盾。 |
| 2 | 只計 `payment_status = 1`（已付款） | **未付款的訂單可能永遠不會付。** 訪客結帳可以留下一筆沒付款的訂單，把它算進營業額會讓報表恆定高於銀行帳戶，而差額不會自己消失。 |
| 3 | 排除 `fulfillment_status = 3`（已退貨），並**另計一行** | 退貨是終態，且 `points` 已經把該筆訂單的點數扣回——系統已經視它為未成立。但**排除不等於抹掉**：退貨筆數與金額單獨呈現，因為「這個月退了三筆」是商家最需要看見的一件事，而把它藏起來正是報表變成粉飾工具的第一步。 |
| 4 | 時間軸是 `orders.created_at` | 候選是下單時間、付款時間（`payments.created_at`）、出貨時間。選下單時間有三個理由：①它是每一筆訂單都有的欄位，付款時間要 join `payments` 且一筆訂單可能有多列；②商家心中的「這個月的單」是下單日——月底下單、月初付款的那筆，在他的認知裡屬於上個月；③它與訂單編號的順序一致，商家可以用訂單列表逐筆核對。**代價明寫：這份報表因此不是現金流量表。** 畫面必須說出這句話。 |
| 5 | 金額不縮減成單一「營業額」 | 五個數字：品項小計 −折扣 ＝ 商品淨額 ＋運費 ＝ 請款總額。理由與 `admin-commerce-operations` design D10 對訂單詳情的要求逐字相同——`order-extras` 改過 `total_amount` 的語意、`tier-discount-application` 又在中間插入了折抵，**組合式呈現是唯一能讓商家察覺這件事的形式**。而報表比訂單詳情更需要它：一個商家問「為什麼營業額比我算的少」，答案幾乎總是折扣或運費，而一個單一數字沒有地方放這個答案。 |

**關於 `payment_status = 2/3`（已退款／部分退款）**：今天**沒有任何程式路徑寫入這兩個值**（`payment/constants.go` 明文：只有 `Paid` 會被寫入，2/3 是替未來的退款 change 保留的列舉槽）。所以本 change 的 WHERE 不替它們決定答案——把它們寫進口徑，等於替一個還不存在的退款流程先選好了「退款算不算營業額」。**這是後續項，寫進 Open Questions**：退款 change 落地時，`internal/report` 的這一行 WHERE 必須被重新評估，而不是預設沿用。

### D4: 對帳分割——四類相加等於期間內建立的全部訂單

這是本 change 最重要的一個設計決定，而它不是一個功能，是一個**可驗證性**的性質。

每一個幣別的摘要同時回報四個計數，它們是期間內建立的全部訂單的一個**分割**（互斥且窮盡，依序判定）：

```
1. cancelled_order_count   status = 1
2. unpaid_order_count      status = 0 AND payment_status <> 1
3. returned_order_count    status = 0 AND payment_status = 1 AND fulfillment_status = 3
4. order_count             status = 0 AND payment_status = 1 AND fulfillment_status <> 3   ← 計入的
```

**依序判定是必要的，不是風格**：已取消的訂單一定也是未付款的（`CancelOrder` 只在三軸皆為初始值時成立），兩個獨立的 `count(*)` 會把它算兩次，而「四個數字加起來比訂單總數多一」是一個沒有人看得出來的錯誤。

它買到的東西：商家可以打開訂單列表，篩「這個月」，看總筆數，然後把報表上的四個數字加起來——相等。**一份可以被自己驗證的報表**，與一份只能被相信的報表，是兩種不同的東西。這也是對 D3 那五個決定唯一有效的防線：口徑寫錯了，這個等式仍然成立，但商家會看到「未付款 47 筆」而停下來問。

### D5: 分桶在 SQL、範圍在 Go——`AT TIME ZONE` 與 `sql.P` ＋ `Builder.Arg`

`orders.created_at` 是 `timestamptz`（`TimeMixin`，實測 migration 確認）。所以：

```sql
(created_at AT TIME ZONE $1)::date              -- group_by=day
date_trunc('month', created_at AT TIME ZONE $1)::date  -- group_by=month
```

`timestamptz AT TIME ZONE '<zone>'` 回傳該時區的牆鐘 `timestamp`，`::date` 即當地日。這是唯一正確的作法，而 **ent 表達不了它**——沒有 `AT TIME ZONE` 的 predicate、沒有這個形狀的 `GroupBy`。

**期間界限在 Go 算，不在 SQL 算。** `from`/`to` 是商家當地的日曆日，Go 用 `time.Date(y, m, d, 0,0,0,0, loc)` 把它們變成兩個 UTC 瞬間，查詢寫成**半開區間**：

```sql
WHERE shop_id = $2 AND created_at >= $3 AND created_at < $4
```

三個理由：①`WHERE` 因此是純 timestamp 比較，不對每一列做時區轉換（`AT TIME ZONE` 只出現在 `GROUP BY` 的運算式上）；②**半開區間而不是 `BETWEEN`**——`BETWEEN` 對 `timestamptz` 會包含隔天 00:00:00.000000 那一瞬間，是一個典型的、只在午夜整點下單時才顯現的錯誤；③邊界計算集中在一個地方，而它有測試。

**參數繫結的作法，實作時改了，記在這裡。** 本節原本寫的是「時區名稱走 `sql.P` ＋ `Builder.Arg`，與 `order.addressFieldMatches` 同型」。那句話的**前提**是這裡要建的是一個 ent predicate——但實際上不是：`AT TIME ZONE` ＋ `FILTER` ＋ `GROUP BY` 是一整條聚合查詢，ent 的 query builder 從頭到尾都沒有它的形狀。所以實作走的是 **`ent.Client.QueryContext`**（`sql/execquery` feature，本專案早已啟用）配 Postgres 原生的 `$N` 佔位符。

**`sql.ExprP` 的那個坑因此根本碰不到，但它的教訓仍然適用**：`admin-commerce-operations` design D3 記錄的是「`?` 不會被改寫成 `$N`，會 500」，而其底下那條規則是「值一律綁定，永遠不拼進 SQL 文字」。原生 `$N` 是那條規則更直接的滿足方式。**唯一被字串插值的是分桶運算式本身**（`(… AT TIME ZONE $1)::date` 或 `date_trunc('month', …)`），它取自本檔案裡一個兩元素的固定集合，永遠不來自請求。

**代價，明寫：** 原生 SQL 完全繞過 `internal/tenant` 的 interceptor。後台路徑本來就不在租戶上下文（所以行為沒有改變），但這意味著**每一條 WHERE 裡的 `shop_id` 在這裡是唯一的一道防線，不是第二道**——整合測試因此有一條專門的跨商家斷言。

**順序明訂**（`commerce-demo-seed` design D8）：`ORDER BY period, currency`（分桶）；商品排行 `ORDER BY gross_amount DESC, quantity DESC, sku_code ASC, product_title ASC`——最後兩個 tiebreak 是為了讓兩筆金額與數量都相同的列有確定的順序，而不是「通常一樣」。

**兩條查詢，不是十條。** 銷售報表是**一條** SQL：`GROUP BY 當地日, currency`，用 `FILTER (WHERE …)` 一次算出四個分割的計數與金額；摘要在 Go 端把分桶相加（int64 加法，無除法，D11）。商品排行是**一條** SQL，且它**完全不需要 `AT TIME ZONE`**——期間已經是兩個瞬間了。

### D6: `report.view` 一個節點，授予 `merchant_owner` 與 `editor`

**只新增一個節點**，理由是 `member-directory` design D4 立下的規則：沒有消費者的節點不新增。沒有 `report.export`（沒有匯出端點）、沒有 `report.manage`（報表沒有可寫入的東西）。

**授予 `editor`，理由不是慷慨。** `editor` 已經持有 `order.view`，而報表的每一個數字都是他已經看得到的訂單的加總——他可以逐頁翻完自己加，只是慢。報表加的是**便利**，不是**存取**。這與 `member-directory` 授予 `editor` `member.view` 是同一個判斷，也是同一句話：**擋不住的界線比沒有界線更糟，因為它會讓人停止思考。**

`shop.update`（設定時區）**不**授予 `editor`——它已經是既有節點且既有授權不動，而時區是商家設定不是內容編輯。實際效果：editor 看得到報表、看得到「時區：Asia/Taipei（平台預設）」的標示，但沒有設定表單。UI 的可見性來源是既有的 `GET /shops/{shopID}/me`（`admin-commerce-operations` design D6），`report.view` 由權限目錄推導，seed 後自動被回報，`admin/lib/rbac.js` 一行不改。

### D7: `PUT /admin/shops/{shopID}/settings`，而不是擴充既有的 `PUT /admin/shops/{shopID}`

既有那條是**平台層**的（`RequirePlatformPermission("shop.update")`，`ShopsHandler.MountPlatform`）——商家自己碰不到它。而時區是商家自己的事實，不是平台代為決定的東西。此外它的 body 是 `cms.ShopUpdate`（`content_json`／`theme_id`／`name`／`status`），把時區塞進去會讓一個「更新主題內容」的請求順手改掉時區。

新端點掛在既有的 `/admin/shops/{shopID}` 子 router 裡，守衛是 `RequireShopPermission("shop.update")`——**商家層**判定，`merchant_owner` 已經持有。

**刻意沒有 GET**（proposal 已定，複驗成立）：沒有消費者。生效中的時區由報表回應自己帶著（`timezone: {name, source}`），而那是唯一需要知道它的地方。這帶出一個 proposal 沒有明說的推論，寫在這裡：**時區設定的表單因此住在報表視圖上**，而不是一個獨立的「設定」分頁——一個只有一個欄位的分頁，其存在的理由會比它的內容多。

**`{"timezone": null}` 必須被接受**：這是 nullable 的必然推論。設錯了要有回頭路，而「回到平台預設」與「設成平台預設值」是 D1 區分的那兩件事——只有 null 能表達前者。回應是設定後的狀態（`{"timezone": …, "effective_timezone": {…}}`），讓呼叫端不必再發一個請求就知道結果。

### D8: 商品排行的分組鍵是快照，不是 id

`order_items.product_id` / `sku_id` 的 schema 註解逐字寫著：

> Informational only — no edge, no FK. May point at rows that no longer exist; **callers must never depend on them resolving.**

用它們當分組鍵會直接違反那句話。所以分組鍵是 **`(product_title, sku_code, currency)`**——三者都是 `NOT NULL` 的結帳快照，永遠存在、永遠不會變成 NULL、永遠對得上那筆訂單上寫的字。

**代價明寫**：商家把「週年花束」改名成「週年紀念花束」之後，排行會出現兩列。**這是誠實的**——那些訂單列上真的寫著兩個名字，而合併它們需要 `product_id`，也就是需要依賴一個契約說不能依賴的東西。畫面必須說出這件事。

**排行不回傳 `product_id`/`sku_id`**，理由同上：回傳一個「可能指向不存在的列」的 id，唯一的用途是讓前端做出一個會 404 的連結。商家用 `product_title` 到商品分頁搜尋即可（既有的 `q` 已經支援）。

**金額是折扣前的 `price_amount × quantity`。** 訂單層的折扣是整筆訂單的一個百分比，把它攤回品項需要一個分攤規則，而任何分攤規則都會產生「這個商品賣了 3,059 元」這種**沒有任何單據對得上**的數字。所以排行回答的是「賣了多少貨」，不是「這個商品貢獻多少收入」，而畫面上必須這麼寫。同理它也不含運費（運費是訂單層的，不屬於任何品項）。

### D9: 不新增領域事件、不新增訂閱者——複驗 proposal，成立

`internal/report` 是純讀取：不寫任何表、不發任何事件、不失效任何快取。檢查 ⑦（`expectedSubscribers` 相等比對）**一行不動**。

`PUT /settings` 寫 `shops.timezone`——**它也不發事件**，而這需要一個理由，因為 `shops` 的其他寫入路徑會發 `SiteMappingChanged`（那是因為 `shop.status` 進了 publishable key 的快取項）。`timezone` 不進任何快取：它只在報表查詢的當下被讀一次。加一個沒有訂閱者的事件會有兩個代價——檢查 ⑦ 轉紅要有人改，以及下一個人會以為時區變更已經接好了某條鏈。

### D10: 不改既有訂單列表的 `delivery_from`/`delivery_to`——複驗成立，但有一個 proposal 沒提的推論

`admin-commerce-operations` design D4 選擇 `requested_delivery_date` 的**理由**（沒有時區欄位）在本 change 之後消失了。但改一個既有選填參數的語意是破壞性變更，而 D13 的破例窗口已於 `order-extras` 用盡並關閉。**記為後續項**（新參數 `created_from`/`created_to`，additive，不動既有兩個）。

**推論，寫在這裡因為它會咬人**：報表的 `from`/`to` 篩的是**下單日**，訂單列表的 `delivery_from`/`delivery_to` 篩的是**指定配送日**。同一個「7 月」在兩個畫面上是兩批不同的訂單，而且訂單列表那邊還會排除掉未指定配送日的訂單。**兩個畫面都必須標明自己篩的是哪一種日期**——訂單列表今天只說了後半句（`#orders-date-note`），本 change 補上前半句。

### D11: 本 change 一次除法都沒有——所以沒有捨入問題

**拒絕交付平均客單價。** 它需要除法，除法需要一個捨入方向，而全系統只有一種捨入（無條件捨去，`tier-discount-application` design D4）。但真正的理由更前面一步：`order_count` 與 `total_amount` 都在畫面上，商家要平均可以自己除——**而我們除出來的那個數字會立刻變成一個看起來權威的數字**，然後在對帳時與商家自己算的差一元。同理拒絕：成長率、佔比、日均。

**實際的性質**：本 change 產出的每一個數字都是 int64 的 `SUM` 或 `COUNT`。沒有乘除、沒有浮點、沒有捨入。這是能給出的最強正確性論證，而它是免費的——只要不加那個平均。

**溢位，明寫**：Postgres 的 `sum(bigint)` 回 `numeric`，Go 端掃進 `int64`。TWD 以元為最小單位，int64 的上限是 9.2×10^18 元——不會發生。若日後支援以分為最小單位的幣別，上限是 9.2×10^16 元，仍然不會發生。掃描失敗（理論上的 numeric 溢位）回 500 而不是靜默截斷。

### D12: 口徑必須與數字同行——它是回應的一部分，不是文件

口徑寫在 OpenAPI 的 description 裡、寫在 spec 裡、寫在 `index.html` 的靜態文字裡——**三個地方都會漂移**，因為它們都不是程式碼。

所以回應本身帶著它：`timezone: {name, source}` 說明這些日期是依哪個時區切的、以及那是商家的選擇還是平台的回落；四個分割計數（D4）說明什麼被算了、什麼沒被算。**這兩樣東西與數字在同一個 JSON 裡，所以它們不可能與數字不一致。** 畫面上的散文負責把它們翻成人話，但即使散文腐爛了，資料本身仍然完整。

代價：多幾行表面鎖，以及 `source` 是一個新的列舉（`"shop"` / `"default"`）需要被記錄。值得。

### D13: 多幣別——結構上無法被加總

`orders.currency` 存在。期間內若有多種幣別，**不得假裝加總出一個總額**。

有兩種實作方式：①回一個 `currency: null` ＋ 一個旗標，要求消費端記得檢查它；②**把 currency 做成回應的最外層維度**，讓「跨幣別總額」在結構上沒有地方可以放。

選 ②。回應是 `currencies: [{ currency, summary, buckets }, …]`，依 currency 遞增排序。一個只有 TWD 的商家看到一個元素，畫面與只支援單幣別時一模一樣；一個有兩種幣別的商家看到兩塊，各自完整、各自可對帳，而**平台從未計算過一個跨幣別的數字**。這與 `money.js` 的 `priceRange()` 在混幣別時回「多幣別」是同一個判斷，只是提升到了資料結構層。

商品排行同構（`currencies: [{ currency, items }]`），`limit` **逐幣別**套用。

### D14: admin tripwire 實測

基準（本 change 之前，實測 `wc -l`）：

| | 行數 |
| --- | --- |
| `admin/lib/*.js` ＋ `admin/app.js` | **873** |
| `admin/views/*.js` | **1,909** |
| `index.html` 457 ＋ `styles.css` 201 | 658 |
| **總計** | **3,440** |
| 最大單一視圖 `views/products.js` | **338** |

**#4′ 的歷史數列**（`member-directory` design D1 起記錄，續下去）：23%（operations）→ 0%（tier-discount）→ 1.9%（member-directory）→ 0%（partial-shipment）→ **本 change**。

**#2 的舊債，本 change 償還一半**：`formatTime` 在 `views/` 有**四份**逐字相同的拷貝（`members`／`orders`／`points`／`storefront-clients`），`partial-shipment` design D11 明文記錄且刻意留給下一個 admin change——就是本 change。它進 `admin/lib/format.js`。

**同一筆債裡的 `label()`（兩份）刻意不還。** `partial-shipment` D11 把它與 `formatTime` 寫在同一句話裡，但兩者不同型：`formatTime` 是四份**逐字相同**的純函式，而 `label(map, v)` 的兩份呼叫端各自帶著自己的列舉對照表（`orders.js` 的四張、`points.js` 的一張）——把它搬進 `lib/` 只會搬走三行，而列舉表仍然留在視圖裡。**兩份不到 tripwire #2 的「第三次」門檻**，所以規則本身也不要求現在動它。第三份出現時再說。

**#4′ 因此必須用兩種量法報告**（落地後的實測見 tasks 與最終報告）：

- **含舊債償還**：`lib/` ＋ `app.js` 的淨增包含 `format.js` 這個新檔，而 `views/` 的淨增被四次刪除拉低。這個比值會偏高，而**那個位移來自償還舊債，不是本 change 的共用層在漏水**。
- **不含舊債償還**：只算本 change 為了「報表」這件事本身而改的行（`app.js` 的註冊表一列 ＋ import 一行）。這才是 #4′ 想量的東西。

兩個數字都報，不挑對自己有利的那個。

**#5 的事前定調**（`partial-shipment` D11 立下的規矩：響了要做什麼，事先決定，免得事後找理由）：報表視圖有六塊內容（期間選擇、時區設定、口徑揭露、摘要、分桶表、商品排行）。處置順序是——①**先把靜態的東西放進 `index.html`**：口徑揭露的散文、表頭、期間選擇的控件全部是靜態 HTML，本來就不該在 JS 裡；②若 `reports.js` 仍逼近 400 行，**拆檔**（`views/report-products.js`），與 `order-shipments.js` 同型：檔案切分，不是新的抽象層，因此不觸發 #4′。**MUST NOT** 為了讓數字好看把邏輯塞進 `lib/`（那會把 #5 的壓力轉移到 #4′，兩個 tripwire 一起失效）。

**無論結論是什麼，本 change 不換框架。** D1 的四條推翻條件中，#4 已於 `member-directory` design D1 被取代（一條「總行數 > N」的 tripwire 在跨過的那一刻就永久失效），#1（跨視圖衍生狀態）本 change 不觸發——報表視圖是唯讀的，它不擁有任何其他視圖也擁有的狀態。

#### 落地後的實測（2026-08-02）

| | 之前 | 之後 | 淨增 |
| --- | --- | --- | --- |
| `admin/lib/*.js` ＋ `admin/app.js` | 873 | **897** | **+24** |
| `admin/views/*.js` | 1,909 | **2,109** | **+200** |
| `index.html` | 457 | 532 | +75 |
| `styles.css` | 201 | 202 | +1 |
| **總計** | 3,440 | **3,740** | +300 |

**#4′：未響，兩種量法都未響。**

- **含舊債償還（帳面值）**：`lib/` ＋ `app.js` 淨增 **24**，門檻是 `views/` 淨增 200 的 1/3 ＝ **66.7**。比值 **12.0%**。
- **不含舊債償還（本 change 自己的壓力）**：`lib/format.js` 的 20 行全部是償還 `formatTime` 舊債，而該次重構同時讓 `views/` **少了 16 行**（刪掉 4 × 5 行的定義、加回 4 行 import）。扣掉它：`lib/` ＋ `app.js` 淨增 **4**（只有 `app.js` 的 import 一行 ＋ 註冊表三行），`views/` 淨增 **216**。比值 **1.9%**。

**兩個數字都報，是因為它們回答的是兩個不同的問題**，而挑對自己有利的那一個正是這條 tripwire 最容易被廢掉的方式。12.0% 這個位移**來自償還一筆前人記在帳上的債，不是本 change 的共用層在漏水**——`admin/lib/` 除了新增 `format.js` 之外一行未改。#4′ 的歷史數列因此續為：23% → 0% → 1.9% → 0% → **1.9%（12.0% 帳面）**。

**#5：未響。** 最大 `views/*.js` 仍是 `products.js` 的 **338**（本 change 未動它）；新檔 `reports.js` **216**，離 400 還很遠。**事前定調的第 ① 步就夠了**：口徑揭露的散文、表頭、期間控件全部進了 `index.html`（+75 行），所以 `reports.js` 只剩取值與畫列。第 ② 步（拆成 `report-products.js`）**沒有用上**，記在這裡是因為下一個往報表加東西的人可以直接接手它。

**共用詞彙淨增一個檔案、一個函式**（`formatTime`），而它是搬進來的不是新寫的。報表視圖用既有的 `el()`／`clear()`／`rows()`／`can()`／`formatMoney()`；`formatCount()` 刻意**留在 `views/reports.js`**——它今天只有一個呼叫端，搬進 `lib/` 會讓 #4′ 量到一筆本 change 並沒有產生的壓力（`partial-shipment` D11 對 `badge`／`formatTime` 的同一個判斷）。**第二個需要「筆數不是金額」格式的視圖出現時，它才該進 `lib/money.js`**，與 `formatPoints` 並列。

### D15: 契約、鎖檔與七道檢查

- **`openapi.yaml`**：新增 3 條路徑（2 GET ＋ 1 PUT）與其 schema（`SalesReport`、`SalesReportCurrency`、`SalesSummary`、`SalesBucket`、`ProductSalesReport`、`ProductSalesCurrency`、`ProductSalesRow`、`EffectiveTimezone`、`ShopSettings`）。既有端點的請求與回應**逐位元組不變**。
- **`v1-surface.txt`**：**只增行**。3 條路由行、其參數行與 optional 行、新 schema 的屬性行。**diff 中必須沒有任何刪除行。**
- **檢查 ②（金額必須 `$ref` `MinorUnitAmount`）**：報表回應有大量 `*_amount` 欄位，全部必須 `$ref`。`order_count` / `*_order_count` / `quantity` 不是金額也不匹配 `moneyProperty`（不以 `_amount`/`_fee` 結尾、不以 `price` 開頭）——**刻意如此命名**：`member-directory` 為 `points` 做過同樣的取捨，理由是名字本身就該說出單位。**`moneyExemptions` 一行不加**（本 change 沒有名字像金額卻不是金額的欄位）。
- **檢查 ①／⑥**：新增 `Deps.Reports` 與 `Deps.ShopSettings` 兩個 handler 欄位；`contractDeps` 以 `testutil.FillNonNil` 反射填滿，所以**契約測試一行不改**，路由自動被列舉，沒寫進 `openapi.yaml` 就自動轉紅。兩者都是 struct pointer，不是介面，因此不需要 overrides 登記。
- **檢查 ③（回應以契約 schema 驗證）**：兩條報表端點各取樣一次，並把新 schema 加進 `requiredSchemas`。這是唯一能抓到「回應多了一個鍵」的檢查，而報表回應是本 change 最大的新 schema 面。
- **檢查 ⑤**：兩個新 `Deps` 欄位必須在 `wire.go` 被賦值，否則轉紅。豁免表維持空的。

  **但它看不到一件事，實作時發現的**：`report.Service.DefaultTimezone` 是一個**普通字串**，不是可為 nil 的相依，所以忘了在 `wire.go` 設定它**不會**讓檢查 ⑤ 轉紅——而 `time.LoadLocation("")` **回傳 UTC 且不回錯誤**，於是每一家未設定時區的商家的日界線會靜默偏移八小時。處置：`shoptz.Resolve` 對空的平台預設值**明確報錯**（有單元測試守著），把一個靜默的錯答案換成一個大聲的失敗。這是「檢查 ⑤ 只認得 nil」這條邊界的一個具體案例，值得記著。
- **檢查 ⑦**：**無新增訂閱者、無新增事件型別**，`expectedSubscribers` 一行不動（D9）。
- **`internal/tenant` 的結構性守衛**：`shops` 沒有 `shop_id` 欄位（它**是** shop），所以 `TestEveryShopScopedTableIsClassified` 不受影響，`tenantOwned`／`notTenantOwned` 一行不改。
- **後台路徑不在租戶上下文**：`internal/report` 的每一條查詢都顯式帶 `shop_id`，不倚賴 interceptor——與 `member.Service` 同一條紀律，且原生 SQL 本來就繞過 interceptor，所以這裡沒有第二道防線，`shop_id` 必須出現在每一個 WHERE 裡。

### D16: 測試策略——「端點回 200 不算」

口徑正確與否只能靠一種方式證明：**刻意建立涵蓋各種邊界的訂單，再斷言數字**。

`internal/report/service_integration_test.go` 建立一組訂單，每一筆都是為了一個特定的口徑決定而存在：

| 訂單 | 用途 |
| --- | --- |
| 正常已付款 | 基準；驗五個金額的組合 |
| 已取消（`status=1`） | 不計入，落在 `cancelled_order_count` |
| 未付款（`payment_status=0`） | 不計入，落在 `unpaid_order_count` |
| 已付款但已退貨（`fulfillment_status=3`） | 不計入主線，落在 `returned_*` |
| 有折扣（`discount_amount > 0`） | 驗 `net_amount = subtotal − discount`、`total = net + fee` |
| 有運費 | 同上 |
| 另一幣別 | 驗兩個 currency 區塊各自完整、無跨幣別總額 |
| 期間外（前一天、後一天） | 驗半開區間的兩端 |
| **跨當地日邊界** | **本 change 最重要的一條**（見下） |
| 另一商家的訂單 | 驗 `shop_id` 隔離 |

**跨時區邊界的三條斷言**（缺一不可，因為它們各自證明不同的事）：

1. `created_at = 2026-08-01T22:00:00Z`，商家時區 `Asia/Taipei`（UTC+8）→ 當地是 **08-02** 06:00 → **必須落在 08-02 的桶**。（證明轉換真的發生了；若程式忘了 `AT TIME ZONE`，它會落在 08-01。）
2. 同一筆訂單，商家時區 `America/New_York`（UTC−4）→ 當地是 **08-01** 18:00 → **必須落在 08-01**。（證明轉換用的是商家的時區，不是某個寫死的常數。）
3. `from=2026-08-02&to=2026-08-02` 配 `Asia/Taipei` → 第 1 筆訂單**必須被包含**。（證明範圍界限與分桶用的是同一個時區——這是最容易寫錯的地方：分桶用商家時區、範圍用 UTC，結果是一筆訂單出現在桶裡但不在範圍內，或反之。）

另外：`internal/shoptz` 的單元測試（tzdata，D2）、`internal/httpapi/reports_integration_test.go`（RBAC 403、跨商家 403、無效時區 422、`to < from` 422、期間過長 422）、`internal/seed` 的權限節點測試自動涵蓋新節點。

**E2E**（基準：`happy-path.mjs` **231** 項、`commerce-path.mjs` **135** 項，兩支共用一個失敗計數器）：

- `commerce-path.mjs` 新增一節，在既有的訂單鏈之後查報表並斷言數字。**它有一個既有 e2e 沒有的性質：期望值是算得出來的**——那些訂單全是本次執行剛建的，金額、折扣、運費都在前面幾節斷言過了，所以「報表的小計等於這幾筆的小計之和」是一個硬斷言而不是「回了 200」。另加對帳分割的斷言（取消的那筆必須落在 `cancelled_order_count`）。

  **實作時量到兩件事前沒有預見的事，記在這裡：**

  ① **跑到本節時，這家店的「計入營業額」是真的零。** 前面每一節建立的訂單最後全部被退貨、取消或停在未付款——那正是它們存在的目的。所以報表是對的，商品排行也真的是空的。但這讓「營業額的數字對不對」只能對著 0 斷言，而**任何一個壞掉的查詢也會回 0**。處置：本節自己下一筆會**留在計入狀態**的訂單，並斷言報表的**增量**逐項等於那一筆訂單自己的欄位（`order_count +1`、小計、折扣、運費、應付總額各自對得上）。這是本節最強的一組斷言，而它是被這個發現逼出來的。

  ② **那一筆訂單一開始寫成用 demo 會員下單，結果打破了 `commerce-path.mjs` 的可重複執行性**（已實測：第二輪的第 ⑥ 節「該會員以無折扣等級進場」轉紅）。理由是這個檔案之所以可重複，正是因為**每一筆訂單最後都被退或被取消，點數因此回到原點**；一筆永遠留著的已付款訂單會永久加點、把 demo 顧客推上銀卡。處置：改以**每輪唯一 email 的訪客身分**下單——點數落在一個沒有人斷言的新身分上，可重複性因此保住。**這一條是本 change 唯一一次真的弄壞了既有東西，寫在這裡是因為下一個要在 e2e 裡「留下一筆訂單」的人會踩到同一個坑。**
- `happy-path.mjs` 新增報表視圖的畫面斷言：口徑揭露文字存在、時區來源標示為「平台預設」、金額組合式呈現、**`order_count` 不含 `NT$`**（點數／筆數與金額的老問題，`admin-commerce-operations` design D10 的同型斷言）。
- **既有斷言一條都不刪。** 落地後實測：`happy-path.mjs` **231 → 261**（+30），`commerce-path.mjs` **135 → 153**（+18）。兩支全綠，且 `commerce-path.mjs` 連跑兩輪不重置仍全綠（可重複執行性已實測復原）。

---

## Risks / Trade-offs

**[報表的數字與商家自己算的不一致，而他相信自己的]** → 這是報表最常見的「故障」，而它通常不是 bug。處置是 D4 的對帳分割：四類相加等於期間內全部訂單，商家可以自己找出差在哪一類。**沒有這個分割，每一次不一致都會變成一張工單。**

**[`payment_status = 2/3` 落地後，口徑會靜默過期]** → 今天沒有路徑寫入它們，所以本 change 的 WHERE 是正確的。但退款 change 落地的那一天，`payment_status = 2` 的訂單會靜默從報表消失（它不等於 1，所以落進 `unpaid_order_count`——一個明顯錯誤的分類）。**緩解**：這一條寫進 Open Questions 與 `internal/report` 的程式碼註解，且 D4 的分割會讓症狀立刻可見（「未付款 12 筆」而商家知道那 12 筆都退過款）。無法用測試守住一個還不存在的狀態。

**[商家改了時區，歷史報表的數字跟著變]** → 這是**正確**的行為（報表是查詢不是快照），但它會嚇到人：昨天看到的「8/1 營業額 40,800」今天變成 37,400。**緩解**：畫面在時區變更後明示「變更時區會改變所有歷史報表的日界線」，設定時要確認。**不做**的替代方案：把時區釘在訂單上（那是把一個商家設定變成 N 筆訂單的快照，且第一次設定時所有既有訂單沒有值）。

**[PG 的 tzdata 與 Go 的不一致]** → D2 已述。實測不會發生，整合測試以三個時區守著，殘留風險明寫。

**[商品排行因改名而分裂]** → D8 已述，是誠實的代價。**緩解**：畫面明示「排行依訂單上記錄的商品名稱分組，改名後的訂單會另外成列」。

**[報表查詢在大量訂單下變慢]** → 兩條查詢都是單表（＋一次 join）聚合，`orders` 上有 `index(shop_id, status)` 但**沒有 `created_at` 的索引**，所以期間篩選會走 shop_id 索引後過濾。單一商家的訂單量在 Phase 2 遠不到需要處理的規模。**明寫不做**：不加索引（一個沒有量測支撐的索引是一個猜測），不加快取（見 Non-Goals）。當它變慢時，正解是 `index(shop_id, created_at)`，而 API 契約不變。

**[`AT TIME ZONE` 的參數化查詢繞過了 ent，因此也繞過了租戶 interceptor]** → D15 已述。**緩解**：每一條 WHERE 都顯式帶 `shop_id`，且整合測試有一條「另一商家的訂單不出現在本商家的報表」的斷言。這是與 `member.Service` 相同的紀律，理由也相同（後台路徑本來就不在租戶上下文）。

---

## Migration Plan

1. `ent/schema/tenancy.go`：`Shop` 新增 `timezone`（optional、nillable、`varchar(64)`）→ `go generate ./internal/ent` → `make migrate-gen name=add_shop_timezone`。
2. **逐行讀過產出的 `.up.sql`**：必須只有一行 `ALTER TABLE "shops" ADD COLUMN "timezone" character varying(64) NULL;`。**零回填、零 CHECK、零索引、零既有列變更。**

   **實測結果：atlas 產出了四行多餘的破壞性敘述**，而它們不是本節事先猜的「CHECK 誤判」，是**每一個姊妹 change 都遇過的同一件事**——ent schema 表達不了的手寫約束，atlas 一律當成「多出來的東西」要求刪掉：

   ```sql
   DROP INDEX "uq_members_shop_email";
   DROP INDEX "uq_members_shop_phone";
   ALTER TABLE "role_user" DROP CONSTRAINT "uq_role_user_user_role_shop";
   ALTER TABLE "user_permission" DROP CONSTRAINT "uq_user_permission_user_perm_shop";
   ```

   照套下去會**靜默移除租戶隔離與 RBAC 的唯一性約束**。四行已從 `.up.sql` 手動刪除、其 re-CREATE 對應行已從 `.down.sql` 刪除（`add_partial_shipment`／`add_order_notifications`／`add_checkout_idempotency` 都留有逐字相同的處置紀錄與註解）。手改 `.sql` 後必須重算 `migrations/atlas.sum`，否則下一次 `make migrate-gen` 的 replay 會失敗。

   **本節原本那句「有任何多餘的敘述就是 atlas 對某個既有 CHECK 產生了誤判」是錯的**——正確的說法是：**任何多餘的敘述都要逐條查它動到的是不是 ent 表達不了的手寫物件**，CHECK 只是其中一種。CHECK 的那條警告本身仍然成立且獨立（atlas 以名稱比對 CHECK，改運算式它完全沉默），只是本次沒有觸發它。
3. `.down.sql` 對稱 `DROP COLUMN`。
4. `make migrate`。

**回滾**：`make migrate-down` 即可，無資料損失風險（新欄位在回滾前若已被寫入，回滾會丟掉那些設定值——這是新增欄位的固有性質，且商家重設一次即可）。

**部署順序無關**：新欄位是 nullable 且舊程式碼不讀它；新程式碼在欄位不存在時無法啟動（查詢會失敗），所以標準順序是 migrate → deploy，與本專案既有的每一次相同。

**設定**：新增 `REPORT_DEFAULT_TIMEZONE`（預設 `Asia/Taipei`）。**不設定時行為與預設值相同**，且本 change 之前不存在報表，所以「與本 change 之前相同」這句話在這裡是自動成立的。啟動時以 `time.LoadLocation` 驗證這個值——**這是唯一一個會讓服務拒絕啟動的新設定**，理由是：一個打錯字的預設時區會讓每一家未設定時區的商家的報表回 500，而那是啟動時就能發現的事。

---

## Open Questions

1. **退款落地後的口徑**（D3）：`payment_status = 2`（已退款）與 `3`（部分退款）今天沒有任何寫入路徑。退款 change 落地時，`internal/report` 的 WHERE 必須被重新評估——最可能的答案是「已退款不計入營業額但另計一行，與退貨同型」，但那應該由擁有退款語意的那個 change 決定，不是本 change 預先猜測。
2. **顧客層級的彙總**（`member-directory` design D5 的舊帳）：本 change 建立了口徑，所以「這位顧客買了幾筆、花了多少」從「未決定」變成「未實作」。它是一個 additive 的欄位加在顧客詳情上，但它會讓 `member.view` 的持有者看到一個 `report.view` 才該看的數字——那個權限交叉需要一個決定。
3. **訂單列表以下單日篩選**（D10）：`created_from`/`created_to` 是 additive 的新參數，不動既有兩個。它需要一個決定：兩組日期參數同時出現時的語意（AND，最直覺）以及 UI 上如何不讓商家混淆它們。
4. **「這一級目前折出了多少錢」**（`tier-discount-application` 留下的問題）：`orders.discount_amount` ＋ `member_tier_name` 已經足以回答它，且本 change 的口徑直接適用。它是商品排行的姊妹報表（依等級分組而非依商品），刻意不夾帶。
5. **圖表**：本 change 交付表格。圖表需要一個函式庫，而引入一個到 `admin/` 是「內容就是換框架」等級的決定——若哪天要做，那是它自己的 change。
