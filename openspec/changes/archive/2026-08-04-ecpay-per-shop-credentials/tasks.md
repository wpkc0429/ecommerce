## 1. 加密與資料模型（design D3／D4／D5）

- [x] 1.1 新增 `internal/cryptbox`（或等同小套件）：`Encrypt`/`Decrypt`，AES-256-GCM，nonce 與密文串接；金鑰來自 `PAYMENT_CREDENTIALS_ENCRYPTION_KEY`（hex 或 base64 解碼後須恰為 32 bytes，長度或編碼錯誤時回錯誤而非 panic）
- [x] 1.2 `internal/config/config.go`：新增 `PaymentCredentialsEncryptionKey` 讀取與啟動時驗證（長度/解碼失敗即 `Load` 回錯誤，讓 `cmd/server`/`cmd/migrate` 等入口據此拒絕啟動）；提供開發用預設值並在 `.env.example` 註明「正式環境必須覆蓋」
- [x] 1.3 `internal/ent/schema/payment.go`（或新檔）：新增 `ShopPaymentProviderCredential`——`shop_id`（int）、`provider`（varchar）、`merchant_id`（varchar，明文）、`hash_key_ciphertext`（bytea）、`hash_iv_ciphertext`（bytea）、`choose_payment`（varchar，nullable）、`TimeMixin`；`UNIQUE(shop_id, provider)`
- [x] 1.4 `cd api && go generate ./internal/ent`
- [x] 1.5 ~~`make migrate-gen name=add_shop_payment_provider_credentials`~~ ——本環境無 Docker/dev DB 可跑 atlas diff，改為手寫 `20260803150000_add_shop_payment_provider_credentials.{up,down}.sql`（欄位/型別/FK/索引名逐一核對 `internal/ent/migrate/schema.go` 的產生結果，非憑空手刻）；`atlas.sum` 用 `ariga.io/atlas/sql/migrate` 的 `NewHashFile`/`WriteSumFile` 實際算過，非手填——已驗證舊有 17 行雜湊逐位元組不變，只新增第 18 行。有 dev DB 後建議照跑一次 `make migrate-gen` 驗證產生的 SQL 與這裡手寫的一致。
- [x] 1.6 `make migrate` 套用並確認成功——已在使用者本機 Docker 執行；額外驗證 up→down→up 全部乾淨（down 正確移除表，欄位/型別/FK/索引與 `\d` 輸出逐項比對 ent 產生的 schema 一致）
- [x] 1.7 `internal/tenant`：把新表登記進 `tenantOwned`（`enttenancy_internal_test.go` 會要求，這是既有機制的一部分，不是新測試）

## 2. Provider 介面擴充（design D1／D2）

- [x] 2.1 `internal/payment/provider.go`：新增可選介面 `ShopScopedProvider { ResolveForShop(ctx, shopID int) (Provider, error) }`，附設計理由註解（延續 `ecpay-payment-provider` design D1「不得對 provider 名稱特判」的界線）
- [x] 2.2 `internal/payment/ecpay/ecpay.go`：`Config` 移除 `MerchantID`/`HashKey`/`HashIV`/`ChoosePayment` 作為固定欄位（改由 `ResolveForShop` 動態組出一份臨時 `Config` 再 `ecpay.New`）；保留 `CheckoutURL`/`ClientBackURL`/`ReturnURL`（作為 base）為平台層設定
- [x] 2.3 `internal/payment/ecpay/ecpay.go`：新增一個「憑證解析器」型別，實作 `ShopScopedProvider`——查 `ShopPaymentProviderCredential`、解密 `HashKey`/`HashIV`、找不到該商家憑證時回一個明確的「未設定」錯誤（不是 panic、不是回退到任何預設值）
- [x] 2.4 `internal/payment/ecpay/ecpay.go`：`InitiatePayment` 組出的 `ReturnURL` 改為 `{base}/{shopID}` 形狀

## 3. `payment.Service` 行為（design D1／D2）

- [x] 3.1 `Service.Providers` 型別/使用方式調整：需要 `Provider` 實例的地方（`InitiatePayment`、webhook 路徑）一律先型別斷言是否實作 `ShopScopedProvider`，是則 `ResolveForShop(ctx, shopID)`，否則使用原本固定實例（`mock` 走這條路，逐字不變）
- [x] 3.2 `InitiatePayment`：把 `shopID` 傳入 provider 解析；解析失敗（憑證未設定）時回應既有的 `ValidationError`/`ConflictError` 分類之一，不建立付款紀錄
- [x] 3.3 `HandleWebhook`：查到的付款列 `shop_id` 若與呼叫端傳入的 shop id（來自新路由）不符，視同查無此付款列（`ErrNotFound`）——防禦性斷言，見 design D2

