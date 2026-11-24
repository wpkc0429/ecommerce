> **DRAFT, DO NOT IMPLEMENT until the user approves.**
> 本文件的每一項主張都對照 commit `088f508` 的程式碼查證過（檔案路徑為證）；若程式碼之後改變，需重新查證。

## Context

ADR 0001 把租戶渲染棄用。本文件盤點「真要移除」時的精確範圍與依賴。**結論先寫：這不是單純刪檔**——有六個耦合點會讓移除比預期難（D3），其中兩個（v1 表面鎖、商家 CRUD 住在 `cms` 套件）是阻擋性的。

## 完整清單（已驗證）

### A. 路由（`api/internal/httpapi/router.go` 掛載；皆在 `api/openapi/v1-surface.txt`）

| 群組 | 路由 | handler |
| --- | --- | --- |
| 主題（平台） | `GET/POST /admin/themes`、`GET/PUT /admin/themes/{themeID}`、`POST /admin/themes/{themeID}/pages`、`PUT/DELETE /admin/themes/{themeID}/pages/{themePageID}`（7） | `themes.go` `MountPlatform`（`theme.manage`） |
| 主題（商家） | `GET /admin/shops/{id}/themes`、`GET /admin/shops/{id}/theme/precheck`、`POST /admin/shops/{id}/theme`（3） | `themes.go` `MountShop`（`theme.view`／`shop.update`） |
| 頁面 | `GET/POST /admin/shops/{id}/pages`、`GET/PUT/DELETE …/pages/{pageID}`、`POST …/publish`、`POST …/unpublish`、`GET …/preview-token`、`GET/PUT /admin/shops/{id}/content`、`GET /admin/shops/{id}/page-types`（11） | `pages.go`（`page.*`、`shop.view`、`shop.update`） |
| Site | `GET/POST /admin/sites`、`DELETE /admin/sites/{siteID}`、`POST /admin/sites/{siteID}/shops`、`PUT/DELETE /admin/sites/{siteID}/shops/{shopID}`（6） | `sites.go`（`shop.manage_domains`） |
| 渲染 | `GET /render/page`、`GET /render/preview`（2） | `render.go` |
| CMS 圖片 | `POST /admin/shops/{id}/media/content-images`（1） | `media.go:37`（`page.edit`） |

合計 **30** 條（`v1-surface.txt` 以 regex 統計：路由行 30；與這些路徑相關的所有行〔參數、回應標頭〕約 73 行）。`api/openapi/openapi.yaml` 的 `Page`、`PageListEntry`、`Theme`、`ThemePage`、`ThemeSummary`、`Site`、`SiteShopBinding`、`RenderBundle` 等 schema 與 `X-Site-Domain` 參數隨之移除；**`Shop` schema 的 `theme_id`、`content_json` 為 required 欄位**（`openapi.yaml` 約 1530 行），`shop-provisioning` 請求的 `theme_id`（約 4017 行）與 `setup-status` 回應的三個布林同理——它們是**欄位層級**的破壞，不只是路由層級。

### B. 服務、套件與程式碼

