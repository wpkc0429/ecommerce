> **APPROVED 2026-11-24（使用者核准：「移除舊功能，直接移除」）：可以開始實作。** 必須遵守下方的順序限制（design D4 與 tasks.md）：**頁面 schema 目前來自主題，必須先把 schema 所有權搬到每店的中性儲存（群組 1），才能移除主題。**
> 範圍：託管的租戶渲染店面、主題與主題系統、site／網域路由。**內容儲存（頁面與全域內容、schema 驗證、草稿／發佈、預覽、消毒、圖片檢查、公開讀取 API、快取失效）保留**，由另一個 change `headless-content-store` 改造。
> 本文件的每一項主張都對照程式碼查證過（檔案路徑為證；程式碼自 commit `088f508` 後只有文件變動）。若程式碼之後改變，需重新查證。

## Context

ADR 0001 與其「修正」把**託管渲染、主題、site／網域路由**棄用，**CMS 內容功能保留**（改造見 `headless-content-store`）。使用者已核准**直接移除**（2026-11-24）：沒有棄用期、沒有 sunset 視窗。本文件盤點移除這三塊的精確範圍、依賴與**安全的順序**。
**已決定（2026-11-24，使用者決定）**：①直接移除，無棄用期；②v1 表面鎖採一次性、明確記錄的例外（D1）；③不做 CMS 資料匯出（沒有真實內容）；④`Shop` 與 `setup-status` 的相關欄位消失可接受；⑤移除 demo 商家的 `demo.localhost` 站點與只為託管店面而種的資料；⑥權限節點移除 `theme.*`、`shop.manage_domains`，保留 `page.*`（屬內容儲存）。

**範圍縮小的後果先寫**：原草稿的六個耦合點中，「商家 CRUD 住在 `cms` 套件，要先抽出」**不再成立**（`cms` 保留）；v1 表面鎖仍是阻擋性的，但只涉及 16 條路由／33 行；
另有**一個順序相依**：頁面 schema 今天來自主題。**解法（已定）**：本 change 的**第一個階段**就是 `headless-content-store` 的第一個切片「每店 schema 儲存」（`content_schemas`），由本 change 自己交付；之後才移除主題。因此不需要等 `headless-content-store` 其餘部分，也不會出現半壞狀態。

## 完整清單（已驗證）

### A. 路由（`api/internal/httpapi/router.go` 掛載；皆在 `api/openapi/v1-surface.txt`）

| 群組 | 路由 | handler |
| --- | --- | --- |
| 主題（平台） | `GET/POST /admin/themes`、`GET/PUT /admin/themes/{themeID}`、`POST /admin/themes/{themeID}/pages`、`PUT/DELETE /admin/themes/{themeID}/pages/{themePageID}`（7） | `themes.go` `MountPlatform`（`theme.manage`） |
| 主題（商家） | `GET /admin/shops/{id}/themes`、`GET /admin/shops/{id}/theme/precheck`、`POST /admin/shops/{id}/theme`（3） | `themes.go` `MountShop`（`theme.view`／`shop.update`） |
| Site | `GET/POST /admin/sites`、`DELETE /admin/sites/{siteID}`、`POST /admin/sites/{siteID}/shops`、`PUT/DELETE /admin/sites/{siteID}/shops/{shopID}`（6） | `sites.go`（`shop.manage_domains`） |

合計 **16** 條路由；`v1-surface.txt` 中含 `/sites`、`/themes`、`/theme` 的行共 **33**（路由行加參數行，已用 `grep -c` 驗證）。
**不在此移除**（由 `headless-content-store` 處理）：`/admin/shops/{id}/pages*`＋`/content`＋`/page-types`（11）、`/render/page`＋`/render/preview`（2）、`POST …/media/content-images`（1）。

