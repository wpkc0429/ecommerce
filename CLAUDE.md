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

cd web && npm run gen:types   # 由 starter schema JSON 重新產生 TS 型別
cd e2e && node happy-path.mjs # 瀏覽器 E2E（需三個服務 + seed-demo 後的乾淨狀態）
```

單一整合測試：`cd api && INTEGRATION=1 go test ./internal/httpapi/ -run TestDraftPublish -count=1`

## Architecture notes（與 openspec 對應）

- **設計權威**：`openspec/changes/phase1-multitenant-cms-foundation/design.md`（D1–D13）；行為驗收：同目錄 `specs/*/spec.md`。
- **ent schema** 在 `api/internal/ent/schema/`；改動後 `go generate ./internal/ent`，再 `make migrate-gen`。UNIQUE NULLS NOT DISTINCT 等 ent 無法表達的約束在 `api/migrations/` 手寫檔。
- **租戶隔離**：`internal/tenant` 的 interceptor/hook 在 `database.Open` 全域註冊——tenant ctx 下所有租戶資料查詢自動加 `shop_id`。
- **快取失效**：一律走 `internal/events` 領域事件（handler 不得直呼 Redis）；渲染快取 key 版本化（`cache:shop:{id}:v{ver}:page:{slug}`）。
- **錯誤格式**：`internal/httpx`（D12 統一 envelope）；公開 API 一律掛 `/api/v1`（D13，v1 只做 additive 變更）。
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
