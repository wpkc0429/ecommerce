> **APPROVED 2026-11-24（使用者核准：「移除舊功能，直接移除」）：可以開始實作。** 必須照下列群組**依序**進行；每個群組結束時 `go build ./... && make lint && make test && make test-int && make openapi` 全綠，`commerce-path.mjs` 通過，且可獨立部署。**群組 1 之前不得刪除任何主題相關程式碼。**
> **實作紀錄（2026-11-25）與計畫修正**——實作時發現並已修進本檔與 design：
> 1. **階段 1 還要同步主題編輯**：`UpdateTheme`／`CreateThemePage`／`UpdateThemePage`／`DeleteThemePage` 與 seed 重跑也必須把變動複製成新的 schema 版本（原計畫只寫 `SwitchTheme`／建店／seed），否則「行為不變」不成立。測試夾具（`richtext_integration_test`）直接改主題的寫法也要改成寫 `content_schemas`。
> 2. **`content_schemas` 加 `type_order`**（頁型順序，`page-types` 回應順序不變）；`sha256` 對回填列是 jsonb 文字的雜湊、Go 寫入列是正規化 JSON 的雜湊，比較一律在 Go 重算。`cms.Service` 不注入 Store，而是由 `Client` 衍生（所有建構點不用改）。
> 3. **3.3 的「不再 `SetThemeID`／移除 starter 主題」順延到 4.9**：bundle 在 4.7 前仍讀 `shop.theme_id` 取過渡的 `theme` 物件，過早移除會讓 demo 商家渲染 404。
> 4. **4.5–4.10 實際上必須是一個 commit**：`sites.go` 用到 `tenant` 的 Host 工具、provisioning 的 `binding`／`SetupStatus` 欄位與 bundle 的 `theme` 都是鎖檔或 `required` 欄位，回應契約測試與鎖檔檢查使它們無法拆開；因此程式碼移除與契約例外同一個 commit（先前的 `web/`、admin、e2e、測試解耦、改名保留已各自成 commit）。
> 5. **admin 的頁面預覽依賴 `web/preview`**：`admin/views/pages.js` 的「預覽」原本開 `${WEB_BASE}/preview?token=`，`web/` 刪除後改為開 `GET /api/v1/render/preview?token=`（工作副本的 JSON bundle）；`WEB_BASE` 移除。
> 6. **新商家沒有 content schema**：主題移除後，在 `headless-content-store` 的 schema 註冊 API 完成前，沒有 API 可替新商家註冊 schema（demo 商家由 seed 寫入）；這是已知的相依，見 ADR 0002「後果」。
> 7. **已移除的權限節點要清資料**：`seed` 每次執行會刪除 `theme.*`／`shop.manage_domains` 與其授予列；migration 2 也含同樣的 DELETE，使只跑 migration 的環境一致。
> 一個實作者可以從頭做到尾，不會遇到半壞狀態：群組 1 把 schema 搬離主題（行為不變），群組 2–3 做準備，群組 4 才移除，群組 5 才 DROP。每個 `- [ ]` 建議一個 commit，除非標為「原子」。
> 決定已寫在 design（D1–D6 與 Decisions record）：無棄用期、v1 例外（ADR 0002＋`v1-removed.txt`）、不匯出、欄位消失可接受、移除 `demo.localhost`、移除 `theme.*` 與 `shop.manage_domains` 而保留 `page.*`。

## 1. 每店 schema 儲存（`headless-content-store` 的第一個切片；本 change 交付，`headless-content-store` 的 tasks 群組 1 即此群組）

目標：頁面與全域內容的 schema 不再讀 `themes`／`theme_pages`。行為與 API 逐位元組不變。

