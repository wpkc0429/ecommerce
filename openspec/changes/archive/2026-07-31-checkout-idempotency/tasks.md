## 1. 前置查證

- [x] 1.1 確認全樹無任何刪除訂單的路徑（design D3：`order_id` 無 FK 的事實前提）
- [x] 1.2 確認 `chi.Walk` 的路由清單不因本變更改變（無新增端點 → 契約檢查 1 的清單不變）
- [x] 1.3 確認 ONDO（`bouquet/public/app.js`）的重試分支語意：400 保留同一把 key、逾時／5xx 重送同一把 key（design D6/D1 的相容性約束來源）

## 2. 資料模型（spec: checkout-idempotency）

- [x] 2.1 `ent/schema/order.go`：新增 `CheckoutIdempotencyKey`（`shop_id`、`idempotency_key varchar(255)`、`request_hash char(64)`、`status smallint`、`order_id`（資訊性、無 edge 無 FK）、`expires_at timestamptz`）
- [x] 2.2 同檔：`UNIQUE(shop_id, idempotency_key)`（**Postgres 預設 NULLS DISTINCT**，兩欄皆 NOT NULL）
- [x] 2.3 同檔：兩條 CHECK — `status IN (0,1)`、`(status = 0 AND order_id IS NULL) OR (status = 1 AND order_id IS NOT NULL)`（design D3）
- [x] 2.4 `internal/tenant/enttenancy.go`：`tenantOwned` 新增 `"CheckoutIdempotencyKey"`
- [x] 2.5 `go generate ./internal/ent`
- [x] 2.6 `make migrate-gen name=add_checkout_idempotency`；確認產生的 SQL 未提議 drop 手寫的 `UNIQUE NULLS NOT DISTINCT`（姊妹 change 踩過）
- [x] 2.7 重算 `atlas.sum`（`ariga.io/atlas/sql/migrate.WriteSumFile`；atlas CLI 未安裝）
- [x] 2.8 `make migrate` 套用；驗證新表存在、既有表零變動

## 3. 冪等服務（spec: checkout-idempotency）

- [x] 3.1 `internal/idempotency/service.go`（新套件）：`StatusInProgress`／`StatusCompleted` 常數與 `ErrKeyInProgress`／`ErrKeyReused`／`ErrInvalidKey`
- [x] 3.2 同檔：`ValidateKey`——8–255 個可見 ASCII 字元（0x21–0x7E），不施加任何結構要求（design D2）
- [x] 3.3 同檔：`Fingerprint(scope string, body []byte) string`——`SHA256(scope || "\n" || body)` 的 hex（design D7）
- [x] 3.4 同檔：`Begin(ctx, shopID, key, fingerprint)`——先嘗試接管逾期列（條件式 UPDATE `WHERE expires_at <= now()` + 受影響列數），否則 INSERT；UNIQUE 違反時載入既有列並判定回放／in-progress／reused（design D3/D8/D11）
- [x] 3.5 同檔：`Release(ctx, reservationID)`——`DELETE WHERE id = ? AND status = in_progress`（design D6：條件讓它不可能刪掉已完成的預約）
- [x] 3.6 同檔：套件註解寫明「仲裁者是資料庫唯一索引，不是應用層的 read-then-write」與三個既有同型機制的出處

## 4. 結帳交易內定案（spec: order-management）

- [x] 4.1 `internal/order/service.go`：`CheckoutInput` 新增 `IdempotencyReservationID *int`（選填；nil 即本變更前的行為）
- [x] 4.2 同檔：`Checkout` 於既有交易內、建立 `order_items` **之後**、`Commit()` **之前**，以條件式 UPDATE（`WHERE id = ? AND status = in_progress`）定案預約並檢查受影響列數；失敗即回滾整筆交易
- [x] 4.3 同檔：**條件式扣庫存與其受影響列數檢查一個字元不改**——以既有三個 `-race` 測試一行不改地通過作為證據
- [x] 4.4 `internal/guest/service.go`：`CheckoutInput` 新增 `IdempotencyReservationID *int` 並標 `json:"-"`（客戶端不得注入），原樣轉交 `order.Service.Checkout`

