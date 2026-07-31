# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

多網站電商 SaaS 的 Phase 1 基礎：多租戶架構、shop 範圍化 RBAC、Schema/Payload 解耦的 CMS 引擎與前台渲染管線。Monorepo：

- `/api` — Go headless API（唯一資料權威）：chi router、ent + versioned migrations、JWT 雙體系認證、RBAC、多租戶解析、CMS/渲染/快取
- `/web` — Next.js (App Router) storefront SSR：以渲染 bundle API 取得資料，主題＝前端元件庫（`themes/registry.ts`）
- `/admin` — 無建置步驟的薄殼 SPA（靜態 ES modules）：登入、schema 驅動的內容編輯、發佈/預覽
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
cd e2e && node happy-path.mjs    # 全部 E2E：瀏覽器 CMS 流程 ＋ 電商鏈（需三個服務 + seed-demo 後的乾淨狀態）
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
- **快取失效**：一律走 `internal/events` 領域事件（handler 不得直呼 Redis）；渲染快取 key 版本化（`cache:shop:{id}:v{ver}:page:{slug}`）；金鑰／origin 快取（`pkey:{key}`、`corsorigin:{origin}`）由 `StorefrontClientChanged` 與 `SiteMappingChanged` 失效。
- **錯誤格式**：`internal/httpx`（D12 統一 envelope）；公開 API 一律掛 `/api/v1`（D13，v1 只做 additive 變更）。**破例窗口已於 change order-extras 用盡並關閉**：後續變更不得再以「尚無外部消費者」為由破壞 v1，破壞性變更請走新端點或 `/api/v2`。
- **API 契約是被測試守著的交付物**（change openapi-contract）：`api/openapi/openapi.yaml`（OpenAPI 3.1，手寫，涵蓋全部 **111** 條路由）。**改任何端點必須同時改它**，否則 `go test ./...` 轉紅。四道檢查全在 `internal/httpapi` 的普通 Go 測試裡，因此自動進 CI（`ci.yml` 無需改動）：①路由覆蓋（`chi.Walk` 雙向）②金額欄位必須 `$ref` `MinorUnitAmount` ③真實回應以契約 schema 驗證（整合層）④`api/openapi/v1-surface.txt` 表面鎖——**刪掉鎖檔任一行＝破壞 v1**，是一個必須人工執行、且在 diff 中可見的動作。**已知：`chi.Walk` 列不到 `GET/PUT /api/v1/admin/shops/{shopID}`**（同一樣板上既有子 router 又有方法處理器），檢查 ① 以實際發請求的可達性探測吸收此盲點——新增端點時若發現它沒出現在 Walk 清單裡，就是踩到了這個坑。契約與實作不一致時**實作是權威**（除非實作違反 `openspec/specs/`）。自帶前端的接入說明在 `docs/storefront-integration.md`（**活文件**，不要再往 archive 的 tasks.md 裡加接入資訊）。
- **事件接線只由 e2e 覆蓋**（change commerce-demo-seed）：`payment`／`shipping`／`points` 互不 import，「誰訂閱了誰」只是 `internal/app/wire.go` 的兩行 `dispatcher.Subscribe`，而**該檔零 Go 測試覆蓋**（每支 integration test 都自建 dispatcher 並註明 mirrors wire.go）。刪掉 `dispatcher.Subscribe(pointsService.Handle)`，`make test-int` 仍全綠而真實伺服器靜默不發點數——**已實測**。唯一守著它的是 `e2e/commerce-path.mjs`，改動 `wire.go` 或事件鏈後請務必跑它。
- **demo seed 的固定識別值**（change commerce-demo-seed，皆僅 `-demo`）：publishable key `pk_demoseedstorefrontkeynotsecret`（固定值；key 是識別碼不是憑證，且 demo 商家本來就能以 `Host: demo.localhost` 免金鑰解析）、origin `http://demo.localhost:3000`、顧客 `demo-customer@example.com` / `demo-customer-change-me`、商家 owner `demo-owner@example.com` / `demo-owner-change-me`。目錄含 ONDO 形狀的花藝品項（3200–4400，S/M/L）、四筆運送方式（**一筆刻意停用**）、三級等級（0／30／100）。**等級門檻與花藝定價互相耦合**：一筆單品訂單必須跨得過 30 點門檻，否則等級評估在 demo 上恆定回同一答案——此不變式由 `internal/seed` 的整合測試守著，改任一邊前先看它。
- **主題 schema 來源**：`api/internal/seed/schemas/starter/*.json` 同時驅動後端驗證/hydration 與 `web` 的 TS 型別（`npm run gen:types`）。
- 注意：Postgres jsonb 不保留物件鍵順序——admin 表單欄位順序非 schema 撰寫順序（Phase 1 已知現象）。

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
