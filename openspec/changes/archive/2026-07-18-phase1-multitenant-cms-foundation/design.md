# Design: phase1-multitenant-cms-foundation

## Context

Repository 目前為空（僅 OpenSpec 鷹架）。本設計依據使用者提供的 PRD V1.0（2026-07-18）與架構審查後定案的四項決策：

1. 技術棧：**Go Headless API + Next.js/Nuxt SSR 前台**（非 Laravel 單體）。
2. 頁面綁定：`pages` 儲存 `type_key`，依商家當前主題**動態解析**頁型（非 FK 直指 `theme_pages`）。
3. RBAC：`role_user`、`user_permission` **增加 `shop_id`** 實現商家範圍化角色（NULL = 平台層級）。
4. site↔shop **維持多對多**，以 `path_prefix` 消歧路由。

另有兩項已確認的前瞻需求（不在 Phase 1 實作範圍，但決定本設計的形態）：主題未來將由 AI 以**純資料**生成（design tokens + 區塊組合，非程式碼）；消費端除 web 外將有 **iOS/Android 雙原生 APP**，以 Server-Driven UI（SDUI）消費同一渲染 bundle。

讀者對象：後端工程師、前端工程師、DBA。資料表欄位一律 snake_case 英文；文件以正體中文撰寫。

## Goals / Non-Goals

**Goals:**

- 多租戶請求解析：Host + 路徑前綴 → 唯一 `shop_id` 上下文，租戶資料隔離在資料存取層強制執行。
- shop 範圍化 RBAC：三層判定（個人覆蓋含強制剝奪 → 角色聯集 → 拒絕）。
- Schema/Payload 解耦 CMS：主題以標準 JSON Schema 定義結構，商家/頁面只存 JSONB payload；寫入校驗（422）、讀取 hydration 回填、草稿/發佈兩態。
- 前台渲染管線：單一渲染 API + Redis 一條龍快取 + 事件驅動主動失效。
- 雙軌認證：後台 users 與前台 members 的 JWT 完全隔離（金鑰、audience、refresh 流程）。
- 可運行的垂直切片：demo 主題 + SSR 前台骨架 + 最小後台編輯 API，端到端驗證整條引擎。

**Non-Goals:**

- 商品、訂單、購物車、金流、物流（Phase 2）。
- 會員等級（`shop_member.level_id` 僅預留欄位）、點數消長邏輯。
- SSL 憑證自動簽發（僅保留 `sites.ssl_status` 欄位與人工更新）。
- 主題市集、佈景視覺化拖拉編輯器（Phase 1 後台編輯器為 schema-driven 表單）。
- 多語系翻譯管理後台（payload 結構支援多語字串，但不做翻譯工作流）。
- 計費/訂閱、Webhook、開放 API。

## Decisions

### D1. 整體架構：Go Headless API + SSR 前台，主題 = 前端元件庫

- **API 服務（`/api`，Go）**：唯一資料權威。提供後台管理 API、前台會員 API、渲染 bundle API。
- **Storefront SSR（`/web`，Next.js 預設，若團隊定案 Nuxt 亦不影響 API 契約）**：依 Host 向 API 取得渲染 bundle，將 `theme.code` + `component_key` 對應到前端主題元件庫渲染頁面。
- **Admin UI（`/admin`，薄殼 SPA）**：登入 + 頁面 CRUD + 由 JSON Schema 驅動的內容編輯表單。Phase 1 僅最小可用。
- **原生 APP（iOS/Android，Phase 2+，本階段不實作）**：以 SDUI 模式消費同一渲染 bundle——section 型別對 APP 即 native 元件註冊表（SwiftUI/Compose），商家改版與頁面編輯不經 App Store 發版即生效。API 形態由 D5（App 通道）、D8（bundle 契約）、D13（版本化）預留。
- PRD 中 Blade 式的 `view_path` / `template_file` 語意改為**前端元件代碼**：`themes.layout_key`、`theme_pages.component_key`（欄位更名，見 D2）。
- **理由**：多租戶 SaaS 長期運行成本與型別安全；PRD 4.3.2 本就定義「渲染 API 或 SSR 服務」，headless 天然相容。放棄的替代方案：Laravel 單體（交付最快但與定案技術棧不符）、Go html/template 單體（主題開發體驗差、生態需自建）。

