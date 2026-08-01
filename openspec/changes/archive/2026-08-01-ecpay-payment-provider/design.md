## Context

`payment-integration`（已歸檔）交付的是一個**尚未被驗證的承諾**：

> 新增支援真實金流商時，MUST 只需新增一個實作該介面的 provider，不需修改呼叫端（handler、webhook 路由、idempotency 邏輯）程式碼。

它是誠實寫下的——當時沒有任何真實金流商可以拿來撞。ONDO 定案採用**綠界 ECPay** 之後，這個 change 就是那次撞擊。**它有三處撞不過去**，全部在本文件正面回答（D2／D3／D4）。

同時，使用者提出一個必須查證而不是照抄的問題：**訂單取消後留下的資訊是否足夠人工對帳？** 查證結果推翻了問題本身的前提，並且找到一個比「資訊不足」嚴重得多的既有缺陷（D8／D9，附實測輸出）。

**已定案、本文件不重新討論的前提**（使用者決定）：金流商是綠界；先做模擬流程、不打真實綠界端點；退款不由平台執行、採人工流程。

## Goals / Non-Goals

**Goals:**

- 依綠界**真實的線路形狀**實作：真實欄位名、真實 `CheckMacValue` 演算法、真實的付款結果通知約定（`1|OK`）。
- 提供一個**不需要綠界帳號、不需外網**就能跑通「發起付款 → 收銀台 → 付款結果通知 → 訂單轉已付款」完整流程的模式。
- 讓 `Provider` 介面**真的**撐得住第二家 provider——撐不住的地方就改介面，而不是在 handler 裡特判。
- 讓商家拿著綠界後台的一筆交易，能在本系統**機械地**找到對應訂單並確認金額與時間。
- 修掉查證過程中發現的既有缺陷：已取消訂單被回呼推成已付款並核發點數。

**Non-Goals:**

- **不執行退款。** 退款是人工流程（商家在綠界後台操作）。平台不提供退款 API、不呼叫綠界退款端點、不寫入 `payment_status = 2/3` 或 `payments.status = 3`。本 change 只交付讓人工退款可行的前提：那筆交易找得到、金額對得上、時間查得到。
- **不做部分金額付款。**
- **不串真實綠界端點。** 不呼叫 `payment.ecpay.com.tw` 或 `payment-stage.ecpay.com.tw`；CI 與所有測試不打外網。
- **不做綠界訂單查詢 API（`QueryTradeInfo`）**——那是主動出站呼叫，與上一條直接衝突。
- **不接 `PaymentInfoURL`（ATM／超商取號結果通知）**：那是綠界的**第二支**通知端點，只在 `ChoosePayment` 選了非即時付款方式時才會被呼叫。本 change 的 `ECPAY_CHOOSE_PAYMENT` 預設 `Credit`（綠界官方也建議固定指定付款方式而非 `ALL`），非即時付款留給後續 change。
- **不改 admin 前端。**

## Decisions

### D1: 「形狀真實、端點可注入」——為什麼這不是第二個 mock provider

`MockProvider` 是「**機制**真實、**形狀**隨便」：HMAC-SHA256 是真的比對（不是永遠通過），但 `provider_reference` 是 `mock_<uuid>`、body 是我們自己編的 `{provider_reference, status}`、header 是我們自己取的 `X-Payment-Signature`。它證明的是「webhook 驗簽這條程式路徑真的會跑」。

ECPay provider 要證明的是完全不同的一句話：「**我們產生的位元組，綠界收得下；綠界送來的位元組，我們讀得懂**」。所以它的形狀不能是我們編的：

| | MockProvider | ECPay provider |
|---|---|---|
| 導向 | GET 一個 URL | **form POST 一組欄位** |
| 簽章 | header `X-Payment-Signature` | **body 裡的 `CheckMacValue` 欄位** |
| 簽章演算法 | HMAC-SHA256(secret, body) | **大小寫不敏感排序 → HashKey/HashIV 夾擠 → .NET UrlEncode → 小寫 → SHA256 → 大寫** |
| body 格式 | JSON | **form-urlencoded** |
| 成功回應 | JSON envelope | **純文字 `1|OK`** |
| 識別碼 | 一個 | **兩個** |

沒有一格相同。這就是為什麼「再寫一個 mock」達不到目的：**mock 不會逼介面改**，而這五格裡有三格逼它改了。

「不打真實端點」因此不是靠「假裝送出」達成，而是靠**把收銀台位址變成設定值**（`ECPAY_CHECKOUT_URL`）。程式產生的欄位集與簽章，與打真站時**逐位元組相同**；差別只在 POST 給誰。改成打綠界測試站是一個環境變數，不是一次 change——這正是「形狀真實」唯一能兌現的意義。

**候選方案與否決理由：**

- **直接打綠界測試站**（公開測試 MerchantID）：違反「不需外網」，CI 會依賴綠界的可用性，且測試會變成 flaky。否決。
- **在 provider 裡加一個 `simulate bool`，為 true 時跳過 HTTP 直接自己發回呼**：這會讓「模擬」與「真實」走**不同的程式路徑**——而模擬模式跑綠的那條路徑，正好不是上線要用的那條。這是最糟的一種假保證。否決。
- **本機模擬收銀台（採用）**：見 D11。

### D2: form POST 裝不進一個字串——回傳**欄位集**，不是一段 HTML

**查證結果：使用者的觀察成立。** 綠界 `AioCheckOut V5` 是把十餘個欄位（含 `CheckMacValue`）以 `application/x-www-form-urlencoded` **POST** 到收銀台網址，不是 GET 導向一個帶 query 的 URL。`InitiateResult.RedirectURL` 這個單一字串裝不下它。

兩個候選：

- **(a) 後端回傳一段自動送出的 HTML form**
- **(b) 後端回傳欄位 map，前端自己組 form**（採用）

**選 (b)，四個理由：**

