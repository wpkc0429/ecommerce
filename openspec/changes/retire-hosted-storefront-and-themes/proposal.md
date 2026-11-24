> **已實作（2026-11-25）**：群組 1–6 完成（commits 見 git log，標題含 content_schemas／ShopStatusChanged／v1 exemption／drop theme）。計畫修正記錄於 `tasks.md` 開頭。正式資料庫的 migration 待操作員備份後套用。
> **APPROVED 2026-11-24（使用者核准：「移除舊功能，直接移除」）：可以開始實作。** 必須遵守下方的順序限制（design D4 與 tasks.md）：**頁面 schema 目前來自主題，必須先把 schema 所有權搬到每店的中性儲存（群組 1），才能移除主題。**
> 範圍：託管的租戶渲染店面、主題與主題系統、site／網域路由。**內容儲存（頁面與全域內容、schema 驗證、草稿／發佈、預覽、消毒、圖片檢查、公開讀取 API、快取失效）保留**，由另一個 change `headless-content-store` 改造。

## Why

ADR `docs/decisions/0001-headless-platform-and-merchant-template.md`（含同日的「修正」）把**託管渲染、主題與 site／網域路由**宣告為產品已棄用：商家店面與後台由 merchant template 建立，平台是 headless 商務 API。
本 change 的前身（`retire-tenant-rendering`）把**整個 CMS** 一起列入退役；那與先前的決定相牴觸（商家用後台表單編輯的內容必須存在商家站台程式碼之外），已被修正：
**CMS 內容功能保留，並由 `headless-content-store` 改造成 headless 內容儲存**。因此本 change 縮小為只退役真正沒有消費者的三塊：

1. **託管店面**：`web/` 的租戶渲染程式碼（Next.js catch-all 渲染、預覽頁、主題元件庫、渲染端的 sanitize 與 allowlist 副本、型別產生腳本、Dockerfile 與 CI／image workflow）。
2. **主題與主題系統**：`themes`／`theme_pages` 實體與表、10 條主題路由、admin 的主題欄位、`starter` 主題 seed、`theme.view`／`theme.manage` 權限節點、`shops.theme_id`。
3. **site／網域路由**：`sites`／`site_shop`、6 條 `/admin/sites*` 路由、`tenantmw` 的 Host 後備與 `resolver.Resolve`、`X-Site-Domain`、`shop.manage_domains` 權限節點、開店精靈的網域欄位。

**明確不在本 change**（保留，並由 `headless-content-store` 改造）：`pages`／`shops.content_json`、頁面與全域內容端點（`/admin/shops/{id}/pages*`、`/content`、`/page-types`）、`/render/page`／`/render/preview`、`internal/cms`（驗證、hydration、消毒、圖片網域、`richtext`）、`internal/render`（bundle、快取、失效器）、preview token、`POST …/media/content-images` 與 `content_image` 用途、`page.*` 權限、admin 的「頁面」「全域內容」視圖與 `schemaform.js`、`cmd/sanitize-content`、**商家 CRUD（留在 `cms`）**。

## What Changes

- **移除 16 條路由**（皆在 v1 表面鎖內；`v1-surface.txt` 共 33 行相關）：主題（平台）7、主題（商家）3、`/admin/sites*` 6。
- **移除欄位**：`Shop.theme_id`（回應中為 required）、`POST /admin/shops` 與 `POST /admin/shop-provisioning` 的 `theme_id`、provisioning 的 `domain`／`path_prefix`／回應 `binding`、`setup-status` 的 `theme_applied`／`domain_bound`、`render/page` 的 `X-Site-Domain` 參數與 Host 解析說明。`setup-status.home_published` 與 `Shop.content_json` **不在此移除**（內容儲存處理）。
- **移除 ent 實體與表**：`themes`、`theme_pages`、`sites`、`site_shop`；欄位 `shops.theme_id`。
- **移除程式碼**：`httpapi/themes.go`、`httpapi/sites.go`；`cms.Service` 的主題方法（`CreateTheme`…`SwitchTheme`、`PrecheckTheme`）與 `ShopInput.ThemeID`；`tenant.Resolver.Resolve`／`loadEntry`／路由快取；`tenantmw` 的 Host 後備；`events.ThemeSwitched`／`ThemeUpdated`；`provision` 的 theme／domain 參數與 `bindDomain`；`seedStarterTheme`、demo 的 `demo.localhost` 站點。
- **必須先做的第一階段**：把頁面／全域內容的 schema 從主題搬到每店的 `content_schemas`（`headless-content-store` 的第一個切片，由本 change 交付，見 design D4）。
- **必須先改名保留**：`SiteMappingChanged`（今天兼負「商家停用時使 publishable key 快取失效」）→ 改名 `ShopStatusChanged` 並保留 key 失效。
- **admin**：開店精靈的主題下拉、網域欄位、檢查清單的 `theme_applied`／`domain_bound`。
- **web／e2e／CI**：刪除 `web/` 的租戶渲染程式碼；平台官網已決定放在**獨立 repo**（`platform-website`，2026-11-24 使用者決定），所以 `web/` 可**整個刪除**，與官網建置無關；e2e 中依賴 `WEB` 與主題換版的斷言、CI 的 web job 與 `image-web.yml` 一併移除。
- **無棄用期**（使用者決定：直接移除）。
- **v1 表面鎖**：採**一次性、明確記錄的例外**（design D1，使用者已決定）：ADR 0002、`api/openapi/v1-removed.txt` 與其測試，與路由移除同一個原子 commit；約 60 行鎖檔行（原草稿約 73 行）。

## Capabilities

### Removed Capabilities（封存時由 spec 增量處理）
- `theme-system`（整個 capability）。
- `multi-tenancy` 的 Host／path-prefix／site-shop／路由快取四條需求（其餘不動）。

### Modified Capabilities
- `page-management`（移除依賴主題的需求：頁型來自目前主題、換版預檢；其餘由 `headless-content-store` 處理）、`content-rendering`（移除 bundle 內的主題欄位與 Host 解析；其餘由 `headless-content-store` 處理）、`api-contract`（表面鎖例外的處置）、`demo-seed`（移除 `demo.localhost` 站點與 starter 主題）、`wiring-integration`（訂閱者清單）、`account-provisioning`（`setup-status` 與開店輸入，**尚在未封存的 change 內**，需先封存）。

## Impact

詳見 design。量級（相較前身草稿大幅縮小）：API 約 1,000 行（`themes.go` 248＋`sites.go` 277＋`cms` 主題方法約 225＋`resolver`／`tenantmw`／`provision` 一部分）＋ 約 14 個整合測試檔需改 tenant 建立方式；admin 約 50 行；`web/` 整個目錄；e2e 的一部分；2 張 migration（破壞性，需備份）。**不再需要**：抽出 `internal/shop`、搬 `ImageURLAllowed`、搬 `showValidationErrors`、改 `media_uploads.purpose` CHECK、動 `internal/render`、動 rich text 消毒。

## Preconditions

1. **已核准**（2026-11-24）；v1 例外已決定（design D1）。
2. **順序**：階段 1（每店 schema 儲存）先於任何主題移除，且其閘門測試通過（tasks 群組 1）。
3. 取得正式環境資料庫備份與階段 5 之前的回滾演練結果。
