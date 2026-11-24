> **DRAFT, DO NOT IMPLEMENT until the user approves.**
> 這是草稿，用來記錄「如果要退役租戶渲染，會動到什麼」。它**沒有**被核准；在使用者明確核准之前，不得執行 tasks.md 的任何一項，也不得因它而刪除任何程式碼、路由、表或文件。

## Why

ADR `docs/decisions/0001-headless-platform-and-merchant-template.md` 把「租戶渲染」宣告為產品已棄用：商家店面與後台由 merchant template 建立，平台是 headless 商務 API。棄用但保留的代價是：持續維護一套沒有消費者的 CMS／主題／渲染／site 系統（約 29 條路由、5 張表、一個 Redis 快取、一個 Next.js 應用、一批 e2e 與 seed），而且它們與商務核心有**比預期更多的耦合**（見 design「已驗證的依賴」）。本草稿把移除的完整清單、順序、風險、棄用期與驗證寫下來，供使用者決定**是否**與**何時**執行。

## What Changes（若核准）

- **移除 29 條路由**（皆在 v1 表面鎖內）：`/admin/themes*`（7）、`/admin/shops/{id}/theme*`＋`/themes`（3）、`/admin/shops/{id}/pages*`＋`/content`＋`/page-types`（11）、`/admin/sites*`（6）、`/render/page`＋`/render/preview`（2）；另 `POST /admin/shops/{id}/media/content-images`（1，CMS 圖片欄位專用）共 30 條涉及的契約條目（見 design 的路由表）。
- **移除 ent 實體與表**：`themes`、`theme_pages`、`pages`、`sites`、`site_shop`；`shops.theme_id` 與 `shops.content_json` 欄位；`media_uploads.purpose = 'content_image'`。
- **移除服務與套件**：`internal/cms`（含 `richtext`）、`internal/render`、`tenant.Resolver.Resolve`（Host→site→shop）與路由快取、`auth` 的 preview token、`cmd/sanitize-content`、`internal/seed/schemas/starter`。
- **必須先搬走的東西**：`cms.Service` 內的**商家 CRUD**（`CreateShop`／`ListShops`／`GetShop`／`UpdateShop`）、`ImageURLAllowed`（admin branding 使用）、`SiteMappingChanged` 事件（今天同時負責「商家停用時使金鑰快取失效」）、`setup-status` 的三個檢查、`provision.Service` 的 theme／domain 參數。
- **admin**：移除「頁面」「全域內容」視圖、`schemaform.js` 的 CMS 部分（保留 `showValidationErrors` 搬到 `lib/`）、`upload.js` 的 CMS 圖片欄位、開店精靈的主題／網域欄位與檢查清單三項。
- **web**：（若 `platform-website` 已先落地則大多已完成）殘留清理。
- **e2e／CI／docs／skills**：移除 CMS 區段與 web 相依、CI 的 gen:types 步驟、`image-web` 相關、相關文件與 skill 段落。
- **API 棄用期**：移除前至少一個棄用期，所有被標記的端點回 `Deprecation: true`、`Sunset: <date>` 與 `Link: rel="deprecation"` 標頭。
- **v1 表面鎖**：本 change **無法在不處置鎖檔的情況下執行**——見 design D1，需要使用者明確決定（v1 破例或 `/api/v2`）。

## Capabilities

### Removed Capabilities（封存時由 spec 增量處理）
- `content-rendering`、`page-management`、`theme-system`（整個 capability）。
- `multi-tenancy` 的 Host／path-prefix／site-shop／路由快取四條需求（其餘不動）。

### Modified Capabilities
- `api-contract`（表面鎖例外的處置）、`demo-seed`（移除 CMS 首頁與 `demo.localhost` 站點）、`wiring-integration`（訂閱者清單）、`account-provisioning`（`setup-status` 與開店輸入，**尚在未封存的 change 內**，需先封存）。

## Impact

詳見 design 的完整清單。量級：API 約 3,000 行（`cms` 2,995 含測試、`httpapi` 四個 handler 917、`render`、`tenant` 一部分）＋ 14 個整合測試檔需改 tenant 建立方式；admin 約 500 行；web 整個應用；e2e 約 210 行；2 張 migration（破壞性，需備份）。

## Preconditions（全部成立才可開始）

1. 使用者明確核准本 change，並決定 v1 表面鎖的處置（design D1）。
2. `platform-website`（或等價的決定）已處理 `web/`。
3. `platform-account-provisioning`、`cms-rich-text-sanitization` 等未封存的 change 已封存（否則 spec 增量對不到基準）。
4. 確認沒有任何商家使用 `/render/*`、`/admin/*/pages` 或 Host 解析（見 design D2 的偵測方法）。
5. 取得正式環境資料庫備份與回滾演練結果。
