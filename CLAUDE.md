# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

多網站電商 SaaS 的 Phase 1 基礎：多租戶架構、shop 範圍化 RBAC、Schema/Payload 解耦的 CMS 引擎與前台渲染管線。Monorepo：

- `/api` — Go headless API（唯一資料權威）：chi router、ent + versioned migrations、JWT 雙體系認證、RBAC、多租戶解析、CMS/渲染/快取
- `/web` — Next.js (App Router) storefront SSR：以渲染 bundle API 取得資料，主題＝前端元件庫（`themes/registry.ts`）
- `/admin` — 無建置步驟的薄殼 SPA（原生 ES modules，`lib/` + `views/`）：登入、CMS 內容編輯與發佈/預覽、商品目錄（分類／商品／巢狀 SKU）、營運（訂單／出貨／運送方式／會員等級／點數／顧客名冊／publishable key 與 origin）
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
cd e2e && node happy-path.mjs    # 全部 E2E：瀏覽器 CMS 流程 ＋ 商品目錄後台 ＋ 營運後台 ＋ 電商鏈（需三個服務 + seed-demo 後的乾淨狀態；目前 187 項斷言）
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
- **等級折扣真的會扣錢**（change tier-discount-application）：`member_tiers.discount_percent` 從 `member-tiers-and-points` 起就存在但**沒有任何程式讀它**，而 `admin-commerce-operations` 已把它做成商家可設定的欄位——「設了卻不作用」是靜默失效，本 change 推翻那條 non-goal（design D1：不是前人判斷錯，是**世界變了**；教訓是**沒有消費端時不要把可設定項先交到商家手上**）。`order.Service.Checkout` 在交易**之外**（與 `resolveShippingMethod` 同階段）由 `members.level_id` 解析等級，在扣庫存迴圈**之後**算一次乘除。**恆等式改為 `total_amount = subtotal_amount − discount_amount + shipping_fee`**，由 DB CHECK 強制，另有 `discount_amount <= subtotal_amount`（折扣不得把商品金額變負）與 `(member_tier_name IS NULL) = (discount_percent IS NULL)`（不得有半套快照）。**折扣只吃品項小計，運費不打折**（design D2：運費是代收轉付，且折運費會獎勵顧客選最貴的配送；「VIP 免運」是日後一個布林欄位，不是百分比）。**捨入是無條件捨去**（`subtotal * pct / 100`，先乘後除）——與點數的 `floor(基準/100)` 同方向，**全系統只有一種捨入**；四捨五入需要一個 `+50`，而那正是下一個人會「順手簡化」掉、且錯了不會噴錯的東西。**點數核發基準細化為 `subtotal_amount − discount_amount`**（`order-extras` design D5 理由 4 早就預告過這個細化）；注意上一次那把尺（「唯一讓已核發點數仍正確的選項」）這次**兩個候選都通過**（既有列 `discount_amount = 0`），所以它是門檻而非判準——真正的理由是折扣與點數是同一份回饋、以原價計點會在商家背後相乘。`events.OrderPaymentSucceeded.SubtotalAmount` 因此**更名**為 `AwardBasisAmount`（第二次更名，理由同 design D8：新增欄位會讓忘記設定的建構點靜默算錯）。**折扣是訂單上的快照，永不重算**：商家改價／改名／刪等級、顧客升降級，既有訂單一個字不動；冪等回放讀的是那份快照（免費得到的正確行為）。**訪客會拿到折扣**（design D9）：`guest.Service` 以 email 解析出的身分若已有等級就套用——ONDO 全站沒有登入 UI，只給會員等於功能不存在。代價是回應金額洩漏「這個 email 屬於哪一級」，**無法以設計繞開**（顧客必須看得到自己付多少），成本由「必須真的建單」＋既有兩條限流界定，正解是 `email-verification`。**訪客刻意沒有結帳前預估端點**（那會把「必須下單才看得到」降級成「一個請求就知道」）；會員用既有的 `GET /shop/points` 的 `level.discount_percent` 自行預估。**atlas 以名稱比對 CHECK，改運算式它完全沉默**——本次的組成 CHECK DROP/ADD 是手寫的，日後改任何 CHECK 運算式都必須手寫。
- **快取失效**：一律走 `internal/events` 領域事件（handler 不得直呼 Redis）；渲染快取 key 版本化（`cache:shop:{id}:v{ver}:page:{slug}`）；金鑰／origin 快取（`pkey:{key}`、`corsorigin:{origin}`）由 `StorefrontClientChanged` 與 `SiteMappingChanged` 失效。
- **錯誤格式**：`internal/httpx`（D12 統一 envelope）；公開 API 一律掛 `/api/v1`（D13，v1 只做 additive 變更）。**破例窗口已於 change order-extras 用盡並關閉**：後續變更不得再以「尚無外部消費者」為由破壞 v1，破壞性變更請走新端點或 `/api/v2`。
- **API 契約是被測試守著的交付物**（change openapi-contract）：`api/openapi/openapi.yaml`（OpenAPI 3.1，手寫，涵蓋全部 **114** 條路由）。**改任何端點必須同時改它**，否則 `go test ./...` 轉紅。**七道**檢查全是普通 Go 測試，因此自動進 CI（`ci.yml` 無需改動）：①路由覆蓋（`chi.Walk` 雙向）②金額欄位必須 `$ref` `MinorUnitAmount` ③真實回應以契約 schema 驗證（整合層）④`api/openapi/v1-surface.txt` 表面鎖——**刪掉鎖檔任一行＝破壞 v1**，是一個必須人工執行、且在 diff 中可見的動作。**表面鎖收錄五種行**（639 項，change api-contract-parameter-lock）：路由、具名 schema 的屬性、**每條端點接受的每個請求參數**（`METHOD /path header|query|path NAME`）、**該參數可否省略**（多一行 ` optional`）、**每條端點承諾的回應標頭**（`… response 201 Idempotency-Replayed`）。參數與標頭以**端點**為單位——刪掉某條端點的 `- $ref: PublishableKey` 而保留元件定義仍然轉紅。選填參數刻意兩行：`required: false → true`（收緊）讓 `optional` 那行消失而轉紅，`true → false`（放寬）只是新增一行而放行——**方向感知住在產生器，檢查器維持「每一行都必須還在」這一句話**（它短到可以在 review 時原地驗證，那正是鎖檔可信的原因）。**鎖不記錄型別／`pattern`／長度／敘述／範例**：改敘述、補範例、放寬值域都不轉紅（假警報是防腐機制最快的死法），代價是「把約束改嚴」本鎖沉默。**仍未受任何檢查保護**（已盤點，記為後續）：就地展開（非 `$ref`）的回應鍵 60 個與請求主體鍵 106 個、每端點的 `security` 宣告（含 14 條 `security: []`）。**已知：`chi.Walk` 列不到 `GET/PUT /api/v1/admin/shops/{shopID}`**（同一樣板上既有子 router 又有方法處理器），檢查 ① 以實際發請求的可達性探測吸收此盲點——新增端點時若發現它沒出現在 Walk 清單裡，就是踩到了這個坑。契約與實作不一致時**實作是權威**（除非實作違反 `openspec/specs/`）。自帶前端的接入說明在 `docs/storefront-integration.md`（**活文件**，不要再往 archive 的 tasks.md 裡加接入資訊）。**檢查 ① 的路由來源不再是人工清單**（change contract-router-completeness）：`contractDeps` 以 `testutil.FillNonNil` 反射 `httpapi.Deps` 逐欄位填滿，所以**新增一個 handler 不需要碰契約測試任何一行**——它的路由自動被列舉，沒寫進 `openapi.yaml` 就自動轉紅（改成清單之前，忘了加那一行＝兩條零文件路由全綠通過，**已實測**）。若新增的 `Deps` 欄位是**介面**（反射生不出實作），`FillNonNil` 會硬失敗要求你去 overrides 登記一個值——那是刻意的，靜默留 nil 就是把這個洞換個型別重開。
- **`wire.go` 的接線現在有 CI 內的檢查**（change contract-router-completeness，推翻 `commerce-demo-seed` 的「不為 wire.go 補 Go 測試」）：`internal/app/wiring_integration_test.go` 的三道檢查跑的是**真正的** `a.wire`／`app.New`（不是代表它的東西，所以 `commerce-demo-seed` design D1 對「新間接層自己也可能沒被呼叫」的反對不適用）——⑤`TestWireFillsEveryDependency` 遞迴斷言每一項相依都被賦值（豁免表**空的**）、⑥`TestWiredRouteTableMatchesContractSource` 斷言生產 router 與檢查 ① 走的 router 逐條相同、⑦`TestWiredDispatcherSubscribers` 以**相等**（非包含）比對真實 dispatcher 的訂閱者集合並斷言全系統只有一個 dispatcher 實例。**新增訂閱者也會轉紅**，那是刻意的：新的跨套件事件鏈必須有人明示更新 `expectedSubscribers`，順便回答「e2e 要不要補一條斷言」。訂閱者以函式符號名識別，匿名 closure 正規化成 `.<closure>`（`funcN` 的編號會被無關的 closure 位移，鎖原始名字＝假警報）。**這兩類漏接的症狀已各自實測**：拿掉 `dispatcher.Subscribe(pointsService.Handle)` → 所有 points 測試仍全綠、只有 ⑦ 紅；拿掉 `deps.Orders.Idempotency` → 路由數不變、全部冪等整合測試與四道契約檢查仍全綠、只有 ⑤ 紅。**分工**：CI 回答「有沒有接」，`e2e/commerce-path.mjs` 回答「接上之後整條鏈的行為對不對」（金額、點數、等級升降、重送冪等）——**e2e 的覆蓋一條都沒有減少，改動事件鏈後仍請務必跑它**。
- **demo seed 的固定識別值**（change commerce-demo-seed，皆僅 `-demo`）：publishable key `pk_demoseedstorefrontkeynotsecret`（固定值；key 是識別碼不是憑證，且 demo 商家本來就能以 `Host: demo.localhost` 免金鑰解析）、origin `http://demo.localhost:3000`、顧客 `demo-customer@example.com` / `demo-customer-change-me`、商家 owner `demo-owner@example.com` / `demo-owner-change-me`。目錄含 ONDO 形狀的花藝品項（3200–4400，S/M/L）、四筆運送方式（**一筆刻意停用**）、三級等級（0／30／100）。**等級門檻與花藝定價互相耦合**：一筆單品訂單必須跨得過 30 點門檻，否則等級評估在 demo 上恆定回同一答案——此不變式由 `internal/seed` 的整合測試守著，改任一邊前先看它。
- **admin 前端架構已定案**（change admin-commerce-catalog，design D1，`admin-commerce-operations` 及之後皆繼承）：**維持無建置步驟**，但不再是單檔——瀏覽器原生 `import`／`export` 讓「無建置步驟」與「多模組」可以並存（`admin/lib/*.js` 共用詞彙、`admin/views/*.js` 各視圖、`admin/app.js` 只剩 shell 與**視圖註冊表**）。新增視圖 = index.html 一個 `.tab-panel` + 註冊表一列。共用詞彙刻意維持在下限：`el()`、`repeater()`、`formatMoney()`、`can()`，**沒有虛擬 DOM／observable／模板語言／路由器**——每加一個都要能指出它替換掉了哪一段重複程式碼。推翻此決策（改用框架）的四條條件寫在 design D1，出現任一條就重開評估，不必再爭論。`admin/Dockerfile` 改為 `COPY .` ＋ `.dockerignore`，新增模組不需要改部署設定。
- **巢狀表單一律用 `repeater()`**（design D2）：`renderRow(value, remove)` 回傳 `{ node, read }`，**`read()` 閉包在該列自己的 input 參考上**；收集時走 entries 陣列呼叫每列的 `read()`，**一次 DOM 查詢都不做**。這與 `lib/schemaform.js`（CMS）用 label 文字回頭找 DOM 的收集路徑刻意相反——後者在「N 列 × 每列 M 個同名子欄位」下會靜默配錯值。因為 `repeater` 對「列裡面有什麼」一無所知，巢狀第二層（SKU 列裡的 `options` key/value）用的是同一個函式。**商品／SKU／分類刻意不走 schema 驅動表單**（design D3）：CMS 需要它是因為頁型形狀在撰寫時未知；商品的形狀是 Go struct ＋ OpenAPI，已知且穩定，`x-editor-order` 要解的 jsonb 鍵序問題在手寫表單上根本不存在。
- **後台金額顯示與輸入絕不縮放**（design D4）：整個 `admin/` 不存在 `/100` 或 `*100`；顯示走 `admin/lib/money.js`，輸入是整數。TWD 的最小貨幣單位是元，`3400` 就是 `NT$3,400`。刻意**不用** `Intl.NumberFormat` 的 `style:"currency"`（它會產出 `$3,400.00`，那個 `.00` 暗示相反的單位語意，下一個人就會去補除法），也**不自建** exponent 對照表（product-catalog design D1 已定案那是後端 `money` package 的事）。守著它的是 `e2e/happy-path.mjs` 第 ⑩ 節的三條斷言——這是本專案唯一「寫錯不會噴錯」的地方。
- **admin 的營運後台與 tripwire 現況**（change admin-commerce-operations）：`admin-commerce-catalog` design D1 的**四條推翻條件已逐條查證**。**#1（跨視圖衍生狀態）未觸發，而且理由是資料模型而不是克制**——`shipping.Service.CreateShipment` 的簽名裡沒有品項也沒有數量，`shipments.order_id` 有唯一索引，`ListShipmentsAdmin` 註解寫明「0 or 1 row this phase」：**一筆訂單至多一筆出貨，粒度是整筆訂單**。所以「已出貨數／待出貨數」在系統裡沒有對應的事實，UI **不得**提供品項勾選或數量輸入（那會讓商家以為可以只出一部分）。要做部分出貨得先讓 `shipments` 長出 line items，**屆時 tripwire #1 才真正到期，該重新評估的是框架，不是在前端手工同步四個地方**。**#2（第三次重寫）觸發了**，處置是 `lib/dom.js` 的 `rows()`（8 個列表視圖）與 `renderPager()`（3 個）——它們是純函式、不需要生命週期也不需要反應性，所以答案是「加進 `lib/`」而不是框架；**刻意不是 `table()`**（表頭是 `index.html` 的靜態 `<thead>`，本來就不重複，接管整張表只會把儲存格推進 config 物件）。**#4（總行數 ~3000）未觸發但只差一點**：實測 2,940 行（JS 2,361 ＋ HTML 395 ＋ CSS 184），**下一個視圖就會越過**——下一個 admin change 開工前先重評 D1，不要先寫程式。
- **tripwire #4 已跨過、已重評、已被取代**（change member-directory design D1，**下一個改 admin 的人先讀這一條**）：實測 **3,229 行**（JS 2,593 ＋ HTML 449 ＋ CSS 187）。重評在寫任何 admin code 之前完成，結論**維持無建置步驟**，理由是量出來的而不是偏好：那 3,229 行裡 51% 是 `views/` 的領域規則（框架會重寫但不會變短），框架真正能刪掉的上界是共用詞彙 ＋ shell ＝ **595 行**（`schemaform.js` 那 274 行是 JSON Schema→控件的編譯器，與框架無關）；而共用層完全沒有在漏水——`admin-commerce-operations` 加 5 個視圖只讓 `lib/`＋`app.js` 長了 197 行（佔視圖增量 23%），本 change 加一個視圖讓它長了 **4 行（1.9%），`admin/lib/` 一行未改**。**#4 被取代而不是放行的理由是結構性的：一條「總行數 > N」的 tripwire 在跨過的那一刻就永久失效**（之後每個 change 都滿足它，於是它要嘛每次強迫一次沒有新資訊的評估，要嘛被忽略）——它是一次性的門，不是持續的偵測器，而它已經完成了唯一能完成的工作：逼我們在 2,956 行時停下來量一次。取代它的是 **#4′：任一次 change 中 `lib/` ＋ `app.js` 的淨增 > 該次 `views/` 淨增的 1/3**（可以在 1,500 行時響、也可以在 6,000 行時沉默，而它響的時候指的正是框架真正修得好的問題），以及 **#5：單一 `views/*.js` 超過約 400 行**（今天最大 `orders.js` 375）。#1／#2／#3 原樣保留。**誠實記錄的代價**：3,000 行 JS 仍然沒有任何靜態檢查，admin 仍無 CI job——但那的正解是一個 CI job（`tsc --checkJS` ＋ JSDoc，產物仍是原生 ES module、仍無建置步驟），不是一個框架；刻意不加象徵性的 `node --check` job，因為語法錯誤在 E2E 第一個斷言就會炸，那個 job 只會讓人以為 admin 有 CI 了。**若哪天真的要換框架，那必須是一個「內容就是換框架」的獨立 change，不是夾帶在功能裡。**
- **後台路由是 40 行的 hash router**（`admin/lib/router.js`，change admin-commerce-operations design D5）：`#/<view>[/<param>]`。`showTab()` 只寫 `location.hash`，畫面切換只發生在單一的路由處理器裡（兩份真相會立刻漂移）；hash 未變時瀏覽器不發事件，所以導航函式回傳 false 時呼叫端必須自己渲染一次。**hash 而不是 History API**：後台是 nginx 服務的靜態檔，History API 的深層連結需要伺服器 rewrite。深層連結刻意**只涵蓋「這一筆」**（`#/orders/1234`、`#/points/57`），**篩選條件不入 hash**。開機時 hash 指向的視圖必須存在、有權限、且非 `transient`（編輯器類視圖的內容由開啟它的視圖預先填好，重新整理會停在空畫面），否則安靜回落到第一個有權限的視圖——過期的書籤不該長得像故障。
- **點數不是金額，絕不能經過 `formatMoney`**（change admin-commerce-operations design D10）：`admin/lib/money.js` 另有 `formatPoints`／`formatPointsDelta`／`formatPercent`。這條規則存在的理由是兩者之間有真實換算（核發是 `floor(subtotal_amount / 100)`），所以一筆 3,400 的訂單核發 34 點，而把 34 丟進 `formatMoney` 會得到 `NT$34`——數字對、單位錯、看起來完全合理、沒有任何東西會噴錯。訂單詳情上兩者同時出現，E2E 有專門斷言（餘額必須是 `34 點`且不含 `NT$`）。訂單金額三件組必須**組合式**呈現（`小計 ＋ 運費 ＝ 應付總額`），那是唯一能讓商家察覺 `order-extras` 改了 `total_amount` 語意的地方。
- **`shop.manage_client_origins` 的 UI 可見性來源就是 `/shops/{shopID}/me`**（change admin-commerce-operations design D6，已實測 ＋ 整合測試）：它雖是**平台層**節點，但 `rbac.Engine.load` 在商家上下文取角色時是 `shop_id = ? OR shop_id IS NULL`，**平台層角色的權限會被聯集進商家層快照**——所以平台管理員的 `/shops/1/me` 含它、商家 owner 不含，正好對上伺服器的 200／403。**不需要第二個可見性來源。** 已知不精確：伺服器的 `RequirePlatformPermission` 另外要求 `HasPlatformRole`，而 `can()` 不查那個；若有人把該節點授予 merchant scope 的角色（seed 不會），UI 會多畫一顆按下去回 403 的按鈕——體驗層不是安全層。
- **商家訂單列表的篩選一律在伺服器端**（change admin-commerce-operations design D3／D4）：`GET /admin/shops/{shopID}/orders` 除既有 `status` 外，新增選填 `payment_status`／`fulfillment_status`／`delivery_from`／`delivery_to`／`q`（皆為 additive，不帶時查詢逐行相同）。**不得在前端過濾**——列表是分頁的，對當頁過濾會回報「今天沒有要出的貨」而其實有一筆在第 3 頁。日期區間篩的是 **`requested_delivery_date`（Postgres `date`，無時區歧義）而不是 `created_at`**（`shops` 沒有時區欄位，「某一天的訂單」在伺服器上無法無歧義定義）；**未指定配送日的訂單在帶任一日期界限時一律被排除**（NULL 比較為假），UI 必須明示這件事。`q` 對 `shipping_address->>'recipient_name'`／`->>'phone'` 做 ILIKE，數字時另比對訂單編號；實作用 `sql.P` + `Builder.Arg`（**`sql.ExprP` 的 `?` 不會被改寫成 Postgres 的 `$N`，會 500**）。
- **顧客名冊與它的 PII 邊界**（change member-directory，補上 `admin-commerce-operations` design D7 刻意留下的缺口）：`GET /api/v1/admin/shops/{shopID}/members{,/{memberID}}`，守衛是**新的 `member.view` 節點**（授予 `merchant_owner` 與 `editor`——授予 editor 的理由不是慷慨，是它經由 `order.view` 已經看得到更寬的 PII：每筆訂單的收件人姓名、電話、完整地址，且同樣分頁可翻。擋不住的界線比沒有界線更糟）。`GET /shops/{shopID}/me` 由權限目錄推導，新節點 seed 後自動被回報，`admin/lib/rbac.js` 不必改。**回應的設計內容有一半是它的省略**：不含 `password_hash`（`registered` 是它唯一的布林投影）、**不含 `phone`**（全樹沒有任何生產路徑寫入 `members.phone`，交付一個恆為空的欄位就是 `tier-discount-application` D1 診斷過的錯誤重演；電話在 `orders.shipping_address`，訂單列表的 `q` 已經能用它找人）、**不含訂單數／終身消費額**（口徑未定：含不含已取消／未付款／運費／折抵，而一個看起來權威的錯數字比沒有數字更糟——那是報表不是欄位）。`q` 是 email 子字串 ＋ 數字比對 id：**列舉面是列表端點本身而不是模糊搜尋**（分頁名冊本來就可以逐頁讀完），只給精確比對是劇場。**刻意不做匯出鍵**——`member-identity-per-shop` 的「可匯出」講的是平台層資料可攜性（租戶搬遷／刪除／個資請求），不是後台按鈕；真要做的前置是**後台讀取的審計軌跡**（今天沒有）與一個比 `member.view` 更窄的 `member.export`。**訪客身分（`password_hash IS NULL`）必須被如實標示**：他們無法登入、從未註冊、從未同意成為任何名單的一員，所以 UI 用「顧客」而不是「會員」，畫面明寫這件事，且**不提供依 `registered` 批次篩選的參數**（那個參數最可能的第一個用途正是我們剛勸阻的動作）。`password_hash` 不外洩的證明落在**回應位元組**（整合測試 ＋ e2e 各一份，連 bcrypt 前綴一起比對），不是讀程式碼——ent 的 `Sensitive()` 與手寫序列化兩層都成立，但兩層都可能被日後一行程式碼推翻。**只新增 `member.view` 一個節點**：`member.edit` 沒有消費者（平台沒有任何寫入 `members` 的後台端點），沒有消費者的節點不新增。訂單側只加一個選填 `member_id`，讓「這位顧客買過什麼」複用既有訂單列表，而不是在顧客端點長出第二份訂單序列化（訂單形狀三個 change 動過三次，兩份會漂移）。**跨商家有兩個答案**：無權存取該商家 → **403**（cross-shop guard，handler 之前）；有權存取但 member id 屬於他店 → **404**（顯式 `shop_id` 條件查不到；說「不存在」比說「你沒權限碰那個」洩漏更少）。
- **顧客詳情不擁有點數**（change member-directory design D9）：`#/members/{id}` 只顯示唯讀餘額與等級加一顆連往 `#/points/{id}` 的按鈕。**調整表單與流水帳只在點數視圖**——複製過去會是同一個小元件的第三次重寫（tripwire #2），而調整後要同步餘額／等級／流水帳三處正是 tripwire #1 描述的東西；產品上點數也只該有一條寫入路徑與一條稽核軌跡。E2E 有一條斷言驗「顧客詳情 panel 內零個 `input`、零個 `form`」。
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