## 5. HTTP 層（spec: checkout-idempotency, guest-checkout）

- [x] 5.1 `internal/httpapi/middleware.go`：新增 `readJSONBody`（1 MiB 上限）與 `unmarshalJSON`，讓主體可同時被雜湊與解碼；`decodeJSON` 維持不變（其餘端點不受影響）
- [x] 5.2 `internal/httpapi/idempotency.go`（新檔）：`beginIdempotency` 共用輔助——無標頭即直通；格式錯 → 422 `invalid_idempotency_key`；reused → 422 `idempotency_key_reused`；in-progress → 409 `idempotency_key_in_progress`；completed → 回傳 replay 訂單 id
- [x] 5.3 同檔：`idemGate.release` 與「服務未接線卻收到標頭」時的 `Log.Error`（design Risks：`wire.go` 零測試覆蓋）
- [x] 5.4 `internal/httpapi/guest.go`：`GuestHandler` 新增 `Idempotency` 欄位；checkout 改為 讀原始主體 → begin → 解碼 → 帶預約結帳 → 失敗釋放；回放以 `GetOrderAdmin` 取回並輸出與首次同構的 201（含**重新簽發**的訂單存取權杖）＋ `Idempotency-Replayed: true`
- [x] 5.5 同檔：把「組裝訪客結帳成功回應」抽成一個函式，讓首次與回放**逐位元組共用同一段序列化程式碼**（避免兩份會漂移的實作）
- [x] 5.6 `internal/httpapi/order.go`：`OrderHandler` 新增 `Idempotency` 欄位；會員 checkout 同上，指紋範圍為 `member-checkout:<memberID>`；回放以**會員範圍**的 `GetOrder(shopID, memberID, orderID)` 取回（design D7 的 defense in depth）
- [x] 5.7 兩個 handler 的錯誤路徑一律先 `release` 再寫回應（design D6）

## 6. 設定與佈線

- [x] 6.1 `internal/config/config.go`：新增 `CheckoutIdempotencyTTL`（`CHECKOUT_IDEMPOTENCY_TTL`，預設 24h）
- [x] 6.2 `.env.example`：新增該變數與一行說明
- [x] 6.3 `internal/app/wire.go`：建立 `idempotency.Service` 並接上兩個 handler

## 7. API 契約（spec: api-contract 的既有四道檢查）

- [x] 7.1 `api/openapi/openapi.yaml`：新增 `components/parameters/IdempotencyKey`（選填標頭，敘述含格式界限、不透明、重試要送相同位元組）
- [x] 7.2 同檔：兩個結帳端點各引用該參數；201 新增 `Idempotency-Replayed` 回應標頭；新增 409 回應；422 敘述補上兩個新 code
- [x] 7.3 同檔：`Error` schema 的 code 列表新增 `invalid_idempotency_key`／`idempotency_key_reused`／`idempotency_key_in_progress`
- [x] 7.4 同檔：`info.description` 的相關段落補上冪等鍵的一段（與既有的租戶解析／金額單位段落同層）
- [x] 7.5 `api/openapi/v1-surface.txt`：以 `-openapi.update-surface` 重新產生，**確認 diff 只有新增、沒有刪除**
- [x] 7.6 `make openapi` 四道檢查全綠

## 8. 文件

- [x] 8.1 `docs/storefront-integration.md`：第 5 節「③ 送出按鈕要在請求期間停用／訪客結帳**沒有冪等鍵**」整段改寫（該敘述在本變更後是錯的）
- [x] 8.2 同檔：新增一節「安全地重試結帳」——key 怎麼產生、標頭怎麼帶、重試要送相同位元組、回放看到什麼、三個新錯誤碼各自該怎麼處理
- [x] 8.3 同檔：第 10 節錯誤對照表新增三個 code
- [x] 8.4 `CLAUDE.md`：Architecture notes 新增一段（表名、鍵的範圍以 shop 為界、交易內定案、失敗不佔用鍵、TTL）