1. **我們是 headless API，消費端不只有瀏覽器。** 第一方 SSR（`/web`）、ONDO 的零建置步驟靜態站、以及 render bundle 明文承諾要服務的「未來原生 APP」——原生 APP 拿到一段 HTML 沒有任何用處，拿到欄位 map 可以直接丟進 WebView 或系統瀏覽器。回傳 HTML 等於把「消費端必須是一份 HTML 文件」寫進 v1 契約。
2. **回傳 HTML 就是在 JSON API 上開一個注入面。** 那段 HTML 會被送進顧客的瀏覽器並執行；任何流進去的欄位值（商品名稱、備註）都需要在**我們這一側**做 HTML escape。回傳資料則把 escape 留在消費端自己的框架裡（React/Vue 預設就做了），而我們的契約維持「只有資料」。
3. **可測性。** 欄位 map 可以逐欄位斷言（`MerchantTradeNo` 是什麼、`TotalAmount` 是不是等於 `total_amount`、`CheckMacValue` 是不是那個值）。一段 HTML 只能靠解析字串來斷言，而那個解析器本身沒人測。
4. **CSP 是商家的事。** 自動送出的 form 需要 inline script 或一顆送出鍵；商家自帶前端的 CSP 由商家自己定，平台不該替他決定。

**代價，明寫：** 前端多寫約 15 行（建 form、塞 hidden input、submit）。`docs/storefront-integration.md` 附上可直接複製的片段。

**`redirect_url` 怎麼辦——這是本決定最不漂亮的一段，不掩飾。**

`PaymentInitiateResult.redirect_url` 在 `api/openapi/v1-surface.txt` 裡是一條已鎖定的 v1 承諾，**不能刪**（D13 破例窗口已關閉）。於是：

- 新增 `redirect_form`（`object | null`），**additive**。
- `redirect_url` 對 ECPay **回空字串**，且契約明寫規則：**消費端 MUST 先看 `redirect_form`；它不是 null 時，`redirect_url` 沒有意義**。

考慮過讓 `redirect_url` 裝收銀台網址（不帶欄位）。**否決**：那會讓只讀 `redirect_url` 的舊消費端把顧客導到綠界的錯誤頁，看起來像綠界壞了。空字串至少壞在自己家裡、壞得明顯。**這是新舊語意共存的必然代價**，記在這裡是為了讓下一個人知道它是被權衡過的，不是漏掉的。

### D3: 回呼要回什麼位元組，是 provider 的契約——**本 change 最重要的介面決定**

**查證結果：使用者的觀察成立。** 綠界要求商店對付款結果通知回**純文字 `1|OK`**（無參數名、無 JSON、無引號）；沒有正確回應時，綠界**每隔 5~15 分鐘重發，當天最多重複 4 次**。文件甚至列了一份「常見的錯誤回應」清單，`"1|OK"`（帶引號）、`1|ok`、`1OK`、`OK` 全部在上面——**一個 JSON envelope 當然也在上面**。今天的 webhook handler 一律走 `httpx`（D12 的 JSON envelope），而 `Provider` 介面**沒有任何地方能表達「這個 provider 的回應長什麼樣」**。

三個候選：

- **(a) 在 handler 裡 `if providerName == "ecpay"` 特判。** 否決——那正是 `payment-integration` design D1 立起來要擋的東西：provider 知識漏進 `httpapi`。第二家、第三家 provider 會讓它長成一串 switch，而且每加一家都要改 handler，直接推翻那句被寫進 spec 的承諾。
- **(b) 讓 `VerifyWebhook` 回傳回應位元組。** 否決——回應形狀取決於**處理結果**（成功 / 認不出這筆交易 / 我們自己壞了），而 `VerifyWebhook` 在處理發生**之前**就結束了。它答不出這個問題。
- **(c) `Provider` 新增一個方法（採用）**：

```go
// Ack 是「這通回呼最後怎麼了」的 provider 中立描述。決定是哪一種的是
// handler；決定它該長成什麼位元組的是 provider。
type Ack int
const (
    AckProcessed        Ack = iota // 簽章通過、找到付款紀錄、狀態已套用
    AckIgnored                     // 簽章通過、但認不得這個 reference——不要重送
    AckInvalidSignature            // 驗簽失敗
    AckMalformed                   // 簽章通過但內容解不開
    AckFailed                      // 我們自己壞了——provider SHOULD 重送
)

type WebhookResponse struct {
    Status      int
    ContentType string
    Body        []byte
}

// WebhookAck 回傳這家 provider 的呼叫者對此 Ack 所要求的確切回應。
// **零值 WebhookResponse 表示「沒有 provider 特定要求」**，此時 HTTP 層
// 以本 API 自己的 JSON envelope 回答。
WebhookAck(ack Ack) WebhookResponse
```

**「零值 = 用平台預設」這個約定是刻意的，它解決一個真實的取捨。** 如果每個 provider 都必須自己產出全部五種回應的位元組，`MockProvider` 就得自己組出 D12 的 error envelope——而那表示**envelope 的形狀會有第二份定義**，`internal/payment` 也會被迫 import `internal/httpx`（domain 套件 import 表現層格式套件）。零值約定讓 `MockProvider` 五個分支全部回 `WebhookResponse{}`，於是它今天的回應**逐位元組不變**，envelope 仍然只定義在 `httpx` 一個地方。

它不是「optional interface」的變裝：方法是**必要的**，新 provider 一定要寫出這個 method 才能編譯，也就一定要停下來想一次「我的呼叫者要看到什麼」。它只是允許答案是「跟大家一樣」。

**明寫的界線：** provider 的 ack 只管**驗簽開始之後**。之前的兩種失敗仍由我們自己答：未註冊的 provider → `httpx.NotFound`（那不是任何 provider 的回呼）、body 讀取失敗 → `httpx.BadRequest`（那是我們的 64 KiB 上限，呼叫者還沒被認證）。這條線可以一句話說完，所以它站得住。

