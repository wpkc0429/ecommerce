## Context

這個系統目前**只會回答問題，從來不主動說話**。所有顧客能知道的事都必須由顧客自己來問：`GET /shop/orders`、`GET /guest/order`、`GET /guest/order/shipment`。

三個既有事實決定了本 change 的形狀，而且它們是查證出來的，不是假設：

1. **`internal/events` 的分派是同步的、在請求執行緒上的、commit 之後的。** `Dispatcher.Publish` 對每個 handler 依序呼叫，`Handler` 的簽名是 `func(ctx, Event)`——**沒有 error 回傳值**，panic 被 `recover` 起來只寫一行 log。也就是說「寄信失敗往上拋」在型別上就做不到；既有的兩個非快取訂閱者（`points.Service.Handle`、`render.Invalidator.Handle`）都是**自己 log 然後吞掉**。
2. **`order.Service` 今天沒有 Dispatcher。** `payment` 與 `shipping` 都有（發 `OrderPaymentSucceeded`／`OrderReturned`），只有訂單建立這件事沒有對應的領域事件。
3. **付款 webhook 的成功事件是重複發佈的，而且是刻意的。** `payment.Service.HandleWebhook` 的 re-assertion 分支在**每一次**「已經是 succeeded」的投遞上都再發一次 `OrderPaymentSucceeded`，註解明寫「subscribers MUST be idempotent」。綠界收不到 `1|OK` 就每 5～15 分鐘重送、當天最多 4 次。

再加上兩個由使用者定案、不重新討論的前提：**用 Gmail 寄**、**寄件人是平台的位址不是商家的**。這兩件事各自帶著一個必須被寫下來的到期日（D3、D9）。

## Goals / Non-Goals

**Goals:**

- 訂單成立與付款成功各寄一封信，**收得到、內容正確、不會重複**。
- **寄信永遠不能讓下單失敗，也不能讓下單變慢。**
- 沒有設定 Gmail 憑證時，系統行為與本 change 之前**逐位元組相同**。
- 測試與 CI **不寄出任何真實郵件**，不需要憑證、不需要外網。
- 把兩個註定要被換掉的決定（平台寄件位址、Gmail 寄送量）寫成**可觀測的推翻條件**，而不是「以後再說」。

**Non-Goals:**

- 出貨通知、取消通知（D2）。
- HTML 信、模板引擎、多語系、退信處理、寄送重試、寄送狀態的後台 UI 或 API。
- 背景工作佇列（D5）。
- 商家自有寄件網域與 SPF/DKIM/DMARC（D3）。
- 信件內的任何連結或訂單存取權杖（D4）。

## Decisions

### D1: 新套件 `internal/notify`，不被任何領域套件認識——**但方向是單向的，不是對稱的**

`order`／`payment`／`shipping` **不得** import `notify`。這才是有意義的那個方向：一個從 `order.Service.Checkout` 構得到的 `Mailer`，遲早會被接進那個函式的 error 回傳鏈，於是「信寄不出去」就變成「訂單沒有成立」。唯一的接觸面是 `internal/events` 上的事件型別與 `wire.go` 的一行 `dispatcher.Subscribe(notifier.Handle)`——與 `points` 的形狀逐字相同。

**反方向是允許的，而且本 change 用了它**（實作時發現前一版本的敘述寫得太對稱，在此修正）：`notify` **有** import `order`，只為了 `order.ShippingAddress`——那是 `orders.shipping_address` 這個 jsonb 的**唯一定義**。在 `notify` 裡複製一份結構的代價是：日後 `order` 往那個結構加欄位時，信件會**安靜地**少印一段地址（不會編譯失敗、不會有測試變紅）。而這個方向本來就是既有慣例：`payment` 與 `shipping` 都 import `order`。`notify` 不呼叫 `order` 的任何寫入方法。

替代方案（被否決）：讓 `Checkout` 直接呼叫 `Mailer`。`Checkout` 是**全系統唯一一份並發安全的扣庫存實作**，任何進入那個函式的新相依都會出現在每一支既有測試的 fixture 裡。事件層讓「寄不出信」在型別上就無法影響下單。

`notify.Service` 的相依只有 `*ent.Client`、`Sender`、`*slog.Logger`，與 `points.Service` 同構。

### D2: 只寄兩封——訂單成立、付款成功

判準是**「不寄就會出事」**，不是「寄了比較好」。四個候選逐一過篩：

**① 訂單成立 —— 寄。**
訪客結帳沒有帳號、沒有訂單列表，手上唯一的憑據是那一次 HTTP 回應。而 `checkout-idempotency` 這個 change 的全部存在理由就是**那個回應可能遺失**——它替客戶端程式解決了「訂單成立了沒有」（重送同一把 key），卻沒有替**人**解決。ONDO 全站沒有登入 UI，所以「人」就是全部的顧客。不寄的話，回應遺失 ＝ 顧客手上零證據、零查詢管道，而訂單真的存在、庫存真的被扣了。