## 9. 測試（spec: checkout-idempotency）

- [x] 9.1 單元：`ValidateKey` 的界限（7／8／255／256 字元、控制字元、非 ASCII、UUID 與 ONDO 形狀的 key 皆通過）
- [x] 9.2 單元：`Fingerprint` 在 scope 不同時必然不同、在主體位元組不同時必然不同
- [x] 9.3 整合：訪客路徑同鍵同請求回放——訂單 id 相同、訂單總數 1、庫存只扣一次、第二次帶 `Idempotency-Replayed`
- [x] 9.4 整合：回放重新簽發的訂單存取權杖可用，且仍只能存取該筆訂單
- [x] 9.5 整合：回放反映訂單當下狀態（取消後回放看到 cancelled，且不建新單）
- [x] 9.6 整合：同鍵不同主體 → 422 `idempotency_key_reused`，原訂單不受影響
- [x] 9.7 整合：**跨商家**——shop A 用過的 key 在 shop B 正常建新單、不回放；兩邊各自重送各自回放（design D4）
- [x] 9.8 整合：驗證失敗（缺收件人）後以同一把 key 修正重送 → 201（design D6 的相容性核心）
- [x] 9.9 整合：庫存不足後補貨、同鍵同請求重送 → 201 新訂單
- [x] 9.10 整合：格式不合的 key → 422 `invalid_idempotency_key`，不建單
- [x] 9.11 整合：逾期後同鍵建立**新**訂單（以直接改寫 `expires_at` 模擬時間流逝）
- [x] 9.12 整合：會員路徑的回放、reused、in-progress 與訪客路徑逐項一致
- [x] 9.13 整合（**安全**）：同商家兩位會員、同一把 key、逐位元組相同的主體 → 第二位 422 reused，看不到第一位的訂單（design D7）
- [x] 9.14 整合（`-race`，**並發證明**）：N 個帶同一 key 的並發訪客結帳 → 恰好 1 筆訂單、庫存恰好扣 1、恰好 1 個 201，其餘 409／回放 201，無 5xx
- [x] 9.15 整合（`-race`，**未削弱證明**）：既有 `TestCheckoutConcurrentOversellingProtection`／`TestGuestCheckoutConcurrentOversellingProtection`／`TestGuestAndMemberCheckoutShareOneStockGuard` 一行不改地通過
- [x] 9.16 整合（`-race`）：10 個帶**各自不同** key 的並發結帳搶 stock=5 → 恰好 5 成 5 敗、`stock_qty = 0`
- [x] 9.17 整合：不帶標頭的結帳連送兩次 → 兩筆訂單（預設行為未改變）
- [x] 9.18 契約：`openapi_conformance_integration_test.go` 新增回放回應與新錯誤碼的 schema 驗證

## 10. e2e（design Risks：`wire.go` 零 Go 測試覆蓋）

- [x] 10.1 `e2e/commerce-path.mjs`：新增「同鍵重送回放同一筆訂單、訂單總數不變」的斷言——這是唯一會打到真實佈線的地方
- [x] 10.2 DB 重置後 `make migrate && make seed-demo`，`cd e2e && node happy-path.mjs` 全綠

## 11. 驗收

- [x] 11.1 `make test` 全綠
- [x] 11.2 `make test-int` 全綠（`-p 1`）
- [x] 11.3 `make lint` 無新增問題
- [x] 11.4 `make openapi` 四道檢查全綠
- [x] 11.5 既有整合測試**零修改**即通過（additive 的操作型定義，design D12）
- [x] 11.6 刻意破壞一次冪等定案（讓 `Checkout` 略過預約定案），確認 9.14 轉紅，再改回
