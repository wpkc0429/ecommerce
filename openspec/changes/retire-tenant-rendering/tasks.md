> **DRAFT, DO NOT IMPLEMENT until the user approves.**
> 任何一項都不得在使用者核准前執行。核准時請同時回答 design 的 Q1–Q6；群組 0–3 可獨立於 4–6 先行核准。

## 0. 棄用標頭與觀察期（additive，不破壞 v1）

- [ ] 0.1 新增 `api/internal/httpapi/deprecation.go`：一個中介層，對 design 清單的 30 條路由加 `Deprecation: true`、`Sunset: <date>`、`Link: <…>; rel="deprecation"`；於 `router.go` 在各 `Mount*` 之處套用（`themes.go`／`pages.go`／`sites.go`／`render.go`／`media.go` 的 content-images）
- [ ] 0.2 測試 `deprecation_integration_test.go`：30 條路由皆有標頭，其他路由（抽樣 20 條）沒有
- [ ] 0.3 `api/openapi/openapi.yaml`：對應 operation 標 `deprecated: true`、回應標頭 `Deprecation`／`Sunset`；跑 `-openapi.update-surface` 確認 `v1-surface.txt` **只新增**
- [ ] 0.4 對這 30 條路由加結構化存取計數日誌（只記路由名與 shop_id，不記 PII），並寫 runbook 說明如何查詢
- [ ] 0.5 觀察期到期前檢視計數，為零才進入群組 4

## 1. 搬家，不刪除（行為逐位元組不變；既有測試即回歸）

- [ ] 1.1 新增 `api/internal/shop/`：由 `cms/service.go` 搬出 `ShopInput`、`ShopListParams`、`ShopPage`、`ShopUpdate`、`CreateShop`、`CreateShopTx`、`ListShops`、`GetShop`、`UpdateShop`、`validShopStatus`；`httpapi/shops.go`、`provision/service.go`、`wire.go` 改用它；`cms` 暫時保留 `createHomePage` 並由 `shop` 呼叫（待群組 4 移除）
- [ ] 1.2 `ImageURLAllowed` 與 `imageChecker` 前綴邏輯搬到 `api/internal/media/`；`httpapi/branding.go`、`settings.go`、`wire.go:403` 改用；`cms` 保留一個轉呼叫的薄殼直到刪除
- [ ] 1.3 `events.SiteMappingChanged` 改名 `ShopStatusChanged`（保留 `ShopIDs`，`Hosts` 暫留）；`wire.go:133` 的訂閱者名稱與 `wiring_integration_test.go` 的 `expectedSubscribers` 同步；`cms.UpdateShop` 搬後由 `shop` 發佈
- [ ] 1.4 **補測試（現在沒有）**：停用商家後，該商家的 publishable key 快取立即失效（`tenancy`／`clientauth` 整合測試）；先在現行程式碼上驗證會因拿掉 `InvalidateShopKeys` 而轉紅
- [ ] 1.5 `admin/lib/schemaform.js` 的 `showValidationErrors` 搬到 `admin/lib/dom.js`；`views/products.js`、`views/pages.js`、`views/content.js` 的 import 同步
- [ ] 1.6 驗證：`go build ./... && make lint && make test && make test-int && make openapi`；`cd e2e && node commerce-path.mjs`

## 2. 測試與 seed 不再依賴 sites／pages／themes

- [ ] 2.1 `api/internal/testutil/`：新增建立租戶的 helper（建立 shop ＋ publishable key，回傳帶 `X-Publishable-Key` 的請求建構器）
- [ ] 2.2 改寫 `httpapi/{cart,categories,clientauth,guest,media,order,payment,points,shipping,shops,tenancy,openapi_conformance}_integration_test.go`：以 helper 取代 `client.Site.Create…`＋`client.SiteShop.Create…`＋`req.Host = domain`（共 22 處 Host 設定）；`tenancy_integration_test.go` 中專測 Host 解析的案例標記為隨 sites 一併移除（群組 4）
- [ ] 2.3 `api/internal/seed/seed.go`：`seedDemoShop` 不再建 `demo.localhost` 站點與 `site_shop`、不建 CMS 首頁、不 `SetThemeID`；`seedStarterTheme` 保留到群組 4；`seed_integration_test.go` 同步（保留「等級門檻可被單筆訂單跨過」不變式）；`CLAUDE.md` 的 demo 固定識別值段落更新（origin 保留，移除 `demo.localhost` Host 解析的說法）
- [ ] 2.4 驗證：同 1.6

## 3. 移除前端、e2e 與 CI

- [ ] 3.1 `admin/views/pages.js`、`admin/views/content.js` 刪除；`admin/app.js` 的 import（18–19）與註冊表（52–54）、`admin/index.html` 的 `#tab-pages`／`#tab-editor`／`#tab-content` 面板；`admin/lib/schemaform.js` 剩餘部分刪除（若無其他消費者）；`admin/lib/upload.js` 移除 CMS 圖片欄位段
- [ ] 3.2 `admin/views/platform-shops.js`：移除 `GET /admin/themes`、`theme_id`、網域輸入、檢查清單的 `theme_applied`／`domain_bound`／`home_published`
- [ ] 3.3 `e2e/happy-path.mjs`：刪 ①–⑦ 中 CMS 區段（約 48–257 行）、檔頭註解、`WEB`／`E2E_WEB_BASE_URL`（950、1038 行改為不依賴 `WEB` 的 admin API origin 推導）、㉔ 對 `domain_bound` 的斷言；重新核對斷言總數並更新 `CLAUDE.md`
- [ ] 3.4 `web/`：若 `platform-website` 已處理則只清殘留；否則刪 `app/[[...path]]`、`app/preview`、`themes/`、`lib/*`、`scripts/gen-types.mjs`、`Dockerfile`；`.github/workflows/ci.yml` 的 web job 與 `image-web.yml`（**需使用者核准 workflow 變更**）
- [ ] 3.5 `Makefile`：移除 `web-dev`、`sanitize-content`；`.env.example`：移除 web 區段與 `PREVIEW_TOKEN_TTL`（群組 4 之後）
- [ ] 3.6 驗證：`node happy-path.mjs` 與 `node commerce-path.mjs` 在乾淨資料庫上全過