**ECPay 的答案：** `AckProcessed` / `AckIgnored` → `200 text/plain "1|OK"`（認不得的交易也回 `1|OK`，因為重送不會讓它變得認得——同一個理由，不同的位元組）；`AckInvalidSignature` → `200 text/plain "0|CheckMacValue Error"`；`AckMalformed` → `200 text/plain "0|Bad Request"`；`AckFailed` → `500 text/plain "0|Internal Error"`（**唯一希望被重送的一種**，所以是唯一的非 2xx）。

### D4: 識別碼有兩個，而且**哪一個當 key 不是選擇題**

**查證結果：使用者的觀察成立，但「兩個都要存嗎」有一個被迫的答案。**

- `MerchantTradeNo`：**我們**產生，隨 `AioCheckOut` 送出，之後每一通回呼都原樣帶回，綠界後台顯示為「商店訂單編號」。
- `TradeNo`：**綠界**產生，**只有在回呼送達時才知道**，綠界後台顯示為「綠界交易單號」。

`payments.provider_reference` 是 webhook 的冪等查找鍵，它**必須在發起付款當下就存在**（`Service.InitiatePayment` 在建立 payments 列時就要寫入它）。`TradeNo` 那時候還不存在。**所以 `provider_reference = MerchantTradeNo` 是被迫的，不是偏好。**

`TradeNo` 則是商家在綠界後台、對帳單、客服對話裡最常拿到的那一個。丟掉它，人工對帳就少一半入口。因此：**新增 `payments.provider_transaction_id`（varchar(64), NULL）**，在回呼送達時寫入。

- 為什麼不是叫 `ecpay_trade_no`：`payments` 是 provider 中立的表，Stripe 的 `PaymentIntent.id`、藍新的 `TradeNo` 都會落在同一欄。
- 為什麼是 NULL 而不是空字串：`mock` 不產生它，既有的每一列也沒有——「這家 provider 沒有第二個識別碼」與「有但還沒收到」都應該是 NULL，而不是一個看起來像值的空字串。
- 為什麼不做唯一索引：它不是查找鍵（查找鍵是 `provider_reference`），而且綠界對「同一筆交易重送」會帶同一個 `TradeNo`，加了唯一索引反而會讓重送炸掉。

`WebhookResult` 因此新增 `ProviderTransactionID string`（空 = 這家 provider 沒有第二個識別碼）。

**查證後補強的兩點（原本只當它是「對帳方便」，實際上更硬）：**

1. 綠界文件**明文要求**特店「保存綠界的交易編號與特店交易編號 `MerchantTradeNo` 的關連」。
2. **退款 API（`CreditDetail/DoAction`）的必填參數同時包含 `MerchantTradeNo` 與 `TradeNo`。** 也就是說，`TradeNo` 沒落庫，日後任何自動化退款都做不到——**這一欄不是為了好查，是退款的前提**。本 change 不做退款（Non-Goal），但把那一天需要的東西先存下來，成本是一個 NULL 欄位。

### D5: `MerchantTradeNo` 的格式——20 字元是硬牆，而唯一性有一個現成的保證

綠界限制：**≤ 20 字元、英數字大小寫混合、「均為唯一值，不可重複使用」**。文件**沒有明文界定唯一性的範圍**（per-MerchantID？跨測試/正式環境？）——查證結果如實記錄為「未界定」，本設計因此取最保守的解讀：**全域唯一**。另一個文件沒明說但語意上跟著來的推論：「不可重複使用」意味著**即使那筆交易失敗，該編號也已經燒掉**，重試必須換一個新號。

採用格式：**`O{orderID}T{base36(unixMilli) 大寫}`**，例如 `O1234TMZQ8K3PA`。

- **唯一性從哪裡來：** `orders.id` 是單一資料表的 serial PK，**跨商家全域唯一**（`shop_id` 只是一個欄位）。所以不需要把 `shop_id` 塞進去。同一張訂單的多次付款嘗試由毫秒時戳區分——這正好對上「失敗的編號也已燒掉、重試必須換號」；真的撞在同一毫秒，`payments(provider, provider_reference)` 的唯一索引會擋下來——**仲裁者是資料庫，不是我們對亂數的信心**。
- **長度：** `1 + len(orderID) + 1 + 8`。base36 的 `unixMilli` 在 2059 年前是 8 碼，因此 orderID 到 10 位數（99 億張訂單）都還在 20 以內。**仍然加一道硬檢查**：產出超過 20 就回錯誤而不是送出去被綠界打回票——一個在 handler 回 500 的失敗，比一個在綠界端才炸、且訊息是中文錯誤碼的失敗好查太多。單元測試直接用一個 10 位數的 orderID 斷言長度。
- **為什麼把 orderID 放進去（而不是純亂數）：** 商家在綠界後台看到 `O1234T…` 就知道是 1234 號訂單。**但這只是給人看的**——`docs` 與程式註解都明寫**沒有任何程式碼會去 parse 它**，機械的答案是 D10 的反查端點。把「可讀」與「可靠」分成兩件事，是為了不讓下一個人寫出一個 `strings.Split(ref, "T")[0][1:]`。

### D6: `Outcome` 需要第三個值——「**收到了，但這不代表拿到錢**」

**先更正一件事：本設計原本寫的理由是錯的。** 初稿寫「ATM 取號成功會以 `RtnCode=2` 送到付款結果通知」——**查證後不成立**：`RtnCode=2` 走的是綠界的**另一支**通知端點 `PaymentInfoURL`（取號結果通知），本 change 刻意不接它（Non-Goals）。付款結果通知（`ReturnURL`）只會收到 `1`（成功）與各式失敗碼。

**但結論反而更強，因為真正的兩個「非終局」情況比 ATM 更難發現：**

**(a) `SimulatePaid=1` —— `RtnCode` 也會是 `1`，但綠界不會撥款。**
綠界廠商後台有一顆「模擬付款」按鈕，用途是讓特店測試 `ReturnURL` 通不通。它送出的通知 `RtnCode=1`、`RtnMsg=交易成功`，只有 `SimulatePaid` 欄位是 `1`。綠界文件明文：**收到 `SimulatePaid=1` 不可出貨、不可視為已付款**。