### D2. 資料模型（含對 PRD 的修正，最終版）

型別約定：主鍵 `BIGINT GENERATED ALWAYS AS IDENTITY`；時間一律 `timestamptz`（PostgreSQL 無 TinyInt，狀態用 `SMALLINT` + CHECK）；JSONB 欄位 `NOT NULL DEFAULT '{}'`（除註明 nullable）。

**租戶與網域**

| 表 | 欄位 | 說明 |
|---|---|---|
| `shops` | id, theme_id(FK themes, NULL), name v(100), status SMALLINT(0停用/1啟用/2審核中), content_json JSONB, meta JSONB, timestamps | content_json = 套用當前主題的全域 payload |
| `sites` | id, domain v(255) UNIQUE(小寫正規化後儲存), ssl_status SMALLINT(0/1/2), meta JSONB, timestamps | |
| `site_shop` | site_id, shop_id, path_prefix v(100) NULL, is_primary bool DEFAULT false, created_at；PK(site_id, shop_id) | 多對多 + 路由消歧，約束見 D5 |

**主題與 CMS**

| 表 | 欄位 | 說明 |
|---|---|---|
| `themes` | id, code v(50) UNIQUE, name v(100), layout_key v(255), config_schema JSONB, is_active bool DEFAULT true, timestamps | code 供前端元件庫對應；layout_key 原 PRD `view_path` |
| `theme_pages` | id, theme_id(FK), type_key v(50), component_key v(255), page_schema JSONB, timestamps；UNIQUE(theme_id, type_key) | component_key 原 PRD `template_file` |
| `pages` | id, shop_id(FK), type_key v(50), title v(150), slug v(150), status SMALLINT(0草稿/1已發佈), content_json JSONB(工作副本), published_json JSONB NULL(發佈快照), meta JSONB(SEO), timestamps；UNIQUE(shop_id, slug), INDEX(shop_id, status) | **無 theme_page_id**（見 D3）；草稿/發佈見 D7 |

**帳戶與 RBAC**

| 表 | 欄位 | 說明 |
|---|---|---|
| `users` | id, email v(150) UNIQUE(小寫正規化), password_hash v(255), status SMALLINT(0/1), meta JSONB, timestamps | |
| `shop_user` | id, shop_id(FK, **NOT NULL**), user_id(FK), created_at；UNIQUE(shop_id, user_id) | 純粹表示商家成員資格；平台超管**不再**以 NULL 列表示（見 D4） |
| `roles` | id INT identity, name v(50), scope v(20) CHECK('platform','merchant'), timestamps；UNIQUE(name, scope) | `scope` 原 PRD `guard_name` |
| `permissions` | id INT identity, name v(100) UNIQUE, description v(255) | 節點如 `page.publish`、`shop.update` |
| `role_user` | user_id(FK), role_id(FK), shop_id(FK, **NULL=平台層級**)；UNIQUE NULLS NOT DISTINCT(user_id, role_id, shop_id) | PG15+ 的 NULLS NOT DISTINCT 避免 NULL 重複列 |
| `role_permission` | role_id, permission_id；PK(role_id, permission_id) | |
| `user_permission` | user_id(FK), permission_id(FK), shop_id(FK, NULL), is_granted bool；UNIQUE NULLS NOT DISTINCT(user_id, permission_id, shop_id) | 個人覆蓋，優先於角色 |

**前台會員**

| 表 | 欄位 | 說明 |
|---|---|---|
| `members` | id, email v(150) NULL UNIQUE, phone v(50) NULL UNIQUE, password_hash v(255) **NULL**(預留社群登入), status SMALLINT(0/1) DEFAULT 1, meta JSONB, timestamps | 平台級單一消費者身分（email/phone 全平台唯一為刻意設計） |
| `shop_member` | id, shop_id(FK), member_id(FK), points INT DEFAULT 0, level_id INT NULL, timestamps；UNIQUE(shop_id, member_id) | 跨店會籍隔離 |

**Token**

| 表 | 欄位 | 說明 |
|---|---|---|
| `user_refresh_tokens` | id, user_id(FK), token_hash v(255) UNIQUE, expires_at, revoked_at NULL, rotated_from BIGINT NULL, created_at | refresh 輪替與撤銷 |
| `member_refresh_tokens` | 同上 + shop_id(FK) | 會員 refresh 綁定商家上下文 |