欄位層級（`api/openapi/openapi.yaml`，**屬於例外範圍**）：`Shop.theme_id`（`required`，約 1530 行）；`POST /admin/shops` 的 `theme_id`（約 4495）；`shop-provisioning` 請求的 `theme_id`／`domain`／`path_prefix`（約 4017–4020）與回應 `binding`（約 1690）；`setup-status` 的 `theme_applied`／`domain_bound`（約 1724–1728）；`render/page` 的 `X-Site-Domain` 參數與 Host 解析說明（約 44、3273–3282）；渲染 bundle 回應的 `shop.theme`（`code`／`layout_key`）與 `page.component_key`（回應 schema 是就地展開，不在鎖檔內）；`GET …/page-types` 回應的 `component_key`；`Site`／`SiteShopBinding`／`Theme`／`ThemePage`／`ThemeSummary` 等 schema。
**不移除**（決定）：`Shop.content_json`（全域內容屬內容儲存，其改造由 `headless-content-store` 處理）、`setup-status.home_published`、`Page.incompatible`／`PageListEntry.incompatible`（語意改為「該頁的 `type_key` 不在此商家的 schema 內」，不再指「主題不支援」）。

**鎖檔（`v1-surface.txt`）要刪的行，精確清單（約 60 行，實作時以腳本列出並逐行核對）**：33 行路由與參數行（含 `/sites`、`/themes`、`/theme` 的所有路徑與參數）＋ `GET /api/v1/render/page header X-Site-Domain`（2 行，含 optional）＋ `ProvisionResult.binding` ＋ `SetupStatus.domain_bound`／`SetupStatus.theme_applied` ＋ `Shop.theme_id` ＋ `Site.*`／`SiteShopBinding.*`／`Theme.*`／`ThemePage.*`／`ThemeSummary.*` 共 21 行。**不在此清單的行一行都不准刪。**

### B. 服務、套件與程式碼

- `api/internal/httpapi/themes.go`（248）、`sites.go`（277）；`router.go` 的 `Deps.Themes`／`Deps.Sites` 與掛載；`shops.go` 內的 `theme_id`。
- `api/internal/cms/service.go`：主題方法 `CreateTheme`／`UpdateTheme`／`CreateThemePage`／`UpdateThemePage`／`DeleteThemePage`／`PrecheckTheme`／`SwitchTheme`（約 75–280 行）與 `ShopInput.ThemeID`、`createShopTx` 的 `themeID`。**`cms` 套件其餘（頁面、全域內容、驗證、hydration、消毒、圖片網域、商家 CRUD）保留。**
  註：`currentPageSchema`（約 280 行）、`ListPages`（`Incompatible`）、`GetShopContent`／`UpdateShopContent`、`httpapi/pages.go` 的 `pageTypes` 與 `render/bundle.go` 都**查詢主題**取得 schema——這些依賴由 `headless-content-store` 改為每店 schema，**本 change 之前必須完成**（Precondition 2）。
- `api/internal/tenant/resolver.go` 的 `Resolve`／`loadEntry`／路由快取（Host→site→shop、path-prefix 消歧）；**`ResolveKey`、`clientauth.go`（publishable key、origin）保留**。
- `api/internal/httpapi/tenantmw.go` 的 Host 後備（`resolver.Resolve(r.Context(), r.Host, "/")`，約 49 行）與 `render.go` 的 `X-Site-Domain`／Host 分支。
- `api/internal/events/events.go`：`ThemeSwitched`、`ThemeUpdated` 移除；`SiteMappingChanged` **改名保留**（D3-3）；`render/invalidator.go` 對應的兩個 case 移除（失效器本身與 `PagePublished` 等保留）。
- `api/internal/provision/`：`service.go` 的 `ThemeID`、`Domain`、`PathPrefix`、`bindDomain`；`status.go` 的 `theme_applied`、`domain_bound`（`home_published` 保留）；`httpapi/provisioning.go` 對應請求／回應欄位。
- `api/internal/app/wire.go`：`deps.Themes`／`deps.Sites`、`resolver` 的 Host 相關失效（`InvalidateHosts`，約 133–141 行；`InvalidateShopKeys` 保留）。
- RBAC（`internal/seed/seed.go` `PermissionCatalog`）：移除 `theme.view`、`theme.manage`、`shop.manage_domains`；角色授予（`merchant_owner` 與 `editor` 各含 `theme.view`）。`page.*` **保留**。移除節點需處理既有 `role_permission`／`user_permission` 列。