## 4. 移除 API 行為（**需 design Q1 的決定**；若選 (c) 則跳過本群組與 5）

- [ ] 4.1 刪除 `httpapi/{themes,pages,sites,render}.go`、`media.go` 的 `createContentImage` 與 `media.Service.CreateContentImage`、`router.go` 的相關 `Deps` 與掛載、`wire.go` 的 `renderCache`／`Invalidator`／`cmsService`／`deps.Themes|Pages|Sites|Render`
- [ ] 4.2 刪除 `api/internal/cms/`（含 `richtext/`）、`api/internal/render/`、`api/cmd/sanitize-content/`、`api/internal/seed/schemas/starter/`、`seed.go` 的 `seedStarterTheme` 與 `starterPages`
- [ ] 4.3 `tenant/resolver.go`：移除 `Resolve`／`loadEntry`／路由快取與 `ErrSiteNotFound` 等；`httpapi/tenantmw.go` 移除 Host 後備（`/api/v1/shop/*` 必須帶金鑰，無金鑰回 404 `unknown_publishable_key` 或新的明確錯誤碼，隨 Q4 決定）；`tenant/enttenancy.go` 與 `enttenancy_internal_test.go` 移除 `Page`／`site_shop` 的登記
- [ ] 4.4 `auth/jwt.go` 移除 preview token；`config.PreviewTokenTTL`；`events.go` 移除 `PagePublished` 等五個事件；`ShopStatusChanged.Hosts` 欄位移除
- [ ] 4.5 `provision/service.go`：移除 `ThemeID`、`Domain`、`PathPrefix`、`bindDomain`、`SiteMappingChanged`；`provision/status.go`：移除三個檢查；`httpapi/provisioning.go` 的請求欄位與回應 `binding`、`theme_applied`／`domain_bound`／`home_published`；`shop.createShopTx` 不再建首頁、不再接受 `theme_id`
- [ ] 4.6 `seed.go`：`PermissionCatalog` 移除 `page.*`、`theme.*`、`shop.manage_domains`（依 Q6 決定），角色授予同步，並以 migration 或 seed 清除既有授權列；`seed_integration_test.go` 的節點集合斷言同步
- [ ] 4.7 `api/openapi/openapi.yaml`：移除 30 條路由、`Page`／`PageListEntry`／`Theme`／`ThemePage`／`ThemeSummary`／`Site`／`SiteShopBinding`／`RenderBundle`、`X-Site-Domain`、`Shop.theme_id`／`Shop.content_json`、provisioning 的 `theme_id`／`binding`、`setup-status` 三欄；**人工刪除** `v1-surface.txt` 的對應行（約 73 行，diff 逐行檢視；依 D1 的核准）；`make openapi` 全綠
- [ ] 4.8 `api/internal/app/wiring_integration_test.go`：`expectedSubscribers` 移除 `render.(*Invalidator).Handle-fm`，檢查 ⑤⑥ 通過
- [ ] 4.9 驗證：`go build ./... && make lint && make test && make test-int && make openapi`；`commerce-path.mjs`

## 5. 移除資料（破壞性；需備份與演練）

- [ ] 5.1 `api/internal/ent/schema/`：刪 `cms.go`；`tenancy.go` 移除 `Site`、`SiteShop`、`Shop.theme_id`／`content_json`／相關 edges 與索引；`media.go` 的 purpose CHECK 移除 `content_image`；`cd api && GOTOOLCHAIN=go1.26.4 go generate ./internal/ent`
- [ ] 5.2 migration 1 `drop_cms_columns`：`ALTER TABLE shops DROP COLUMN theme_id, DROP COLUMN content_json`、`DROP INDEX shop_theme_id`；`media_uploads_purpose_check` 手寫替換（先確認無 `content_image` 列）；down 檔拒絕回滾並說明從備份還原
- [ ] 5.3 migration 2 `drop_cms_tables`：`DROP TABLE site_shop, sites, pages, theme_pages, themes`；down 同上；重算 `atlas.sum`；剪除 atlas 夾帶的假差異
- [ ] 5.4 在正式資料副本上演練 `migrate` 與從備份還原；記錄結果於本 change
- [ ] 5.5 整合測試：migration 在含舊資料的資料庫上可套用；`tenantOwned` 登記同步

## 6. 文件、skill 與 spec 收尾

- [ ] 6.1 `README.md`、`CLAUDE.md`（移除或改寫 CMS／渲染／主題／rich text／site 條目、斷言計數）、`docs/storefront-integration.md`（移除棄用通知，改為單純的接入說明）、`docs/media-r2-setup.md`、`docs/admin-frontend-integration.md`（content_image）、`openspec/project.md`
- [ ] 6.2 `.claude/skills/merchant-onboarding/`：`setup-status` 欄位說明、`known-gaps.md`、cookbook 步驟 2／3 同步
- [ ] 6.3 封存：`openspec/specs/{content-rendering,page-management,theme-system}` 刪除；`multi-tenancy` 移除四條需求；`api-contract` 記錄 v1 破例（若 Q1(a)）；`demo-seed` 更新
- [ ] 6.4 最終驗證：design D6 全部項目；部署後 24 小時觀察