## 4. HTTP 層（design D2）

- [x] 4.1 `internal/httpapi/router.go`：新增路由 `POST /api/v1/webhooks/payments/{provider}/{shopID}`；既有 `POST /api/v1/webhooks/payments/{provider}` 原樣保留
- [x] 4.2 `internal/httpapi/payment.go`：`Webhook` handler 讀出 `{shopID}`（若存在）；讀 body 後、呼叫 `VerifyWebhook` 前先解析出對應商家的 `Provider` 實例（見 3.1）；無 `{shopID}` 時走原本邏輯（`mock` 不受影響）
- [x] 4.3 `internal/httpapi/payment.go`（或新檔 `payment_credentials.go`）：新增後台憑證管理 handler——`PUT`（設定/更新，only-write）與 `GET`（檢視，密鑰欄位只回布林）於 `/api/v1/admin/shops/{shopID}/ecpay-credentials`，掛 `payment_credential.manage`；跨店存取沿用既有 cross-shop guard

## 5. RBAC 與 seed（design D6）

- [x] 5.1 `internal/seed/seed.go`：`PermissionCatalog` 新增 `payment_credential.manage`；`roleDefs` 只把它加進 `merchant_owner`（**不**加進 `editor`），附理由註解（同 `publishable_key.*`）
- [x] 5.2 `internal/seed`：demo 商家（`Options.Demo`）種一筆 `ShopPaymentProviderCredential`，值沿用綠界官方範例憑證（`3002607`/`pwFHCqoQZGmho4w6`/`EkRm7iFT261dpevs`），確保 `make seed-demo`/CI/e2e 的 ecpay 流程不受影響
- [x] 5.3 `internal/seed` 整合測試：新增/更新斷言涵蓋新權限節點已授予 `merchant_owner`、未授予 `editor`，以及 demo 憑證確實可解密還原

## 6. 設定與接線

- [x] 6.1 `internal/config/config.go`：移除 `ECPAY_MERCHANT_ID`/`ECPAY_HASH_KEY`/`ECPAY_HASH_IV`/`ECPAY_CHOOSE_PAYMENT`（憑證改為資料庫來源）；保留 `ECPAY_CHECKOUT_URL`/`ECPAY_CLIENT_BACK_URL`/`ECPAY_RETURN_URL`
- [x] 6.2 `internal/app/wire.go`：`ecpay.Provider` 改為建構「憑證解析器」（帶 `ent.Client`＋解密函式＋平台層的 `CheckoutURL`/`ClientBackURL`/`ReturnURL`），登記進 `Service.Providers["ecpay"]`
- [x] 6.3 `.env.example`：更新註解，移除已刪除的變數，新增 `PAYMENT_CREDENTIALS_ENCRYPTION_KEY` 與其說明
- [x] 6.4 `internal/app/wiring_integration_test.go`：確認檢查 ⑤⑥⑦ 不受影響（本 change 未新增訂閱者，不改變 dispatcher 訂閱集合）

## 7. Admin 前端

- [x] 7.1 `admin/views/`：新增（或於既有商家設定畫面擴充）ECPay 憑證設定區塊——`MerchantID`/`ChoosePayment` 為一般輸入框並回顯目前值，`HashKey`/`HashIV` 為只寫欄位（留空＝不變更），並顯示「已設定/未設定」狀態
- [x] 7.2 UI 可見性沿用 `GET /shops/{shopID}/me` 推導出的權限節點（`payment_credential.manage`），比照既有 admin RBAC 慣例，`admin/lib/rbac.js` 不需改
- [x] 7.3 儲存後重新整理畫面，確認不會把剛送出的密鑰欄位值回顯（不得緩存明文在前端狀態中）

## 8. API 契約（**改端點必須同步**）

- [x] 8.1 `api/openapi/openapi.yaml`：新增 `PUT`/`GET /api/v1/admin/shops/{shopID}/ecpay-credentials`；新增 webhook `POST /api/v1/webhooks/payments/{provider}/{shopID}` 路徑
- [x] 8.2 重新產生 `v1-surface.txt` 並逐行確認 diff 只有新增、沒有刪除
- [x] 8.3 `make openapi` 四道契約檢查通過——已在使用者本機 Docker 執行，全綠（③ 以真實回應驗證了 33 個共用 schema，含 `ECPayCredentials`；④ 表面鎖 747 項承諾全部仍在）