### C. 資料（ent schema 與 migration）

| 實體／表 | 來源 | 備註 |
| --- | --- | --- |
| `Theme`/`themes`、`ThemePage`/`theme_pages` | `ent/schema/cms.go`；建於 `20260718063844_init_schema` | 平台資料，非租戶；**移除前其 schema 已被 `headless-content-store` 複製為每店 schema** |
| `Site`/`sites`、`SiteShop`/`site_shop` | `ent/schema/tenancy.go`；`site_shop` 有 `NULLS NOT DISTINCT` 手寫唯一性（`20260718063900_rbac_unique_nulls_not_distinct`）；`enttenancy_internal_test.go` 的 `notTenantOwned` 登記 | |
| `shops.theme_id`（索引 `shop_theme_id`） | `ent/schema/tenancy.go:28,79` | 欄位，非表 |

**保留**：`pages`、`shops.content_json`、`media_uploads.purpose` 的 `content_image`（原草稿需手寫 CHECK 變更，現在不需要）。下一個 migration 時間戳需大於 `20261124142354`。ent 生成碼：`api/internal/ent/{theme,themepage,site,siteshop}/` 與其餘生成檔中的相關片段（`go generate`）。

### D. Seed（`api/internal/seed/seed.go`）

`seedStarterTheme`（主題＋頁型；`starterPages`、`StarterPageSchema`、`StarterConfigSchema`）、`seedDemoShop` 內的 `demoDomain`（`demo.localhost`）站點與 `site_shop`、`SetThemeID`。
**`internal/seed/schemas/starter/*.json` 不刪**：它們是現行 demo 內容的 schema，`headless-content-store` 會把它們改成 demo 商家的每店註冊 schema（範例）。demo 商家本身與其 CMS 首頁草稿保留（電商 e2e 依賴 demo 商家；它以 publishable key `pk_demoseedstorefrontkeynotsecret` 解析，不靠 Host）。

### E. Admin（`admin/`）

只有 `views/platform-shops.js`：`GET /admin/themes`（line 78）與主題下拉（74–93）、檢查清單的 `theme_applied`（15）與 `domain_bound`、網域輸入欄；`index.html` 的對應欄位；`e2e/happy-path.mjs` ㉔ 斷言 `data-check="domain_bound"`。
**不動**：`views/pages.js`、`views/content.js`、`lib/schemaform.js`（內容編輯器，保留）、`lib/upload.js`（CMS 圖片欄位與商品圖片共檔，保留）。

### F. Web（`web/`）與官網

整個 `web/` 都是租戶渲染：`app/[[...path]]/page.tsx`、`app/preview/page.tsx`、`components/`、`lib/api.ts`、`lib/sanitize.ts`、`lib/richtext-allowlist.json`（由 `api/internal/cms/richtext/allowlist.json` 複製的產生檔）、`themes/`、`scripts/gen-types.mjs`、`Dockerfile`、`.github/workflows/image-web.yml`、`ci.yml` 的 web job。
**與官網的關係（已改變）**：使用者已決定（2026-11-24）平台官網放在**獨立 repo**、採零建置的設計層模式（`platform-website`），因此 `web/` **不會被改造成官網**，本 change 可**整個刪除 `web/`**，與官網建置完全獨立；原草稿所說「`web/` 與官網的 `output: "export"` 互斥」的問題因此消失。
（互斥本身在程式碼上仍是真的：`web/` 靠動態 Host 渲染，靜態輸出做不到；只是不再有人需要二選一。）
**需要的後續**：`web/lib/sanitize.ts` 目前是 rich text 的渲染端縱深防禦。刪除後，商家站台自己要有等價的消毒（見 `headless-content-store` 的 template 後續工作）；`allowlist.json` 仍是單一來源（API 的 `go:embed`），只是不再複製到 `web/`。

### G. e2e、CI、docs、skills

