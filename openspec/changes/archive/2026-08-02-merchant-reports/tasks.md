## 1. 資料模型與遷移

- [x] 1.1 `ent/schema/tenancy.go`：`Shop` 新增 `timezone`（`field.String`、`Optional().Nillable()`、`varchar(64)`），註解寫明 NULL ≠ 平台預設（design D1）
- [x] 1.2 `cd api && go generate ./internal/ent`
- [x] 1.3 `make migrate-gen name=add_shop_timezone`，**逐行讀過** `.up.sql`：必須只有一行 `ALTER TABLE "shops" ADD COLUMN`，**零回填、零 CHECK 變動、零索引變動**；atlas 若順手動了任何既有 CHECK 就手改掉
- [x] 1.4 `.down.sql` 對稱 `DROP COLUMN`；`make migrate` 通過
- [x] 1.5 確認 `internal/tenant` 的結構性守衛不受影響（`shops` 沒有 `shop_id` 欄位，`tenantOwned`／`notTenantOwned` 一行不改）

## 2. 時區解析（`internal/shoptz`）

- [x] 2.1 新套件 `internal/shoptz`：`import _ "time/tzdata"`（design D2，檔頭寫明理由與 Dockerfile 的關係）
- [x] 2.2 `Validate(name string) error`：以 `time.LoadLocation` 驗證，空字串視為「清除」由呼叫端處理
- [x] 2.3 `Effective` 型別（`Name string`、`Source string`：`"shop"` / `"default"`）與 `Resolve(shopTZ *string, defaultTZ string) (Effective, *time.Location, error)`——**回落邏輯只有這一份**
- [x] 2.4 單元測試：三個代表性時區（`Asia/Taipei` UTC+8、`America/New_York` 2026-08-02 為 UTC−4、`Asia/Kolkata` UTC+5:30）的**固定偏移比對**（半小時偏移是嵌入資料庫真的被讀到的證據）；不合法名稱回 error；`Resolve` 的兩種 `Source`

## 3. 設定（`internal/config`）

- [x] 3.1 `Config` 新增 `ReportDefaultTimezone`，`REPORT_DEFAULT_TIMEZONE` 預設 `Asia/Taipei`
- [x] 3.2 `Load()` 以 `shoptz.Validate` 驗證該值，不合法時回 error（**服務拒絕啟動**，design Migration Plan）
- [x] 3.3 `.env.example` 加一行並註明預設值與「不設定＝預設」
- [x] 3.4 單元測試：預設值合法；設成垃圾值時 `Load()` 回 error

## 4. 報表領域（`internal/report`）

- [x] 4.1 新套件 `internal/report`：`Service{Client *ent.Client}`，套件註解寫明「純讀取、不發事件、每條查詢顯式帶 `shop_id`（後台路徑不在租戶上下文，且原生 SQL 本來就繞過 interceptor）」
- [x] 4.2 `SalesParams`（`From`/`To` 為當地日曆日、`GroupBy`）與期間驗證：`To < From` → 422；`day` 上限 366 天、`month` 上限 1830 天 → 422
- [x] 4.3 期間界限在 Go 換算成 UTC 瞬間的**半開區間** `[from 00:00 local, to+1day 00:00 local)`（design D5；**不得用 `BETWEEN`**）
- [x] 4.4 `Sales()`：**一條** SQL，`GROUP BY (created_at AT TIME ZONE $tz)::date`（或 `date_trunc('month', …)::date`）與 `currency`，以 `FILTER (WHERE …)` 一次算出四類分割的計數與金額。**實作時改了作法（design D5 已同步更正）**：這不是 ent predicate 而是一整條聚合查詢，所以走 `ent.Client.QueryContext`（`sql/execquery`）配原生 `$N`，而不是 `sql.P` ＋ `Builder.Arg`；`sql.ExprP` 的坑因此碰不到，但「值一律綁定、永不拼進 SQL 文字」這條規則原樣適用
- [x] 4.5 四類分割**依序判定**（cancelled → unpaid → returned → counted），確保互斥（design D4）
- [x] 4.6 Go 端把分桶加總成摘要：**只有 int64 加法，沒有任何除法**（design D11）。`net_amount = subtotal − discount`
- [x] 4.7 依 currency 分組回傳，`ORDER BY period, currency`；每個幣別的 buckets 依 period 遞增
- [x] 4.8 `Products()`：**一條** SQL，join `orders`，同一組口徑條件，`GROUP BY product_title, sku_code, currency`，`ORDER BY gross_amount DESC, quantity DESC, sku_code ASC, product_title ASC`；**不需要 `AT TIME ZONE`**（期間已是瞬間）
- [x] 4.9 `Products()` 的 `limit` **逐幣別**套用（在 Go 端截斷），預設 20、上限 100；回應**不含** `product_id`／`sku_id`（design D8）

