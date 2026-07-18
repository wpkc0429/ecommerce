# Tasks: phase1-multitenant-cms-foundation

## 1. 專案骨架（M0）

- [x] 1.1 建立 monorepo 佈局：`/api`（Go module）、`/web`（storefront）、`/admin`（薄殼 SPA）、根目錄 `docker-compose.yml`
- [x] 1.2 docker-compose 提供 PostgreSQL 16 與 Redis 7（含健康檢查、本機開發環境變數樣板 `.env.example`）
- [x] 1.3 `/api` 初始化：chi router、slog JSON 日誌、設定載入（env）、`/healthz` 端點、統一錯誤回應格式（design D12）、公開 API 一律掛載 `/api/v1` 前綴（design D13）
- [x] 1.4 建立 Makefile／task runner：`make dev`、`make test`、`make lint`（golangci-lint）、`make migrate`

## 2. 資料層（M1）

- [x] 2.1 以 ent 定義全部 schema（design D2）：shops、sites、site_shop、themes、theme_pages、pages、users、shop_user、roles、permissions、role_user、role_permission、user_permission、members、shop_member、user_refresh_tokens、member_refresh_tokens
- [x] 2.2 補齊 ent 無法表達的約束（Atlas migration 手寫）：`site_shop` 三個 partial unique、`role_user`/`user_permission` 的 UNIQUE NULLS NOT DISTINCT、SMALLINT CHECK
- [x] 2.3 產生並驗證 Atlas versioned migrations（up/down 可往返）
- [x] 2.4 Seed 指令：權限目錄（`shop.*`、`page.*`、`user.*`、`theme.*`）、平台 `super_admin` 角色與初始平台管理員、demo 主題 `starter`（home/about/landing_page 三頁型；page_schema 採 `sections` 組合式結構、config_schema 含 `tokens` 區段，schema 含 default 與 x-editor 標註，design D6）
- [x] 2.5 ent privacy/interceptor：租戶擁有 entity 強制附加 `shop_id` predicate（specs/multi-tenancy「Tenant data isolation enforcement」的自動化測試一併完成）

## 3. 認證（M2）

- [x] 3.1 Argon2id 密碼雜湊模組（alexedwards/argon2id，含參數設定與單元測試）
- [x] 3.2 JWT 雙體系模組：admin（`aud=admin`）與 member（`aud=shop:{id}`）各自 secret/issuer，簽發與驗證 middleware
- [x] 3.3 Refresh token 輪替與撤銷（rotation、重放偵測撤銷整鏈、登出撤銷；user 與 member 兩套表）
- [x] 3.4 後台 auth API：login／refresh／logout（含停用帳號拒絕、不洩漏失敗原因）
- [x] 3.5 會員 auth API（商家上下文）：register／login／refresh／logout（既有 members 掛新 shop_member、回應同構防列舉）
- [x] 3.6 認證整合測試：token 隔離矩陣（admin/member × 正確/錯誤 aud × 跨店）

## 4. RBAC（M3）

- [x] 4.1 三層判定引擎：user_permission 覆蓋（含 shop 層與平台層）→ role 聯集 → 拒絕（specs/rbac 判定矩陣單元測試全覆蓋）
- [x] 4.2 Redis 權限快取 `authz:user:{id}:shop:{shop_id}`（TTL 10 分鐘）＋ 角色/權限/覆蓋寫入時失效
- [x] 4.3 授權 middleware：`RequirePermission(node)` ＋ 跨商家操作前置檢查（非平台角色須為該店成員）
- [x] 4.4 角色管理 API：商家層角色指派/移除（限自店）、平台層角色與權限目錄管理（限平台管理員）

## 5. 多租戶路由（M4）