- `e2e/happy-path.mjs`：依賴 `WEB` 的斷言（使用處 102、120、151、239）與「換版 → 前台反映新主題」（約 164–249 行）；`WEB` 在 950、1038 行也用於推導 admin 的 API origin（需改為不依賴 `WEB`）；檔頭註解與 `E2E_WEB_BASE_URL`；㉔ 的 `domain_bound`。**admin 的頁面編輯與發佈斷言保留**。`e2e/commerce-path.mjs` 第 47 行註解與 demo 的 Host 說法。
- CI：**e2e 不在 CI 內**；影響僅限 web job 與 `image-web.yml`（workflow 變更已隨使用者核准一併授權）。
- 文件：`README.md`、`CLAUDE.md`（web、主題、site、Host 條目與斷言計數）、`docs/storefront-integration.md`（移除「已棄用」通知中對應部分）、`openspec/project.md`、`openspec/specs/theme-system`（整個移除）與 `multi-tenancy` 四條需求。
- Skill：`.claude/skills/merchant-onboarding/` 已是 headless 唯一路徑，只需確認 `setup-status` 欄位說明同步。

### H. 測試受影響面（已用 grep 驗證）

直接使用 `Site`／`SiteShop` ent client 的測試檔（用 `Site`＋`SiteShop`＋`req.Host = domain` 建立租戶）：`httpapi/` 的 cart、categories、clientauth、cms、guest、media、openapi_conformance、order、payment、points、richtext、shipping、shops、tenancy 各 `_integration_test.go`，`seed/seed_integration_test.go`、`tenant/enttenancy_test.go`。全 `httpapi` 測試有 22 處設定 `Host`。要改成 publishable key 或 `tenant.WithShopID`。使用 `Theme`／`ThemePage` 的測試（cms、richtext、pages 相關）隨 `headless-content-store` 改寫 schema 來源，不在本 change。另有 `wiring_integration_test.go` 的 `expectedSubscribers` 與 `openapi_contract_test.go`／`v1-surface.txt`。

## 重新評估：哪些依賴仍成立、哪些不再適用

| 原草稿的項目 | 現況 | 理由 |
| --- | --- | --- |
| 商家 CRUD 住在 `cms.Service`，要先抽出 `internal/shop` | **不再適用** | `cms` 保留。只需從 `ShopInput`／`createShopTx` 拿掉 `ThemeID`。**日後**若 `headless-content-store` 的清理階段刪除 `cms` 的舊頁面碼，才需要重新評估把商家 CRUD 搬出去——那是該 change 的事 |
| `ImageURLAllowed` 搬到 `internal/media` | **不再適用** | 圖片網域檢查是內容儲存的一部分，留在原處 |
| `showValidationErrors` 搬出 `schemaform.js` | **不再適用** | `schemaform.js` 是保留的內容編輯器 |
| `media_uploads.purpose` CHECK 的 `content_image` | **不再適用** | 內容圖片仍用它 |
| rich text、渲染快取、preview token、`cmd/sanitize-content`、`PREVIEW_TOKEN_TTL` | **不再適用**（保留） | 內容儲存使用 |
| `createHomePage` 副作用 | **不再適用**（保留，由內容儲存重塑） | `pages` 保留 |
| v1 表面鎖 | **仍成立，範圍縮小** | 16 條路由、33 行；加欄位層級破壞（見 A） |
| `SiteMappingChanged` 身兼二職（停用商家時使 key 快取失效） | **仍成立** | 見 D3-3；`Hosts` 欄位隨 sites 移除 |
| Host 後備與約 14 個測試檔靠 `Site`＋`Host` 建立租戶 | **仍成立** | 見 D3-4 |
| `setup-status`／`provision` 輸入 | **縮小** | 只剩 `theme_applied`、`domain_bound` 與 `theme_id`、`domain`、`path_prefix`、`binding` |
| `Shop` 回應 required 欄位 | **縮小** | 只剩 `theme_id`；`content_json` 保留 |
| `web/` 與官網的互斥 | **消失** | 官網獨立 repo；`web/` 整個刪除 |
| **新增**：schema 來自主題的順序相依 | **新** | Precondition 2；內容儲存先行 |

## Decisions

