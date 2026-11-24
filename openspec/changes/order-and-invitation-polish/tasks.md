## 1. 出貨守衛（design D1）

- [x] 1.1 `api/internal/shipping/service.go`：`CreateShipment` 在鎖訂單後檢查已付款，否則 `ConflictError`（`isPaidForShipping`）
- [x] 1.2 `shipping/service_integration_test.go`：fixture 預設已付款；新增未付款／已退款／部分退款 409 且無副作用、付款後成功
- [x] 1.3 `httpapi` 整合測試與既有 fixture 對未付款訂單出貨者改為先付款，並新增 HTTP 層 409 斷言

## 2. 邀請清單（design D2）

- [x] 2.1 `account.ListInvitations` 含已過期並帶 `Status`；`httpapi/team.go` 輸出 `status`
- [x] 2.2 整合測試：待接受與已過期並列、已過期可撤回後消失、他店／已撤回仍 404、清單無 token
- [x] 2.3 `admin/views/team.js`：已過期顯示「（已過期）」

## 3. paid_at（design D3）

- [x] 3.1 `payment.Service.HandleWebhook`：成功但 `paid_at` 為 NULL 時以條件式 UPDATE 補上處理時間
- [x] 3.2 測試：mock 成功有 `paid_at`、失敗／待處理沒有、重送補歷史缺值且不覆蓋；綠界有／無 `PaymentDate` 兩條 HTTP 層測試

## 4. 訂單品項選項（design D4）

- [x] 4.1 ent schema `OrderItem.options`（jsonb，可為 NULL）；`GOTOOLCHAIN=go1.26.4 go generate ./internal/ent`
- [x] 4.2 migration `add_order_item_options`：ADD COLUMN ＋ 兩段冪等回填；移除 atlas 夾帶的假差異並重算 `atlas.sum`；dev DB 上 up→down→up
- [x] 4.3 `order.Service.Checkout` 快照；`orderItemJSON` 輸出 `options`
- [x] 4.4 整合測試：快照、SKU 修改／刪除後不變、無選項為 `{}`、舊列為 null、回填 SQL（從 migration 檔讀出）

## 5. API 契約

- [x] 5.1 `openapi.yaml`：`createShipment` 409 清單、`Invitation.status`、`listShopInvitations`／`revokeShopInvitation` 敘述、`OrderItem.options`、`Payment.paid_at` 敘述
- [x] 5.2 `v1-surface.txt` 只新增 `Invitation.status`、`OrderItem.options`

## 6. 文件與 E2E

- [x] 6.1 `docs/admin-frontend-integration.md`（出貨 409、邀請 `status`、`paid_at`、`options`）、`docs/storefront-integration.md`（品項 `options`）、`known-gaps.md` 移除兩項缺口、`CLAUDE.md`
- [x] 6.2 `e2e/commerce-path.mjs`、`e2e/happy-path.mjs`：出貨前先付款；新增未付款出貨 409、`options`、`paid_at`、過期邀請的斷言

## 7. 驗收

- [x] 7.1 `go build`／`go vet`／`go test ./...`、`INTEGRATION=1 go test -p 1 ./...`
- [x] 7.2 在獨立資料庫 `ecommerce_e2e` 跑 happy-path，之後清理