若只看 `RtnCode`，任何能登入商家綠界後台的人（包含綠界客服、包含商家自己手滑）都能讓本系統把一張訂單標成已付款、**核發點數、並讓它進入待出貨清單，而帳上一毛錢都沒有**。這是一個「寫錯不會噴錯」的錯誤，而且它的觸發者不在我們的系統裡。

**(b) `RtnCode=10300066`「交易付款結果待確認中，請勿出貨」。**
這是綠界明列的一個**既非成功也非失敗**的狀態。把它當失敗，就會踩到下面這條連鎖：

1. 第一通把 payments 列 CAS 成 **failed**（終局）。
2. 結果確認為成功後，綠界送出 `RtnCode=1`。
3. `HandleWebhook` 的 CAS 因為列已非 pending 而失敗 → 重讀 → `p.Status == failed` → **`target == Succeeded && p.Status == Succeeded` 為假 → 訂單永遠不會被標記已付款。**

**錢進了商家的綠界帳戶，訂單永遠是未付款，而且沒有任何東西會噴錯。**

**處置：新增 `OutcomePending`**——「這一通回呼沒有帶來終局結果」。`statusForOutcome(OutcomePending) = StatusPending`，`HandleWebhook` 在 target 為 pending 時**不做任何狀態轉換**、直接回目前這一列，但**仍然寫入 `provider_transaction_id`**（那一通就已經帶了 `TradeNo`，而那正是商家對帳與日後退款要的）。回應仍是 `AckProcessed`（`1|OK`）——我們確實收到了，重送不會改變任何事。

映射表（ECPay provider 內部，不外洩到介面）：

| 條件 | Outcome |
|---|---|
| `RtnCode == 1` 且 `SimulatePaid != 1` | `OutcomeSucceeded` |
| `RtnCode == 1` 且 `SimulatePaid == 1` | **`OutcomePending`** |
| `RtnCode == 10300066` | **`OutcomePending`** |
| 其他 | `OutcomeFailed` |

**這個值不是為了 ECPay 才存在的**：任何有「已授權未請款」「等待對方銀行」「風控人工審核」的 provider 都會用到它。`Outcome` 的註解原本寫「1:1 對應本 change 會產生的非 pending `payments.status` 值」——本 change 推翻那句話並在原處說明理由。

### D7: `TotalAmount` 對得上，但**必須被檢查而不是被假設**

專案硬約束：金額是 `int64` 最小貨幣單位，TWD 的最小單位是**元**，全系統不存在 `/100` 或 `*100`。綠界的 `TotalAmount` 是**整數新台幣元**。所以 `TotalAmount = payments.amount`，**沒有任何換算**。

**但這句話只在 currency 是 TWD 時成立**，而綠界只支援 TWD。如果哪天有商家的訂單是 JPY（最小單位也是 1，會巧合地正確）或 USD（最小單位是分，會**差 100 倍**且看起來完全合理），這個等式就是錯的。

所以 ECPay provider 在 `InitiatePayment` 的第一件事是：**`req.Currency != "TWD"` 就拒絕**，並且有一支單元測試。這是「不會噴錯的錯誤」清單上該有的東西——`admin/lib/money.js` 那一條的同類。

**回呼側同樣不假設：** 綠界的通知帶 `TradeAmt`。`WebhookResult` 新增 `Amount int64`（0 = 這家 provider 不回報金額），`HandleWebhook` 在回報值非 0 且**不等於** `payments.amount` 時**拒絕推進**（回一個明確的錯誤 → `AckFailed` → 綠界依它的重送規則試 4 次後，在它的後台留下失敗紀錄）。這一步的價值不在防篡改（簽章已經做了），而在於：**它把「兩邊金額不一致」從一個沒人會發現的靜默狀態，變成一個雙方系統都有紀錄的響鈴**——而那正是人工對帳存在的理由。

**另一個查證出來的坑：`ItemName` 上限 400 字元，而綠界的行為是「超過就截斷」，截斷處會產生亂碼，導致 `CheckMacValue` 對不上而掉單。** 所以截斷必須由我們自己做、且必須在 **rune 邊界**上（與 `order-extras` 的 `gift_message` 同一個理由：Go 的 byte 長度不是字元長度，中文品名一刀切下去就是半個字）。

### D8: 查證「訂單取消後的資訊是否足夠人工對帳」——**問題的前提被推翻了兩次**

使用者的問題預設了一個情境：「取消一筆已付款的訂單」，並擔心「靜默留下一筆已付款但已取消的訂單」。**實測後有三個發現，前兩個推翻前提，第三個比前提擔心的更糟。**

**發現 1：已付款的訂單根本無法取消。** `order.Service.CancelOrder` 的原子條件式 UPDATE 同時要求 `status=Created AND payment_status=Unpaid AND fulfillment_status=Unfulfilled`；已付款的訂單 affected=0，回 `ConflictError`（409）。實測：

```
CancelOrder on a PAID order => err=order can no longer be cancelled (*order.ConflictError)
```

所以「取消已付款訂單」這條路徑**不存在**，`payments` 也就不會因此掉資料。

**發現 2：現存的 `payments` 欄位已經涵蓋「付款單號、金額、時間」的一半，而缺的那一半不是時間。** `payments` 有 `provider`、`provider_reference`、`amount`、`currency`、`status`、`created_at`、`updated_at`，且 `order_id` 是**真的 FK 且刻意沒有 `ON DELETE CASCADE`**（`api/internal/ent/schema/payment.go` 的型別註解寫明這是為了保護財務軌跡）。取消訂單完全不碰 `payments`，所以取消後**紀錄一個位元都沒少**。守著它的是本 change 新增的 `TestCancelledOrderRetainsPaymentTrail`。

**真正缺的是「反查」**：今天唯一能看付款紀錄的端點是 `GET /admin/shops/{shopID}/orders/{orderID}/payments`——**你必須先知道訂單編號**。而人工對帳的起點恰恰相反：商家手上有的是綠界後台的一筆交易。這個缺口由 D10 補上。