### D1：v1 表面鎖——一次性、明確記錄的例外（**已決定**，2026-11-24，使用者決定）
背景：`v1-surface.txt` 規定「刪除任一行＝破壞 v1」，`CLAUDE.md`／`openspec/project.md` 記載 design D13 的破例窗口**已在 `order-extras` 用盡並關閉**，不得再以「尚無外部消費者」為由破壞 v1。使用者**明確核准**本 change 的移除，所以這是一個**新的、有名稱的例外**，與 D13 的窗口不是同一件事，且**限定於本 change 列出的移除**。記錄方式（全部要做，缺一不可）：
1. **決策紀錄**：新增 `docs/decisions/0002-v1-exemption-retire-hosted-storefront-and-themes.md`：理由（整個 capability 已棄用、使用者核准、已知消費者為零：ONDO 是 headless，`web/` 是第一方，沒有其他商家）、日期、核准者、精確的移除清單（A 節）、**明說這不重新開啟 D13 的窗口，未列入的任何移除仍須另行例外**。
2. **鎖檔變更與程式碼移除在同一個原子 commit**（見 D4 階段 4）：`api/openapi/v1-surface.txt` 的刪除**只有被列出的行**；人工審閱該 commit 的鎖檔 diff（這是鎖檔設計中「必須人工執行且在 diff 中可見」的動作）。`openapi.yaml` 對應的移除在同一個 commit。
3. **新增追加專用的紀錄檔** `api/openapi/v1-removed.txt`：被例外移除的鎖檔行原文，每行一條，檔頭註解指向 ADR 0002。**新增一道契約測試**（`openapi_contract_test.go`）：斷言 `v1-removed.txt` 內的每一行都**不在**目前產生的表面中（被移除的東西不能悄悄回來），且其行數與 ADR 0002 記錄的數字一致（防止之後有人把額外的行偷偷塞進去）。既有的「鎖檔每一行必須還在」檢查不改。
4. **文件**：`CLAUDE.md`（API 契約條目）與 `openspec/project.md` 的「破例窗口已關閉」句子改為：「**已關閉；唯一的例外是 ADR 0002 記錄的一次性移除，清單見 `api/openapi/v1-removed.txt`**」；`openspec/specs/api-contract/spec.md` 增加一條需求：v1 表面鎖允許且僅允許 `v1-removed.txt` 所列的例外。

### D2：沒有棄用期（**已決定**，2026-11-24，使用者決定：「直接移除」）
不加 `Deprecation`／`Sunset` 標頭、不設觀察期、不加計數日誌。理由：已知消費者為零（ONDO／bouquet 是 headless，`web/` 是第一方，沒有其他商家，且沒有真實 CMS／主題內容）。風險由「在移除前以 `pg_dump` 備份（D5）」與階段式、每階段可獨立回滾（D4）承擔。

### D3：耦合點（逐項驗證，範圍縮小後的版本）
1. **v1 表面鎖**（D1）。
2. **（原「商家 CRUD 住在 `cms`」已刪除，見上表。）**
3. **`SiteMappingChanged` 事件身兼二職**：`cms/service.go` 在商家狀態變更時發佈它，`wire.go` 約 133–141 行的訂閱者呼叫 `resolver.InvalidateHosts` **以及 `resolver.InvalidateShopKeys`**——後者是「停用商家時，publishable key 快取同時失效」的唯一機制（CLAUDE.md 的 storefront-client-auth D10）。不可跟著刪；改名 `ShopStatusChanged`、保留 key 失效，`wiring_integration_test` 的 `expectedSubscribers` 有意識地更新。漏掉＝停用商家的金鑰會在快取 TTL 內繼續可用，**而且沒有任何測試會轉紅**（除非先補一條）。
4. **租戶解析的 Host 後備**：`NewTenantMiddleware` 在沒有 `X-Publishable-Key` 時以 `Host` 解析。移除 sites 即移除這條後備，`/api/v1/shop/*` 與 `/render/page` 變成「必須帶金鑰」——對 headless 商家沒影響，但**是行為變更**，且約 14 個整合測試檔靠 `Site`＋`Host` 建立租戶（H 節）。`docs/storefront-integration.md`、`openapi.yaml` 的「Host 解析」敘述與 `shop_disabled`（503）說明要同步。
5. **`Shop.theme_id` 是 `Shop` 回應的 required 欄位**，且 admin 與 e2e 讀它；建店路徑（`CreateShop`、`ProvisionShopTx`）接受 `theme_id`。
6. **（原「admin 共用碼」已刪除，見上表。）**
7. **新增：schema 來源相依**（Precondition 2）。