**② 付款成功 —— 寄。**
綠界的付款是**帶外**的：顧客離開我們的頁面付錢，錢到帳只有 webhook 知道，webhook 顧客看不到。`ECPAY_CLIENT_BACK_URL` 的預設值是**空字串**（`config.Load`），也就是說今天存在一條完全合法的部署：顧客付完錢，瀏覽器停在綠界頁面，本平台一個字都沒有對他說。「錢已經離開顧客的帳戶，而賣方沒有任何表示」是不寄就會出事的教科書定義。

**③ 出貨通知 —— 不寄。** 見 D12（它有一個獨立小節，因為理由跨到下一個 change）。

**④ 取消通知 —— 不寄。** 兩條取消路徑的性質完全不同：會員自己按取消（他剛按完，最不需要被通知的就是他），以及商家在後台取消（顧客確實不知道）。後者才是真的缺口，但它今天**不值得一封自動信**，理由有三，第三個是決定性的：

- 商家取消時手上有 `shipping_address.phone`，而台灣花藝的實務是打電話（取消通常伴隨「換一個日期好嗎」的對話，不是一封通知）。
- 後台今天**沒有取消原因欄位**，所以自動信只能寫「您的訂單已被取消」，這種信會**製造**客服電話而不是減少。
- `payment.Service.HandleWebhook` 有一條已知路徑：**已取消的訂單仍可能收到付款成功回呼**（`ecpay-payment-provider` D9，實測過）。這時付款紀錄如實記為成功、訂單不推進、留 WARN log 等人處理。如果我們有取消通知信，顧客會先收到「已取消」、然後卡真的被扣款——**一封在特定情況下必然說謊的信，比沒有信更糟。**

**推翻條件（取消信）**：後台長出「取消原因」欄位、或已取消訂單的退款變成自動流程（兩者任一）之後重新評估。屆時信的內容才有東西可寫，而且不會與扣款事實矛盾。

### D3: 寄件人是**平台**的 Gmail 位址——這是一個註定要被換掉的決定

**問題是真的，不是理論上的。** 顧客在 ONDO 買花，收到一封寄件人是 `noreply@<平台的 gmail>` 的訂單信。後果有三層，一層比一層嚴重：

1. 商家無法用自己的網域建立信任（收件匣裡看不到 ONDO 這個名字，只看到平台的）。
2. 顧客**按回覆會寄到平台**，而平台不看那個信箱、也不知道這張訂單的花該怎麼換。
3. 多租戶下這是**共用的信譽池**：任一商家的信被大量標為垃圾郵件，受害的是同一個 Gmail 帳號上所有其他商家的訂單信。這一層在單租戶系統裡不存在，是這個決定在本專案特別糟的原因。

**為什麼今天仍然這樣做**：`shops` 表**沒有任何 email 欄位**（已查證：`theme_id`／`name`／`status`／`content_json`／`meta`）。也就是說「商家的寄件位址」在這個系統裡連一個存放處都沒有——它不是一行程式碼的差別。而真正的阻礙也不在程式：以商家網域寄信需要該網域的 SPF／DKIM／DMARC 設定，那是**商家上線流程**的一環（要商家去改 DNS），不是後端可以單方面交付的東西。

**推翻條件（任一成立即重開）**：

- 第二家商家上線（見 D9——寄送量與信譽都是共用的，兩家就開始互相傷害）；
- 任何一家商家要求顧客能直接回信給他；
- 平台 Gmail 帳號因任一商家的信被標記而觸發限制。

**換掉時要動哪些地方**（這份清單就是本 change 把介面切在這裡的理由）：

| 位置 | 今天 | 換掉之後 |
| --- | --- | --- |
| `shops` | 沒有寄件身分 | 新增寄件位址 ＋ 網域驗證狀態（新 migration） |
| `notify.Message` | 沒有 `From`／`Reply-To` | 長出這兩欄，由 renderer 依 shop 填入 |
| `notify.Sender` | 一個實例、一組憑證 | 依 shop 解析（每商家 SMTP 憑證，或改用支援多網域驗證的服務商 API） |
| `notify.Service` | 不讀 shop 的寄件設定 | 讀，且未驗證網域時 MUST 回退到平台位址而不是寄不出去 |
| 商家上線流程 | 不存在 | 新增 DNS 驗證步驟 |

**`Message` 今天刻意不帶 `From`。** 這與 `member-directory` D1（「不要交付一個恆為空的欄位」）是同一條規則：一個永遠等於設定值的 `From` 欄位只會讓每個讀到它的人以為寄件人已經是可變的。它應該與**它的資料來源**（`shops` 的寄件身分）在同一個 change 裡一起出現。

### D4: 信裡**不放任何連結、不放訂單存取權杖**——正面權衡後的決定

這一條的兩邊都有份量，所以逐條列出：

**放的理由**：訪客的訂單存取權杖（`aud=order:{shop_id}`，TTL 24h）是他唯一能再看到訂單的東西。回應遺失時，一封帶連結的信可以直接把他接回去；不放的話，他就算收到信也只能看信，不能查即時狀態。