**發現 3（本 change 修的既有缺陷）：** 見 D9。

### D9: 已取消訂單收到付款成功回呼——**記錄，但不推進**

**這不是 ECPay 帶來的問題，它今天就在，用 mock provider 就能重現。** 實測輸出（`internal/payment` 的一次性探針）：

```
order cancelled: status=1 payment_status=0 cancelled_at=...
RESULT payment.status=1 order.status=1 order.payment_status=1 events=1
  event: order.payment_succeeded {ShopID:1 OrderID:1 MemberID:1 AwardBasisAmount:5000 Currency:TWD}
>>> HOLE CONFIRMED: order is BOTH cancelled AND paid, and the loyalty event fired
```

情境完全真實，而且 ECPay 只會讓它**更常**發生（顧客停在綠界收銀台，商家在後台按取消，顧客回頭把款付了）：

1. 未付款訂單有一筆 pending 付款 → 取消成功（三軸都還在初始值），**庫存已歸還**。
2. 顧客仍在綠界完成付款 → 成功回呼送達。
3. `HandleWebhook` 找到那筆 pending 列 → CAS 成 succeeded → 呼叫 `UpdatePaymentStatus(paid)`（**該方法刻意不做狀態機檢查**，那是 order-management design D6 的設計）→ 發出 `OrderPaymentSucceeded` → **點數核發**。

結果：一張**同時是「已取消」與「已付款」**的訂單，庫存已經還回去了，顧客拿到了點數，而沒有任何東西噴錯。

**處置：**

- **付款紀錄仍然如實轉成 `succeeded`。** 錢真的動了。把它記成失敗或拒絕受理，就是為了讓狀態表好看而在財務紀錄上說謊——那與本 change 的目的（人工對帳）直接相反。
- **不推進訂單的 `payment_status`、不發 `OrderPaymentSucceeded`、不核發點數。** 已取消的訂單不會出貨，它沒有理由產生忠誠度回饋。
- **留下 WARN log**，並且——這是關鍵——**這個狀態是可查詢的事實**：`payments.status = 1(succeeded)` 而其 `orders.status = 1(cancelled)`。D10 的反查端點就是商家發現它的地方。
- **回呼仍回 `AckProcessed`（`1|OK`）**：我們確實處理完了，重送不會改變任何事。

**為什麼不新增一個 `payments.status` 值（例如「成功但訂單已取消」）：** `payments.status` 的列舉在 `api/openapi/openapi.yaml` 上是 `enum: [0,1,2,3]`，而 `3` 是保留給退款的。新增第五個值會讓所有對它做 switch 的消費端遇到未知值，而它表達的東西是**兩張表的關係**，不是這一列自己的狀態——那本來就該用 join 回答。

**明寫的殘留：** 修好之後，這種訂單需要**人工退款**（本 change 的 Non-Goal），這正是 D10 存在的理由。系統的責任到「讓商家一定找得到它」為止。

### D10: 反查端點——人工對帳缺的那一步

新增 `GET /api/v1/admin/shops/{shopID}/payments`，RBAC 守衛沿用既有的 **`payment.view`**（不新增權限節點：能看某張訂單的付款紀錄的人，看得到的 PII 只多不少，`member-directory` 已經定過這條界線；**沒有消費者的權限節點不新增**）。

- 選填 `q`：**精確比對**（trim 後）`provider_reference` 或 `provider_transaction_id`；`q` 是正整數時另外比對 `order_id`。
  - **為什麼是精確而不是 ILIKE 子字串**：對帳的動作是「把綠界後台的號碼貼過來」。子字串搜尋在這裡不增加任何能力，卻要付出無法走索引的代價，還會讓 `1` 這種輸入撈回半個資料表。訂單列表的 `q`（找人）與這裡的 `q`（找交易）是兩種不同的問題，答案不必一樣。
- 選填 `page` / `page_size`：不帶 `q` 時就是一份分頁的付款流水帳，依 id 遞減。與 `ListOrdersAdmin` 同一套 normalize（預設 20、上限 100）。
- 跨商家：`shop_id` 顯式條件。**無權存取該商家 → 403**（既有 cross-shop guard，在 handler 之前）；**有權但那筆交易屬於他店 → 空清單**（不是 404——這是列表端點，「查無資料」本來就是它的正常答案，而且說「沒有」比說「有但不給你」洩漏更少）。
- 回應沿用既有的 `Payment` schema，只新增 `provider_transaction_id` 與 `paid_at` 兩個鍵（additive）。**刻意不塞訂單狀態進去**：訂單的形狀在三個 change 裡被改過三次，第二份序列化一定會漂移（`member-directory` design D2 已經記過這個教訓）。商家拿到 `order_id` 後點過去看訂單。

### D11: 模擬收銀台是**獨立 binary**，不是 API 上的一條路由

`api/cmd/ecpay-sim`（獨立 `main`，預設 `:8081`）：

1. 服務 `POST /Cashier/AioCheckOut/V5`（**刻意用綠界真實的路徑**）。
2. 以設定的 HashKey/HashIV **獨立重算 `CheckMacValue`**，不符就顯示錯誤——**它是我們產生的簽章的第二個實作的驗證者**（雖然共用同一個 `ecpay` 套件的函式，但它是從「收方」的角度、對「實際送出的 form 位元組」重算）。
3. 顯示一個模擬收銀台頁面，**三顆按鈕正好對應 D6 的三種 Outcome**：付款成功（`RtnCode=1, SimulatePaid=0`）／付款失敗（`RtnCode=10100252 額度不足`）／**模擬付款（`RtnCode=1, SimulatePaid=1`）**。第三顆是刻意放的：它讓「`RtnCode` 說成功但錢沒進來」這件事變成一顆按得到的按鈕，而不是一段只有測試看得到的分支。
4. 按下後，以綠界的欄位形狀（`MerchantID`/`MerchantTradeNo`/`StoreID`/`RtnCode`/`RtnMsg`/`TradeNo`/`TradeAmt`/`PaymentDate`/`PaymentType`/`PaymentTypeChargeFee`/`TradeDate`/`SimulatePaid`/`CustomField1~4`/`CheckMacValue`）**form-urlencoded POST 到請求裡帶來的 `ReturnURL`**，並**檢查回應是不是恰好 `1|OK`**——回別的就在自己的頁面上顯示「綠界會重送」，把那條規則變成看得見的。
5. 再把瀏覽器導去 `ClientBackURL`。