- `api/internal/cms/`（`service.go` 817、`hydrate.go`、`schemaval.go`、`sanitize.go`、`fieldorder.go`、`imagedomain.go`、`backfill.go`、`richtext/`）——主題 CRUD、頁面生命週期、hydration、schema 驗證、rich text 消毒、**以及商家 CRUD（見 D3-2）**。
- `api/internal/render/`（`bundle.go`、`cache.go`、`invalidator.go`）——bundle 組裝、`cache:shop:{id}:v{ver}:page:{slug}`、事件失效。
- `api/internal/tenant/resolver.go` 的 `Resolve`／`loadEntry`／路由快取（Host→site→shop、path-prefix 消歧）；**`clientauth.go`（publishable key、origin）保留**。
- `api/internal/auth/jwt.go` 的 `IssuePreview`／`VerifyPreview`／`PreviewClaims`；`config.PreviewTokenTTL`（`PREVIEW_TOKEN_TTL`，`.env.example:74`）。
- `api/internal/httpapi/`：`themes.go`、`pages.go`、`sites.go`、`render.go`；`shops.go` 內的 `theme_id`（第 40 行）；`router.go` 的 `Deps.Themes/Pages/Sites/Render` 與掛載；`tenantmw.go` 的 Host 後備（`resolver.Resolve(r.Context(), r.Host, "/")`）。
- `api/internal/app/wire.go`：`resolver` 的 `SiteMappingChanged` 訂閱（line 133）保留其 key 失效功能（D3-3）；`render.NewCache`／`Invalidator`／`dispatcher.Subscribe(invalidator.Handle)`（178–181）、`cmsService`（191–）、`deps.Themes/Pages/Sites/Render`（175、198、204、406–）。
- `api/cmd/sanitize-content/`、`Makefile` 的 `sanitize-content` 目標與 `web-dev` 目標；`.env.example` 的 `PREVIEW_TOKEN_TTL` 與 `# ─── web (storefront SSR)` 區段（`API_BASE_URL`、`WEB_PORT`）。
- `api/internal/events/events.go`：`PagePublished`、`PageUnpublished`、`ShopContentUpdated`、`ThemeSwitched`、`ThemeUpdated`（移除）；`SiteMappingChanged`（**保留並更名**，D3-3）。
- RBAC 權限節點（`internal/seed/seed.go` `PermissionCatalog`）：`page.view/create/edit/delete/publish`、`theme.view`、`theme.manage`、`shop.manage_domains`；角色授予（`seed.go` 約 161、196 行）。移除節點需處理既有 `role_permission`／`user_permission` 列。

### C. 資料（ent schema 與 migration）

| 實體／表 | 來源 | 備註 |
| --- | --- | --- |
| `Theme`/`themes`、`ThemePage`/`theme_pages` | `ent/schema/cms.go`；建於 `20260718063844_init_schema` | 平台資料，非租戶 |
| `Page`/`pages` | `ent/schema/cms.go`；`tenantOwned["Page"]`（`tenant/enttenancy.go:16`） | 租戶資料；含 `content_json`、`published_json` |
| `Site`/`sites`、`SiteShop`/`site_shop` | `ent/schema/tenancy.go`；`site_shop` 有 `NULLS NOT DISTINCT` 手寫唯一性（`20260718063900_rbac_unique_nulls_not_distinct`）；`enttenancy_internal_test.go:34` 的 `notTenantOwned` 登記 | |
| `shops.theme_id`（索引 `shop_theme_id`）、`shops.content_json` | `ent/schema/tenancy.go` | 欄位，非表 |
| `media_uploads.purpose` CHECK 的 `'content_image'` | `ent/schema/media.go:59`、`ent/migrate/schema.go:1515` | 需手寫 CHECK 變更（atlas 不偵測 CHECK 運算式變更） |

下一個 migration 時間戳需大於 `20261124142354`。ent 生成碼：`api/internal/ent/{theme,themepage,page,site,siteshop}/` 整個目錄與其餘生成檔中的相關片段（`go generate`）。

### D. Seed（`api/internal/seed/seed.go`）

`seedStarterTheme`（主題＋三個頁型；`starterPages`、`StarterPageSchema`、`StarterConfigSchema`、`internal/seed/schemas/starter/*.json`）、`seedDemoShop` 內的 `demoDomain`（`demo.localhost`）站點與 `site_shop`（約 484–530 行）、CMS 首頁草稿（約 532–560 行）、`SetThemeID`；`cmd/seed/main.go` 與 `seed_integration_test.go` 對應斷言（5 處 ent 引用）。**demo 商家本身保留**（電商 e2e 依賴它；它以 publishable key `pk_demoseedstorefrontkeynotsecret` 解析，不靠 Host）。

### E. Admin（`admin/`）

