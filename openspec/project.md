# Project Context

> **方向更新（ADR 0001，2026-11-24，同日修正）**：平台定位為 headless 商務 API ＋ 平台後台 ＋ 平台官網；商家店面與後台由 merchant template 建立。下文的**主題／託管渲染（Next.js 店面，含「主題店面電商前台選項 C」）／site 網域對應**皆**已棄用、不會實作**；退役見已核准的 change `retire-hosted-storefront-and-themes`（直接移除，無棄用期），官網見 change `platform-website`。**CMS 的內容功能（頁面與全域內容、消毒、渲染 bundle 的公開讀取與快取）不在棄用之列**，會改造成 headless 內容儲存（change `headless-content-store`，提案、未實作）。
## Purpose

多網站電商 SaaS。Phase 1（`phase1-multitenant-cms-foundation`）交付：多租戶資料模型與路由、shop 範圍化 RBAC、雙軌 JWT 認證、Schema/Payload 解耦的 CMS 引擎、前台渲染管線（Redis 版本化快取 + 事件失效）、Next.js SSR storefront 與薄殼 admin。Phase 2 起疊加電商核心（商品、訂單、金物流）。

## Tech Stack

- **API**（`/api`，Go 1.26）：chi v5、ent v0.14（+ Atlas diff 引擎產 golang-migrate 格式 versioned migrations）、golang-jwt/jwt v5、santhosh-tekuri/jsonschema v6、go-redis v9 + x/sync/singleflight、alexedwards/argon2id、log/slog
- **DB/快取**：PostgreSQL 16（jsonb、UNIQUE NULLS NOT DISTINCT、partial unique indexes）、Redis 7（cache-only，可降級）
- **Storefront**（`/web`）：Next.js 15 App Router + React 19 + TypeScript；json-schema-to-typescript 由主題 schema 產型別
- **Admin**（`/admin`）：無建置步驟靜態 ES modules SPA
- **E2E**（`/e2e`）：Playwright (Chromium)
- **開發環境**：docker compose（`make up`）；指令一覽見根目錄 `Makefile` 與 `CLAUDE.md`

## Project Conventions

### Code Style

- Go：gofmt + golangci-lint（`api/.golangci.yml`，standard 集）；錯誤 envelope 統一走 `internal/httpx`（design D12）
- 資料表欄位 snake_case 英文；文件與 spec 正體中文
- ent 生成碼（`api/internal/ent`，除 `schema/`）不手改；`go generate ./internal/ent` 重生

### Architecture Patterns

- 公開 API 一律 `/api/v1`，v1 只做 additive 變更（design D13）
- 租戶隔離：`internal/tenant` interceptor/hook 於 `database.Open` 全域註冊，tenant ctx 下自動附加 `shop_id` predicate；後台目標 shop 一律取自 URL + RBAC，不信任 payload
- RBAC 三層判定（覆蓋→角色聯集→拒絕）＋ Redis snapshot 快取（`authz:user:{id}:shop:{sid}`，寫入時失效）
- 快取失效集中於 `internal/events` 領域事件（發佈於交易成功後）；渲染快取版本化 key + 整租戶 bump
- 主題＝資料（JSON Schema + sections 組合）＋前端元件庫（`web/themes/registry.ts`）；hydration 補 default、剔除未定義鍵與未知區塊
- **主題模式店面的電商前台：採混合式（選項 C），但尚未實作**（2026-08-01 決定，無 change、無程式碼）。電商路由（商品列表／詳情／購物車／結帳）由主題提供**獨立的前端路由**、直接呼叫電商 API；行銷頁維持 CMS section 管線，並可嵌入「精選商品」這類電商 section。理由與三個不同構之處：①路由——CMS 是 slug→單筆 `pages` 列，商品詳情是一條路由服務 N 個商品；②快取——渲染快取是 `cache:shop:{id}:v{ver}:page:{slug}`／TTL 24h／發佈頁面才 bump 版本，商品價格與庫存變動不會使其失效，**商品頁若進此快取會顯示最長 24 小時前的庫存**；③狀態——購物車與結帳是互動狀態，塞不進宣告式 section。實作前 MUST 先決定「商品資料是否進渲染快取」，否則會做出顯示過期庫存的店面。**目前零消費者**（ONDO 是自帶前端、不碰 `/web`，無任何走主題模式的電商商家），故刻意延後至 ONDO 上線後、以其實戰驗證過的流程為藍本移植

### Testing Strategy