**為什麼不掛在 API router 上（三個理由，第三個是決定性的）：**

1. **它會被鎖進 v1。** 檢查 ① 要求每一條註冊的路由都寫進 `openapi.yaml`，而 `v1-surface.txt` 收錄每一條文件裡的路由——**包含非 `/api/v1` 的 `GET /healthz`**（已查證）。也就是說，掛上去的那一刻，模擬收銀台就變成一條「刪掉即為破壞 v1」的承諾。而它**注定要被刪**（真的接上綠界那天）。把一個計畫中要刪的東西放進一個以「不准刪」為全部意義的機制裡，是在教下一個人忽略那個機制。
2. **設定相依的路由是地雷。** 若依 `ECPAY_SIMULATOR` 決定掛不掛，生產環境的路由表就與檢查 ⑥ 比對的那張表不同；若永遠掛上但在 handler 裡回 404，生產環境就長期帶著一條假路由。兩條都是為了測試方便而讓生產環境變複雜。
3. **獨立 process 才是真的模擬。** 綠界是**另一台伺服器**：它跨 HTTP 收下我們的 form、跨 HTTP 打回我們的 `ReturnURL`。同 process 的 handler 會少掉序列化、跨主機 URL、以及「`ReturnURL` 必須是對方連得到的絕對網址」這幾件真實會踩到的事。

**代價：** 人工 demo 要多起一個 process（`make ecpay-sim`）。**自動化測試不需要它**——Go 整合測試與 `e2e/commerce-path.mjs` 都**自己簽發回呼**，與今天 `SignMockWebhook` 的做法完全一致（`ecpay` 套件匯出 `SignCallback`，理由與 `SignMockWebhook` 逐字相同）。所以 `commerce-path.mjs` 仍然「只需要 API」，process 數量不變。

### D12: `paid_at` 值得一欄，`updated_at` 不夠

綠界的通知帶 `PaymentDate`（`yyyy/MM/dd HH:mm:ss`，台北時間）——**那是出現在綠界對帳單上的時間**，也是商家會拿來比對的那一個。

`payments.updated_at` 是「我們最後一次寫這一列的時間」。今天它碰巧接近付款時間，但**它的定義不是那個**：任何未來對這一列的寫入都會蓋掉它，而那一天不會有人記得它曾經被當成付款時間用。用一個定義不同、只是暫時碰巧相等的欄位去回答對帳問題，是那種**寫錯不會噴錯**的錯誤。

因此新增 `payments.paid_at`（timestamptz, NULL）：**provider 說錢是什麼時候動的**。`WebhookResult.PaidAt *time.Time`，provider 沒回報就是 NULL（`mock` 不回報，既有的每一列也都是 NULL——如實）。

**時區：** 綠界給的是台北時間且不帶時區標記，解析時明確套用 `UTC+8` 的固定時區（`time.FixedZone`，不依賴 tzdata），存成 timestamptz。**這是全系統唯一出現的時區，而且它是綠界的，不是商家的**——`order-extras` design D3 記過「系統不存 per-shop 時區」，這一條不推翻它：這個時間的定義來自綠界，不來自商家。

### D13: 預設 provider 維持 `mock`，由設定切換

新增 `PAYMENT_DEFAULT_PROVIDER`（預設 `mock`）。ECPay provider 一律註冊進 registry，但**不帶 `provider` 的請求行為與今天完全相同**。

理由是 v1 的相容性：`DefaultProvider` 決定 `POST /shop/orders/{id}/payments` 與 `POST /shop/guest/order/payments` 回應裡的 `provider`、`provider_reference` 與（新的）`redirect_form`。把預設換掉，等於在沒有任何人要求的情況下改變兩條既有端點的回應內容。真正要上線的部署設 `PAYMENT_DEFAULT_PROVIDER=ecpay` 即可；測試與 e2e 明確帶 `{"provider":"ecpay"}`。

**綠界憑證的預設值：** `ECPAY_MERCHANT_ID` / `ECPAY_HASH_KEY` / `ECPAY_HASH_IV` 預設為綠界**公開的 AIO 測試商店**值：`3002607` / `pwFHCqoQZGmho4w6` / `EkRm7iFT261dpevs`。它們印在綠界的公開文件上，不是秘密。

**這裡有一個查證糾正，值得記下來免得下一個人重踩：** 網路上流傳極廣的 `2000132` / `5294y06JbISpM5x9` / `v77hoKGq4kWxNNIS` 是**舊版**；而搜尋常命中的 `2000132` / `ejCk326UnaZWKisg` / `q9jcZX8Ib9LM8wYk` 是**B2C 電子發票 API** 的測試值，不是 AIO 的。用錯了的症狀是 `CheckMacValue` 一直對不上，而那個症狀跟「演算法寫錯」完全一樣——會浪費很長時間。

與 `PAYMENT_MOCK_WEBHOOK_SECRET` 一樣，**production 不強制**——因為預設 provider 不是 ecpay，沒設定它也不會有人用到。真的要在 production 用 ecpay 而沒換掉憑證，收不到錢會立刻發現；把它做成開機硬失敗會讓「只是想跑起來看看」的人卡住。

### D14: `WebhookRequest{Headers, Body}` 的 raw bytes 設計**撐住了**——記一筆前人做對的事

`provider.go` 的註解寫著：

> Body is deliberately raw bytes, not a pre-parsed struct: signature verification MUST run over the exact bytes the provider signed

