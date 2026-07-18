## Why

訂單建立後目前沒有任何方式把 `payment_status` 從「未付款」推進——order-management 刻意把這條軸線留空（`CHECK (payment_status >= 0)`），交給本次變更定案。商家需要一個金流串接的抽象層：現在還沒有真實金流商（Stripe/綠界/藍新等）的 API 憑證，也不該在 CI 對外打真實網路，但架構必須現在就做對，讓未來換上真實 provider 時只需新增一個實作，不必改任何呼叫端程式碼（handler、webhook 路由、idempotency 邏輯全部不變）。

## What Changes

- 新增 `PaymentProvider` 抽象介面（`api/internal/payment`）：`InitiatePayment`（回傳 provider 端參照與導向資訊）與 `VerifyWebhook`（驗證簽章並解析付款結果）。
- 新增 mock provider：可設定成功/失敗結果供測試控制情境；**真的**實作 HMAC-SHA256 簽章簽發與驗證（不是永遠通過的假驗證），讓 webhook 端點的簽章驗證骨架對未來真實 provider 有效。
- 新增 `payments` 資料表（ent schema）：shop-scoped tenant-owned，記錄每一次付款嘗試（`order_id`、`provider`、`provider_reference`、`amount`/`currency`、`status`、時間戳）。一個訂單可有多筆付款記錄（重試情境），`order_id` 不加唯一約束。
- 定案 `orders.payment_status` 完整列舉值（0=未付款、1=已付款、2=已退款、3=部分退款——見 design.md），以及新表 `payments.status` 的列舉值（pending/succeeded/failed/refunded）。
- 新增會員自助 API：`POST /api/v1/shop/orders/{id}/payments`（對自己的訂單發起付款）。
- 新增 webhook API：`POST /api/v1/webhooks/payments/{provider}`（provider 回呼，簽章驗證取代 JWT 身分驗證，冪等處理重複投遞）。
- 新增商家後台 API：`GET /api/v1/admin/shops/{shopID}/orders/{id}/payments`（查看付款紀錄，RBAC `payment.view`）。
- RBAC permission catalog 新增 `payment.*` 節點並加進既有角色定義（`merchant_owner`、`editor`）。
- `api/internal/tenant/enttenancy.go` 的 `tenantOwned` 註冊 `Payment`。

**不做**（評估後縮小範圍，理由見 design.md）：商家手動標記已付款端點（`payment.mark_paid`）——本階段線下付款情境可留給未來變更，避免這次的核心（provider 抽象 + 簽章驗證 + idempotency）被稀釋。

## Capabilities

### New Capabilities
- `payment-integration`: 金流 provider 抽象介面、mock provider、付款交易紀錄資料模型、會員發起付款/商家查詢付款紀錄 API、webhook 簽章驗證與冪等確認流程。

### Modified Capabilities
（無——`payment_status` 的更新沿用 order-management 已提供的 `order.Service.UpdatePaymentStatus` 服務層方法，不改變其既有 requirements。）

## Impact

- 新增 Go package `api/internal/payment`（provider 介面、mock provider、Service）。
- 新增 ent schema `Payment`（新表 `payments`）與對應 migration。
- 新增 `api/internal/httpapi/payment.go`（handler），修改 `router.go`（新增會員路由、admin 路由、webhook 路由群組）。
- 修改 `api/internal/tenant/enttenancy.go`（註冊 `Payment` 為 tenant-owned）。
- 修改 `api/internal/seed/seed.go`（新增 `payment.*` permission catalog 與角色授權)。
- 修改 `api/internal/config/config.go` 與 `api/internal/app/wire.go`（新增 webhook 簽章密鑰設定、組裝 payment.Service）。
- 修改 `.env.example`（新增對應環境變數，含測試預設值說明）。
- 依賴既有 `order.Service`（`GetOrder`/`GetOrderAdmin`/`UpdatePaymentStatus`），不修改其程式碼。