**不放的理由，兩條，第二條是決定性的**：

1. **信箱被翻 ＝ 訂單被看到**，而信件是一個**存活好幾年**的成品，權杖的 TTL 只有 24 小時。把一枚 bearer 憑證寫進一個比它壽命長三個數量級的載體，等於把它的暴露窗口從「一次 HTTP 回應」擴大到「這個信箱的餘生」。`guest-checkout` 的 spec 明文寫著這枚權杖 **MUST NOT 演化成 session**——而「信箱裡永遠有一個能打開訂單的連結」正是 session 的實質定義。若改成寄信時重新簽發一枚新的，那更糟：通知路徑會變成一條**鑄造憑證**的路徑。
2. **這個系統不知道商家店面的網址。** 連結需要一個「訂單查詢頁」的 URL，而平台手上只有 `sites.domain`（可能沒有，ONDO 是自帶前端以 publishable key 識別）與 `shop_allowed_origins`（CORS 用的 origin，不是頁面路徑）。**訂單查詢頁在哪個路徑上是商家前端自己的選擇**，平台沒有任何欄位記錄它。所以連結只能用猜的，而猜錯的後果是**每一封信裡都有一個壞掉的連結**——比沒有連結糟得多。

**所以信是自足的收據**：顧客需要知道的一切都印在信裡（訂單編號、品項、金額組成、收件資訊、指定配送日）。他不需要點任何東西。

**推翻條件**：`shops`（或商家上線流程）長出一個「訂單查詢頁 URL」的設定值之後重新評估。屆時第二條理由消失，第一條仍在——而那時正確的做法是連到一個**要求顧客輸入 email／訂單編號**的頁面，而不是把權杖放進 URL。

### D5: 分派模型——同步的事件層 ＋ 有界的行程內寄送，**不是**工作佇列

**先是查證，因為問題問的就是這個：**

- `Dispatcher.Publish` 是**同步**的：`for each event { for each handler { h(ctx, e) } }`，全部在發佈者的 goroutine 上、發佈者的 `ctx` 上。
- **錯誤無法往上拋**：`Handler` 是 `func(context.Context, Event)`，沒有 error 回傳。panic 被 `recover` 吃掉，只留一行 `d.log.Error("event handler panic", ...)`。
- **既有訂閱者失敗時的行為**：`points.Service.Handle` 自己 `s.logger().Error(...)` 然後 return；`render.Invalidator` 同樣。**沒有任何機制會重試，也沒有任何東西會通知任何人。**

也就是說：「寄送失敗往上拋」在型別上就不可能發生；而「慢的 SMTP 連線拖住 HTTP 回應」**是真的會發生**，因為 handler 就跑在請求執行緒上。Gmail 的 SMTP 提交（TCP → STARTTLS → AUTH → DATA → QUIT，不重用連線）是 **0.5～1.5 秒**等級；`POST /shop/checkout` 今天是幾十毫秒等級。把它同步接上去，等於讓本系統最重要的端點的延遲從此由一台我們不擁有的郵件伺服器決定——**外加一個更糟的失效模式**：Gmail 慢的時候，每一筆結帳都慢，而顧客不知道自己在等的是一封他沒要求的信。

**決定**：`notify.Service.Handle` 在請求執行緒上**只做一件事**——把 `{shopID, orderID, kind}` 這個 12 bytes 的 job 丟進一個**有界 channel**（預設 256），非阻塞。滿了就丟棄並 log ERROR。固定數量的 worker goroutine（預設 4）從 channel 取出後，才做讀 DB、渲染、寄送、記錄結果。worker 用的是**與請求無關的 context**（`context.WithoutCancel` 之上再加 timeout）——直接用請求的 ctx 會在 handler 回傳的瞬間被取消，寄送必然失敗，而且是那種只在生產環境出現的失敗。

**這不是工作佇列，我明說它在哪裡壞**：

| 故障 | 後果 |
| --- | --- |
| 行程重啟／崩潰時 channel 裡還有 job | 那些信永遠不會寄出，`order_notifications` 也不會留下列（job 還沒被 worker 取出，預約還沒發生） |
| 送信失敗（Gmail 拒絕、逾時、網路不通） | **不重試**。留下 `status=failed` ＋ `error_reason` 的一列，以及一行 log |
| 產生速率持續 > 4 個 worker 的寄送速率 | channel 填滿後開始丟棄，log ERROR。以 1 秒／封估，4 個 worker ≈ 240 封/分鐘——遠高於 D9 那個 Gmail 帳號上限，所以**佇列容量不會是先壞的那一個**（帳號額度會先壞） |

**引入真正的工作佇列（持久化 ＋ 重試 ＋ 死信）明確超出本 change 範圍。** 它該在什麼時候做：當「寄丟一封信」的成本高到需要重試時——具體來說，就是 D3 被推翻（商家自有網域寄件）或 D9 被推翻（換掉 Gmail）的那一刻，因為兩者都代表寄信已經是產品承諾而不是加值。屆時 `order_notifications` 這張表已經是它需要的狀態機（`queued`／`sent`／`failed`），加的是一個掃描 `failed` 的 worker 與一個重試次數欄位。