### D3. 頁面 × 主題：`type_key` 動態解析

`pages` 不持有 `theme_page_id`。渲染與編輯時以（`shops.theme_id`, `pages.type_key`）查 `theme_pages` 取得 `page_schema` 與 `component_key`。

- 換版即時生效、零資料遷移；新主題缺少某 `type_key` 時該頁面回 404 並於後台標示「不受新主題支援」。
- 寫入 `pages` 時驗證 `type_key` 存在於商家當前主題。
- 換版後 payload 與新 schema 的落差由 hydration（D6）吸收：缺欄位補預設值、多餘欄位輸出時剔除。
- `shops.content_json` 同理依當前主題的 `config_schema` 校驗與 hydration；Phase 1 不保留舊主題 payload 快照（換回舊主題需重新編輯，`shop_theme_settings` 副表列為開放問題）。
- **放棄的替代方案**：保留 FK + 換版批次遷移（每次換版需遷移作業，違反「靈活切換」目標）；Phase 1 鎖定主題（直接砍掉核心賣點）。

### D4. RBAC：shop 範圍化 + 三層判定

- 授權判定輸入為（user, permission, shop_ctx）。順序：
  1. `user_permission` 精確匹配（user, permission, shop_ctx）→ 依 `is_granted` 直接允許/拒絕；
  2. 無覆蓋 → `role_user`（shop_id = shop_ctx 或 NULL 平台層）聯集 `role_permission`，含該權限即允許；
  3. 否則 403。
- **平台超級管理員**：持有 `scope='platform'` 角色（`role_user.shop_id IS NULL`）者；平台角色授權對所有 shop_ctx 生效。捨棄 PRD 的「`shop_user.shop_id = NULL` 代表超管」設計，`shop_user` 回歸純成員資格表，少一種 NULL 特例。
- **跨商家隔離**：middleware 驗證操作目標 shop 必須 ∈ 使用者的 `shop_user` 商家集合，或使用者具平台角色。
- 權限解析結果快取於 Redis：`authz:user:{id}:shop:{shop_id}` → 權限集合（TTL 10 分鐘）；任何角色/權限寫入時刪除受影響 key。JWT **不內嵌**權限清單（見 D9）。
- 實作為自建三表邏輯（Go 生態無 Spatie 等價物；且 PRD 的 `is_granted=false` 強制剝奪語意本就需自建）。

### D5. 路由：site↔shop 多對多 + `path_prefix` 消歧

`site_shop` 約束（partial unique index）：

```sql
UNIQUE (site_id, path_prefix)                          -- 同站前綴不重複
CREATE UNIQUE INDEX ... ON site_shop (site_id) WHERE path_prefix IS NULL;  -- 每站僅一個預設商家
CREATE UNIQUE INDEX ... ON site_shop (shop_id) WHERE is_primary;           -- 每商家僅一個主網域
```

解析流程（middleware）：

1. `Host` 小寫正規化 → 查 `sites.domain`，未命中 → 404。
2. 取該 site 的全部 `site_shop` 列，對請求路徑做**最長前綴匹配**（`path_prefix` 正規化為 `/x` 形式、無尾斜線）；無任何前綴命中 → 落到 `path_prefix IS NULL` 的預設商家；連預設都沒有 → 404。
3. 命中前綴時，該前綴自路徑剝離後才進入頁面 slug 解析（`/brand-a/about` → shop A 的 `about`）。
4. `shops.status`：0 停用 → 503（SSR 渲染維護頁）；2 審核中 → 404；1 才放行。
5. Host→(site, mappings) 解析結果快取 `route:{host}`（Redis，TTL 5 分鐘 + `sites`/`site_shop` 寫入時失效）。
6. **App 用戶端通道**：請求含 `X-Site-Domain` 標頭時，以其值（同樣小寫正規化）取代 Host 進入同一條解析流程——品牌 APP 於 build config 寫死站點、平台聚合 APP 由使用者選店後帶入。不新增安全風險：租戶隔離依資料層 scope 與 token `aud` 綁定，本就不依 Host 可信度；`route:{host}` 快取鍵一體適用。
- 租戶隔離強制：解析出的 `shop_id` 注入 request context；資料層以 ent 的 privacy/interceptor 對所有租戶擁有的 entity 強制附加 `shop_id` predicate（對應 PRD 4.1.2 的 ORM global scope），後台 API 的 shop 範圍由 RBAC 上下文決定、不信任 client 傳入值。