當時的理由是 JSON 的欄位順序與空白。**ECPay 的情況不同但結論一樣，而且更強**：綠界的簽章欄位 `CheckMacValue` 就**在 body 裡面**，演算法要求把「其餘所有欄位」排序後重組——如果介面先把 body 解析成某個預設的 struct，就會發生兩件事：(a) 不在 struct 裡的欄位（綠界會隨方案增減欄位，例如 `SimulatePaid`、`CustomField1~4`）會被丟掉，而它們**必須參與簽章計算**，簽章直接算不對；(b) `Content-Type` 是 form-urlencoded 而不是 JSON，任何 JSON 預設都會直接壞掉。

**三個介面裡有兩個要改，這一個不用改一個字。** 記在這裡是因為「哪些抽象撐住了」與「哪些沒撐住」同等重要——下一個 provider 來的時候，這一條是可以直接依賴的。

### D15: 退款是人工流程——這不是偷懶，但也不是免費的

使用者已定案。查證後，把理由寫下來——**其中一條是決定性的，而且不是我原本以為的那條**：

- **綠界的測試環境完全不提供退款 API。** 官方文件原文：`CreditDetail/DoAction`「因無法提供實際授權，故無法使用此 API」。也就是說，就算現在做了退款，**它在上線前一秒都無法被測試過一次**——第一次執行就是拿真錢在真帳戶上跑。在一個 CI 不打外網的專案裡，這不是「先做起來再說」的候選。
- 綠界的退刷有一整套營運限制，不是一支 API 那麼簡單：`Action` 有 `C`（關帳）／`R`（退刷）／`E`（取消關帳）／`N`（放棄）四種，每日自動關帳期間 20:15~20:30 禁止呼叫，關閉自動關帳後須 21 天內手動關帳、90 天未關帳系統自動放棄，分期與紅利折抵交易**必須全額退刷**，而且**綠界帳戶餘額不足時退不了款**。做半套比不做更危險。
- 自動退款是**把錢送出去**的能力。它需要的授權模型（誰能按、要不要雙人覆核、額度上限）本系統一個都還沒有；掛在 `payment.view` 或現成任一節點上都是錯的。
- 保留值不動：`orders.payment_status` 的 `2/3` 與 `payments.status` 的 `3` 從 `payment-integration` 起就沒有任何程式路徑會產生，本 change **維持如此**。這也是 `tier-discount-application` design D1 那條教訓的正面應用——**沒有消費端時，不要把可設定項（或這裡：可寫入的狀態值）先交出去**。

**代價，明寫：** 商家在綠界後台退款後，本系統的 `payment_status` 仍是 `1`（已付款）。系統與現實會不一致，而系統**知道自己不知道**——因為它從來沒有聲稱過相反的事。要修這一點的正解是一個 `refunds` change（含 `payment.refund` 權限節點與後台的「登記一筆已在綠界完成的退款」入口），不是在這裡加一個沒人守的欄位。

### D16: `CheckMacValue` 的驗證基準是**綠界官方的範例向量**，不是「跟自己比對」

一個自己算、自己驗的簽章函式，只證明它跟自己一致。綠界文件提供了一組完整的輸入 → 輸出範例，這支測試因此是本 change 唯一能**單向**證明「我們算的跟綠界算的一樣」的東西：

```
HashKey = pwFHCqoQZGmho4w6   HashIV = EkRm7iFT261dpevs
ChoosePayment=ALL  EncryptType=1  ItemName=Apple iphone 15  MerchantID=3002607
MerchantTradeDate=2023/03/12 15:30:23  MerchantTradeNo=ecpay20230312153023
PaymentType=aio  ReturnURL=https://www.ecpay.com.tw/receive.php
TotalAmount=30000  TradeDesc=促銷方案
⇒ 6C51C9E6888DE861FD62FB1DD17029FC742634498FD813DC43D4243B5685B840
```

它同時涵蓋了四個容易寫錯的點：中文（UTF-8 百分比編碼後轉小寫）、空白（編成 `+` 不是 `%20`）、URL 裡的 `:` 與 `/`（要編）、以及日期裡的 `/`。

**Go 特有的三個地雷（實測 `url.QueryEscape`，PHP 的官方 SDK 不會遇到）：**

1. **`~` 綠界要求編成 `%7e`，而 Go 不編碼它。** 這是唯一一個「.NET 編碼比標準編碼**更**積極」的字元——所有討論這個演算法的文章都在講「要把 `%2d` 還原成 `-`」那個方向，反方向沒人提。必須手動補上。
2. `!` `*` `(` `)` Go 會編成 `%21 %2A %28 %29`，綠界要求還原成原字元。
3. 步驟順序必須是 **encode → 轉小寫 → 替換**（官方 SDK 的順序）。先替換再轉小寫會把 `%7E` 一起轉成 `%7e`，那個結果碰巧對；但先轉小寫再 encode 會把整個值弄壞。順序寫進註解。

替換表以綠界官方的「URLEncode 轉換表」為準，逐項寫成 Go 的 `strings.NewReplacer` 並在註解附上來源。

### D17: 契約檢查會沉默的一個角落，先寫下來

`redirect_form.fields` 在契約裡是 `additionalProperties: {type: string}`（form 欄位天生全是字串），**沒有逐欄位的 `properties`**。因此檢查 ②（金額欄位必須 `$ref` `MinorUnitAmount`）走不到 `TotalAmount`——它會沉默。

這是刻意的：把綠界的十餘個欄位逐一寫進契約的 `properties`，等於把綠界的 API 版本鎖進我們的 v1（綠界加一個欄位，我們的 `additionalProperties: false` 就會讓契約檢查轉紅）。代價是那一個金額欄位的單位紀律沒有機械保護，補償是：`fields` 的 `description` 明寫 `TotalAmount` 等於訂單 `total_amount`、單位相同、**無換算**，並且 Go 這一側有一支單元測試直接斷言 `fields["TotalAmount"] == strconv(order.total_amount)`。**機械保護的位置從契約移到了測試，而不是消失。**

## Risks / Trade-offs