- 視圖：`views/pages.js`（158）、`views/content.js`（33）；`app.js` 的 import（18–19）與註冊表（52–54）；`index.html` 的 `#tab-pages`、`#tab-editor`、`#tab-content` 面板。
- `lib/schemaform.js`（300 行，CMS 的 JSON-Schema→表單編譯器）：**`views/products.js:16` 匯入其 `showValidationErrors`**——須先把這個函式搬到 `lib/dom.js`，否則商品頁壞掉。
- `lib/upload.js` 的 CMS 圖片欄位段（119–131，`x-editor: image`）與 `purpose: content_image` 流程；`docs/admin-frontend-integration.md` 對應段落（29、45 行）。
- `views/platform-shops.js`：`GET /admin/themes`（line 78）與 `theme_id` 欄位（93）、檢查清單的 `theme_applied`／`domain_bound`／`home_published`（15–19 行附近）、網域輸入欄；`e2e/happy-path.mjs` ㉔ 斷言 `data-check="domain_bound"`。

### F. Web（`web/`，若 `platform-website` 尚未處理）

`app/[[...path]]/page.tsx`（70）、`app/preview/page.tsx`、`components/Maintenance.tsx`、`lib/api.ts`（69）、`lib/sanitize.ts`、`lib/richtext-allowlist.json`（由 `api/internal/cms/richtext/allowlist.json` 複製的產生檔）、`themes/`（`registry.ts` 30、`starter/*`）、`scripts/gen-types.mjs`、`Dockerfile`、`.github/workflows/image-web.yml`、`ci.yml` 的 web job（`npm ci`、「Generated types are current」、`npm run build`）。

### G. e2e、CI、docs、skills

- `e2e/happy-path.mjs`：①–⑦ 區段（約 48–257 行，CMS 與主題；其中 ⑤⑥⑥b⑦ 依賴 `WEB`，使用處 102、120、151、239）；`WEB` 在 950、1038 行也用於推導 admin 的 API origin（需改為不依賴 `WEB`）；檔頭註解與 `E2E_WEB_BASE_URL`。`e2e/commerce-path.mjs` 第 47 行註解與 demo 的 Host 說法。
- CI：**e2e 不在 CI 內**（`openspec/project.md`、`.github/workflows/ci.yml` 只有 API 與 web 兩個 job）；CI 影響僅限 web job 與 `image-web.yml`。API job 會因整合測試改動而重跑。
- 文件：`README.md`、`CLAUDE.md`（多條）、`docs/storefront-integration.md`（棄用通知）、`docs/media-r2-setup.md`（CMS 圖片欄位一節）、`docs/admin-frontend-integration.md`、`openspec/project.md`、`openspec/specs/{content-rendering,page-management,theme-system}`（整個移除）與 `multi-tenancy` 四條需求。
- Skill：`.claude/skills/merchant-onboarding/` 已在 ADR 步驟 1 改為 headless 唯一路徑，移除時只需確認 `setup-status` 欄位的說明同步。

### H. 測試受影響面（已用 grep 驗證）

直接使用 `Site`／`SiteShop`／`Theme`／`Page` ent client 的測試檔：`httpapi/` 的 cart、categories、clientauth、cms、guest、media、openapi_conformance、order、payment、points、richtext、shipping、shops、tenancy 各 `_integration_test.go`，`seed/seed_integration_test.go`、`tenant/enttenancy_test.go`。其中 cart／guest／order／payment／points／shipping／categories／media 等**只是用 `Site`＋`SiteShop`＋`req.Host = domain` 建立租戶**（例如 `cart_integration_test.go:56–58、128`），要改成 publishable key 或 `tenant.WithShopID`。全 `httpapi` 測試有 22 處設定 `Host`。另有 `wiring_integration_test.go` 的 `expectedSubscribers`（≥2 行要改）與 `openapi_contract_test.go`／`v1-surface.txt`。

## 已驗證的依賴（回應任務要求的幾項）

