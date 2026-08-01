# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

多網站電商 SaaS 的 Phase 1 基礎：多租戶架構、shop 範圍化 RBAC、Schema/Payload 解耦的 CMS 引擎與前台渲染管線。Monorepo：

- `/api` — Go headless API（唯一資料權威）：chi router、ent + versioned migrations、JWT 雙體系認證、RBAC、多租戶解析、CMS/渲染/快取
- `/web` — Next.js (App Router) storefront SSR：以渲染 bundle API 取得資料，主題＝前端元件庫（`themes/registry.ts`）
- `/admin` — 無建置步驟的薄殼 SPA（原生 ES modules，`lib/` + `views/`）：登入、CMS 內容編輯與發佈/預覽、商品目錄（分類／商品／巢狀 SKU）、營運（訂單／出貨／運送方式／會員等級／點數／publishable key 與 origin）
- `/e2e` — Playwright 瀏覽器 E2E（happy path）
- `docker-compose.yml` — PostgreSQL 16 + Redis 7（開發基礎設施）

## Commands

前置：`cp .env.example .env`（首次），Docker 需可用。

```bash
make up            # 啟動 PostgreSQL 16 + Redis 7（等待健康檢查）
make migrate       # 套用 versioned migrations（golang-migrate 格式）
make seed          # 權限目錄、平台角色、super admin、starter 主題
make seed-demo     # 以上 + demo 商家（綁 demo.localhost，home 為草稿）
make dev           # 啟動 API（:8080）
make web-dev       # 啟動 storefront（:3000）
make admin-dev     # 啟動 admin SPA（:3001）

make test          # API 單元測試（不需基礎設施）
make test-int      # API 單元 + 整合測試（需 docker infra；串行 -p 1）
make lint          # golangci-lint（需安裝於 PATH）
make migrate-down  # 回滾最近一個 migration
make migrate-gen name=<snake_case>  # 由 ent schema diff 產生新 migration（需 dev DB）

make openapi       # 四道 API 契約檢查（路由覆蓋／金額單位／回應結構／v1 表面鎖）
make openapi-mock  # 由契約起 Prism mock server（:4010，npx，無需 DB/Redis）
make openapi-ui    # 由契約起 Redocly 可瀏覽文件（:8081，npx）

cd web && npm run gen:types   # 由 starter schema JSON 重新產生 TS 型別
cd e2e && node happy-path.mjs    # 全部 E2E：瀏覽器 CMS 流程 ＋ 商品目錄後台 ＋ 營運後台 ＋ 電商鏈（需三個服務 + seed-demo 後的乾淨狀態；目前 138 項斷言）
cd e2e && node commerce-path.mjs # 只跑電商鏈（無瀏覽器，只需 API；可重複執行）
```

單一整合測試：`cd api && INTEGRATION=1 go test ./internal/httpapi/ -run TestDraftPublish -count=1`

## Architecture notes（與 openspec 對應）