**逾時是硬邊界，不是希望**：SMTP 連線用 `net.DialTimeout` 建立、並在 conn 上設 deadline（預設 20 秒，`SMTP_TIMEOUT`）。沒有這個，一條黑洞路由會讓一個 worker 卡在核心預設的 TCP 重試上（Linux 約 130 秒），四個就是整條寄送鏈停擺兩分鐘。

### D6: 重複寄送——查證結果，以及為什麼答案是 `UNIQUE(order_id, kind)` ＋ at-most-once

**兩條路徑逐一查證：**

**① 結帳冪等回放：不會重複發事件。** `httpapi.GuestHandler.replayCheckout` 與 `OrderHandler.checkout` 的回放分支都只呼叫 `GetOrderAdmin`／`GetOrder` 再序列化，**完全不進入 `order.Service.Checkout`**。所以只要事件的發佈點在 `Checkout` 內部（本 change 就是這樣做的），回放天生不發第二次事件——這是**結構保證，不是紀律**。順帶：沒帶 key 的重送會撞上購物車的 Active→Converted CAS 而拿到 409，同樣不建單、不發事件。

**② 付款 webhook：會重複發事件，而且是刻意的。** `HandleWebhook` 的 re-assertion 分支條件是 `target == StatusSucceeded && p.Status == StatusSucceeded`——**不是**「這次 CAS 贏了」。註解明寫：「events.OrderPaymentSucceeded is published on every such delivery too, not just the first — subscribers must be idempotent」。綠界收不到 `1|OK` 會重送（每 5～15 分鐘，當天最多 4 次），而且重送本來就可能發生。**所以重複投遞不是邊角案例，是預期行為。**

**因此本 change 必須自己防重，而不是假設事件唯一。** 做法與 `points` 逐字相同：一張表 ＋ 一個 `UNIQUE(order_id, kind)`，**由資料庫仲裁，不是應用層 read-then-write**。這是本 repo 反覆使用的同一個習慣（`point_transactions(order_id, kind)`、`payments(provider, provider_reference)`、`shipments(order_id)`、`checkout_idempotency_keys(shop_id, idempotency_key)`）。

**順序：先預約，再寄送（at-most-once），不是先寄再記。** 兩種失效各有代價，而選擇是被「本系統沒有任何重試」決定的：

- 先寄再記：寄完之後寫入失敗／行程崩潰 → 下一次投遞**再寄一次**。顧客收到兩封「訂單已成立」是**看得見的故障**。
- 先記再寄：寄送失敗 → 沒有任何東西會重試 → 信丟了。

「先寄再記」多出來的重複風險，換到的好處是「之後可以重試」——而**這個系統沒有重試**，所以那個好處是零。選 at-most-once，並且**把失敗記在列上**（`status=failed` ＋ `error_reason`）而不只是 log，這樣「信丟了」是一個查得到的事實而不是一行會被輪替掉的日誌。

**實作順序上有一個由資料模型決定的細節**：`recipient_email` 是 NOT NULL，所以預約之前必須先把訂單與顧客讀出來。順序因此是「讀（唯讀）→ 預約 → 渲染 → 寄 → 記結果」，而不是「預約 → 讀 → …」。這不影響仲裁性質（預約仍在寄送之前，唯一約束仍是唯一的仲裁者），而且順帶讓「顧客沒有 email」這件事**不留下任何列**——否則就要寫一列再刪掉它。

**`kind` 是 `int16` 列舉（0 訂單成立 / 1 付款成功），不是字串**，比照 `point_transactions.kind`。理由同樣是那條：它是機器判定的冪等軸，不是給人看的標籤。

### D7: 收件人解析——商家邊界必須顯式，不能靠 interceptor

`notify.Service` 從 **dispatcher** 被呼叫，也就是說它跑在**沒有租戶上下文**的地方（付款 webhook 路徑更是明確地沒有）。`members` 是租戶資料，但 interceptor 只在 tenant ctx 下才附加 `shop_id`。所以每一次查詢都必須顯式帶 `shop_id`：

```
Member.Query().Where(entmember.IDEQ(ev.MemberID), entmember.ShopIDEQ(ev.ShopID))
Order.Query().Where(entorder.IDEQ(ev.OrderID), entorder.ShopIDEQ(ev.ShopID))
```

這與 `points.Service` 的做法逐字相同，理由也一樣（`member-identity-per-shop` D4）。**沒有這一條，一個猜對 order id 的事件就能把 A 店的訂單內容寄到 B 店的顧客信箱**——顧客身分以商家為界，同一個 email 在兩家店是兩個互不相干的身分。