- [x] 1.1 新增 ent schema `api/internal/ent/schema/contentschema.go`：`ContentSchema`（`shop_id`、`version` int、`schema_json` JSON、`sha256` varchar(64)、`source` varchar(30)〔`theme_backfill`｜`registered`〕、`created_at`；`UNIQUE(shop_id, version)`；`version >= 1` CHECK）；`api/internal/tenant/enttenancy.go` 的 `tenantOwned` 加 `"ContentSchema"`（帶 `shop_id` 的表必須被分類，否則測試轉紅）。儲存格式 v1：`{"format":1,"types":{"<type_key>":<JSON Schema>},"global":<JSON Schema>}`（`types` 來自 `theme_pages.page_schema`，`global` 來自 `themes.config_schema`；`component_key` 暫存於 `component_keys:{type_key:...}` 供階段 4 前的 bundle 使用）
- [x] 1.2 `cd api && GOTOOLCHAIN=go1.26.4 go generate ./internal/ent`；手寫 migration `add_content_schemas`（建表 ＋ **回填 SQL**：對每個 `theme_id IS NOT NULL` 的商家以 `jsonb_object_agg` 把該主題的 `theme_pages` 組成 `types`、`config_schema` 放 `global`，寫成 `version = 1`、`source = 'theme_backfill'`、`sha256 = encode(sha256(convert_to(schema_json::text,'UTF8')),'hex')`）；down 檔 `DROP TABLE content_schemas`；重算 `atlas.sum`；手動剪除 atlas 夾帶的假差異（見 `CLAUDE.md` 的 migration 注意）
- [x] 1.3 新增 `api/internal/contentschema/`：`Store`（`Active(ctx, shopID)`、`PageSchema(ctx, shopID, typeKey)`、`GlobalSchema(ctx, shopID)`、`Types(ctx, shopID)`、`WriteFromTheme(ctx, tx, shopID, themeID)`〔寫入下一個版本，內容與回填 SQL 相同〕）；單元測試以 SQLite 以外的方式不可行，故為整合測試（`INTEGRATION=1`）
- [x] 1.4 `api/internal/cms/service.go`：建構子注入 `*contentschema.Store`（`wire.go` 接線，稽核 ⑤ 要求非 nil）；`currentPageSchema`、`GetShopContent`、`UpdateShopContent`、`ListPages`（`Incompatible` ＝ type 不在 `Types`）改讀 Store；`SwitchTheme`／`CreateShopTx` 帶 `theme_id`／`provision` 帶 `theme_id` 時呼叫 `WriteFromTheme`（同一交易）
- [x] 1.5 `api/internal/httpapi/pages.go` 的 `pageTypes` 與 `api/internal/render/bundle.go`：不再查詢 `Theme`／`ThemePage`，改讀 Store（`component_key` 由 `component_keys` 帶出；`shop.theme.code`／`layout_key` 暫時仍由 `shops.theme_id` 查詢——階段 4 才移除，這是階段 1 唯一仍讀 `Theme` 表的地方，且只為了回傳過渡值）
- [x] 1.6 `api/internal/seed/seed.go`：`seedStarterTheme` 之後呼叫 `WriteFromTheme`，demo 商家因此有 schema 版本 1
- [x] 1.7 **閘門測試**（階段 4 的前置條件；全部通過才能進群組 4）：①回填 migration 在含舊資料的資料庫上套用後，每個有 `theme_id` 的商家恰有一個版本 1 且內容與主題 schema 逐位元組相等（比對 JSON 正規化後相等）；②**刪掉該商家對應的 `theme_pages` 列後**，建立／更新／發佈頁面、讀寫全域內容、`GET /render/page`、`GET /render/preview`、`GET …/page-types` 仍全部成功（證明沒有讀取者還依賴主題 schema）；③`SwitchTheme` 後該商家有新版本且頁面驗證用新版本；④既有 `cms`／`richtext`／`pages`／render 整合測試全綠
- [x] 1.8 驗證：`go build ./... && make lint && make test && make test-int && make openapi`（`openapi.yaml` 不應有任何變動）；`cd e2e && node commerce-path.mjs`

## 2. 改名，不刪除（行為不變；既有測試即回歸）

- [x] 2.1 `events.SiteMappingChanged` 改名 `ShopStatusChanged`（保留 `ShopIDs`，`Hosts` 暫留）；`cms/service.go` 發佈處、`wire.go` 約 133 行的訂閱者名稱與 `wiring_integration_test.go` 的 `expectedSubscribers` 同步
- [x] 2.2 **補測試（現在沒有）**：停用商家後，其 publishable key 快取立即失效（`tenancy`／`clientauth` 整合測試）；先在現行程式碼上驗證會因拿掉 `InvalidateShopKeys` 而轉紅
- [x] 2.3 驗證：同 1.8

## 3. 測試與 seed 不再依賴 sites／themes