### D6. Schema 格式：標準 JSON Schema draft 2020-12 + hydration

- `themes.config_schema`、`theme_pages.page_schema` 一律為合法 JSON Schema（draft 2020-12），主題匯入/更新時先以 metaschema 驗證 schema 本身。
- 平台撰寫規範：物件層級設 `additionalProperties: false`；每個葉節點提供 `default`；以 `x-editor` 擴充關鍵字標註後台表單控件（如 `image`、`richtext`、`color`），admin UI 據此渲染編輯表單——同一份 schema 同時驅動驗證與編輯器。
- **組合式頁面結構（官方主題規範）**：`page_schema` 的主體為 `sections` 陣列 + 區塊型別（`oneOf` + `type` 辨識欄位），`config_schema` 含 `tokens` 區段（色彩、字體、間距等 design tokens；web 以 CSS variables、APP 以主題屬性消費）。如此「新主題」成為純資料——這是 AI 生成主題的形態基礎，也是 web 與原生 APP 共用的 SDUI 契約。
- **寫入**（後台存檔）：以 santhosh-tekuri/jsonschema 驗證 payload，失敗回 422，錯誤附 JSON Pointer 路徑（如 `/banner/images/0/src`）。
- **讀取**（hydration）：遞迴走訪 schema——缺漏欄位以 `default` 補全；物件遞迴合併；一般**陣列以 payload 整體為準**（不做逐元素合併），惟 **section 陣列例外**——每一項依其 `type` 對應的區塊 schema 補全 default；schema 未定義的多餘鍵與未知區塊型別在輸出時剔除（防舊主題殘留鍵與注入）。對應 PRD 4.3.3。
- Schema 可由 CI 以 json-schema-to-typescript 產出 TS 型別給前端主題元件庫，收斂前後端漂移。

### D7. 草稿/發佈：工作副本 + 發佈快照

- 編輯存檔只寫 `content_json`（工作副本），不影響線上。
- 「發佈」動作：校驗通過後將 `content_json` 複製到 `published_json`、`status=1`、觸發快取失效。
- 前台渲染一律讀 `published_json`；`status=0` 或 `published_json IS NULL` 的頁面前台 404。
- 後台預覽：帶短效 preview token 的渲染 API 變體回傳工作副本 bundle（不進快取）。
- `shops.content_json`（全域 payload）Phase 1 不做草稿、存檔即生效（全域內容變更頻率低；如需草稿留待後續）。
- 保留 slug 保留字：`home` 固定代表首頁（建店時自動建立 `type_key=home, slug=home` 頁面），路徑 `/` 解析為 `home`；另保留 `api`、`admin`、`preview`、`_next` 等註冊時拒用。

### D8. 快取：版本化 key + 事件失效

- **Key 設計**：`cache:shop:{shop_id}:v{ver}:page:{slug}`，value = 組裝完成的渲染 bundle JSON，TTL 24h。`ver` 取自 `cache:shop:{shop_id}:ver`（Redis counter）。
- **失效即 bump**：商家全域內容更新、換版、主題升級 → `INCR ver`（整租戶全失效，舊 key 靠 TTL 回收，免 SCAN 逐刪）；單頁發佈 → 直接 DEL 該頁 key（省一次整租戶失效）。
- **主題級失效**：平台更新 theme（schema/元件版本）→ `SELECT id FROM shops WHERE theme_id = ?` 後 pipeline 批次 INCR 各租戶 ver。
- **Cache miss 保護**：以 `golang.org/x/sync/singleflight` 合併同 key 併發回源，防快取擊穿。
- 失效觸發點集中於 service 層領域事件（`PagePublished`、`ShopContentUpdated`、`ThemeSwitched`、`ThemeUpdated`、`SiteMappingChanged`），禁止在 handler 內散落直呼 Redis。
- 渲染 bundle 結構（快取即回應）：