- [x] 5.1 站點解析 middleware：`X-Site-Domain` 標頭優先、否則 `Host`（App 通道，design D5）→ 小寫正規化 → sites 查詢 → 未知網域 404
- [x] 5.2 path_prefix 最長前綴匹配與剝離、預設商家 fallback、無預設 404；shop status gating（0→503、2→404）
- [x] 5.3 路由快取 `route:{host}`（TTL 5 分鐘）＋ sites/site_shop 異動失效
- [x] 5.4 sites／site_shop 管理 API（平台端）：網域綁定 CRUD、is_primary 與 path_prefix 約束錯誤轉 422
- [x] 5.5 多租戶整合測試：多網域/多前綴/預設商家/停用商家情境

## 6. CMS 引擎核心（M5）

- [x] 6.1 JSON Schema 驗證模組：metaschema 驗證（主題匯入）＋ payload 校驗（422 附 JSON Pointer details）
- [x] 6.2 Hydration 模組：default 補全（物件遞迴、一般陣列整體採 payload、section 陣列逐項依區塊型別 schema 補全）＋ 未定義鍵與未知區塊型別剔除（含主題升級/換版殘留鍵測試）
- [x] 6.3 主題管理 API（平台端）：themes/theme_pages CRUD、is_active 控制、更新觸發全商家 version bump
- [x] 6.4 商家換版 API：is_active 檢查、type_key 相容性預檢清單、切換後租戶 version bump
- [x] 6.5 頁面 CRUD API：type_key 對當前主題驗證、slug 正規化/保留字/同店唯一、SEO meta
- [x] 6.6 草稿/發佈流：存檔寫 content_json、發佈複製至 published_json＋status=1＋單頁快取失效、下架、建店自動建立 home 頁
- [x] 6.7 商家全域內容 API：config_schema 校驗、存檔即生效＋租戶 version bump

## 7. 渲染 API 與快取（M5）

- [x] 7.1 渲染 bundle 組裝：shop 全域 hydrated content ＋ page hydrated published_json ＋ theme/layout/component key（design D8 結構）
- [x] 7.2 Redis read-through：版本化 key、TTL 24h、singleflight、Redis 故障降級直查 DB
- [x] 7.3 領域事件失效層：PagePublished/PageUnpublished（DEL 單頁）、ShopContentUpdated/ThemeSwitched/ThemeUpdated/SiteMappingChanged（version bump，主題更新批次掃 shops.theme_id）
- [x] 7.4 Preview 端點：短效 preview token、輸出工作副本、不寫快取、驗證頁面讀取權限
- [x] 7.5 渲染整合測試：快取命中/失效/發佈後即時性/降級

## 8. Storefront SSR（M6）

- [x] 8.1 Next.js（或定案之 Nuxt）骨架：catch-all route 依 Host+path 呼叫渲染 API、404/503 維護頁
- [x] 8.2 starter 主題元件庫：通用 SectionRenderer（未知區塊型別優雅略過，design D8）＋ 區塊元件庫（props 對應各區塊 schema）、layout 以 CSS variables 消費 `tokens`（header/footer/logo 吃 shop.content）
- [x] 8.3 SEO：以 `page.seo` 產生 title/meta 標籤
- [x] 8.4 由 schema 產出 TS 型別（json-schema-to-typescript）並接入元件 props

## 9. Admin 薄殼（M7）

- [x] 9.1 登入頁與 token 管理（refresh 自動換發）
- [x] 9.2 頁面清單/建立/編輯：JSON Schema 驅動表單（依 x-editor 渲染控件）、422 錯誤定位顯示
- [x] 9.3 發佈/下架/預覽動作、商家全域內容編輯頁

## 10. 端到端驗證與收尾

- [x] 10.1 E2E happy path：建店（seed）→ 綁網域 → 編輯全域內容與首頁 → 發佈 → 前台渲染 → 換版 → 前台反映新主題
- [x] 10.2 併發讀取煙霧測試：快取命中率與 singleflight 行為確認
- [x] 10.3 回填 `CLAUDE.md`（實際 build/lint/test 指令、架構概覽）與 `openspec/project.md`
- [x] 10.4 CI：lint + test（api 與 web）
