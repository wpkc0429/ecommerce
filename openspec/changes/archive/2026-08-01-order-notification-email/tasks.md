## 1. 領域事件（design D15）

- [x] 1.1 `internal/events/events.go`：新增 `OrderPlaced{ShopID, OrderID, MemberID}`，doc comment 說明為什麼只帶 id 而不帶內容（與 `OrderPaymentSucceeded` 帶 `AwardBasisAmount` 的規則是同一條）
- [x] 1.2 `internal/order/service.go`：`Service` 新增 `Dispatcher *events.Dispatcher`（nil-safe，既有測試的 `order.Service{Client: ...}` 維持可編譯）
- [x] 1.3 `internal/order/service.go`：`Checkout` 在 `tx.Commit()` **之後**發佈 `OrderPlaced`；註解寫明「發佈點在此而非 handler」的結構理由（冪等回放不進這個函式，因此自動不重發）

## 2. `order_notifications` 資料模型（design D6／D13／D16）

- [x] 2.1 `internal/ent/schema/notify.go`：`OrderNotification`（`shop_id`／`order_id`／`kind` int16／`recipient_email`／`status` int16／`sent_at` nillable／`error_reason` nillable），`UNIQUE(order_id, kind)` ＋ `(shop_id, order_id)` 索引，CHECK 限制 `kind`／`status` 值域；doc comment 說明 at-most-once 與「不存信件內文」
- [x] 2.2 `cd api && go generate ./internal/ent`
- [x] 2.3 `internal/tenant/enttenancy.go`：登記 `OrderNotification` 為租戶資料
- [x] 2.4 `make migrate-gen name=add_order_notifications`；依慣例手動移除 atlas 誤判要 DROP 的四條手寫約束（members 的兩個 UNIQUE、role_user／user_permission 的 NULLS NOT DISTINCT）與 down 的對應重建，補說明註解，並重算 `atlas.sum`（`migrate.Validate` 通過）
- [x] 2.5 `make migrate` 套用並確認成功

## 3. `internal/notify` 套件（design D1／D5／D14）

- [x] 3.1 `notify/message.go`：`Message{To, Subject, TextBody}`；`Encode(fromAddr, fromName)` 產生完整 RFC 5322 位元組（RFC 2047 主旨、`Content-Type: text/plain; charset="UTF-8"`、`Content-Transfer-Encoding: base64` 並折行至 76 字元）；任一標頭值含 CR/LF 時回 `ErrHeaderInjection`。**刻意不帶 `From` 欄位**（design D3）
- [x] 3.2 `notify/message_test.go`：中文主旨為 encoded-word、內文 base64 可解回原文、base64 折行、`\r`／`\n` 注入被拒（收件位址／主旨／寄件顯示名稱三處）、`Message.String()` 不外洩收件位址與內文
- [x] 3.3 `notify/sender.go`：`Sender` 介面、`DiscardSender`（未設定憑證時的實體實作，非 nil）、`RecordingSender`（測試假物，執行緒安全，可注入 `Err`）
- [x] 3.4 `notify/smtpsender.go`：`SMTPSender` —— `net.Dialer{Deadline}` ＋ `conn.SetDeadline`（**不用 `smtp.SendMail`**，它沒有 timeout）、埠 465 走隱式 TLS 其餘走 STARTTLS 且**伺服器不提供 STARTTLS 時拒絕寄送**、`smtp.PlainAuth`；doc comment 記下 Gmail 的應用程式密碼、`From` 改寫行為與寄送量上限
- [x] 3.5 `notify/render.go`：`FormatMoney`（**整個套件不存在 `/100` 或 `*100`**，不用會產生 `.00` 的格式化）＋ 訂單成立／付款成功兩個模板；金額組合式（小計／折扣／運費／應付總額，折扣為零時省略該列）；**沒有點數格式化函式**；地址經 `order.ShippingAddress` 解碼（唯一定義，不複製）
- [x] 3.6 `notify/render_test.go`：3400 呈現為 `NT$3,400`（且無小數點）、組合式四列齊全且等式成立、零折扣不出現折扣列、內容不含 URL／權杖／點數、含品項卡片訊息與指定配送日、時間戳帶明示的 UTC+8、壞掉的地址 blob 不會毀掉整封信
- [x] 3.7 `notify/service.go`：`Service{Client, Sender, Log, Workers, QueueSize, Timeout}`——`Handle` 只做「轉成 job ＋ 非阻塞入隊」；`Start()` 起 worker、`Close()` 排空；`sendingEnabled()`（nil 或 `DiscardSender` ＝ 關閉）讓未設定憑證時整條路徑不執行
- [x] 3.8 `notify/service.go`：`deliver`——①顯式 `shop_id` 讀訂單＋品項、會員、商家 ②email 為 nil 則靜默略過（**不留任何列**）③預約列（唯一約束衝突即無操作）④渲染 ⑤`Sender.Send` ⑥更新為 sent／failed。**讀在預約之前**是被 `recipient_email` NOT NULL 決定的，仲裁性質不變（預約仍在寄送之前）；`DeliverEvent` 匯出給測試同步呼叫
- [x] 3.9 `notify/smtpsender_test.go`：本機 listener（**不打外網**）驗「伺服器不提供 STARTTLS 就拒絕寄送」、「連不上時在逾時內失敗」、「標頭注入在撥號之前就被擋下」