```json
{
  "shop":  { "id": 1, "name": "...", "theme": { "code": "starter", "layout_key": "starter/main" }, "content": { /* hydrated content_json */ } },
  "page":  { "type_key": "home", "component_key": "starter/home", "title": "...", "content": { /* hydrated published_json */ }, "seo": { /* pages.meta */ } }
}
```

- bundle 為**用戶端無關的 SDUI 契約**：web 的 SectionRenderer 與未來 APP 的 native 元件註冊表消費同一結構。任何渲染端遇到未知 section 型別 MUST 優雅略過（不得 crash，記 log），以容納「伺服器先於用戶端演進」的長尾（新區塊上線、舊版 APP 未更新）。

### D9. 認證與 Token

- **兩套完全隔離的 JWT 體系**（不同 HS256 secret、不同 issuer/audience）：
  - 後台：`aud=admin`，claims：`sub`（user_id）、`sids`（所屬 shop_id 列表，僅作 UI 提示）。
  - 前台會員：`aud=shop:{shop_id}`，claims：`sub`（member_id）。跨店重用 token 因 aud 不符直接 401。
- **與 PRD 5.2.2 的偏差**：JWT **不內嵌**角色權限快取。內嵌會造成撤權延遲（token 到期前舊權限仍有效）與 payload 膨脹；「減少跨表查詢」的目標改由 D4 的 Redis 權限快取達成（每請求一次 Redis GET）。
- access token 15 分鐘；refresh token 為不透明隨機值，雜湊後存 DB，30 天效期、每次使用輪替（rotation）、可撤銷；偵測到已輪替 token 重放 → 撤銷整條鏈。
- 密碼一律 Argon2id（alexedwards/argon2id，OWASP 建議參數起步）。
- 會員註冊/登入一律在商家上下文（Host 解析）發生：email/phone 命中既有 `members` 時掛新 `shop_member`（首次互動自動建立會籍）；回應訊息避免洩漏「此帳號已存在於其他商家」語意。

### D10. 索引策略：精準 btree，暫不建 GIN

- 必要索引即 D2 表列的 UNIQUE/INDEX（`pages(shop_id, slug)`、`theme_pages(theme_id, type_key)`、`site_shop` 三個 partial unique、`shop_user(shop_id, user_id)`、`shop_member(shop_id, member_id)`、`sites(domain)`、FK 欄位 btree）。
- **不**對 `content_json`/`published_json` 建 GIN：前台熱路徑為 Redis 命中與 `(shop_id, slug)` 點查，無 JSONB 內部查詢；GIN 不服務排序、只增寫入成本。待實際出現 `@>`/jsonpath 查詢需求（最可能在 `meta` 欄位）再針對該查詢建立（修正 PRD 5.1.2）。

### D11. 技術選型（Go / 前端 / 基礎設施）

| 項目 | 選擇 | 理由（vs 替代方案） |
|---|---|---|
| HTTP router | chi | stdlib 相容、middleware 生態；echo 亦可但無明顯優勢 |
| 資料層 | ent + Atlas versioned migrations | schema-as-code、privacy layer 原生支援租戶強制隔離（vs sqlc+pgx：SQL 控制力強但隔離靠紀律）；partial index 以 `entsql.IndexWhere` 或手寫 migration 補 |
| JWT | golang-jwt/jwt v5 | 社群標準 |
| JSON Schema | santhosh-tekuri/jsonschema v6 | 完整支援 draft 2020-12 |
| Redis | go-redis v9 + x/sync/singleflight | |
| 密碼 | alexedwards/argon2id | |
| 日誌 | log/slog（JSON handler） | 標準庫 |
| Storefront | Next.js（App Router）；Nuxt 為可接受替代 | 生態最大；最終擇一見 Open Questions |
| Repo 佈局 | monorepo：`/api`（Go）、`/web`（storefront）、`/admin`（薄殼 SPA）、`docker-compose.yml`（PG16 + Redis7） | 單人/小團隊起步最低摩擦 |

### D12. API 錯誤語意

401 未認證 / token aud 不符；403 權限判定拒絕或跨商家操作；404 網域未綁定、頁面不存在、審核中商家；422 Schema 校驗失敗（附 JSON Pointer 錯誤明細）；503 商家停用（維護頁）。錯誤 body 統一 `{ "error": { "code", "message", "details" } }`。

