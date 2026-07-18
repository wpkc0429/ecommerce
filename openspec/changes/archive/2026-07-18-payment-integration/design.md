## Context

order-management（已歸檔）建立了訂單的三軸狀態模型，`payment_status` 欄位刻意只留一個寬鬆的 `CHECK (payment_status >= 0)` 與唯一初始值 `0`（未付款），並提供受控入口 `order.Service.UpdatePaymentStatus(ctx, shopID, orderID, status int16) (*ent.Order, error)`（做租戶範圍檢查、存在性檢查、非負值檢查，直接覆寫欄位，不做狀態機檢查）給未來的金流能力使用。本變更是這條軸線的第一個真正寫入者。

本專案目前沒有任何真實金流商的 API 憑證，CI 也不打真實外部網路，因此本變更的產出是一個**可驗證的參考架構**：provider 抽象介面 + 一個 CI 內可完整測試的 mock provider + 交易紀錄 + webhook 確認流程，讓未來新增 Stripe/綠界/藍新等 provider 時，只需實作 `payment.Provider` 介面、註冊進 provider registry，呼叫端（handler、webhook 路由、idempotency 邏輯）完全不用改。

## Goals / Non-Goals

**Goals:**
- `payment.Provider` 抽象介面：發起付款、驗證 webhook 簽章並解析結果。
- Mock provider：可在 CI 完整測試，簽章驗證是「真的」HMAC-SHA256 比對，不是永遠通過的假驗證。
- `payments` 交易紀錄表：shop-scoped tenant-owned，一個訂單可有多筆付款嘗試（重試情境）。
- 定案 `orders.payment_status` 與 `payments.status` 完整列舉值。
- 會員自助發起付款、webhook 確認、商家後台查詢付款紀錄三個 API。
- Webhook 冪等：同一 `provider_reference` 的重複投遞不可重複更新訂單狀態或造成資料不一致。
- 嚴格遵守 order-management 交接的邊界：`orders.payment_status` 只能透過 `order.Service.UpdatePaymentStatus` 寫入。

**Non-Goals:**
- 不串接任何真實金流商（沒有憑證、CI 不打外網）。
- 不支援部分金額付款（partial capture）——本變更的付款金額固定等於 `order.total_amount`；DB 層的 `amount <= order.total_amount` 不做跨表 CHECK（Postgres 不支援），改由 service 層保證（本變更永遠設 `amount = order.total_amount`，未來若要支援部分付款，該不變量的檢查點就在這裡）。
- 不做退款流程的實際邏輯——`payment_status`/`payments.status` 的列舉值把「已退款/部分退款」的位置定案下來（見 Decisions D4/D5），但本變更沒有任何程式路徑會產生這兩個值；留給未來的退款變更實作。
- 不做「同一訂單已有 pending 付款時擋下新的付款嘗試」的去重——demo 範圍精簡，多筆並存的 pending 付款不視為錯誤（見 Risks）。
- 不做「商家手動標記已付款」端點（`payment.mark_paid`）——見 D8，評估後判斷這會稀釋本變更的核心（provider 抽象、簽章驗證、idempotency），留給未來變更。

## Decisions

### D1: Provider 抽象介面形狀

```go
package payment

type Outcome int
const (
    OutcomeSucceeded Outcome = iota
    OutcomeFailed
)

type InitiateRequest struct {
    ShopID   int
    OrderID  int
    Amount   int64  // minor units
    Currency string // ISO 4217
}

type InitiateResult struct {
    ProviderReference string // provider-side transaction id; MUST be usable as an idempotent webhook lookup key
    RedirectURL       string // where to send the member to complete payment
}

type WebhookRequest struct {
    Headers http.Header // raw header access — signature schemes vary per provider (header name, encoding)
    Body    []byte       // raw body bytes, NOT pre-parsed — HMAC verification must run over the exact bytes the provider signed
}

type WebhookResult struct {
    ProviderReference string
    Outcome           Outcome
}

type Provider interface {
    Name() string
    InitiatePayment(ctx context.Context, req InitiateRequest) (*InitiateResult, error)
    VerifyWebhook(ctx context.Context, req WebhookRequest) (*WebhookResult, error)
}
```

`WebhookRequest` 帶原始 `http.Header` 與原始 body bytes（不是先解析成 struct 再簽章）——這是業界慣例（Stripe/GitHub webhook 驗簽都要求對「收到的原始 bytes」算 HMAC，先 decode 再 re-encode 可能因欄位順序/空白差異導致簽章不符）。介面刻意不知道任何 mock/HMAC 細節，換成真實 provider 時完全不用碰這個介面或呼叫端。