## 4. 設定與接線（design D10／D11）

- [x] 4.1 `internal/config`：新增 `SMTPConfig{Host, Port, Username, Password, From, FromName, Timeout}`（`Enabled()` 需帳號與密碼皆非空；`From` 留空時等於 `Username`）與 `NotifyConfig{Workers, QueueSize}`，全部有預設值
- [x] 4.2 `internal/app/wire.go`：建構 `notify.Service`（有憑證→`SMTPSender`，否則→`DiscardSender` 並 log 一行），`notifier.Start()`、`a.closers` 加入 `notifier.Close`、`dispatcher.Subscribe(notifier.Handle)` **無條件**執行；`orderService` 接上同一個 dispatcher
- [x] 4.3 `internal/app/wiring_integration_test.go`：`expectedSubscribers` 新增 `notify.(*Service).Handle-fm`（註解就地回答「e2e 要不要補斷言」）；檢查 ⑦ 的「同一個 dispatcher 實例」比對表新增 `"Orders"`
- [x] 4.4 `.env.example`：新增全部新環境變數與說明（不設定＝不寄信、Gmail 應用程式密碼、查證後的寄送量上限與兩條推翻門檻、換服務商只需改三個值）

## 5. 測試（design D11 的分工：整合測試承接 e2e 無法觀察的部分）

- [x] 5.1 `internal/httpapi/notification_integration_test.go`：訪客結帳與會員結帳各恰好一封確認信（收件人是下單者、金額組合式）；庫存不足與地址不合法皆零封
- [x] 5.2 `internal/httpapi/notification_integration_test.go`：**冪等回放不寄第二封**（同一把 key 兩次 → 201 ＋ `Idempotency-Replayed` ＋ 同一張訂單 ＋ 一封信）
- [x] 5.3 `internal/notify/service_integration_test.go`：訂單成立信與付款成功信各寄一次、內容含金額四列、列狀態為 sent 且 `sent_at`／`recipient_email` 正確
- [x] 5.4 `internal/notify/service_integration_test.go`：**重複投遞只寄一封**（同一事件三次 → 一則訊息一列）＋ **六個 goroutine 併發只寄一封**（證明仲裁者是唯一索引而非 read-then-write）；`internal/httpapi` 另有一份走真實 webhook 路由的版本
- [x] 5.5 `internal/notify/service_integration_test.go`：`members.email` 為 NULL → 不寄、不報錯、不留列；跨商家事件 → 失敗且不寄不留列；`DiscardSender` → 不留列；`OrderReturned` → 無操作
- [x] 5.6 `internal/notify/service_integration_test.go`：`Sender` 回錯誤 → 列為 failed ＋ `error_reason`、`sent_at` 維持空、**訂單狀態一個字不變**
- [x] 5.7 `internal/notify/pii_integration_test.go`：`slog` 導進 buffer，成功／失敗／略過三條路徑各跑一次，斷言 buffer 不含姓名／電話／地址／卡片訊息／email／主旨／金額（並先斷言信件本身確實含有那些特徵值，否則測的是一封空信）
- [x] 5.8 `internal/httpapi/notification_integration_test.go`：已取消訂單收到成功回呼 → **不寄付款信**（且不追寄任何取消信）
- [x] 5.9 `internal/notify/service_integration_test.go`：非同步半邊——`Handle`＋worker＋`Close` 會送出；佇列滿時**丟棄而不阻塞**（若阻塞，卡住的郵件伺服器就會卡住結帳）

## 6. 驗收閘門

- [x] 6.1 `make test` 全綠（含契約檢查 ①②④）
- [x] 6.2 `make test-int` 全綠（含契約檢查 ③ 與接線檢查 ⑤⑥⑦）
- [x] 6.3 `make openapi` 全綠；`git status` 確認 `openapi.yaml` 與 `v1-surface.txt` **一行未動**
- [x] 6.4 `cd api && golangci-lint run` 全綠（0 issues）
- [x] 6.5 `make seed-demo` ＋ 起 API ＋ `cd e2e && node commerce-path.mjs` 全綠（已先確認 :8080 沒有殘留舊 server）
- [x] 6.6 反向驗證：暫時移除 `dispatcher.Subscribe(notifier.Handle)`，確認檢查 ⑦ **確實轉紅**並指名少了哪一個訂閱者，再還原

## 7. 文件

- [x] 7.1 `CLAUDE.md`：新增本 change 的架構要點（兩封信與理由、事件發佈點在 `Checkout` 內、at-most-once ＋ `UNIQUE(order_id, kind)`、非阻塞分派與它會壞的地方、Gmail 兩條推翻門檻與查證更正、不放連結、不顯示點數、日誌 PII 邊界、給 `partial-shipment` 的出貨粒度交棒）
- [x] 7.2 `design.md` D1：實作時發現原敘述把相依方向寫得過於對稱，就地更正——禁止的是 `order/payment/shipping → notify`，而 `notify → order`（只為 `ShippingAddress` 這個唯一定義）是既有慣例且必要