會員與訪客**沒有分支**：訪客身分是 `guest.Service.resolveIdentity` 以 email 建立的 `members` 列（`password_hash IS NULL`），`email` 一定有值。真正需要處理的是第三種情況——**`members.email` 是 `Optional().Nillable()`**（schema 保留給純手機註冊），所以 `nil` 必須靜靜跳過：不寄、不寫列、log 一行只帶 `shop_id`／`order_id`／`member_id`（**不帶 email，因為它是 nil；也不帶姓名電話，見 D13**）。

### D8: 信件內容——金額必須組合式呈現、絕不縮放、**不顯示點數**

**金額**：TWD 的最小貨幣單位是元，`3400` 就是 `NT$3,400`。整個 `internal/notify` **不存在 `/100` 或 `*100`**（與 `admin/` 的 D4 是同一條規則），並且不使用任何會產生 `.00` 的格式化——那個小數點會暗示相反的單位語意，而下一個人就會去補除法。

信件 MUST **組合式**列出，不能只印一個總額：

```
商品小計      NT$3,400
會員折扣（VIP 10%）  -NT$340
運費          NT$120
──────────────────
應付總額      NT$3,180
```

理由與 admin 的 D10 相同：`total_amount` 的語意被 `order-extras` 與 `tier-discount-application` 各改過一次（`subtotal − discount + shipping`，由 DB CHECK 強制），而**唯一能讓商家與顧客察覺算式的地方就是把算式印出來**。折扣那兩列在 `discount_amount = 0` 時省略（沒有折扣就沒有那一行，而不是印一個 `-NT$0`）。

**點數不進信裡。** 主要理由不是「怕搞錯單位」，而是**時序**：`points.Service` 與 `notify.Service` 是同一個事件的**兩個獨立訂閱者**，之間沒有任何順序契約；而本 change 的寄送還是非同步的（D5），所以「寄信時點數已經入帳了嗎」這個問題**沒有答案**。靠訂閱順序去讀剛剛核發的點數，是把一條看不見的耦合寫進兩個刻意互不認識的套件之間。

次要理由才是單位：核發是 `floor((subtotal − discount)/100)`，一筆 3,400 的訂單核發 34 點。把 34 丟進金額格式化會得到 `NT$34`——數字對、單位錯、看起來完全合理、沒有任何東西會噴錯。所以 `internal/notify` **只有一個金額格式化函式，沒有點數格式化函式**：不存在的函式不會被誤用。

**推翻條件**：`points` 自己發佈一個「點數已核發」事件之後，那封信可以由那個事件觸發（順序問題自然消失）。

### D9: Gmail 的寄送量上限——**使用者給的數字有一半是錯的**，這是查證後的實際值

使用者記憶中的數字是「一般 Gmail 約 500 封／日、Workspace 約 2000 封／日」。查證結果：

| | 網頁介面 | **SMTP／第三方用戶端（＝我們走的路）** |
| --- | --- | --- |
| 免費 gmail.com | 500 封／日 | **約 100 封／日** |
| Google Workspace（付費） | 2,000 封／日 | 2,000 封／日／使用者（SMTP relay 另有每封 100 收件者上限） |

**免費帳號那一格是錯的，而且錯了 5 倍。** 500 是**網頁介面**的數字；程式透過 `smtp.gmail.com` 送信走的是第三方用戶端那一格，約 **100 封／日**。Workspace 的 2,000 則是對的。另外兩件同樣重要、同樣不在使用者的前提裡的事實：

- 額度是**滾動 24 小時窗口**，不是自然日。晚上 8 點送滿，隔天晚上 8 點才恢復。
- 超過額度時 Google **暫時鎖住整個帳號 1～24 小時**——不是「這封退回」，是**接下來所有的信都寄不出去**，包含隔天早上的訂單信。這是這個限制最糟的性質：它的失效是**全有全無**，而且會跨過午夜。

**換算成本系統可觀測的門檻**：一筆走完付款的訂單寄 **2 封**（成立 ＋ 付款），未付款的訂單寄 1 封。所以

- 免費 gmail.com（100／日）：天花板約 **50 筆已付款訂單／日（全平台，不是每商家）**。
- Google Workspace（2,000／日）：天花板約 **1,000 筆已付款訂單／日（全平台）**。

**推翻條件（具體、今天就量得出來）**：

1. **單日訂單數 > 40**（免費帳號）或 **> 800**（Workspace）。留 20% 邊際，因為額度是滾動窗口，踩線的代價是整個帳號被鎖。
2. **上線商家數 ≥ 2**。這一條與訂單量無關：額度是**全平台共用的一個 Gmail 帳號**，第二家商家一上線，任何一家的促銷都會安靜地吃掉另一家的訂單信額度——而受害的商家完全看不到原因。（這一條與 D3 的第 3 層後果是同一件事的兩面：共用的不只是額度，還有寄件信譽。）

**這個數字今天就查得到，不需要新工具**：`SELECT count(*) FROM order_notifications WHERE created_at >= now() - interval '24 hours'` 就是精確的已寄送封數。這是 `order_notifications` 除了防重之外的第二個消費者，也是本 change 願意新增這張表的理由之一。