## 5. HTTP 層

- [x] 5.1 `httpapi/settings.go`：`ShopSettingsHandler`，`PUT /settings` 掛 `RequireShopPermission("shop.update")`；body `{"timezone": string|null}`，null 清除設定
- [x] 5.2 同上：更新 MUST NOT 觸及 `shops` 的其他欄位；回應帶 `timezone` 與 `effective_timezone`
- [x] 5.3 `httpapi/reports.go`：`ReportsHandler`，兩條 GET 掛 `RequireShopPermission("report.view")`；`from`/`to` **必填**（無法重現的期間就沒有報表），`group_by` 選填（預設 `day`），`limit` 選填
- [x] 5.4 序列化：每個 `*_amount` 都是最小貨幣單位整數；`order_count`／`*_order_count`／`quantity` **刻意不以 `_amount` 結尾**（契約檢查 ② 的命名紀律，design D15）
- [x] 5.5 `Deps` 新增 `Reports *ReportsHandler` 與 `ShopSettings *ShopSettingsHandler`；`router.go` 掛進既有的 `/admin/shops/{shopID}` 子 router
- [x] 5.6 `app/wire.go` 接上兩個 handler（否則**檢查 ⑤** 轉紅）；確認 `wiredDepsExemptions` 維持空的
- [x] 5.7 確認**檢查 ⑦** 一行不動：無新增訂閱者、無新增事件型別（design D9）

## 6. 權限

- [x] 6.1 `internal/seed`：`PermissionCatalog` 新增 `{"report.view", "檢視營運報表"}`——**恰好一個節點**，有真實消費者（design D6）
- [x] 6.2 `roleDefs`：授予 `merchant_owner` 與 `editor`（理由同 `member-directory` design D4：editor 已由 `order.view` 看得到每一筆訂單的金額，報表加的是便利不是存取）
- [x] 6.3 確認 `GET /shops/{shopID}/me` 由權限目錄推導，`admin/lib/rbac.js` **一行不改**

## 7. 契約

- [x] 7.1 `openapi/openapi.yaml` 新增 3 條路徑（2 GET ＋ 1 PUT）與其 schema：`EffectiveTimezone`、`ShopSettings`、`SalesReport`、`SalesReportCurrency`、`ReportAmounts`（原擬名 `SalesSummary`；改名是因為分桶與摘要用的是同一組欄位）、`SalesBucket`、`ProductSalesReport`、`ProductSalesCurrency`、`ProductSalesRow`
- [x] 7.2 所有回應 schema 加 `additionalProperties: false`（不加，**檢查 ③** 抓不到形狀漂移）
- [x] 7.3 所有 `*_amount` 欄位 `$ref` `MinorUnitAmount`（**檢查 ②**）；`moneyExemptions` **一行不加**
- [x] 7.4 口徑寫進每條端點的 `description`（design D3 的五項 ＋ D4 的對帳分割 ＋ D8 的排行口徑）
- [x] 7.5 `openapi/v1-surface.txt` 以 `-openapi.update-surface` 重生，**逐行檢查 diff 只有新增、零刪除**
- [x] 7.6 既有端點的請求與回應**逐位元組不變**——確認 diff 中沒有任何既有行被改動

## 8. 測試（Go）

- [x] 8.1 `internal/report/service_integration_test.go`：建立涵蓋各邊界的訂單（正常／已取消／未付款／已退貨／有折扣／有運費／另一幣別／期間外前後各一／另一商家），逐項斷言數字。**端點回 200 不算**
- [x] 8.2 同上：**對帳分割等式**——四類筆數之和 == 期間內建立的訂單總數；已取消且未付款的訂單只被計一次
- [x] 8.3 同上：金額組合——`net = subtotal − discount`、`total = net + fee`，逐筆可對回訂單
- [x] 8.4 同上：**跨時區邊界三條**（design D16）——① `2026-08-01T22:00:00Z` 配 `Asia/Taipei` 必須落在 **08-02**；② 同一筆配 `America/New_York` 必須落在 **08-01**；③ `from=to=2026-08-02` 配 `Asia/Taipei` 必須**包含**該筆（範圍與分桶同一時區）
- [x] 8.5 同上：多幣別期間產出兩個獨立區塊，且回應結構中不存在任何跨幣別合計
- [x] 8.6 同上：`month` 分組；商品排行的分組鍵、折扣前金額、確定性排序、改名後成兩列
- [x] 8.7 `internal/httpapi/reports_integration_test.go`：`report.view` 403、跨商家 403、`to < from` 422、期間過長 422、`PUT /settings` 的合法／不合法時區、`null` 清除、更新不影響 `shops` 其他欄位
- [x] 8.8 `internal/httpapi/openapi_conformance_integration_test.go`：兩條報表端點各取樣一次；新 schema 加進 `requiredSchemas`（**檢查 ③**）
- [x] 8.9 `cd api && go vet ./...` 通過（**不是** `go build`——`go build` 不是成功訊號）