### D13. API 版本化與多端相容

全部公開 API 掛載於 `/api/v1` 前綴。web 隨部署即時更新，原生 APP 有無法強制更新的舊版長尾，因此 v1 契約**只做 additive 變更**（新增欄位/端點；不改既有語意、不刪欄位），破壞性變更另開 `/api/v2` 並行。hydration 的 default 補全即向後相容引擎：舊用戶端讀新 schema 的資料不缺欄位、新用戶端讀舊資料有預設值；搭配 D8 的未知區塊容錯，伺服器端可先於所有用戶端演進。

## Risks / Trade-offs

- [Go 起步樣板碼多、初期速度慢於全家桶框架] → ent/atlas codegen + 本 change 的 tasks 提供明確 scaffolding 順序；admin UI 刻意薄殼。
- [前端主題元件庫與後端 schema 漂移（component_key 對不到元件、props 與 schema 不符）] → schema 產 TS 型別；demo 主題 E2E 測試蓋「解析→校驗→hydration→渲染」全鏈；`component_key` 解析失敗時 SSR 顯式報錯而非白屏。
- [快取失效遺漏造成前台舊資料] → 失效集中於領域事件層 + 整租戶 version bump（寧可多失效不可漏失效）+ TTL 24h 兜底。
- [換版後頁面 `type_key` 不被新主題支援] → 後台換版前預檢並列出不相容頁面清單；前台該頁 404 而非壞版渲染。
- [deny override 或平台角色判定寫錯 = 權限漏洞] → D4 判定順序以單元測試矩陣全覆蓋（覆蓋/角色/平台/跨店 × 允許/拒絕）。
- [`UNIQUE NULLS NOT DISTINCT` 需 PostgreSQL 15+] → 環境鎖定 PG16；docker-compose 與文件明定。
- [members 全平台唯一 email 有跨店帳號存在性洩漏面] → 註冊/登入回應與時序不區分「新建」與「掛會籍」；速率限制。
- [單 Redis 故障時快取與權限快取同時失效] → 全部快取路徑皆有 DB fallback（degrade 而非 down）；Redis 僅作 cache，不作 source of truth。

## Migration Plan

Greenfield，無既有資料。里程碑順序（對應 tasks.md）：

1. **M0 骨架**：monorepo 佈局、docker-compose（PG16/Redis7）、Go module、CI lint/test。
2. **M1 資料層**：ent schema 全表 + Atlas migrations + seed（平台超管、權限目錄、demo 主題 `starter`）。
3. **M2 認證/RBAC**：Argon2id、JWT 雙體系、refresh 輪替、三層判定 + Redis 權限快取。
4. **M3 多租戶**：Host/path_prefix 解析 middleware、租戶 privacy 強制、路由快取。
5. **M4 CMS 引擎**：schema 校驗、hydration、pages CRUD/發佈、渲染 bundle API、版本化快取與事件失效。
6. **M5 前台**：Next.js SSR 骨架 + starter 主題元件庫 + 維護頁/404。
7. **M6 後台薄殼**：登入、頁面清單/編輯（schema-driven 表單）、發佈。

回滾策略：Atlas versioned migration down；尚無生產資料，風險低。完成後回填 `CLAUDE.md` 與 `openspec/project.md`。

## Open Questions

- Storefront 最終採 Next.js 或 Nuxt（預設 Next.js；依前端團隊技能定案，不影響 API 契約）。
- Admin UI 深度：Phase 1 薄殼是否足夠，或需在 Phase 1.5 擴成完整後台。
- 換版時舊主題 payload 是否需保留快照（`shop_theme_settings(shop_id, theme_id, content_json)` 副表）——Phase 1 不做，觀察實際換版頻率再決定。
- 會員社群登入（OAuth provider 集合）與 email/手機驗證流程，留待 Phase 2 前定義。
- 原生 APP 形態：每商家品牌 APP（站點寫死於 build config）或平台聚合 APP（選店後帶站點）——D5 的 App 通道兩者皆支援，發佈管線設計留待 APP phase。
- AI 生成主題若需突破區塊庫表現力（AI 產出元件程式碼而非資料），需另立 phase 處理 build pipeline 與沙箱，不納入本階段預留範圍。