- **`setup-status`**（`provision/status.go`）：八個布林中 `theme_applied`（`shops.theme_id`）、`domain_bound`（`site_shop`）、`home_published`（`pages`）直接查被移除的表；`openapi.yaml` 與 `admin/views/platform-shops.js`、`e2e` ㉔ 讀它們。移除＝回應欄位減少（v1 破壞）。
- **渲染快取失效器**（`render/invalidator.go`）：只服務渲染快取，可隨之移除；但它訂閱的 `SiteMappingChanged` 在 `wire.go:133` 另有一個**不同的**訂閱者（見 D3-3）。
- **rich text 消毒**：只有 CMS 使用（grep：`richtext` 套件只被 `cms/sanitize.go` 引用；商品描述不經它）。**可乾淨移除**；`web/lib/richtext-allowlist.json` 是它的產生檔。
- **admin host 綁定**（`httpapi/adminorigin.go`）：使用 `OriginRegistry.LookupOrigin`（`shop_allowed_origins`，`purpose=admin`），**不使用 sites**。不受影響。
- **CORS 的商家 origin**：`shop_allowed_origins`，不受影響（`docs/storefront-integration.md` 寫的是 origin 不是 site 網域）。
- **`provision.Service.ProvisionShop`**：`CreateShopTx`（`cms`）、`bindDomain`（`site`）、`SiteMappingChanged`；輸入的 `ThemeID`、`Domain`、`PathPrefix`。需改寫成不含這三者的版本（`platform-website` 的核准流程只用 headless 子集，所以不受此影響，但函式簽名要改）。
- **`createHomePage`**：每個新商家（`CreateShop`、`ProvisionShopTx`）都自動建 `pages` 首頁列（`cms/service.go:594`）；移除 `pages` 時必須同時移除這個副作用，且 `shop-provisioning`／`POST /admin/shops` 的行為改變。

## Decisions

### D1：v1 表面鎖——阻擋性的先決決定
`v1-surface.txt` 規定「刪除任一行＝破壞 v1」，且 `CLAUDE.md`／`openspec/project.md` 記載 design D13 的破例窗口**已在 `order-extras` 用盡並關閉，後續不得再以『尚無外部消費者』為由破壞 v1**。因此本 change 的路由與欄位移除**不能**直接做。選項：
- (a) **明確的 v1 破例**：使用者書面核准一次性破例（理由是「整個 capability 已棄用且經棄用期無消費者」，與 D13 的「尚無外部消費者」不同——有 `Sunset` 期間的證據），並由人手動刪除鎖檔的相應行（diff 中可見）。需要更新 `api-contract` spec 說明破例條件。
- (b) **`/api/v2`**：新增 v2 表面（不含這些端點），v1 繼續存在直到整體 sunset。成本高（重複路由表與契約檢查），且 v1 的那些端點仍要維護。
- (c) **永遠保留 v1 端點但不維護**：只移除前端、seed 與 UI，API 與表永久保留為「死端點」。風險最低、清理最少；`cms`／`render` 套件繼續存在。
**建議 (a)，且僅在棄用期結束後**。這是需要使用者回答的問題 Q1。

### D2：棄用期與 Sunset 標頭
進入條件：使用者核准後，**先做一個只加標頭的 change**（additive，不破壞）：對上述 30 條路由回 `Deprecation: true`、`Sunset: <HTTP-date>`、`Link: <…/docs/decisions/0001…>; rel="deprecation"`；`openapi.yaml` 標 `deprecated: true`（不改 `v1-surface.txt`）；以中介層一處加入，測試斷言標頭存在、其他路由沒有。棄用期建議 ≥ 90 天（Q2）。偵測是否仍有使用：在棄用期內對這些路由加結構化存取日誌計數（只記路由名與 shop_id，不記 PII），到期前檢視為零才進入移除。**目前已知消費者為零**：ONDO（bouquet）是 headless；`web/` 是第一方；沒有其他商家。