- 單元測試無外部依賴（`make test`）；整合測試以 `INTEGRATION=1` gate、共用 test DB 串行執行（`make test-int`）
- specs 的 WHEN/THEN 場景即驗收：RBAC 判定矩陣、token 隔離矩陣、hydration/section 補全、多租戶解析與隔離皆有對應測試
- E2E 兩支共用 `e2e/lib.mjs`（單一失敗計數器）：`happy-path.mjs` 為總入口（瀏覽器 CMS 流程 ＋ 電商鏈），`commerce-path.mjs` 為無瀏覽器、可重複執行的電商子集
- **`internal/app/wire.go` 的接線完整性由 CI 內的三道測試守著**（change contract-router-completeness，capability `wiring-integrity`）：`internal/app/wiring_integration_test.go` 跑真正的 `app.wire`，⑤遞迴 nil 稽核 ⑥生產 router 與契約 router 逐條比對 ⑦事件訂閱集合相等比對。此前每支 integration test 都自建 dispatcher，刪掉 `wire.go` 的訂閱不會讓任何 Go 測試轉紅（commerce-demo-seed design D1 已實測，該洞現已關閉）。分工：CI 答「有沒有接」，e2e 答「接上之後行為對不對」——改動事件鏈或 wire 後仍 MUST 跑 `commerce-path.mjs`
- **API 契約由四道測試守著**（change openapi-contract）：`api/openapi/openapi.yaml` 與實作的一致性不靠自律。①路由覆蓋（`chi.Walk` 對真實 router 雙向比對，文件有而 Walk 沒有的必須通過**實際發請求的可達性探測**——`chi.Walk` 對 `/shops/{shopID}` 這種「子 router 與方法處理器同樣板」有已知盲點）②金額欄位必須 `$ref` 單一的 `MinorUnitAmount` ③真實回應以契約 schema 驗證（`santhosh-tekuri/jsonschema/v6`，取樣原則：ONDO 路徑 100% ＋ 每個共用 schema 至少一次 ＋ 401/404/422 各一次）④v1 表面鎖。①②④免基礎設施故 `make test` 即涵蓋，③在 `make test-int`；**四道全部靠既有的 `go test ./...` 進 CI，`ci.yml` 未因此改動**
- demo seed 是可測的契約而非佈景（spec `demo-seed`）：`internal/seed` 的整合測試守著佈建完整性、冪等性、以及「等級門檻必須能被 seed 目錄的單筆訂單跨過」這條內部一致性不變式

### Git Workflow

單一 `main` 分支，每個 change 一個 commit（訊息格式：`Add <摘要>: <細節> (change <name>)`）。CI：`.github/workflows/ci.yml`（api lint + **unit + integration** test、web typecheck+build）；**e2e 不在 CI 內**，故任何需要進 CI 的檢查都必須寫成 Go 測試。

## Domain Context

- shop＝租戶；site↔shop 多對多，`path_prefix` 消歧、`is_primary` 主網域；不由平台託管的前端（商家自帶靜態站／SPA／原生 APP）以 `X-Publishable-Key` 宣告所代表的商家（change storefront-client-auth），`X-Site-Domain` 僅存活於渲染 bundle 端點
- 後台 `users`（RBAC）與前台 `members`（每商家獨立身分，change member-identity-per-shop）完全分離，JWT 金鑰/audience 隔離
- 頁面以 `type_key` 動態綁定當前主題頁型（換版免遷移）；draft（`content_json`）/published（`published_json`）兩態
- 渲染 bundle 為用戶端無關 SDUI 契約：web SSR 與未來原生 APP 消費同一結構，未知區塊型別必須優雅略過

## Important Constraints

- PostgreSQL 15+ 才有 UNIQUE NULLS NOT DISTINCT（環境鎖定 PG16）
- Redis 僅作 cache：所有快取路徑皆有 DB fallback，Redis 故障是 degrade 不是 down
- jsonb 不保留物件鍵順序：schema 驅動表單的欄位順序非撰寫順序（如需固定順序需另訂 x-editor 排序約定）
- `members.email/phone` 唯一性以商家為界（`UNIQUE(shop_id, email/phone)`，change member-identity-per-shop），註冊/登入的**失敗**回應不得洩漏原因；可由回應區分的邊界是「該 email 於本店是否已設定密碼」（change guest-checkout design D5）
- publishable key 是公開識別碼不是憑證：明文儲存、可完整顯示、MUST NOT 作為授權依據（change storefront-client-auth design D1）
- 扣庫存只有一份實作：任何下單入口 MUST 呼叫 `order.Service.Checkout`（change guest-checkout design D1）；訂單存取 token（`aud=order:{shop_id}`）只能觸及它指名的那一筆訂單，MUST NOT 演化成 session
- 訂單金額：`total_amount = subtotal_amount + shipping_fee`（DB CHECK 強制，change order-extras design D6/D7），`total_amount` 是**應付總額**也是請款金額；**點數以 `subtotal_amount` 核發，運費不計點**（design D5）
- D13 的 v1 破例窗口已在 change order-extras 用盡並關閉：後續破壞性變更 MUST 走新端點或 `/api/v2`，MUST NOT 再引用「尚無外部消費者」
- **`/api/v1` 的介面契約是 `api/openapi/openapi.yaml`**（change openapi-contract）：改端點 MUST 同步改它。`api/openapi/v1-surface.txt` 是 D13 additive-only 的機械化強制——**刪掉任一行即為破壞 v1**，且該刪除 MUST 出現在 code review 的 diff 中。契約與實作不一致時的權威順序：`openspec/specs/` > 實作 > `openapi.yaml`；契約 MUST NOT 成為執行期的第二權威（不參與 request 驗證、回應過濾或授權判定）
- 回傳集合的端點 MUST 明訂順序，MUST NOT 交給資料庫決定（change commerce-demo-seed design D8）：商品的 SKU 集合依 id 遞增，公開與後台一致

## External Dependencies

- 無外部 SaaS 依賴；本機/CI 皆以 docker compose 提供 PostgreSQL 16 + Redis 7
- Go module proxy 與 npm registry（建置時）