**推翻之後要換成什麼**：一家有網域驗證的交易信服務商（SES／Postmark／Resend 之類）。**換掉它不需要碰 `notify.Service` 一行**——`Sender` 介面就是為此而切的，而且大多數服務商都提供 SMTP 介面，所以最省的一條路甚至只是改 `SMTP_HOST`／`SMTP_USERNAME`／`SMTP_PASSWORD` 三個環境變數。這是**刻意的**：本 change 的程式碼裡沒有任何一個字是 Gmail 專屬的，Gmail 只活在預設值裡（`SMTP_HOST=smtp.gmail.com`、`SMTP_PORT=587`）。

**Gmail 專屬的操作細節，記下來免得下一個人踩**：Google 已移除「低安全性應用程式存取」，所以 `SMTP_PASSWORD` 必須是開啟兩步驟驗證後產生的**應用程式密碼**，不是帳號密碼。另外 Gmail 會把 `From` 改寫成通過驗證的那個帳號（除非是已驗證的別名），所以 `SMTP_FROM` 預設就等於 `SMTP_USERNAME`——設成別的位址不會生效，只會讓人以為生效了。

### D10: 未設定憑證 ＝ 什麼都不做；但**訂閱是無條件的**

沒有 `SMTP_USERNAME`／`SMTP_PASSWORD` 時，`wire.go` 接上 `notify.DiscardSender`，`notify.Service` 的 `Handle` 直接 return：**不寄信、不報錯、不寫 `order_notifications`、不起任何 worker goroutine**。這是本 repo 既有的慣例（`PAYMENT_DEFAULT_PROVIDER`、`ECPAY_*` 皆如此），也是讓 `make dev`／`make seed-demo`／e2e 完全不受影響的原因。

**但訂閱本身不可以是條件式的。** `dispatcher.Subscribe(notifier.Handle)` MUST 無條件執行，因為檢查 ⑦ 是**相等**比對——把訂閱掛在 `if cfg.SMTP.Username != ""` 底下，會讓接線檢查的結果取決於執行測試那台機器的環境變數，而一個會因環境而異的鎖不是鎖。開關住在 `Sender` 的選擇上（`DiscardSender` vs `SMTPSender`），不住在訂閱上。

同理，`Sender` **不能是 nil**：`DiscardSender{}` 是一個真的實作。理由與 `wiredDepsExemptions` 空表背後的紀律一致——「合法的 nil」與「忘了接的 nil」長得一模一樣。

### D11: `expectedSubscribers` 怎麼改，以及它逼出的那個問題

新增一行：

```go
"ksdevworks/ecommerce/api/internal/notify.(*Service).Handle-fm", // 訂單成立／付款成功通知信
```

同時把 `Orders` 加進檢查 ⑦ 的「同一個 dispatcher 實例」比對表——`order.Service` 從本 change 起是發佈者，而**發佈者持有第二個 dispatcher 的症狀與沒有訂閱完全相同**，這正是那份比對表存在的理由。

**它逼出的問題：e2e 要不要補一條斷言？答案是不要，理由是可觀測性而不是懶惰。**

`commerce-path.mjs` 只透過 API 觀察系統，而本 change **刻意沒有新增任何 API**（D2 的 Non-Goal：寄送狀態不長出端點）。在 demo 環境下 `SMTP_USERNAME` 未設定，所以連信都不會寄——e2e 唯一能斷言的東西是「寄信沒有把結帳弄壞」，而那已經被既有的每一條結帳斷言涵蓋了。

要讓 e2e 有東西可斷言，只有三條路，三條都比它們解決的問題貴：①開一個查寄送紀錄的後台端點（`member-directory` 已定調：沒有消費者的端點不新增）；②在 e2e 環境跑一台假 SMTP 伺服器（新的基礎設施元件，而它驗的是 `SMTPSender`，那已經有單元測試）；③真的寄信（違反本 change 的硬約束）。

**取而代之的覆蓋**（寫在這裡，因為檢查 ⑦ 的整個用意就是逼人回答這件事）：

- 檢查 ⑦ 本身回答「有沒有接」——刪掉那行 `Subscribe` 會轉紅。
- `internal/notify` 的整合測試以 `RecordingSender` 回答「接上之後寄了什麼」：兩封信各一次、重複投遞只寄一次、跨店不外洩、金額組成正確。
- `internal/order` 的整合測試回答「`Checkout` 有沒有發事件、回放有沒有不發」。

這條分工與專案既有的那條（CI 答「有沒有接」、e2e 答「行為對不對」）唯一的差別是：本 change 的「行為對不對」在 e2e 的可觀測範圍**之外**，所以由整合測試承接。**e2e 既有的覆蓋一條都沒有減少**，而且事件鏈改了，`commerce-path.mjs` 仍然必須跑。

### D12: 本 change 依賴的出貨粒度假設（給 `partial-shipment` 的交棒）

**這一節是寫給下一個人的，標題請保留原樣以便搜尋。**

本 change **不寄出貨通知信**，而理由**有兩條，只有第一條是決定性的**：