### D3：六個讓移除變難的耦合點（逐項驗證）
1. **v1 表面鎖**（D1）。
2. **商家 CRUD 住在 `cms.Service`**：`CreateShop`／`ListShops`／`GetShop`／`UpdateShop`（`cms/service.go` 623–817）、`ShopsHandler`（`httpapi/shops.go` 用 `cms.ShopInput`／`ShopListParams`／`ShopUpdate`）與 `provision`（`CMS: cmsService`）都經它。要先把它們抽到新的 `internal/shop`（行為逐位元組不變，既有整合測試即回歸），之後才能刪 `cms`。同一個套件還有 `ImageURLAllowed`（`httpapi/branding.go:71` 與 `settings.go` 的 `ImagePrefixes` 使用，`wire.go:403` 以 `cmsService.ImagePrefixes` 注入）——需搬到 `internal/media`。
3. **`SiteMappingChanged` 事件身兼二職**：`cms/service.go:814` 在商家狀態變更時發佈它，`wire.go:133` 的訂閱者呼叫 `resolver.InvalidateHosts` **以及 `resolver.InvalidateShopKeys`**——後者是「停用商家時，publishable key 快取同時失效」的唯一機制（CLAUDE.md 的 storefront-client-auth D10）。不可跟著刪；要改名（如 `ShopStatusChanged`）並保留 key 失效，且 `wiring_integration_test` 的 `expectedSubscribers` 需有意識地更新。漏掉＝停用的商家的金鑰會在快取 TTL 內繼續可用，**而且沒有任何測試會轉紅**（除非先補一條）。
4. **租戶解析的 Host 後備**：`NewTenantMiddleware` 在沒有 `X-Publishable-Key` 時以 `Host` 解析（`tenantmw.go`）。移除 sites 即移除這條後備，`/api/v1/shop/*` 變成「必須帶金鑰」——對 headless 商家沒影響，但**是行為變更**，且 ~14 個整合測試檔靠 `Site`＋`Host` 建立租戶（H 節）。`docs/storefront-integration.md`、`openapi.yaml` 的「Host 解析」敘述與 `shop_disabled`（503）的說明要同步。
5. **`shops.theme_id`／`content_json` 是 `Shop` 回應的 required 欄位**，且 admin 與 e2e 讀它們；`createHomePage` 副作用同在建店路徑。
6. **admin 共用碼**：`schemaform.js` 的 `showValidationErrors` 被商品視圖使用（E 節）；`upload.js` 的 CMS 區段與商品圖片共檔。

另一個較小的：`media_uploads.purpose` 的 CHECK 含 `content_image`（手寫 migration）；`seed` 的 `PermissionCatalog` 移除節點需處理既有授權列；`tenantOwned` 與 `notTenantOwned` 的登記在刪實體後必須同步（測試會要求）。

### D4：順序（每一步獨立可部署、可回滾）
0. 棄用標頭（D2）＋觀察期。
1. **搬家，不刪除**：抽出 `internal/shop`（D3-2）、搬 `ImageURLAllowed`、搬 `showValidationErrors`、把 `SiteMappingChanged` 改名並保留 key 失效、補一條「停用商家後金鑰快取立即失效」的測試。此步驟後所有既有測試仍綠。
2. 讓測試與 seed 不再依賴 sites／pages／themes：整合測試改用 publishable key（或 `tenant.WithShopID`）；demo seed 移除 CMS 首頁與 `demo.localhost` 站點（保留商家、金鑰、origin）。
3. 移除前端：admin 視圖、`platform-shops.js` 欄位、e2e 區段、（若未做）`web/`、CI 步驟。
4. 移除 API 行為（需 D1 的決定）：路由、handler、`cms`／`render`、Host 後備、preview token、events、權限節點、`setup-status` 三欄、`provision` 輸入、`createHomePage`。
5. 移除資料：migration 1（`DROP` 外鍵與欄位 `shops.theme_id`／`content_json`、CHECK 變更）、migration 2（`DROP TABLE theme_pages, themes, pages, site_shop, sites`）、`go generate`、`tenantOwned` 清單。
6. 文件與 skill 收尾；封存 spec；`specs/` 刪除三個 capability、改 `multi-tenancy`。

