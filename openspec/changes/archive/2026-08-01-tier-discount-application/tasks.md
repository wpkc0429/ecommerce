## 1. 前置量測（tripwire #4）

- [x] 1.1（實測 2,940 ＝ JS 2,361 ＋ HTML 395 ＋ CSS 184）實作**前**量測 `admin/` 總行數（JS ＋ HTML ＋ CSS）並記錄基準值（`admin-commerce-operations` 實測為 2,940；tripwire #4 的界線是 ~3000）

## 2. 資料模型與 migration

- [x] 2.1 `api/internal/ent/schema/order.go`：`Order` 新增 `discount_amount`（`Int64`，`Default(0)`）、`discount_percent`（`Int16`，`Optional().Nillable()`）、`member_tier_name`（`String`，`MaxLen(100)`，Postgres `varchar(100)`，`Optional().Nillable()`），並附上說明「快照後永不重算」的註解（design D3/D8）
- [x] 2.2 同檔 `Annotations`：新增 `orders_discount_amount_check`、`orders_discount_amount_bound_check`、`orders_discount_percent_check`、`orders_tier_snapshot_check`，並把 `orders_total_amount_composition_check` 改為 `total_amount = subtotal_amount - discount_amount + shipping_fee`（design D5）
- [x] 2.3 `go generate ./internal/ent`
- [x] 2.4 `make migrate-gen name=add_tier_discount`；**手動檢查產生的 SQL**：ADD COLUMN 在 ADD CONSTRAINT 之前、DROP 舊組成 CHECK 的順序正確、且沒有順手提議 drop 掉手寫的 `UNIQUE NULLS NOT DISTINCT` 或 `UNIQUE(shop_id, email|phone)`（兩個姊妹 change 都踩過）
- [x] 2.5 up/down 兩支 migration 加檔頭註解；down 必須寫明「若已存在 `discount_amount > 0` 的訂單，還原舊組成 CHECK 會失敗，回滾前必須先處理那些列」（design Migration Plan 的 Rollback）
- [x] 2.6 重算 `atlas.sum`
- [x] 2.7 `make migrate` 套用並驗收：`SELECT count(*) FROM orders WHERE total_amount <> subtotal_amount - discount_amount + shipping_fee` ＝ 0，且既有列的 `total_amount`／`subtotal_amount`／`shipping_fee` 值未變

## 3. 結帳套用折扣（`order` 套件）

- [x] 3.1 `order.Service.resolveTierDiscount(ctx, shopID, memberID)`：以顯式 `shop_id` 查 `members` 並 join `member_tier`，回傳 `(tierName *string, percent *int16, err error)`；不屬於任何等級時全為 nil（design D8）
- [x] 3.2 `Checkout`：在 `s.Client.Tx(ctx)` **之前**、與 `resolveShippingMethod` 同階段呼叫它
- [x] 3.3 `Checkout`：在扣庫存迴圈**之後**、`tx.Order.Create()` 之前計算 `discount := subtotal * int64(percent) / 100`（整數、無條件捨去、先乘後除，design D4），並把三個快照欄位與 `SetTotalAmount(subtotal - discount + shippingFee)` 寫進建單
- [x] 3.4 確認扣庫存迴圈與其受影響列數檢查、cart CAS、冪等預約定案**逐字未改**（以 `git diff` 逐行核對）
- [x] 3.5 `internal/order` 的單元測試：折抵計算的邊界（整除、不整除 `.3`／`.9`、0%、100%、`subtotal < 100/percent` 得 0、無等級）

## 4. 點數核發基準

- [x] 4.1 `events.OrderPaymentSucceeded.SubtotalAmount` **更名**為 `AwardBasisAmount`，並更新型別註解說明它是「折後品項小計」以及為何是更名而不是新增欄位（design D7）
- [x] 4.2 `payment.Service.HandleWebhook`：發佈事件時帶 `ord.SubtotalAmount - ord.DiscountAmount`
- [x] 4.3 `points.Service.HandleOrderPaid`：改讀 `ev.AwardBasisAmount`，更新 doc comment（核發基準的完整理由，含「折扣與點數是同一份回饋」與「100% 折抵不核發」）
- [x] 4.4 修正所有因更名而編譯失敗的既有測試建構點（`payment`／`points`／`shipping`／`httpapi` 各套件）
- [x] 4.5 `points` 整合測試：折後基準核發（`floor((subtotal-discount)/100)`）、100% 折抵核發 0 點、**歷史形狀重現**（`discount_amount = 0` 的訂單核發值與變更前相同）、退貨扣回取自原核發列而非重算

## 5. HTTP 與契約

- [x] 5.1 `httpapi.orderJSON` 新增 `discount_amount`／`discount_percent`／`member_tier_name` 三個鍵
- [x] 5.2 `api/openapi/openapi.yaml`：`Order` 新增三個屬性（`discount_amount` 必須 `$ref` `MinorUnitAmount`）、加入 `required`、更新 `Order.description` 的恆等式與 `total_amount`／`subtotal_amount` 的敘述
- [x] 5.3 `api/openapi/openapi.yaml`：`MemberTier.discount_percent` 的敘述由「保留欄位，本階段沒有任何邏輯讀它」改為說明它在結帳時套用於商品金額、運費不折、且捨入為無條件捨去；兩條結帳端點與 `GET /shop/points` 的敘述補上折扣說明
- [x] 5.4 以 `-openapi.update-surface` 重生 `v1-surface.txt`，逐行檢查 diff **只有新增（預期 3 行）、零刪除**
- [x] 5.5 `make openapi` 四道全綠