**① 出貨粒度的不變式即將被推翻。** 今天「一筆訂單至多一筆出貨、粒度是整筆訂單」是系統事實，證據三項：`shipping.Service.CreateShipment` 的簽名裡沒有品項也沒有數量、`shipments.order_id` 有唯一索引、`ListShipmentsAdmin` 的註解寫明「0 or 1 row this phase」。下一個 change `partial-shipment` 的內容就是打破它。

一封今天寫的「您的訂單已出貨」**必然**隱含「全部都出了」——那是這封信在顧客腦中的意思，不是我們措辭能繞開的。等 `shipments` 長出品項之後，這封信就從「正確」變成「在部分出貨時說謊」，而**沒有任何測試會發現**（信件內容不會因為資料模型變了就編譯失敗）。也就是說我們會交付一個**排定要變成錯的**顧客可見成品。這與 `tier-discount-application` D1 診斷過的錯誤是同一類：在消費端還沒定形的時候先把東西交出去。

**② 資訊已經拿得到（次要，單獨不足以構成理由）。** 出貨資訊今天已有兩條讀取路徑：會員走 `GET /shop/orders/{orderID}/shipment`、訪客走 `GET /guest/order/shipment`。但這條理由對 D2 的①②同樣成立過（訂單資訊也查得到），我們仍然寄了那兩封——所以它是佐證，不是判準。**判準是①。**

**部分出貨落地之後，這封信該怎麼做**（兩個選項，以及選擇的判準）：

- **每次出貨各寄一封，信裡只列該次出貨的品項。** 這是預設答案：它與資料模型同構（一列 `shipments` ＝ 一封信），`UNIQUE(order_id, kind)` 只要換成 `UNIQUE(shipment_id, kind)` 就繼續是仲裁者，而顧客要的正是「這箱裡有什麼、什麼時候到」。
- **全部出完時只寄一封。** 只有在「顧客會被多封信煩到」這件事被實際觀察到時才選它。它的代價是要引入一個系統今天沒有的概念——「這張訂單全部出完了」需要比對訂單品項數量與各次出貨數量的總和，那是一個**衍生狀態**，而衍生狀態正是 admin tripwire #1 描述的東西。

**觸發點**：`partial-shipment` 讓 `shipments` 長出品項的那一刻，D12 到期，出貨通知信重新評估。屆時 `notify` 這一側要動的東西是有限且已知的：新增一個 `kind`、事件從 `shipping.Service` 發佈（該套件已有 dispatcher）、renderer 多一個模板。**`Sender`／分派模型／防重機制一個字都不用改。**

### D13: PII——信裡有，日誌裡沒有，而且要有東西守著

訂單信裡必然包含收件人姓名、電話、完整地址、卡片訊息——那是這封信的**內容**，寄給下單者本人是正確的。真正的邊界在別處：

- **日誌 MUST 只帶 `shop_id`／`order_id`／`member_id`／`kind`／錯誤類別。** 不得記下信件主旨、內容、收件位址、姓名、電話、地址、卡片訊息。
- 這條規則的證明**落在位元組上，不是靠讀程式碼**（比照 `member-directory` 對 `password_hash` 的做法）：一支測試把 `slog` 導進 buffer，以一張帶有特徵值的訂單（姓名／電話／地址／卡片訊息／email 都是不會巧合出現的字串）跑完成功與失敗兩條路徑，再斷言 buffer 裡**一個都找不到**。
- `order_notifications.recipient_email` **是**一份 PII 快照，而它有一個真的消費者：`members` 沒有 email 變更歷史，所以一旦顧客改了 email，「當初那封信寄到哪裡」在系統裡就再也答不出來。它不由任何 API 回傳、不進任何日誌。
- **信件內容不落地**：`order_notifications` 只存 `recipient_email`／`status`／時間／錯誤原因，**不存渲染後的信件主體**。存了就等於在資料庫裡多開一份完整的收件地址副本，而它唯一的用途是「重看當初寄了什麼」——那個需求今天不存在。

### D14: 純文字信，但 MIME 必須做對——中文主旨不編碼會壞

`text/plain; charset=UTF-8`，沒有 HTML、沒有模板引擎（Go 的 `text/template` 都不需要，字串組裝就夠，而且渲染結果可以在測試裡逐字斷言）。

但「純文字」不等於「可以不管 MIME」，有三件事不做就會壞，而且是**在真實信箱裡壞、在開發機上看不出來**：

1. **主旨必須以 RFC 2047 encoded-word 編碼**（`=?UTF-8?B?...?=`）。中文主旨直接塞進 header 會被當成 8-bit，多數 MTA 會拒收或亂碼。用 `mime.QEncoding`／`BEncoding`（stdlib）。
2. **內文必須宣告 `Content-Transfer-Encoding: base64`** 並照做。SMTP 的 header/body 一律當 7-bit 處理，中文原樣送出遇到不支援 `8BITMIME` 的中繼就毀了。
3. **header 注入必須擋掉**：任何進入 header 的值（收件位址、主旨裡的訂單編號、商家名稱）若含 `\r` 或 `\n`，MUST 拒絕寄送。`shops.name` 是商家可自由輸入的欄位，這條不是理論風險。