- [x] 3.1 `api/internal/testutil/`：新增建立租戶的 helper（建立 shop ＋ publishable key，回傳帶 `X-Publishable-Key` 的請求建構器）
- [x] 3.2 改寫 `httpapi/{cart,categories,clientauth,cms,guest,media,order,payment,points,richtext,shipping,shops,tenancy,openapi_conformance}_integration_test.go`、`seed/seed_integration_test.go`、`tenant/enttenancy_test.go`：以 helper 取代 `client.Site.Create…`＋`client.SiteShop.Create…`＋`req.Host = domain`（22 處 Host 設定）；測 cms／richtext 的 fixture 改為直接寫 `content_schemas`（不再建 `Theme`／`ThemePage`）；專測 Host 解析的案例與專測主題端點的案例**標記為隨群組 4 刪除**（先保留，群組 4 與其端點同 commit 刪）
- [x] 3.3 `api/internal/seed/seed.go`：`seedDemoShop` 不再建 `demo.localhost` 站點與 `site_shop`、不 `SetThemeID`，改為直接以 `internal/seed/schemas/starter/*.json` 種進 `content_schemas`（**`schemas/starter/` 保留**，是 demo 的內容 schema）；CMS 首頁草稿保留；`seed_integration_test.go` 同步（保留「等級門檻可被單筆訂單跨過」不變式）；`CLAUDE.md` 的 demo 固定識別值段落更新（移除 `demo.localhost` Host 解析的說法）
- [x] 3.4 驗證：同 1.8

## 4. 移除（依序；路由與契約為最後一個原子 commit）

### 4a. 非契約性移除（各自一個 commit，CI 皆綠）

- [x] 4.1 `admin/views/platform-shops.js` 與 `admin/index.html`：移除 `GET /admin/themes`、主題下拉、`theme_id`、網域輸入、檢查清單的 `theme_applied`／`domain_bound`（`home_published` 保留）；此時 API 仍接受舊欄位，不影響
- [x] 4.2 `e2e/happy-path.mjs`：刪依賴 `WEB` 的斷言（102、120、151、239 行）與「換版 → 前台反映新主題」區段（約 164–249）、檔頭註解、`WEB`／`E2E_WEB_BASE_URL`（950、1038 行改為不依賴 `WEB` 的 admin API origin 推導）、㉔ 對 `domain_bound` 的斷言；**保留 admin 頁面編輯與發佈的斷言**；重新核對斷言總數並更新 `CLAUDE.md`
- [x] 4.3 刪除整個 `web/`（官網在獨立 repo，與本項無關）；`.github/workflows/ci.yml` 的 web job 與 `.github/workflows/image-web.yml`（**workflow 變更已隨本核准一併授權**；commit 訊息註明）；`Makefile` 移除 `web-dev`；`.env.example` 移除 `# ─── web (storefront SSR)` 區段（`API_BASE_URL`、`WEB_PORT`）；`api/internal/cms/richtext/allowlist.json` 的 `$comment` 與 `richtext` 套件註解不再提 `web/lib/richtext-allowlist.json`；`README.md`／`CLAUDE.md` 的 `web/` 條目移除
- [x] 4.4 驗證：`node happy-path.mjs` 與 `node commerce-path.mjs` 在乾淨資料庫上全過

### 4b. 程式碼移除（可拆成數個 commit，只要 CI 全綠；**涉及路由的刪除一律與 4.10 同一個 commit**）

- [x] 4.5 `tenant/resolver.go`：移除 `Resolve`／`loadEntry`／路由快取與 `ErrSiteNotFound` 等（`ResolveKey` 保留）；`httpapi/tenantmw.go` 移除 Host 後備（`/api/v1/shop/*` 無金鑰回 404 `unknown_publishable_key`，與金鑰無效同一個回應）；`httpapi/render.go` 移除 `X-Site-Domain`／Host 分支（缺金鑰同上）；`tenant/enttenancy_internal_test.go` 移除 `site_shop` 的登記；刪除專測 Host 解析的測試
- [x] 4.6 `provision/service.go`：移除 `ThemeID`、`Domain`、`PathPrefix`、`bindDomain`；`provision/status.go`：移除 `theme_applied`、`domain_bound`；`httpapi/provisioning.go`、`shops.go` 的請求欄位與回應 `binding`；`cms/service.go` 的 `ShopInput.ThemeID`、`createShopTx` 的 `themeID`；`contentschema.WriteFromTheme` 的呼叫端一併移除
- [x] 4.7 `render/bundle.go`：移除 `BundleTheme`、`ComponentKey`、`component_keys` 的使用；`httpapi/pages.go` 的 `pageTypes` 不再回 `component_key`；`contentschema` 儲存格式移除 `component_keys`（以新版本寫入或忽略）
- [x] 4.8 `events.go` 移除 `ThemeSwitched`／`ThemeUpdated` 與 `ShopStatusChanged.Hosts`；`render/invalidator.go` 移除對應兩個 case；`wire.go` 的 `InvalidateHosts` 訂閱移除（`InvalidateShopKeys` 保留）；`wiring_integration_test.go` 的 `expectedSubscribers` 更新
- [x] 4.9 `seed.go`：`PermissionCatalog` 移除 `theme.view`、`theme.manage`、`shop.manage_domains`，角色授予同步（`merchant_owner`、`editor`），並在 seed 內刪除既有 `role_permission`／`user_permission` 對這些節點的列；移除 `seedStarterTheme`／`starterPages`／`StarterConfigSchema` 對主題的建立（schema JSON 保留）；`seed_integration_test.go` 的節點集合斷言同步；**`page.*` 一個字不動**

