## Why

顧客下單之後，這個系統**從來沒有主動聯絡過顧客**。

對會員來說這只是不方便（`GET /shop/orders` 查得到）。對**訪客**來說這是一個真的洞：`guest-checkout` 刻意讓訪客不需要密碼、不需要帳號，代價是他手上唯一的憑據就是那一次 HTTP 回應裡的訂單存取權杖——而 `checkout-idempotency` 存在的理由正是**「回應可能遺失」**。回應遺失時，我們替客戶端程式解決了「訂單到底成立了沒有」（重送同一把 key 得到同構回應），卻沒有替**人**解決同一個問題。ONDO 全站沒有登入 UI，所以「人」就是全部的顧客。

第二個洞在付款。綠界的金流是**帶外**的：顧客離開我們的頁面去綠界付錢，錢有沒有到帳只有 webhook 知道，而顧客看不到 webhook。`ECPAY_CLIENT_BACK_URL` 預設是空字串——也就是說**今天存在一條完全合法的路徑：顧客的卡被扣款，而本平台一個字都沒有對他說**。

## What Changes

### 新增：訂單通知信（兩封，不是四封）

- 新套件 `api/internal/notify`：`Sender` 介面 ＋ `SMTPSender`（真實 SMTP，預設指向 Gmail）＋ `RecordingSender`（測試假物）＋ `DiscardSender`（未設定憑證時的預設）。形狀比照 `payment.Provider` / `MockProvider`（介面 ＋ 可測假物 ＋ 真實機制）。
- 只寄兩封：**訂單成立**與**付款成功**。出貨通知與取消通知是 Non-Goal，各自附推翻條件（見 design D2）。
- 純文字信（`text/plain; charset=UTF-8`，base64 傳輸編碼，主旨以 RFC 2047 編碼）——中文主旨不編碼會壞，這是必須實作而不是可選的。

### 新增：`events.OrderPlaced`，由 `order.Service.Checkout` 在交易 commit 後發佈

- `order.Service` 首次取得 `Dispatcher`（既有的 `payment`/`shipping` 早已如此）。**發佈點在 `Checkout` 裡面，不在兩個 handler 裡**：那是全系統唯一的建單實作，所以「一張訂單一個事件」是結構事實而不是紀律；冪等回放不呼叫 `Checkout`，因此**自動**不會寄第二封（見 design D6）。

### 新增：`order_notifications` 表——寄送的仲裁者與稽核軌跡

- `UNIQUE(order_id, kind)`，形狀完全比照 `point_transactions`。**先預約再寄送**（INSERT 贏了才寄），因為本系統沒有任何重試機制，所以正確的語義是 **at-most-once**：重複的「訂單已成立」是顧客看得見的故障，寄丟則留下一列 `status=failed` 的可查紀錄。
- 付款 webhook **每一次**成功投遞都會發佈 `OrderPaymentSucceeded`（既有行為，綠界一天最多重送 4 次），所以這張表不是防禦性設計，是**必需品**（見 design D6）。

### 新增：設定（未設定時行為與本 change 之前逐位元組相同）

- `SMTP_HOST`／`SMTP_PORT`／`SMTP_USERNAME`／`SMTP_PASSWORD`／`SMTP_FROM`／`SMTP_FROM_NAME`／`SMTP_TIMEOUT`／`NOTIFY_WORKERS`／`NOTIFY_QUEUE_SIZE`。**沒有帳號密碼 ＝ 不寄信、不報錯、不寫任何列、不影響下單**（比照 `PAYMENT_DEFAULT_PROVIDER`／`ECPAY_*`）。

## Capabilities

### New Capabilities

- `order-notification-email`: 訂單事件觸發的顧客電子郵件通知——寄哪幾封與何時寄、收件人如何以商家為界解析、信件內容的金額組成與單位規則、寄送失敗與重複投遞的語義（at-most-once ＋ `UNIQUE(order_id, kind)` 仲裁）、不阻塞 HTTP 回應的分派模型、未設定寄件憑證時的行為、以及日誌的 PII 邊界。

### Modified Capabilities

- `order-management`: `Checkout` 成功建單後 MUST 發佈一個「訂單已成立」領域事件，且該發佈點 MUST 在 `Checkout` 內部——每一張建立出來的訂單恰好一個事件，任何不建單的路徑（驗證失敗、庫存不足、冪等回放）MUST NOT 發佈。

## Impact

- **Go 套件**：新增 `internal/notify`；修改 `internal/events`（新增 `OrderPlaced`）、`internal/order`（新增 `Dispatcher` 欄位與發佈點）、`internal/config`、`internal/app/wire.go`、`internal/app/app.go`（關閉時排空）、`internal/tenant/enttenancy.go`（新表登記為租戶資料）。
- **資料庫**：新增 `order_notifications` 表（`shop_id`／`order_id`／`kind`／`recipient_email`／`status`／`sent_at`／`error_reason`），含 `UNIQUE(order_id, kind)`；一支新的 versioned migration。無 CHECK 運算式變更。
- **接線檢查**：`internal/app/wiring_integration_test.go` 的 `expectedSubscribers` **必然轉紅**（檢查 ⑦ 是相等比對），本 change MUST 更新它並在 design 裡回答它逼出的問題「e2e 要不要補一條斷言」（答案與理由見 design D11）。
- **v1 公開 API**：**零變更**。不新增端點、不改任何回應主體，`api/openapi/openapi.yaml` 與 `v1-surface.txt` 一行未動——通知是事件的下游，不是 API 的表面。
- **admin 前端**：一行未改（tripwire #4′／#5 不觸發）。
- **對外網路**：**測試與 CI 永不寄出真實郵件**——`make test`／`make test-int`／e2e 全程使用 `RecordingSender`／`DiscardSender`，不需要憑證、不需要外網。

## Non-Goals

- **不寄出貨通知信**（design D2／D12）：`shipments` 的粒度是**整筆訂單**，而下一個 change `partial-shipment` 就要打破這個不變式。今天寫一封「您的訂單已出貨」等於把一個排定要被推翻的假設寫進顧客看得到的成品。
- **不寄取消通知信**（design D2）。
- **不做 HTML 信、不做模板引擎、不做多語系**：純文字，商家名稱取自 `shops.name`，其餘固定正體中文。
- **不做背景工作佇列**：本系統沒有佇列，本 change 也不引入。取而代之的是**行程內、有界、關閉時排空**的分派器，並在 design D5 寫明它在什麼流量與什麼故障下會壞。
- **不做重試、不做退信處理（bounce）、不做寄送狀態的後台 UI 或 API**：`order_notifications` 今天的消費者是資料庫本身（仲裁 ＋ 人工查詢 ＋ 寄送量統計），刻意不長出端點。
- **不寄給收件人**：信寄給**下單者**（`members.email`），不寄給 `shipping_address.recipient_name` 那個人——我們沒有他的 email，而且送花給誰是下單者的事。
- **信裡不放任何連結、不放訂單存取權杖**（design D4，正面權衡後的決定，不是遺漏）。
- **信裡不顯示點數**（design D8）。
- **寄件人是平台的 Gmail 位址，不是商家的**（design D3，附推翻條件與換掉時要動的清單）。
- **寄送量受 Gmail 帳號上限硬限制**（design D9，附查證後的實際數字與可觀測的推翻門檻）。
