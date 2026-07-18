# Proposal: phase1-multitenant-cms-foundation

## Why

建立多網站電商 SaaS 的第一階段基礎：多租戶架構、RBAC 權限與 Schema/Payload 解耦的 CMS 引擎。目前 repository 為空，本 change 依據 PRD V1.0（2026-07-18）及架構審查後定案的修正（頁面以 `type_key` 動態解析頁型、RBAC 依 shop 範圍化、site↔shop 多對多以 `path_prefix` 消歧、技術棧採 Go Headless API + Next.js/Nuxt SSR），一次奠定後續電商模組（Phase 2：商品、訂單、金物流）賴以擴充的地基。

## What Changes

- 建立全新 codebase：Go JSON API（headless 後端）+ PostgreSQL + Redis，前台由 Next.js/Nuxt SSR 服務渲染，主題實作為前端元件庫（`view_path`/`template_file` 為元件代碼而非後端模板路徑）。
- 多租戶資料模型與路由：`sites`、`shops`、`site_shop`（多對多，含 `path_prefix` 消歧與 `is_primary` 主網域），middleware 由 Host + 路徑最長前綴解析出唯一 `shop_id` 進入請求上下文；所有租戶資料查詢強制掛載 shop 範圍。
- RBAC 權限系統：`roles`/`permissions`/`role_user`/`role_permission`/`user_permission`，其中 `role_user` 與 `user_permission` 依 PRD 審查修正**增加 `shop_id`**（NULL = 平台層級），三層判定順序為個人覆蓋（含強制剝奪）→ 角色聯集 → 拒絕。
- CMS 引擎：`themes`（`config_schema`）、`theme_pages`（`page_schema`）以**標準 JSON Schema（draft 2020-12）**定義結構；`pages` 以 `type_key` 綁定頁型（換版即時生效、免資料遷移）、儲存 JSONB payload、寫入時後端校驗（422）、讀取時依 schema 預設值 hydration 回填；頁面具 draft/published 狀態。
- 前台渲染管線：單一渲染 API 回傳「商家全域 payload + 頁面 payload + 主題元件代碼」，以 `cache:shop:{shop_id}:page:{slug}` 為 key 的 Redis 一條龍快取（TTL 24h），內容更新、換版、主題升級時事件驅動主動失效（含依 `shops.theme_id` 的批次失效）。
- 帳戶體系：後台 `users`/`shop_user` 與前台 `members`/`shop_member`（跨店會員邏輯隔離、點數欄位預留），JWT 認證（後台/前台簽章金鑰完全隔離、access token 短時效 + refresh token、會員 token 綁定 shop audience），密碼採 Argon2id。
- 索引策略修正：以明確的 btree/unique 索引取代 PRD 原定的無差別 JSONB GIN 索引（`pages(shop_id, slug)`、`theme_pages(theme_id, type_key)`、`site_shop` partial unique 等）；GIN 留待實際 JSONB 查詢需求出現時再建。

## Capabilities

### New Capabilities

- `multi-tenancy`: 網域/路徑到商家的解析（site↔shop 多對多 + `path_prefix` 消歧、`is_primary` 主網域）、請求租戶上下文、租戶資料隔離強制與未匹配/停用商家的回應行為。
- `rbac`: shop 範圍化的角色與權限模型、三層權限判定（個人覆蓋 → 角色聯集 → 拒絕）、平台超級管理員與跨商家操作隔離。
- `authentication`: 後台 users 與前台 members 的註冊/登入、JWT 發行與驗證（金鑰隔離、短時效 access + refresh、會員 token 的 shop audience 綁定）、密碼雜湊、會員跨店關聯（`shop_member`）。
- `theme-system`: 平台端主題與頁型管理、JSON Schema（draft 2020-12）格式的 `config_schema`/`page_schema` 定義與啟用控制。
- `page-management`: 商家頁面 CRUD（`type_key` 綁定、slug 於 shop 內唯一、draft/published）、商家全域內容（`shops.content_json`）編輯、寫入時 Schema 校驗（422）與 SEO meta。
- `content-rendering`: 前台混合渲染 API（全域 + 頁面 payload + 元件代碼）、schema 預設值 hydration 回填、Redis 快取讀取與事件驅動主動失效。

### Modified Capabilities

（無 — `openspec/specs/` 目前為空，本 change 全數為新增能力。）

## Impact

- **程式碼**: 全新建立 — Go API 服務（HTTP router、middleware、ent/pgx 資料層、JSON Schema 驗證、JWT）、PostgreSQL 遷移（goose/atlas）、Redis 快取層、Next.js/Nuxt SSR 前台骨架與主題元件庫介面、後台管理 API。
- **相依套件**: Go（chi 或 echo、pgx/ent、golang-jwt、santhosh-tekuri/jsonschema、argon2id、go-redis）、Node.js（Next.js 或 Nuxt）、Docker Compose 開發環境（PostgreSQL 16+、Redis 7+）。
- **系統邊界**: 電商核心（商品、訂單、金物流）、會員等級（`shop_member.level_id`）、SSL 憑證自動化簽發（僅保留 `ssl_status` 欄位）均不在本 change 範圍，屬後續階段。
- **文件**: 完成後需回填 `CLAUDE.md`（實際 build/lint/test 指令與架構概覽）與 `openspec/project.md`。