### 4c. 契約例外與路由移除（**原子 commit**，commit 訊息開頭 `v1 exemption:`）

- [x] 4.10 同一個 commit 內完成：①刪除 `httpapi/themes.go`、`httpapi/sites.go`、`router.go` 的 `Deps.Themes`／`Deps.Sites` 與掛載、`wire.go` 的 `deps.Themes|Sites`、`cms/service.go` 的主題方法、`cms` 與 `internal/ent` 對 `Theme`／`ThemePage`／`Site`／`SiteShop` 的剩餘引用（`ent/schema` 此時**尚不刪**，留到群組 5）；②`api/openapi/openapi.yaml`：移除 16 條路由、`Theme`／`ThemePage`／`ThemeSummary`／`Site`／`SiteShopBinding`、`X-Site-Domain` 與 Host 解析說明、`Shop.theme_id`、provisioning 的 `theme_id`／`domain`／`path_prefix`／`binding`、`setup-status` 兩欄、bundle 的 `theme`／`component_key`、`page-types` 的 `component_key`；③`api/openapi/v1-surface.txt`：**以腳本列出並人工逐行核對**後刪除 design A 節定義的約 60 行（不得多刪一行）；④新增 `api/openapi/v1-removed.txt`（被刪行原文＋檔頭指向 ADR 0002）與 `openapi_contract_test.go` 的新斷言（見 design D1.3）；⑤新增 `docs/decisions/0002-v1-exemption-retire-hosted-storefront-and-themes.md`；⑥`CLAUDE.md`、`openspec/project.md` 的「破例窗口已關閉」句子改為 D1.4 的新說法；`openspec/specs/api-contract/spec.md` 增加例外需求
- [x] 4.11 驗證：`go build ./... && make lint && make test && make test-int && make openapi`（四道契約檢查全綠，且 `v1-removed.txt` 測試通過）；`node commerce-path.mjs`；確認內容儲存的回歸：頁面發佈、讀取、預覽、快取失效測試全綠

## 5. 移除資料（破壞性；需備份與演練）

- [x] 5.1 備份（本機以測試資料庫演練；**正式資料庫的備份由操作員於套用前執行**）：`pg_dump` `themes`、`theme_pages`、`sites`、`site_shop`、`shops.theme_id`、`content_schemas`（操作員執行並驗證，記錄於本 change）
- [x] 5.2 `api/internal/ent/schema/`：`cms.go` 移除 `Theme`、`ThemePage`；`tenancy.go` 移除 `Site`、`SiteShop`、`Shop.theme_id`／`theme` edge／索引；`cd api && GOTOOLCHAIN=go1.26.4 go generate ./internal/ent`
- [x] 5.3 migration 1 `drop_theme_column`：`ALTER TABLE shops DROP COLUMN theme_id`（含外鍵與索引 `shop_theme_id`）；down 檔拒絕回滾並說明從備份還原
- [x] 5.4 migration 2 `drop_theme_and_site_tables`：`DROP TABLE site_shop, sites, theme_pages, themes`；down 同上；重算 `atlas.sum`；剪除 atlas 夾帶的假差異
- [x] 5.5 在正式資料副本上演練 `migrate` 與從備份還原；整合測試：migration 在含舊資料的資料庫上可套用；`tenantOwned`／`notTenantOwned` 登記同步；驗證：同 4.11

## 6. 文件、skill 與 spec 收尾

- [x] 6.1 `README.md`、`CLAUDE.md`（移除 web／主題／site／Host 條目與棄用說明、斷言計數）、`docs/storefront-integration.md`（移除對應的「已棄用」通知，Host 解析敘述）、`openspec/project.md`
- [x] 6.2 `.claude/skills/merchant-onboarding/`：`setup-status` 欄位說明（移除 `theme_applied`／`domain_bound`）、`known-gaps.md`、cookbook 同步
- [x] 6.3 封存：`openspec/specs/theme-system` 刪除；`multi-tenancy` 移除四條需求；`page-management`／`content-rendering` 移除主題相關需求；`demo-seed` 更新；`api-contract` 已於 4.10 處理
- [x] 6.4 最終驗證：design D6 全部項目