### D4：順序（**安全的順序，已定**；每個階段獨立可測、可部署，每個 commit 的 CI 為綠）
**不變式**：任何時刻，內容儲存（頁面與全域內容的驗證、發佈、讀取、預覽、快取）都完整可用；主題只在沒有人讀它之後才被刪。

1. **階段 1：每店 schema 儲存（這是 `headless-content-store` 的第一個切片，由本 change 交付）**。新增實體 `ContentSchema`（表 `content_schemas`，最終形狀見 `headless-content-store` design「資料模型」：`shop_id`、`version`、`schema_json`、`sha256`、`source`、`created_at`…）；資料遷移：對每個已套用主題的商家，把主題的 `theme_pages.page_schema`（依 `type_key`）與 `themes.config_schema`（型別 `global`）複製成該商家的 schema 版本 1（`source = "theme_backfill"`）；`SwitchTheme`、開店帶 `theme_id`、seed 套用主題時**同步寫一個新版本**（行為等價，到階段 4 為止）；讀取端全部改讀 `content_schemas`：`cms.currentPageSchema`、`GetShopContent`／`UpdateShopContent`、`ListPages` 的 `Incompatible`、`pageTypes`、`render/bundle.go`（不再查詢 `Theme`／`ThemePage`）。此階段**不刪任何東西、不改任何 API 行為**（`component_key` 暫時由 schema 儲存帶出，階段 4 才移除），現有整合測試即回歸。部署後主題表仍在但已無讀取者。
2. **階段 2：改名，不刪除**：`SiteMappingChanged` → `ShopStatusChanged`（保留 key 失效）＋補測試。
3. **階段 3：測試與 seed 不再依賴 sites／themes**：整合測試改用 publishable key；demo seed 移除 `demo.localhost` 站點與 starter 主題套用（schema 改由 seed 直接種進 `content_schemas`）。
4. **階段 4：移除前端與 API 行為（含契約例外，原子 commit）**：`web/` 整個刪除、CI web job 與 image workflow、e2e 的 `WEB` 相依斷言與換版區段、admin 開店精靈的主題／網域欄位、16 條路由、handler、`cms` 主題方法、Host 後備、events、權限節點、`provision` 與 `setup-status` 欄位、bundle 的 `theme`／`component_key`。**因為契約檢查（路由覆蓋、表面鎖）會在路由消失的那一刻要求 `openapi.yaml` 與 `v1-surface.txt` 同步，所以「刪路由」與「契約例外（D1）」必須在同一個 commit**，該 commit 同時含 ADR 0002、`v1-removed.txt` 與其測試；為了讓 commit 小到可審，之前的非契約性移除（`web/`、admin 欄位、e2e、CI、seed、權限節點的授予）先各自成 commit，路由與契約的移除最後一次做。
5. **階段 5：移除資料**：migration 1 `DROP COLUMN shops.theme_id`；migration 2 `DROP TABLE theme_pages, themes, site_shop, sites`；`go generate`；`tenantOwned`／`notTenantOwned` 登記。
6. **階段 6：文件與 skill 收尾**；封存 spec：刪除 `theme-system`、改 `multi-tenancy`、`page-management`、`content-rendering`、`api-contract`。

**`headless-content-store` 的哪些部分必須在本 change 之前或之內落地**：**只有「每店 schema 儲存」的資料表、資料遷移與讀取端切換（階段 1）**，由本 change 交付；`headless-content-store` 的其餘（schema 註冊 API、`content_documents` 與穩定 `key`、版本歷史、`content.*` 權限、發佈 hook、新的公開讀取端點、template 後續工作）**不是前置條件**，可在本 change 之後進行。`headless-content-store` 的 tasks 群組 1 與本 change 的群組 1 是**同一組工作**，只做一次。