- **設計權威**：`openspec/changes/phase1-multitenant-cms-foundation/design.md`（D1–D13）；行為驗收：同目錄 `specs/*/spec.md`。
- **ent schema** 在 `api/internal/ent/schema/`；改動後 `go generate ./internal/ent`，再 `make migrate-gen`。UNIQUE NULLS NOT DISTINCT 等 ent 無法表達的約束在 `api/migrations/` 手寫檔。
- **租戶隔離**：`internal/tenant` 的 interceptor/hook 在 `database.Open` 全域註冊——tenant ctx 下所有租戶資料查詢自動加 `shop_id`。
- **顧客身分以商家為界**（change member-identity-per-shop）：`members` 為租戶資料，唯一性是 `UNIQUE(shop_id, email)` / `UNIQUE(shop_id, phone)`（**維持 Postgres 預設 NULLS DISTINCT**，手寫 migration 建立；勿誤套 `site_shop`/`role_user` 的 `NULLS NOT DISTINCT`）。同一 email 於兩家商家＝兩筆互不相干的身分與密碼，無任何跨店沿用路徑。點數／等級（`points`、`level_id`）直接掛在 `members`（原 `shop_member` 已刪除）。後台路徑不在租戶上下文，對 `members` 的查詢一律顯式帶 `shop_id`，不得倚賴 interceptor。
- **前台租戶解析**（change storefront-client-auth）：`/api/v1/shop/*` 為「`X-Publishable-Key` 優先，否則 `Host`」；金鑰無效即 404，**不回退**。`X-Site-Domain` / `X-Site-Path` 已由該群組移除，**僅存活於 `/api/v1/render/page`**（第一方 SSR 中繼訪客 Host，回傳為已發佈公開內容）。publishable key（`pk_` + base32）是**識別碼不是憑證**：公開、明文儲存、不參與任何授權判定；勿在其上掛存取控制。CORS 允許集合 = `CORS_ALLOWED_ORIGINS`（平台自己的前端）∪ `shop_allowed_origins`（商家自帶前端，平台層 API 登記）。金鑰為商家層 RBAC（`publishable_key.*`），origin 為平台層（`shop.manage_client_origins`，理由同 `shop.manage_domains`：全域競爭命名空間）。
- **訪客結帳**（change guest-checkout）：`POST /api/v1/shop/guest/checkout` 未認證，以 email 找出或建立**無密碼身分**（`members.password_hash IS NULL`，`status=1`；「無法登入」由 password_hash 為 NULL 表達，勿借用 `status`），再走 `cart.Service.ReplaceItems` → **既有的** `order.Service.Checkout`。**任何下單入口都必須呼叫 `order.Service.Checkout`**，不得另寫扣庫存（那是全系統唯一一份並發安全的條件式扣減）。回傳的**訂單存取 token**（`aud=order:{shop_id}`、member 金鑰簽發、`ORDER_ACCESS_TOKEN_TTL` 預設 24h）只能存取該筆訂單，且其路由**不接受路徑上的訂單 id**；它不是 session（不能列訂單歷史、不能取消、不能刷新）。日後以同一 email 註冊即「認領」該身分（`Register` 的 `password_hash IS NULL` 分支，條件式寫入），訂單與點數因 `member_id` 未變而自動浮現——此關聯**未經 email 驗證**（design D6 的已知殘留風險）。
- **訂單金額三欄**（change order-extras）：`orders.subtotal_amount`（品項小計）＋ `shipping_fee`（結帳選定運送方式時由 `shipping_methods.flat_rate` 快照）＝ **`total_amount`（應付總額，即向金流請款的金額）**。三者關係由 DB CHECK `orders_total_amount_composition_check` 強制，任何路徑都不得繞過。`total_amount` 的語意在此變更前是「品項小計」——這是 D13 破例窗口的**第三次也是最後一次**使用（既有列經回填後值未變）。**點數核發基準是 `subtotal_amount`，運費不計點**（design D5：獎勵購買而非物流成本；也是唯一能讓既有已核發點數回溯成立的選擇）；`events.OrderPaymentSucceeded` 攜帶的是 `SubtotalAmount`。運送方式與指定配送日（`requested_delivery_date`，Postgres `date`，只擋「在任何時區都已過去」的日期）皆為**選填**，不帶時行為與變更前完全相同。卡片訊息（`gift_message`）掛在**品項**上，`cart_items` 與 `order_items` 各存一份；購物車合併鍵是 `(sku_id, gift_message)`，庫存檢查則跨訊息以 SKU 加總。長度上限 500，**以 rune 計**（ent 的 `MaxLen` 算 byte，故刻意不用）。
- **結帳冪等鍵**（change checkout-idempotency）：兩條下單端點（`POST /api/v1/shop/checkout`、`POST /api/v1/shop/guest/checkout`）接受**選填**的 `Idempotency-Key` 標頭；**不帶時行為與過去逐位元組相同，且永不變成必填**。它解決的不是雙擊（那是前端的事），而是**回應遺失時客戶端無法分辨「沒送到」與「送到了但回應掉了」**——只有伺服器答得出來。這推翻了 `guest-checkout` design D11 的「前端停用按鈕即可」（該決定的另外兩個理由仍成立，見本 change design D1）。仲裁者是 `checkout_idempotency_keys` 的 **`UNIQUE(shop_id, idempotency_key)`**（Postgres 預設 NULLS DISTINCT，**勿套 `NULLS NOT DISTINCT`**），預約＝INSERT、輸掉 INSERT 就是「你是重複請求」，**絕不是應用層 read-then-write**。**預約的定案在 `order.Service.Checkout` 的既有交易內**（扣庫存迴圈之外、`Commit()` 之前，條件式 UPDATE ＋ 受影響列數）——「訂單存在」與「key 已完成」是同一個原子事實；改成交易外的第二次寫入會重新打開「訂單存在但 key 未定案」的窗口。回放＝**201 ＋ 與首次同構的主體**（刻意無法分辨）＋ `Idempotency-Replayed: true` 標頭；訪客路徑**重新簽發**訂單存取權杖（原本那枚不儲存、可能已過期）。指紋＝`SHA256(呼叫者範圍 || "\n" || 原始 body)`，**呼叫者範圍含 member id 是安全需求不是裝飾**（會員結帳的 body 只是收件地址，兩位顧客寄同一地址會產生逐位元組相同的 body）。**沒建成訂單的請求一律釋放 key**——「改好欄位再送一次」依賴它。TTL 由每列自帶的 `expires_at` 決定（預設 24h，`CHECKOUT_IDEMPOTENCY_TTL`），逾期列由後續同鍵請求原子接管，**無背景清理排程**。`Idempotency-Key` 已加入 CORS 的 `Allow-Headers`、`Idempotency-Replayed` 已加入 `Expose-Headers`——漏掉任一個，瀏覽器端就壞。
- **快取失效**：一律走 `internal/events` 領域事件（handler 不得直呼 Redis）；渲染快取 key 版本化（`cache:shop:{id}:v{ver}:page:{slug}`）；金鑰／origin 快取（`pkey:{key}`、`corsorigin:{origin}`）由 `StorefrontClientChanged` 與 `SiteMappingChanged` 失效。
- **錯誤格式**：`internal/httpx`（D12 統一 envelope）；公開 API 一律掛 `/api/v1`（D13，v1 只做 additive 變更）。**破例窗口已於 change order-extras 用盡並關閉**：後續變更不得再以「尚無外部消費者」為由破壞 v1，破壞性變更請走新端點或 `/api/v2`。
- **API 契約是被測試守著的交付物**（change openapi-contract）：`api/openapi/openapi.yaml`（OpenAPI 3.1，手寫，涵蓋全部 **112** 條路由）。**改任何端點必須同時改它**，否則 `go test ./...` 轉紅。四道檢查全在 `internal/httpapi` 的普通 Go 測試裡，因此自動進 CI（`ci.yml` 無需改動）：①路由覆蓋（`chi.Walk` 雙向）②金額欄位必須 `$ref` `MinorUnitAmount` ③真實回應以契約 schema 驗證（整合層）④`api/openapi/v1-surface.txt` 表面鎖——**刪掉鎖檔任一行＝破壞 v1**，是一個必須人工執行、且在 diff 中可見的動作。**表面鎖收錄五種行**（599 項，change api-contract-parameter-lock）：路由、具名 schema 的屬性、**每條端點接受的每個請求參數**（`METHOD /path header|query|path NAME`）、**該參數可否省略**（多一行 ` optional`）、**每條端點承諾的回應標頭**（`… response 201 Idempotency-Replayed`）。參數與標頭以**端點**為單位——刪掉某條端點的 `- $ref: PublishableKey` 而保留元件定義仍然轉紅。選填參數刻意兩行：`required: false → true`（收緊）讓 `optional` 那行消失而轉紅，`true → false`（放寬）只是新增一行而放行——**方向感知住在產生器，檢查器維持「每一行都必須還在」這一句話**（它短到可以在 review 時原地驗證，那正是鎖檔可信的原因）。**鎖不記錄型別／`pattern`／長度／敘述／範例**：改敘述、補範例、放寬值域都不轉紅（假警報是防腐機制最快的死法），代價是「把約束改嚴」本鎖沉默。**仍未受任何檢查保護**（已盤點，記為後續）：就地展開（非 `$ref`）的回應鍵 60 個與請求主體鍵 106 個、每端點的 `security` 宣告（含 14 條 `security: []`）。**已知：`chi.Walk` 列不到 `GET/PUT /api/v1/admin/shops/{shopID}`**（同一樣板上既有子 router 又有方法處理器），檢查 ① 以實際發請求的可達性探測吸收此盲點——新增端點時若發現它沒出現在 Walk 清單裡，就是踩到了這個坑。契約與實作不一致時**實作是權威**（除非實作違反 `openspec/specs/`）。自帶前端的接入說明在 `docs/storefront-integration.md`（**活文件**，不要再往 archive 的 tasks.md 裡加接入資訊）。
- **事件接線只由 e2e 覆蓋**（change commerce-demo-seed）：`payment`／`shipping`／`points` 互不 import，「誰訂閱了誰」只是 `internal/app/wire.go` 的兩行 `dispatcher.Subscribe`，而**該檔零 Go 測試覆蓋**（每支 integration test 都自建 dispatcher 並註明 mirrors wire.go）。刪掉 `dispatcher.Subscribe(pointsService.Handle)`，`make test-int` 仍全綠而真實伺服器靜默不發點數——**已實測**。唯一守著它的是 `e2e/commerce-path.mjs`，改動 `wire.go` 或事件鏈後請務必跑它。**`idempotency.Service` 的接線同型**（change checkout-idempotency）：忘了掛上去，整套 Go 測試仍全綠而真實伺服器靜默忽略 `Idempotency-Key`（重送就是第二筆訂單）；守著它的同樣只有 `commerce-path.mjs` 的第 ⑩ 節。
- **demo seed 的固定識別值**（change commerce-demo-seed，皆僅 `-demo`）：publishable key `pk_demoseedstorefrontkeynotsecret`（固定值；key 是識別碼不是憑證，且 demo 商家本來就能以 `Host: demo.localhost` 免金鑰解析）、origin `http://demo.localhost:3000`、顧客 `demo-customer@example.com` / `demo-customer-change-me`、商家 owner `demo-owner@example.com` / `demo-owner-change-me`。目錄含 ONDO 形狀的花藝品項（3200–4400，S/M/L）、四筆運送方式（**一筆刻意停用**）、三級等級（0／30／100）。**等級門檻與花藝定價互相耦合**：一筆單品訂單必須跨得過 30 點門檻，否則等級評估在 demo 上恆定回同一答案——此不變式由 `internal/seed` 的整合測試守著，改任一邊前先看它。
- **admin 前端架構已定案**（change admin-commerce-catalog，design D1，`admin-commerce-operations` 及之後皆繼承）：**維持無建置步驟**，但不再是單檔——瀏覽器原生 `import`／`export` 讓「無建置步驟」與「多模組」可以並存（`admin/lib/*.js` 共用詞彙、`admin/views/*.js` 各視圖、`admin/app.js` 只剩 shell 與**視圖註冊表**）。新增視圖 = index.html 一個 `.tab-panel` + 註冊表一列。共用詞彙刻意維持在下限：`el()`、`repeater()`、`formatMoney()`、`can()`，**沒有虛擬 DOM／observable／模板語言／路由器**——每加一個都要能指出它替換掉了哪一段重複程式碼。推翻此決策（改用框架）的四條條件寫在 design D1，出現任一條就重開評估，不必再爭論。`admin/Dockerfile` 改為 `COPY .` ＋ `.dockerignore`，新增模組不需要改部署設定。
- **巢狀表單一律用 `repeater()`**（design D2）：`renderRow(value, remove)` 回傳 `{ node, read }`，**`read()` 閉包在該列自己的 input 參考上**；收集時走 entries 陣列呼叫每列的 `read()`，**一次 DOM 查詢都不做**。這與 `lib/schemaform.js`（CMS）用 label 文字回頭找 DOM 的收集路徑刻意相反——後者在「N 列 × 每列 M 個同名子欄位」下會靜默配錯值。因為 `repeater` 對「列裡面有什麼」一無所知，巢狀第二層（SKU 列裡的 `options` key/value）用的是同一個函式。**商品／SKU／分類刻意不走 schema 驅動表單**（design D3）：CMS 需要它是因為頁型形狀在撰寫時未知；商品的形狀是 Go struct ＋ OpenAPI，已知且穩定，`x-editor-order` 要解的 jsonb 鍵序問題在手寫表單上根本不存在。
- **後台金額顯示與輸入絕不縮放**（design D4）：整個 `admin/` 不存在 `/100` 或 `*100`；顯示走 `admin/lib/money.js`，輸入是整數。TWD 的最小貨幣單位是元，`3400` 就是 `NT$3,400`。刻意**不用** `Intl.NumberFormat` 的 `style:"currency"`（它會產出 `$3,400.00`，那個 `.00` 暗示相反的單位語意，下一個人就會去補除法），也**不自建** exponent 對照表（product-catalog design D1 已定案那是後端 `money` package 的事）。守著它的是 `e2e/happy-path.mjs` 第 ⑩ 節的三條斷言——這是本專案唯一「寫錯不會噴錯」的地方。
- **admin 的營運後台與 tripwire 現況**（change admin-commerce-operations）：`admin-commerce-catalog` design D1 的**四條推翻條件已逐條查證**。**#1（跨視圖衍生狀態）未觸發，而且理由是資料模型而不是克制**——`shipping.Service.CreateShipment` 的簽名裡沒有品項也沒有數量，`shipments.order_id` 有唯一索引，`ListShipmentsAdmin` 註解寫明「0 or 1 row this phase」：**一筆訂單至多一筆出貨，粒度是整筆訂單**。所以「已出貨數／待出貨數」在系統裡沒有對應的事實，UI **不得**提供品項勾選或數量輸入（那會讓商家以為可以只出一部分）。要做部分出貨得先讓 `shipments` 長出 line items，**屆時 tripwire #1 才真正到期，該重新評估的是框架，不是在前端手工同步四個地方**。**#2（第三次重寫）觸發了**，處置是 `lib/dom.js` 的 `rows()`（8 個列表視圖）與 `renderPager()`（3 個）——它們是純函式、不需要生命週期也不需要反應性，所以答案是「加進 `lib/`」而不是框架；**刻意不是 `table()`**（表頭是 `index.html` 的靜態 `<thead>`，本來就不重複，接管整張表只會把儲存格推進 config 物件）。**#4（總行數 ~3000）未觸發但只差一點**：實測 2,940 行（JS 2,361 ＋ HTML 395 ＋ CSS 184），**下一個視圖就會越過**——下一個 admin change 開工前先重評 D1，不要先寫程式。
- **後台路由是 40 行的 hash router**（`admin/lib/router.js`，change admin-commerce-operations design D5）：`#/<view>[/<param>]`。`showTab()` 只寫 `location.hash`，畫面切換只發生在單一的路由處理器裡（兩份真相會立刻漂移）；hash 未變時瀏覽器不發事件，所以導航函式回傳 false 時呼叫端必須自己渲染一次。**hash 而不是 History API**：後台是 nginx 服務的靜態檔，History API 的深層連結需要伺服器 rewrite。深層連結刻意**只涵蓋「這一筆」**（`#/orders/1234`、`#/points/57`），**篩選條件不入 hash**。開機時 hash 指向的視圖必須存在、有權限、且非 `transient`（編輯器類視圖的內容由開啟它的視圖預先填好，重新整理會停在空畫面），否則安靜回落到第一個有權限的視圖——過期的書籤不該長得像故障。
- **點數不是金額，絕不能經過 `formatMoney`**（change admin-commerce-operations design D10）：`admin/lib/money.js` 另有 `formatPoints`／`formatPointsDelta`／`formatPercent`。這條規則存在的理由是兩者之間有真實換算（核發是 `floor(subtotal_amount / 100)`），所以一筆 3,400 的訂單核發 34 點，而把 34 丟進 `formatMoney` 會得到 `NT$34`——數字對、單位錯、看起來完全合理、沒有任何東西會噴錯。訂單詳情上兩者同時出現，E2E 有專門斷言（餘額必須是 `34 點`且不含 `NT$`）。訂單金額三件組必須**組合式**呈現（`小計 ＋ 運費 ＝ 應付總額`），那是唯一能讓商家察覺 `order-extras` 改了 `total_amount` 語意的地方。
- **`shop.manage_client_origins` 的 UI 可見性來源就是 `/shops/{shopID}/me`**（change admin-commerce-operations design D6，已實測 ＋ 整合測試）：它雖是**平台層**節點，但 `rbac.Engine.load` 在商家上下文取角色時是 `shop_id = ? OR shop_id IS NULL`，**平台層角色的權限會被聯集進商家層快照**——所以平台管理員的 `/shops/1/me` 含它、商家 owner 不含，正好對上伺服器的 200／403。**不需要第二個可見性來源。** 已知不精確：伺服器的 `RequirePlatformPermission` 另外要求 `HasPlatformRole`，而 `can()` 不查那個；若有人把該節點授予 merchant scope 的角色（seed 不會），UI 會多畫一顆按下去回 403 的按鈕——體驗層不是安全層。
- **商家訂單列表的篩選一律在伺服器端**（change admin-commerce-operations design D3／D4）：`GET /admin/shops/{shopID}/orders` 除既有 `status` 外，新增選填 `payment_status`／`fulfillment_status`／`delivery_from`／`delivery_to`／`q`（皆為 additive，不帶時查詢逐行相同）。**不得在前端過濾**——列表是分頁的，對當頁過濾會回報「今天沒有要出的貨」而其實有一筆在第 3 頁。日期區間篩的是 **`requested_delivery_date`（Postgres `date`，無時區歧義）而不是 `created_at`**（`shops` 沒有時區欄位，「某一天的訂單」在伺服器上無法無歧義定義）；**未指定配送日的訂單在帶任一日期界限時一律被排除**（NULL 比較為假），UI 必須明示這件事。`q` 對 `shipping_address->>'recipient_name'`／`->>'phone'` 做 ILIKE，數字時另比對訂單編號；實作用 `sql.P` + `Builder.Arg`（**`sql.ExprP` 的 `?` 不會被改寫成 Postgres 的 `$N`，會 500**）。
- **平台沒有任何列出會員的端點**（change admin-commerce-operations design D7）：後台點數視圖只能以 member id 定址（訂單詳情的按鈕、深層連結 `#/points/{memberID}`、或手動輸入）。做顧客名冊需要分頁、搜尋、一份 PII 揭露決策與一個今天不存在的權限節點——留給獨立 change，別順手加一個 `GET /members`。
- **商品 PUT 是全量語意，UI 必須永遠送完整 `skus`**（design D8）：`catalog.upsertSKUs` 會**刪除未出現在酬載中的既有 SKU**，`category_ids` 同樣全量取代。漏送一列＝靜默刪掉它的價格與庫存。因此商品編輯器只有一顆儲存鍵，不提供「只存基本資料」；任何日後新增的「快速編輯」入口動手前先讀 design D8。E2E 第 ⑪ 節專門守著它。
- **UI 的權限來源是 `GET /api/v1/admin/shops/{shopID}/me`**（design D5）：回傳呼叫者在該商家**三層判定後**的有效權限節點。它的守衛只有「已認證 ＋ 跨店存取守衛」，**不要求任何權限節點**——以任一既有節點當守衛，只被授予 `product.view` 的自訂角色就會拿到 403 而看到空白後台。**這是體驗層不是安全層**，伺服器的 `RequireShopPermission` 永遠是唯一權威。切換商家必須重新取得。
- **主題 schema 來源**：`api/internal/seed/schemas/starter/*.json` 同時驅動後端驗證/hydration 與 `web` 的 TS 型別（`npm run gen:types`）。
- 注意：Postgres jsonb 不保留物件鍵順序——admin 的 **CMS** 表單欄位順序非 schema 撰寫順序（Phase 1 已知現象；商品等固定形狀表單不受影響，見上方 design D3）。

