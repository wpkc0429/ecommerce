# 0002 — v1 表面鎖的一次性例外：移除託管店面、主題與 site／網域路由

- 狀態：**已決定並已執行**（2026-11-24 由使用者核准：「移除舊功能，直接移除」；2026-11-25 實作，change `retire-hosted-storefront-and-themes`）
- 核准者：專案擁有者（使用者）
- 相關：[ADR 0001](0001-headless-platform-and-merchant-template.md)（方向與「修正」）、`openspec/changes/retire-hosted-storefront-and-themes/`
- 記錄於：`api/openapi/v1-removed.txt`（被移除的鎖檔行原文）、`api/internal/httpapi/openapi_contract_test.go` 的 `TestOpenAPIV1RemovedStaysRemoved`

v1-removed-lines: 60

## 背景

`api/openapi/v1-surface.txt` 規定：刪除任一行＝破壞 `/api/v1`；`CLAUDE.md`、`openspec/project.md` 記載 design D13 的破例窗口已在 change `order-extras` 用盡並關閉，之後不得再以「尚無外部消費者」為由破壞 v1。

使用者明確核准移除託管渲染店面、主題與主題系統、site／網域路由，沒有棄用期。因此這是**一個新的、有名稱的例外**，與 D13 的窗口不是同一件事，且**僅限本文件列出的移除**。

## 為什麼可以破例

- 整個 capability 已被 ADR 0001 宣告棄用：商家的店面與後台由 merchant template 建立，平台是 headless 商務 API。
- 已知消費者為零：ONDO（`bouquet`）是 headless、只用 `X-Publishable-Key` 與 `/api/v1/shop/*`；`web/` 是第一方；沒有其他商家；沒有真實的 CMS／主題內容（資料遷移不會遺失 schema，主題 schema 已複製進每店的 `content_schemas`）。
- 已查證（唯讀）bouquet、merchant-template 與其後台沒有使用任何被移除的端點或欄位。

## 移除清單（精確）

**16 條路由**
- 主題（平台，7）：`GET/POST /admin/themes`、`GET/PUT /admin/themes/{themeID}`、`POST /admin/themes/{themeID}/pages`、`PUT/DELETE /admin/themes/{themeID}/pages/{themePageID}`
- 主題（商家，3）：`GET /admin/shops/{shopID}/themes`、`GET /admin/shops/{shopID}/theme/precheck`、`POST /admin/shops/{shopID}/theme`
- Site（6）：`GET/POST /admin/sites`、`DELETE /admin/sites/{siteID}`、`POST /admin/sites/{siteID}/shops`、`PUT/DELETE /admin/sites/{siteID}/shops/{shopID}`

**欄位與參數**：`Shop.theme_id`；`ProvisionResult.binding`；`SetupStatus.theme_applied`、`SetupStatus.domain_bound`；`GET /render/page` 的 `X-Site-Domain` 標頭參數；schema `Site`、`SiteShopBinding`、`Theme`、`ThemePage`、`ThemeSummary` 的屬性。

**不鎖檔但同時移除的行為**（未在鎖檔內，仍在 change 內記錄）：`POST /admin/shops` 與 `POST /admin/shop-provisioning` 的 `theme_id`、provisioning 的 `domain`／`path_prefix`；渲染 bundle 的 `shop.theme` 與 `page.component_key`；`GET …/page-types` 的 `component_key`；前台端點的 Host 後備（無金鑰一律 404 `unknown_publishable_key`）；權限節點 `theme.view`、`theme.manage`、`shop.manage_domains`（`page.*` 保留）。

鎖檔行的精確清單就是 `api/openapi/v1-removed.txt`（60 行：16 條路由行、19 行路徑／查詢／標頭參數行〔含 `X-Site-Domain` 的兩行〕、25 行 schema 屬性行〔`Site`／`SiteShopBinding`／`Theme`／`ThemePage`／`ThemeSummary` 共 21 行，加 `ProvisionResult.binding`、`SetupStatus.domain_bound`、`SetupStatus.theme_applied`、`Shop.theme_id` 共 4 行〕）。

## 這個例外不是什麼

- **不重新開啟 D13 的窗口。** 未列入 `v1-removed.txt` 的任何移除，仍使 `TestOpenAPIV1SurfaceLock` 轉紅，需要自己的例外與自己的決策紀錄。
- 內容儲存（頁面與全域內容端點、`/render/*`、`Shop.content_json`、`setup-status.home_published`、`page.*` 權限）**不在**例外之內，保持不變。

## 執行與防護

1. 路由、handler 與契約（`openapi.yaml`、`v1-surface.txt`、`v1-removed.txt`、契約測試、本文件）在**同一個原子 commit**。
2. `TestOpenAPIV1RemovedStaysRemoved`：`v1-removed.txt` 的每一行都不得出現在目前的契約或 `v1-surface.txt`，且行數必須等於上方 `v1-removed-lines`——被移除的東西不能悄悄回來，例外清單也不能被偷偷擴充。
3. 資料移除分兩個 migration（破壞性，down 檔拒絕回滾；回滾＝從備份還原），見 change 的 design D5。

## 後果

- 平台沒有託管渲染：新店沒有主題可套用，**也暫時沒有 API 可註冊它的內容 schema**，直到 `headless-content-store` 的 schema 註冊 API 完成（demo 商家由 seed 直接寫入）。這是計畫中的相依，不是疏漏。
- 任何仍使用已移除路由或欄位的外部程式碼會得到 404／欄位消失；已知消費者為零。