## 6. 整合測試（API 層）

- [x] 6.1 端到端：會員持有折扣等級 → 結帳 → 訂單金額正確、三個快照欄位正確、DB 恆等式成立
- [x] 6.2 訪客路徑：全新 email 無折扣；**回頭客**（同 email 已累積點數並升級）套用折扣，且與同一身分的會員結帳結果完全相同（design D9）
- [x] 6.3 快照證明：訂單建立後改動該等級的 `discount_percent`、刪除該等級、以及讓顧客升降級，三種情況下重讀訂單其金額與折扣快照**完全不變**
- [x] 6.4 資料庫約束：直接以 ent 建立 `total_amount != subtotal - discount + fee` 的訂單被拒；`discount_amount > subtotal_amount` 被拒；半套等級快照（只有名字或只有百分比）被拒
- [x] 6.5 冪等：帶同一把 key 的重送在等級／折扣率改變後仍回放原折扣快照；同 key 不同 body 仍為 `422 idempotency_key_reused`
- [x] 6.6 `-race` 並發：既有三個扣庫存並發測試**一行不改**通過；另加一個「多位持有折扣等級的顧客並發搶購」版本，確認不超賣且每筆折抵各自正確
- [x] 6.7 契約回應驗證（`openapi_conformance_integration_test.go`）：實際回應含新鍵且通過 schema 驗證；補一條帶折扣訂單的金額恆等式斷言

## 7. 後台 UI

- [x] 7.1 `admin/views/member-tiers.js`：檔頭註解與畫面說明改為「結帳時自動套用於商品金額（運費不折），儲存後立即對新訂單生效、不影響既有訂單」
- [x] 7.2 `admin/index.html`：會員等級區塊與折扣輸入欄的說明文字同步；訂單詳情的金額組合區塊加入折抵列的容器
- [x] 7.3 `admin/views/orders.js`：`renderSummary` 的金額組合在 `discount_amount > 0` 時插入折抵列（`− 金額（等級名稱 百分比）`），`= 0` 時不顯示；折抵金額走 `formatMoney`、百分比走 `formatPercent`
- [x] 7.4（實測 2,956 ＝ JS 2,376 ＋ HTML/CSS 580，＋16 行，未跨 ~3000）實作**後**再量一次 `admin/` 總行數；若跨過 ~3000 則**停下來**依 tripwire #4 重新評估 `admin-commerce-catalog` design D1，並在回報說明

## 8. 文件

- [x] 8.1 `docs/storefront-integration.md`：新增「會員等級折扣」一節——折扣只吃商品金額、整數無條件捨去的公式、會員可由 `GET /shop/points` 的 `level.discount_percent` 自行預估、**訪客沒有結帳前預估且為何沒有**、送出後一律讀回應的 `total_amount`／`discount_amount`
- [x] 8.2 同檔第 9 節（點數）：核發基準改為「**實付**商品金額（折扣後）每 100 元 1 點，運費不計」，並給一個含折扣的算例
- [x] 8.3 同檔第 0 節與錯誤／症狀對照表：補「合計比預期多了折扣沒生效」與「回頭客金額變少」兩列
- [x] 8.4 `CLAUDE.md` 的 Architecture notes 新增一段記錄本變更的定案（恆等式、捨入方向、點數基準、快照、訪客等級行為）

## 9. E2E

- [x] 9.1 `e2e/commerce-path.mjs`：把既有的「`subtotal + shipping_fee = total`」斷言改為四項恆等式，並為全新訪客加上 `discount_amount === 0` 的斷言
- [x] 9.2 `e2e/commerce-path.mjs`：新增「等級折扣」區段（置於 webhook 冪等之後、出貨退貨之前，此時會員已真的升上銀卡）——折扣率與名稱正確、折抵金額為 `floor(subtotal × pct / 100)`、四項恆等式成立、改動等級設定後訂單不變、付款後點數以折後基準核發且與折前基準的答案不同；該訂單付款後出貨並退貨，使該區段對點數餘額淨零
- [x] 9.3 `e2e/commerce-path.mjs`：`STOCK_PER_RUN` 由 3 調整為 4，註解更新為實際可執行次數
- [x] 9.4 `e2e/happy-path.mjs`：營運後台區段的第一筆訂單斷言「無折扣時不顯示折抵列」；付款後該顧客越過銀卡門檻，以**同一 email** 再下一筆訂單並在後台詳情斷言折抵列、來源等級與應付總額
- [x] 9.5 `e2e/happy-path.mjs`：會員等級畫面斷言說明文字不再出現「不套用」字樣

## 10. 全套驗證

- [x] 10.1 `make test` 全綠
- [x] 10.2 `make test-int` 全綠（`make up`，串行 `-p 1`）
- [x] 10.3 `cd api && go test -race ./internal/httpapi/ -run 'Concurrent|Oversell|StockGuard' -count=1` 全綠
- [x] 10.4 `make lint` 無新增問題
- [x] 10.5 `make openapi` 四道全綠、`v1-surface.txt` 的 diff 零刪除
- [x] 10.6（**159 項全綠**，138 → 159）DB 重置 → `make migrate && make seed-demo` → 起三個服務 → `cd e2e && node happy-path.mjs` 全綠，記錄新的斷言總數
- [x] 10.7 `openspec validate tier-discount-application` 通過