### D5：資料遷移與回滾
- **上線前備份**：`pg_dump` `themes`、`theme_pages`、`sites`、`site_shop` 與 `shops.theme_id` 欄，以及階段 1 之後的 `content_schemas`；備份存放與驗證由操作員執行。
- **不做資料匯出**（已決定）：沒有真實 CMS／主題內容；主題 schema 已複製進 `content_schemas`（資料不丟）。
- **migration down**：照既有慣例，破壞性的 down 檔**拒絕回滾**並說明手動步驟（參考 `20261124062253_add_admin_credential_tokens.down.sql`），回滾＝從備份還原。階段 1 的 migration 是新增表，down 可直接 DROP。程式碼層階段（1–4）可用 git revert 回滾，資料表在階段 5 之前都還在。
- 階段 5 前必須在「正式資料的副本」上演練 `migrate` 與還原。

### D6：驗證計畫（每個步驟後必須通過）
- `cd api && go build ./... && make lint && make test && make openapi`（`openapi.yaml`／`v1-surface.txt` 的 diff 人工檢視，**只刪除本 change 核准的行**）。
- `make test-int`（需 infra）：`tenancy`、`clientauth`（key 撤銷、商家停用後金鑰失效）、`cart/order/payment/guest`（租戶解析）、`seed_integration_test`（demo 完整性）、`wiring_integration_test`。**內容儲存的回歸**：頁面發佈、讀取、預覽、快取失效仍通過（本 change 不得破壞）。
- `cd e2e && node commerce-path.mjs` 與 `node happy-path.mjs`（剩餘的瀏覽器區段：頁面編輯、目錄、營運、㉔ 開店精靈）。
- **仍必須通過的行為**：publishable key 解析與撤銷、CORS 的 storefront／admin 分流、admin host 綁定、`POST /admin/shop-provisioning`（headless 子集）、商家停用後 key 立即失效、結帳、冪等鍵、付款 webhook、點數與等級、通知信、報表、商品圖片、員工邀請、**內容發佈與讀取**。
- 手動：以 merchant template 的 demo 店面跑一次完整結帳（staging）；確認 `bouquet` 正式站不受影響。
- 部署後 24 小時觀察 404／5xx 與金鑰解析錯誤率。

## Risks / Trade-offs

- 順序錯誤：若在階段 1 之前刪主題，頁面無法驗證／渲染——階段 1 的整合測試（每個有頁面的商家都有 schema 版本、驗證與渲染不查主題）是階段 4 的前置閘門，寫在 `tasks.md`。
- `SiteMappingChanged` 的隱性失效（D3-3 最危險：沒有測試守著）——階段 2 先補測試。
- 破壞 v1 契約：以 D1 的記錄方式與 `v1-removed.txt` 測試控制，範圍限定於 A 節清單。
- 表被 DROP 後資料遺失：以備份與「拒絕回滾的 down」明示。
- 階段 1 的 `component_key` 暫時從 schema 儲存帶出，是過渡值；階段 4 移除後 bundle 不再含它（`web/` 同時刪除，沒有消費者）。

## Decisions record（原 Open Questions，**已全部由使用者於 2026-11-24 決定**）

1. **v1 表面鎖**：一次性、明確記錄的例外（D1），不走 `/api/v2`、不永久保留死端點。
2. **棄用期／Sunset**：沒有，直接移除（D2）。
3. **資料匯出**：不做（沒有真實內容）。
4. **`Shop.theme_id`、`setup-status.theme_applied`／`domain_bound` 消失**：可接受（A 節）。`Shop.content_json` 與 `home_published` 保留。
5. **`demo.localhost` 站點與只為託管店面而種的資料**：移除。
6. **權限節點**：移除 `theme.view`／`theme.manage`／`shop.manage_domains`；**保留 `page.*`**。留給 `headless-content-store` 的問題：`page.*` 是否改名為 `content.*`（該 change 提議新增 `content.*` 並讓 `page.*` 在相容期內等價，見其 design「權限」）；本 change 不動 `page.*`。

註記：`Page.incompatible` 語意改為「type 不在此商家 schema 內」不是新決定，只是實作者要照做。