### D2: Mock provider——真實 HMAC-SHA256 驗簽，不是假驗證

`internal/payment/mockprovider.go` 的 `MockProvider{secret string}`：
- `InitiatePayment`：產生 `provider_reference = "mock_" + uuid.NewString()`（用 `github.com/google/uuid`，已是間接依賴，本變更改列為直接依賴），`RedirectURL` 是一個不會真的被打的假網址（`https://mock-payments.test/pay/<reference>`）。
- `VerifyWebhook`：從 `X-Payment-Signature` header 讀 `hex(HMAC-SHA256(secret, body))`，用 `hmac.Equal`（非 `==`，避免 timing attack）比對；不符或缺少 header 回 `ErrInvalidSignature`。簽章通過後才 `json.Unmarshal(body)` 成 `{provider_reference, status}`，`status` 只接受 `"succeeded"`/`"failed"`，其餘值視為格式錯誤。
- 匯出 `SignMockWebhook(secret string, body []byte) string`：產生合法簽章字串，供整合測試組出「provider 端會送來的合法 webhook 請求」，不用在測試裡重複實作 HMAC。

這樣設計的理由：未來換成 Stripe 等真實 provider 時，只要新增一個實作同一 `Provider` 介面、驗證邏輯換成該 provider SDK 的檔案，webhook handler 與 idempotency 程式碼一行都不用改。

### D3: `payments` 資料表——與 `OrderItem` 的刻意差異

比照 `Order`/`OrderItem` 的 tenant-owned 慣例（直接 `shop_id` 欄位、註冊進 `tenant.tenantOwned`、`TimeMixin`）。欄位：`shop_id`、`order_id`、`provider`（varchar(32)）、`provider_reference`（varchar(191)）、`amount`（int64, minor units）、`currency`（char(3)）、`status`（int16, D5）。

**與 `OrderItem.product_id`/`sku_id` 的刻意差異**：`OrderItem` 把 `product_id`/`sku_id` 設計成「無 edge、無 FK 的純資訊欄位」，因為訂單品項是不可變的歷史快照，即使原商品被刪除也必須完整顯示。`Payment` 不是快照——它的存在意義就是「這筆錢對應到哪張訂單」，訂單目前沒有刪除端點，讓 `order_id` 保留真正的 FK（`edge.To("order", Order.Type).Unique().Required().Field("order_id")`，單向宣告在 `payment.go`，不用改動既有的 `order.go` schema 檔案）更能保護稽核軌跡的完整性；不加 `OnDelete` annotation，維持 Postgres 預設的 `NO ACTION`（訂單存在付款記錄時不可被刪除）——這比 `OrderItem` 的 `Cascade`更保守，是刻意的選擇：付款記錄是財務稽核資料，寧可擋刪除也不要被連坐刪掉。

**Idempotency 索引**：`(provider, provider_reference)` 唯一索引——webhook 沒有租戶 context，只能靠這組全域唯一鍵定位付款記錄（D7 說明為何這樣做仍保持租戶隔離安全）。`provider_reference` 由我方（或 mock provider）產生，保證全域唯一。

**不對 `order_id` 加唯一約束**——一個訂單可有多筆付款嘗試（第一次失敗、第二次重試成功）。

DB 層 CHECK：`amount > 0`、`status IN (0,1,2,3)`。`amount <= order.total_amount` 與「currency 必須等於 order.currency」不做資料庫層 CHECK（跨表，Postgres CHECK 做不到，需要 trigger——本變更不引入 trigger），改在 `payment.Service.InitiatePayment` 用 order 快照的金額/幣別建立 Payment（見 Goals/Non-Goals：本變更只支援全額付款，`amount` 恆等於 `order.total_amount`）。

### D4: `orders.payment_status` 完整列舉值定案

| 值 | 意義 | 本變更是否有程式路徑寫入 |
|---|---|---|
| 0 | 未付款（unpaid）| 是（order-management 既有初始值，本變更不變） |
| 1 | 已付款（paid）| 是（webhook 確認成功時，經 `order.Service.UpdatePaymentStatus`）|
| 2 | 已退款（refunded）| 否，保留給未來退款變更 |
| 3 | 部分退款（partially_refunded）| 否，保留給未來退款變更 |

常數定義在 `internal/payment/service.go`（`payment` package 是這條軸線這次的擁有者，比照 order-management 把 `StatusCreated`/`StatusCancelled` 定義在 `order` package 的模式）：`order.PaymentStatusUnpaid`（既有）沿用，新增 `payment.OrderPaymentStatusPaid/Refunded/PartiallyRefunded int16` 常數。

### D5: `payments.status` 列舉值定案

