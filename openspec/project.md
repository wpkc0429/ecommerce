# Project Context

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

### Testing Strategy

- 單元測試無外部依賴（`make test`）；整合測試以 `INTEGRATION=1` gate、共用 test DB 串行執行（`make test-int`）
- specs 的 WHEN/THEN 場景即驗收：RBAC 判定矩陣、token 隔離矩陣、hydration/section 補全、多租戶解析與隔離皆有對應測試
- 瀏覽器 E2E：`e2e/happy-path.mjs`（admin 編輯→發佈→前台渲染→換版）

### Git Workflow

尚未初始化 git repo（由使用者決定時點）。CI 範本：`.github/workflows/ci.yml`（api lint+test、web typecheck+build）。

## Domain Context

- shop＝租戶；site↔shop 多對多，`path_prefix` 消歧、`is_primary` 主網域；`X-Site-Domain` 標頭為原生 APP 通道
- 後台 `users`（RBAC）與前台 `members`（平台級身分 + `shop_member` 會籍）完全分離，JWT 金鑰/audience 隔離
- 頁面以 `type_key` 動態綁定當前主題頁型（換版免遷移）；draft（`content_json`）/published（`published_json`）兩態
- 渲染 bundle 為用戶端無關 SDUI 契約：web SSR 與未來原生 APP 消費同一結構，未知區塊型別必須優雅略過

## Important Constraints

- PostgreSQL 15+ 才有 UNIQUE NULLS NOT DISTINCT（環境鎖定 PG16）
- Redis 僅作 cache：所有快取路徑皆有 DB fallback，Redis 故障是 degrade 不是 down
- jsonb 不保留物件鍵順序：schema 驅動表單的欄位順序非撰寫順序（如需固定順序需另訂 x-editor 排序約定）
- `members.email/phone` 全平台唯一為刻意設計，註冊/登入回應不得洩漏跨店存在性

## External Dependencies

- 無外部 SaaS 依賴；本機/CI 皆以 docker compose 提供 PostgreSQL 16 + Redis 7
- Go module proxy 與 npm registry（建置時）