- **[模擬模式跑綠 ≠ 真接得上綠界]** → 本 change 能證明的是「我們產生的欄位集與簽章，符合綠界公開文件的演算法」（D16 的官方向量）與「綠界形狀的回呼，我們讀得懂並正確地推進了狀態」。**它不能證明綠界真的會接受**：帳號開通狀態、`ReturnURL` **只支援 80/443 port**且必須公網可達、不支援中文網址、`ChoosePayment` 的實際可用值域、`TotalAmount` 的下限（測試站 1 元只開 ATM 會回 `5100070`）。緩解：所有值都可注入，切到綠界測試站是一個環境變數。**上線前必須有一次真站的手動驗證，這件事本 change 不宣稱做完。**
- **[綠界文件自身有矛盾處]** → 已知三處：`StoreID` 在建單表寫 `String(10)`、在回呼表寫 `String(20)`（我們送出時取保守的 10）；`2862` 與 `2878` 頁尾的 YAML 區塊與中文正文不一致（`ChoosePayment` 少列值、`CheckMacValue` 的 example 還是 MD5 長度、content-type 寫成 `html/text`）——**一律以中文正文為準**，並在程式註解標明來源頁。
- **[`redirect_url` 對 ECPay 是空字串]** → 舊消費端若只讀它會壞。緩解：契約與 `docs/storefront-integration.md` 明寫規則；`redirect_form` 為 additive 新鍵；**預設 provider 維持 `mock`**，所以沒有任何既有部署會在沒改設定的情況下遇到它（D13）。
- **[`Provider` 介面新增方法 = 所有實作都要改]** → 今天只有 `MockProvider` 一個實作，成本是 5 行。這正是**現在**改介面而不是等第三家 provider 的理由。
- **[`OutcomePending` 讓 `HandleWebhook` 多一條分支]** → 分支本身是「什麼都不做然後回目前狀態」，並有專門的整合測試（兩通回呼的完整序列：`SimulatePaid=1` → 訂單**仍未付款**且**沒有點數** → 真實成功回呼 → 訂單已付款）。第二通能成功，正是因為第一通沒有把那一列變成終局。
- **[已取消訂單的成功付款仍需要人工退款]** → 系統只負責讓它**一定被找到**（D9/D10）。緩解：WARN log ＋ 反查端點 ＋ 整合測試。**明寫的殘留風險：今天沒有主動通知**（沒有 email、沒有後台紅點），商家必須自己去查。這是 `order-notification-email` 的事。
- **[綠界的欄位會隨時間變]** → 我們只送必填欄位 ＋ 少數明確有用的選填欄位，並且**回呼側不做 `additionalProperties: false` 式的嚴格解析**（未知欄位一律忽略，但**全部參與簽章計算**——這是 D14 的直接推論）。綠界新增欄位不會讓我們壞掉。
- **[多租戶：所有商家共用一組 `ECPAY_*` 憑證]** → 本 change 的設定是**平台層**的，不是 per-shop。這對 ONDO（單一商家）成立，對多商家 SaaS **不成立**：兩家商家的錢會進同一個綠界帳戶。緩解：`MerchantTradeNo` 已經跨商家唯一（D5），所以資料層面沒有衝突；**明寫這是一個已知的、必須在第二個商家上線前解決的限制**，解法是 `shops` 長出一組加密的 provider 憑證欄位 ＋ `Provider` 從 `InitiateRequest.ShopID` 解析憑證。刻意不現在做：沒有第二個商家，而把憑證存進資料庫需要一套本系統還沒有的加密與輪替機制。

## Migration Plan

1. `payments` 新增兩個 NULL 欄位（`provider_transaction_id`、`paid_at`）。**沒有回填、沒有 CHECK 變更、沒有索引變更**——既有每一列的所有欄位值一個位元都不動。
2. `atlas` 以名稱比對 CHECK：本次**沒有**動任何 CHECK 運算式，所以產生器的沉默不構成風險；migration 由 `make migrate-gen` 產生後，仍需按既有慣例手動移除 atlas 誤判要 DROP 的手寫約束（`members` 的 `UNIQUE(shop_id,email/phone)` 等）。
3. 設定：新增的 8 個環境變數全部有預設值。**一個都不設定時，選到的 provider 仍是 `mock`，付款流程與本 change 之前完全相同**——包括 webhook 的 JSON envelope 回應（`WebhookAck` 的零值約定，D3）。**唯一的差異是回應多了幾個鍵**（`provider_transaction_id`／`paid_at`／`redirect_form`，前兩者為 `null`、第三者為 `null`），那是 additive 且被 v1 允許的。刻意不寫「逐位元組相同」：那句話在這裡是錯的。
4. 回滾：`.down.sql` 直接 DROP 兩欄。**會遺失已收到的 `TradeNo` 與 `paid_at`**——若已有 ECPay 交易，回滾前先匯出 `payments`。

## Open Questions

- **綠界的 `PaymentType` 在回呼中的實際值域**（`Credit_CreditCard`、`ATM_TAISHIN`…）目前不解析、不分支。等真站驗證後再決定要不要落成欄位。
- **`MerchantTradeNo` 唯一性的確切範圍**綠界文件未界定（見 D5）。本設計取最保守解讀，真站驗證時應確認測試站與正式站是否共用命名空間。
- **`ReturnURL` 只支援 80/443 port**，自架環境需要反向代理或通道；本 change 只把它做成設定值，不提供解法。
- **per-shop 憑證**（見 Risks 最後一條）——第二個商家上線前必須回答。
- **`OrderResultURL`（前端 POST 回付款結果）本 change 不使用。** 綠界明文它與 `ReturnURL` **沒有固定先後順序**、且**不可設為相同位置**。要用它得先想清楚「顧客的瀏覽器先回來、但背景通知還沒到」時前端該顯示什麼——那是一個 UX 決定，不是一個欄位。目前的答案是 `ClientBackURL` ＋ 前端自己重查訂單，與既有 `redirect_url` 的文件說法一致。