`0` pending（建立付款嘗試時）、`1` succeeded（webhook 確認成功）、`2` failed（webhook 確認失敗，訂單 `payment_status` 不受影響，允許重試）、`3` refunded（保留，本變更無程式路徑寫入，理由同 D4）。

### D6: Webhook idempotency 機制

核心保證：條件式 UPDATE（CAS）作為第一道防線，加上「後續重複投遞時安全地重新宣告訂單付款狀態」的防禦性補強，兩者合起來同時處理「同一 webhook 重送」與「CAS 贏家呼叫 `order.Service.UpdatePaymentStatus` 失敗後重試」兩種情境：

1. 依 `(provider, provider_reference)` 查找 payment（找不到 → 視為未知交易，回應但不作任何寫入，見下）。
2. `UPDATE payments SET status = <target> WHERE id = ? AND status = 0 /* pending */`，用受影響列數判斷這次呼叫是否是「贏家」（把 pending 轉成終態的那一個）。
   - 受影響 = 1（贏家）：若 `target = succeeded`，呼叫 `order.Service.UpdatePaymentStatus(ctx, payment.ShopID, payment.OrderID, OrderPaymentStatusPaid)`；若 `target = failed`，不呼叫（訂單 `payment_status` 維持未付款，允許會員重新發起付款）。
   - 受影響 = 0（已經是終態，重送或競態）：重新查一次目前的 `payment.status`。只有在「目前狀態剛好等於這次要宣告的 `succeeded`」時，才**再次**呼叫 `order.Service.UpdatePaymentStatus`（安全，因為它是純覆寫、非累加——重覆設成同一個值沒有副作用）；若目前狀態是 `failed`（代表這是一個「先失敗、現在卻聲稱成功」的衝突回呼）或這次宣告是 `failed`，一律不碰訂單，安全地 no-op。
3. 找不到對應 payment：回應視為「我們不認識這筆交易」（記錄但不視為安全漏洞——簽章已經驗證過，代表呼叫者持有正確密鑰；只是這個 reference 從未被我方 `InitiatePayment` 建立過，理論上不該發生）。

第 2 步「受影響=0 時仍重新宣告 succeeded」這個補強，關掉了一個真實的一致性漏洞：如果贏家那次呼叫在 payment 已標成功、但呼叫 `order.Service.UpdatePaymentStatus` 卻失敗（例如 DB 瞬斷）就回 500，provider 依慣例會重送 webhook；沒有這個補強的話，重送時 CAS 會直接因為 `status` 已非 `pending` 而判定「已處理過」，永遠不會再嘗試把訂單標成已付款——訂單就會卡在「金流商認為已收款，我方訂單卻永遠未付款」。

測試會證明：同一個成功 webhook 打兩次，訂單只被標記已付款一次（呼叫 `UpdatePaymentStatus` 的次數不是重點，重點是最終狀態一致且無錯誤——同值覆寫本來就無害）。

### D7: Webhook 路由為何不掛 TenantMW，以及如何在缺少 ambient shop context 下仍保持租戶隔離

`POST /api/v1/webhooks/payments/{provider}` 直接掛在 `/api/v1` 下（不在 `/shop` 或 `/admin` 底下）：
- 呼叫方是金流商的伺服器，不是瀏覽器上的 member/admin session，沒有辦法帶會員 JWT，也沒有辦法用 `X-Site-Domain` 走既有的租戶網域解析（金流商回呼的 URL 是我方在 `InitiatePayment` 時登記給它的固定 webhook endpoint，不會知道、也不需要知道商家網域）。
- 身分驗證機制换成簽章（D2/D6），本質上與 JWT/RBAC 是平行的驗證模型，硬塞進 `TenantMW`/`MemberMW`/`AdminMW` 任一個都不合語意。

租戶隔離仍然成立，理由：
- 查找鍵 `(provider, provider_reference)` 的 `provider_reference` 是我方在 `InitiatePayment`（一個已通過會員 JWT + 租戶解析的請求）當下產生的高熵值，只有「知道這個值」才查得到對應 payment；不存在「猜 ID 就能查到別人訂單」的風險（這與 order/cart 用遞增整數 ID 但用 JWT/RBAC 擋的模型不同——webhook 沒有身分可擋，換成用「必須持有正確的 provider 密鑰才能產生合法簽章」+「必須知道我方核發的高熵 reference」兩層作為等效的存取控制）。
- 找到 payment 後，`shop_id`/`order_id` 直接來自該筆已知合法的 payment row 本身，不是呼叫方提供的參數——呼叫方沒有任何管道指定「幫我改別的 shop/訂單的付款狀態」。
- 後續寫入一律用明確傳入的 `shopID`/`orderID`（來自查到的 payment row）呼叫 `order.Service.UpdatePaymentStatus(ctx, shopID, orderID, ...)`——該方法自己做 `shop_id` 範圍檢查與存在性檢查，即使 ctx 沒有租戶資訊也不會誤傷其他商家的訂單。
- payment 本身的 CAS UPDATE 用主鍵 `id` 定位（已經是查找出的單一列），不依賴 tenant hook 的自動 `shop_id` 過濾。