## 9. 測試

- [x] 9.1 `internal/payment/ecpay`：憑證解析器單元測試——找不到憑證回明確錯誤、加解密往返正確
- [x] 9.2 `internal/payment/service_integration_test.go`：兩個不同商家各自設定憑證後互相隔離發起付款；未設定憑證的商家發起付款被拒；webhook 用錯商家 id 驗簽必然失敗
- [x] 9.3 `internal/httpapi/payment_integration_test.go`：憑證管理 API（設定/更新/檢視、RBAC 403、跨店 403、回應位元組不含密鑰明文）；新 webhook 路由端到端（發起 → 表單欄位含該商家 `MerchantID` → 自簽回呼打 `/webhooks/payments/ecpay/{shopID}` → 訂單轉已付款）
- [x] 9.4 `internal/httpapi/openapi_conformance_integration_test.go`：新端點納入取樣
- [x] 9.5 `internal/tenant` 相關測試：確認新表已正確分類進 `tenantOwned`
- [x] 9.6 `e2e/commerce-path.mjs`：demo 商家改用種子憑證後，既有 ECPay 鏈斷言全綠——**已修正腳本並實測**：`deliverECPayCallback` 原本打舊的、不帶 shop id 的路徑，wire.go 把 `ecpay` 註冊成 shop-scoped 後會 404，已改打新路由；使用者本機 Docker 上跑全 153 項斷言（含第 ⑫ 節綠界 ECPay 全段）全數通過，且驗證過可重複執行（`make seed-demo` 與 e2e 各連跑兩次無殘留狀態問題）

## 10. 文件

- [x] 10.1 `CLAUDE.md`：新增本 change 的架構要點（per-shop 憑證解析、webhook 路由新形狀與其安全論證、加密方式、RBAC 節點只給 merchant_owner、demo 憑證遷移）
- [x] 10.2 更新 ONDO 上線阻塞項的追蹤（此項已解決，其餘四項不受影響）

## 11. 驗收閘門

- [x] 11.1 `make test` 全綠（含契約檢查 ①②④）——已執行，全綠
- [x] 11.2 `make test-int` 全綠（含契約檢查 ③ 與接線檢查 ⑤⑥⑦）——已在使用者本機 Docker 執行，全部套件 `ok`（過程中發現並修正兩個真實 bug，見下方附註）
- [x] 11.3 `make openapi` 全綠——已執行，四道檢查全 PASS
- [x] 11.4 `cd api && golangci-lint run` 全綠——已執行，0 issues
- [x] 11.5 `make seed-demo` ＋ 起 API ＋ `cd e2e && node commerce-path.mjs` 全綠——已執行，153 項斷言全數通過

**驗收過程中發現並修正的兩個真實問題**（皆是本機 Docker 跑 `test-int` 才會浮現，`go build`/`go vet`/`golangci-lint`/`make test` 都測不出來）：

1. **多處手打的 hex 加密金鑰字串長度不足 64（少一碼）**：`internal/httpapi/payment_integration_test.go`、`ecpay_per_shop_integration_test.go`、`openapi_conformance_integration_test.go`、`internal/payment/ecpay/resolver_integration_test.go` 四處的測試用金鑰都是手動輸入時漏了一個字元，`cryptbox.ParseKey` 在跑到才報錯。修法是改用 Python 腳本產生金鑰、寫回檔案，全程不再用肉眼讀寫十六進位字串，並補一道程式化的長度掃描（`grep`/`regex` 找出所有 `ParseKey("...")` 與 `PaymentCredentialsEncryptionKey: "..."` 常數，斷言長度皆為 64）。另發現 `internal/httpapi/cms_integration_test.go` 呼叫 `seed.Run(..., Demo: true)` 但完全沒設定新欄位 `PaymentCredentialsEncryptionKey`——這條是我改了 `seed.Options` 的必要條件後、忘記检查的既有呼叫端，已補上。
2. **`ecpay_per_shop_integration_test.go` 的獨立 fixture 忘記設定頂層 `httpapi.Deps.Log`**：只設定了 `PaymentHandler.Log`，router 自己的 `requestLogger` 中介層讀的是 `Deps.Log`，是 nil 時第一個進來的請求就對 nil `*slog.Logger` 呼叫 `.Info(...)` panic。修法是在 fixture 補上 `Log: log`。