## OpenSpec workflow

This project uses [OpenSpec](https://github.com/Fission-AI/OpenSpec) for spec-driven development. Changes are proposed, specified, and implemented as tracked artifacts before/while writing code.

- `openspec/changes/` — active change proposals, each with artifacts like `proposal.md`, `design.md`, `tasks.md`
- `openspec/changes/archive/` — completed changes, archived as `YYYY-MM-DD-<change-name>/`
- `openspec/specs/<capability>/spec.md` — the current source-of-truth specs, updated when changes are archived

Slash commands (in `.claude/commands/opsx/`):

- `/opsx:explore` — thinking/discovery mode; investigate the codebase and clarify requirements, never implements code
- `/opsx:propose "<description>"` — create a new change and generate its artifacts (proposal, design, tasks)
- `/opsx:apply [change-name]` — implement the tasks for a change, checking them off as completed
- `/opsx:archive [change-name]` — archive a completed change and sync its delta specs into `openspec/specs/`

Typical flow: `/opsx:explore` (optional) → `/opsx:propose` → `/opsx:apply` → `/opsx:archive`.

Useful CLI commands directly:

```bash
openspec list --json                              # active changes
openspec status --change "<name>" --json          # artifact/task completion for a change
openspec instructions <artifact-id> --change "<name>" --json
```