**不用 `smtp.SendMail`**：它內部用 `net.Dial`（**沒有 timeout**），也就是 D5 的硬邊界做不到。改成 `net.DialTimeout` → `SetDeadline` → `smtp.NewClient` → `StartTLS` → `Auth` → `Mail`/`Rcpt`/`Data`/`Quit`。埠 465 走 `tls.Dial`（隱式 TLS），其餘走 STARTTLS——Gmail 兩者都支援，預設 587。

**零新增 Go 相依**：`net/smtp`、`crypto/tls`、`mime`、`encoding/base64` 全在標準函式庫。

### D15: 事件 `OrderPlaced` 只帶 id，不帶內容

`events.OrderPlaced{ShopID, OrderID, MemberID}`，三個 int，訂閱者自己去讀。

這與 `OrderPaymentSucceeded` 帶 `AwardBasisAmount` 的做法**看起來相反，但規則是同一條**：當一個數字的**定義**是微妙的、且只有發佈者知道正確口徑時（點數基準的「小計減折扣」被改過兩次名字就是為了這個），它必須由發佈者算一次；當訂閱者需要的是**一整份資料的當下樣貌**（品項、地址、金額三件組、配送日）時，複製進事件只會製造第二份會漂移的序列化——`member-directory` D2 已經為同樣的理由拒絕過在顧客端點裡長第二份訂單序列化。

副作用是好的：`notify` 讀的是**寄信當下**的訂單狀態，所以一封因為佇列積壓晚了幾秒才寄出的信，內容不會是幾秒前的快照。

### D16: `order_notifications` 是租戶資料

新表登記進 `tenant.tenantOwned`。它帶 `shop_id`，形狀與 `point_transactions` 相同。雖然本 change 的所有存取都在租戶上下文之外、且都顯式帶 `shop_id`（D7），登記它仍然是對的：**它是這張表未來被任何後台端點讀到時的預設防線**，而漏登記的症狀（跨租戶可見）不會有任何測試發現。

## Risks / Trade-offs

- **行程重啟會丟掉佇列裡的信** → 佇列容量小（256）、worker 少（4）、每封信的處理時間以秒計，所以窗口本來就短；`order_notifications` 對這種丟失**不會**留下列（預約發生在 worker 裡），這是 at-most-once 的誠實代價。要消除它就是引入持久化佇列，觸發條件見 D5。
- **寄送失敗不重試** → 留下 `status=failed` ＋ `error_reason` 的可查列，而不只是一行 log。人工補寄今天沒有介面（Non-Goal）。
- **Gmail 額度用盡會鎖住整個帳號 1～24 小時，且跨過午夜** → D9 的兩條推翻門檻都留了邊際，而且用 SQL 就量得出來。這仍是本 change 最大的營運風險。
- **平台寄件位址傷害商家信任與跨商家信譽** → D3，已寫明推翻條件與換掉時要動的清單。第二家商家上線就到期。
- **顧客可能收不到信而不知道**（垃圾郵件、擋信） → 本 change 不做退信處理。緩解是信件自足（D4：不需要點連結）＋ 訂單資訊仍可由既有端點查詢。
- **新增訂閱者讓檢查 ⑦ 轉紅** → 這是刻意設計，本 change 更新 `expectedSubscribers` 並在 D11 回答它逼出的問題。
- **`order.Service` 首次取得 Dispatcher** → 既有測試自建的 `order.Service{Client: ...}` 全部維持可編譯（新欄位是 nil-safe 的 optional），行為與過去相同（nil dispatcher 不發佈）。

## Migration Plan

1. 一支 versioned migration 建立 `order_notifications`（含 `UNIQUE(order_id, kind)` 與 `(shop_id, order_id)` 索引）。**純新增，無 CHECK 運算式變更，無既有資料回填。**
2. 部署後**不設定** `SMTP_USERNAME`／`SMTP_PASSWORD` ⇒ 系統行為與部署前逐位元組相同（`DiscardSender`）。這就是回滾策略：**清掉那兩個環境變數即可停止一切寄信**，不需要回滾程式碼、不需要回滾 migration。
3. 確認可行後再填入應用程式密碼；先以一家商家、一個信箱觀察，並開始追蹤 D9 的兩條門檻。
4. 真正的回滾（`make migrate-down`）只在需要移除表時執行；表留著不影響任何行為。

## Open Questions

- **D3 的到期日是「第二家商家上線」，而那可能比預期早。** 屆時是先做「商家寄件位址 ＋ 網域驗證」還是先換掉 Gmail？兩者可以分開，但順序沒有定論——本 change 不預先決定。
- **顧客有沒有退訂權？** 訂單交易信在多數司法管轄區不屬於行銷郵件，因此不強制提供退訂；但這件事沒有被正式確認過，也沒有任何欄位表達「這位顧客不要收信」。記在這裡，不在本 change 解決。