## 9. admin 前端

- [x] 9.1 新增 `admin/lib/format.js`：`formatTime` 一份（tripwire #2 的舊債，`partial-shipment` design D11 交棒）
- [x] 9.2 `views/{orders,members,points,storefront-clients}.js` 各刪掉自己的 `formatTime`，改為 import；`order-shipments.js` 的注入路徑維持不變（它拿的是 `orders.js` 傳進去的那一個）
- [x] 9.3 `admin/index.html` 新增 `#tab-reports` panel：期間／分組控件、時區區塊、**口徑揭露的散文（靜態 HTML）**、摘要容器、分桶表頭、商品排行表頭
- [x] 9.4 `admin/views/reports.js`：期間查詢（伺服器端）、依 currency 逐塊渲染、摘要組合式呈現、對帳分割、商品排行
- [x] 9.5 時區設定表單：`can("shop.update")` 才顯示；變更前 confirm 說明「會改變所有歷史報表的日界線」
- [x] 9.6 `admin/app.js` 註冊表新增一列（`perm: "report.view"`）＋ import 一行
- [x] 9.7 `admin/index.html` 的 `#orders-date-note` 補上「此篩選作用於**指定配送日**，不是訂單建立日」（design D10 的推論）
- [x] 9.8 確認 `admin/` 全域仍不存在 `/100` 或 `*100`；筆數與數量不經過 `formatMoney`
- [x] 9.9 **量測並記錄 tripwire**：#5（最大 `views/*.js`）與 #4′ 的**兩種量法**（含／不含 `formatTime` 舊債償還），把實測數字寫回 design D14。基準：`lib/`＋`app.js` 873、`views/` 1,909、`index.html` 457、`styles.css` 201、總計 3,440、最大視圖 `products.js` 338
- [x] 9.10 若 `reports.js` 逼近 400 行，依 design D14 的事前定調拆檔（`views/report-products.js`），**MUST NOT** 把邏輯塞進 `lib/`

## 10. E2E

- [x] 10.1 `e2e/commerce-path.mjs` 新增一節：以本次執行剛建立的訂單為期望值，斷言報表的小計／折扣／運費／總額，以及對帳分割（取消的那筆落在 `cancelled_order_count`）
- [x] 10.2 同上：斷言未設定時區的 demo 商家其 `timezone.source` 為 `default`
- [x] 10.3 `e2e/happy-path.mjs` 新增報表視圖的畫面斷言：口徑揭露文字存在、時區來源標示、金額組合式、`order_count` 不含 `NT$`
- [x] 10.4 既有斷言**一條都不刪**；更新 `CLAUDE.md` 的斷言計數。實測 happy-path **231 → 261**、commerce-path **135 → 153**
- [x] 10.5 **驗證 `commerce-path.mjs` 仍可重複執行**（連跑兩輪不重置）——第一版讓報表用的訂單由 demo 會員下單，永久加點後把顧客推上銀卡，實測讓第二輪的第 ⑥ 節轉紅；改為每輪唯一 email 的訪客身分後復原

## 11. 閘門

- [x] 11.1 `make up; make migrate`
- [x] 11.2 `cd api && go vet ./...`
- [x] 11.3 `make test`（單元 ＋ 契約 ①②④）
- [x] 11.4 `make test-int`（整合 ＋ 契約 ③ ＋ 接線 ⑤⑥⑦）
- [x] 11.5 `make openapi`
- [x] 11.6 `cd api && golangci-lint run`（若 PATH 有）
- [x] 11.7 確認 `:8080` 沒有殘留的舊 API server，重新啟動後跑 `cd e2e && node happy-path.mjs` 與 `node commerce-path.mjs`
- [x] 11.8 更新 `CLAUDE.md` 的架構註記（路由數、表面鎖項數、斷言數、本 change 的口徑與時區紀律）