### D8: 不做「商家手動標記已付款」端點

評估後決定本次不做。理由：(1) 範圍紀律——本變更的核心承諾是「provider 抽象 + 簽章驗證骨架 + idempotency」，手動標記付款是一個獨立的商業情境（銀行轉帳等線下付款），加進來會讓 review 的注意力分散，且不影響核心可驗證性；(2) 手動標記付款一旦做錯（例如沒有防止對已付款訂單重複標記、或沒有搭配退款流程）容易產生比它解決的問題更大的資料一致性風險，值得獨立一個變更完整設計（例如它可能也想寫進 `payments` 表一筆 `provider = "manual"` 的記錄，需要重新想一次 provider 抽象要不要涵蓋「無 provider」情境）。`payment.mark_paid` 權限節點名稱保留給那個未來變更。

### D9: Config / wiring

新增 `PAYMENT_MOCK_WEBHOOK_SECRET`（`config.Config.PaymentMockWebhookSecret`），比照 `ADMIN_JWT_SECRET`/`MEMBER_JWT_SECRET` 的 dev fallback 慣例（開發環境給預設值 `dev-only-mock-payment-webhook-secret`，`.env.example` 註記）。**不**比照它們在 `production` env 強制非空——mock provider 定位是示範/測試用途，不預期在正式環境流量上被使用；真的接上真實 provider 時，那個 provider 自己的密鑰配置與生產環境校驗規則屬於那次變更的範圍。

`app/wire.go` 組裝：
```go
mockProvider := payment.NewMockProvider(cfg.PaymentMockWebhookSecret)
providers := map[string]payment.Provider{mockProvider.Name(): mockProvider}
paymentService := &payment.Service{
    Client: client, Orders: orderService,
    Providers: providers, DefaultProvider: mockProvider.Name(),
}
deps.Payments = &httpapi.PaymentHandler{Client: client, Service: paymentService, Authz: authz, Log: a.log}
```

### D10: 錯誤處理慣例

`payment` package 比照 `order`/`cart`/`catalog`：`ValidationError`（422，請求本身有問題，例如未知 provider 名稱）、`ConflictError`（409，訂單狀態不允許這個操作——已取消或已付款）、`ErrNotFound`（404）。`writePaymentError` 比照 `writeOrderError` 的 switch 寫法。

## Risks / Trade-offs

- **[CAS 贏家呼叫 `order.Service.UpdatePaymentStatus` 之後、回應 provider 之前若崩潰]** → provider 端因未收到 2xx 會重送，D6 的「受影響=0 時仍重新宣告 succeeded」補強讓重送能自我修復；崩潰後、provider 又剛好不重送的極端情況會留下「payment=succeeded 但 order 仍未付款」的不一致——這是示範性參考實作接受的已知風險，正式串接真實 provider 時應該加上背景對帳（reconciliation）機制，不在本變更範圍。
- **[同一訂單可同時存在多筆 `pending` 付款記錄]** → 不視為錯誤（Non-Goals），代價是商家後台會看到多筆 pending 記錄；不影響訂單狀態正確性（只有 `succeeded` 才會推進 `payment_status`）。
- **[`amount <= order.total_amount`、`currency = order.currency` 不是 DB 層約束]** → 由 `payment.Service.InitiatePayment` 保證（本變更固定全額付款，不接受呼叫端指定金額），未來若開放部分付款需要在該處補上顯式檢查與相應測試。
- **[Mock provider 的 `RedirectURL` 是假網址，不是真的可導向頁面]** → 對這個變更的目的（驗證 provider 抽象與 webhook 骨架）而言足夠；串接真實 provider 時自然會是真實導向網址。

## Migration Plan

新增 `payments` 表為純新增（無既有資料遷移），走既有 `make migrate-gen name=add_payment_integration` 流程從 ent schema diff 產生 up/down SQL。Rollback：`down` migration drop `payments` 表；`payment.*` permission 節點與角色授權是 seed 資料，重跑 `make seed` 冪等更新，回滾只需回退程式碼版本（seed 不會主動刪除已授權的 permission，符合既有 seed 慣例）。