### D5：資料遷移與回滾
- **上線前備份**：`pg_dump` 這五張表與 `shops` 兩欄；備份存放與驗證流程由操作員執行（本 change 不碰部署）。
- **匯出而非靜默丟棄**：提供一次性的 `cmd/export-cms`（唯讀，輸出 JSON 到 stdout／檔案）供想保留內容的人使用——是否真的需要由 Q3 決定（目前沒有商家有 CMS 內容；demo 的首頁是 seed 出來的）。
- **migration down**：照既有慣例，破壞性的 down 檔**拒絕回滾**並說明手動步驟（參考 `20261124062253_add_admin_credential_tokens.down.sql`），回滾＝從備份還原那五張表與兩欄。因此資料層回滾的時間點是「migration 之前」；所有**程式碼層**的步驟（D4 的 0–4）可用 git revert 回滾，資料表在步驟 5 之前都還在。
- 步驟 5 前必須在「正式資料的副本」上演練 `migrate` 與還原。

### D6：驗證計畫（每個步驟後必須通過）
- `cd api && go build ./... && make lint && make test && make openapi`（`openapi.yaml`／`v1-surface.txt` 的 diff 人工檢視，**只刪除本 change 核准的行**）。
- `make test-int`（需 infra）：特別是 `tenancy`、`clientauth`（key 撤銷、商家停用後金鑰失效）、`cart/order/payment/guest`（租戶解析）、`seed_integration_test`（demo 完整性不變式：等級門檻可被單筆訂單跨過）、`wiring_integration_test` ⑤⑥⑦。
- `cd e2e && node commerce-path.mjs`（電商鏈）與 `node happy-path.mjs`（剩餘的瀏覽器區段：目錄、營運、㉔ 開店精靈、㉕㉖㉗）。
- **仍必須通過的行為（回歸清單）**：publishable key 解析與撤銷、CORS 的 storefront／admin 分流、admin host 綁定、`POST /admin/shop-provisioning`（headless 子集）與 owner 密碼連結、商家停用後 key 立即失效、訪客／會員結帳、冪等鍵、付款 webhook、點數與等級、通知信、報表、商品圖片、員工邀請。
- 手動：以 merchant template 的 demo 店面跑一次完整結帳（針對 staging）；確認 `bouquet` 正式站不受影響（它只用 `/api/v1/shop/*` 與金鑰——移除前以日誌計數再確認）。
- 部署後 24 小時觀察 404／5xx 與 `X-Publishable-Key` 解析錯誤率。

## Risks / Trade-offs

- 破壞 v1 契約（D1）：以棄用期、日誌計數與使用者明確核准控制。
- 多個耦合點的隱性失效（D3-3 最危險：沒有測試守著）——步驟 1 先補測試。
- 表被 DROP 後資料遺失：以備份與「拒絕回滾的 down」明示。
- 長期工程量：API 約 3,000 行＋14 個測試檔改動＋兩個 migration，建議拆成 D4 的步驟分次核准。
- 若選 D1(c)（永久保留死端點），本 change 縮減為步驟 0–3，風險最低。

## Open Questions（需使用者回答）

1. **Q1**：v1 表面鎖的處置——(a) 一次性破例、(b) `/api/v2`、(c) 永久保留死端點？（建議 (a)，在棄用期後。）
2. **Q2**：棄用期長度與 `Sunset` 日期（建議 ≥ 90 天）。
3. **Q3**：是否需要保留既有 CMS／主題資料的匯出？（目前看起來沒有真實內容。）
4. **Q4**：`shops.theme_id`／`content_json` 與 `setup-status` 欄位移除時，是否接受 `Shop` 回應與 `setup-status` 的欄位消失（需在 v1 破例範圍內一併處理）？
5. **Q5**：demo 商家是否仍保留 `demo.localhost` 站點供本機開發？（建議移除；e2e 與 seed 都不需要它。）
6. **Q6**：RBAC 權限節點 `page.*`／`theme.*`／`shop.manage_domains` 是移除還是保留為保留字（避免自訂角色引用失效）？
