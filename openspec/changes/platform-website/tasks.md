> 前置：design「Open Questions」尚未回答的項目（Q3–Q12）需由使用者回答後才可實作。**已決定（2026-11-24，使用者決定）**：①官網是零建置靜態站，沿用 merchant template 的鎖定核心＋設計層、守門員與嚴格 CSP；②官網程式碼放在**獨立 repo**，不放在 ecommerce repo，`web/` 不動。
> 工作分兩部分：**A. 落在 ecommerce repo**（群組 1–9：公開申請端點、`shop_applications` 模型、平台後台審核與核准、Turnstile 驗證、通知、限流與稽核、文件與 e2e）；**B. 落在新的官網 repo**（群組 B1–B7：腳手架、鎖定核心的表單邏輯、設計層頁面、守門員、CSP、部署與 Cloudflare、文案、live demo 連結）。A 的群組 1–7 不依賴 B，B 的表單核心需要 A 的端點契約（群組 5）先定。

> **放置規則（design D13，使用者決定）**：官網 repo 只放靜態內容與只呼叫公開申請端點的鎖定核心，**不需要任何自己的後端**（沒有 server／function／資料庫／secrets）；Turnstile secret、儲存、審核、通知、稽核都在 A 部分。

# A. ecommerce repo

## 1. 資料模型（design D4）

- [ ] 1.1 新增 `api/internal/ent/schema/application.go`：`ShopApplication`、`ShopApplicationToken`、`ShopApplicationEvent`、`ShopApplicationMessage`（欄位、CHECK、部分唯一索引 `UNIQUE(email) WHERE status IN (…)`；**不得有名為 `shop_id` 的欄位**，用 `provisioned_shop_id`）
- [ ] 1.2 `cd api && GOTOOLCHAIN=go1.26.4 go generate ./internal/ent`；`make migrate-gen name=add_shop_applications`；手動剪除 atlas 夾帶的假差異（見 CLAUDE.md 的 migration 注意）、手寫部分唯一索引與 CHECK、重算 `atlas.sum`；down 檔 DROP 四表
- [ ] 1.3 `api/internal/tenant/enttenancy_internal_test.go`：確認新表不帶 `shop_id` 因而無需分類（測試應保持綠）；若選擇加 `shop_id` 則必須分類
- [ ] 1.4 整合測試：部分唯一索引（同 email 兩筆未完成申請第二筆衝突、已拒絕後可再建）

## 2. 設定與 Turnstile（design D3）

- [ ] 2.1 `api/internal/config/config.go` ＋ `.env.example`：`TURNSTILE_SECRET_KEY`、`TURNSTILE_SITE_KEY`（僅供文件）、`WEBSITE_URL`（驗證連結基底，http(s) origin）、`APPLICATION_DAILY_CAP`、`APPLICATION_REAPPLY_COOLDOWN`、`APPLICATION_REJECTED_RETENTION`、`APPLICATION_APPROVED_RETENTION`、`RATE_LIMIT_APPLICATION_{BROAD,TIGHT,EMAIL}_{LIMIT,WINDOW}`；production 缺 `TURNSTILE_SECRET_KEY` 或 `WEBSITE_URL` 拒絕啟動；單元測試覆蓋
- [ ] 2.2 新增 `api/internal/turnstile/verify.go`（`Verifier` 介面、HTTP 實作、3 秒逾時、5xx／網路錯誤回 `ErrUnavailable`）與 `verify_test.go`（`httptest` 伺服器：成功、失敗、逾時、5xx、錯誤碼對應；不連網）
- [ ] 2.3 `api/internal/seed/seed.go`：`PermissionCatalog` 新增 `shop_application.view`、`shop_application.review`（平台層）；授予 `super_admin`（`*` 已涵蓋則補測試斷言**未**授予任何商家角色）；`seed_integration_test.go` 對應更新

## 3. 申請服務與公開端點（design D2–D6）

- [ ] 3.1 新增 `api/internal/application/service.go`：`Submit`（正規化、冷卻／重複判斷、入庫、簽 token、寫 outbox、稽核）、`Verify`（原子消費 token）、`VerifyManually`、`Reject`、`List`／`Get`、`Purge`；所有轉移用條件式 UPDATE ＋影響列數
- [ ] 3.2 新增 `api/internal/application/notifier.go`：`Notifier` 介面、`OutboxNotifier`、`SMTPNotifier`（包 `notify.Sender`）；`wire.go` 依 `cfg.SMTP.Enabled()` 選擇（兩者皆是真實實作，非 nil）
- [ ] 3.3 新增 `api/internal/httpapi/applications_public.go`：`POST /api/v1/public/shop-applications`（一律 202）、`POST …/verify`（無效 token 一律 404）；body 上限 16 KiB、欄位驗證、honeypot、Turnstile、回應位元組一致
- [ ] 3.4 `api/internal/httpapi/ratelimitmw.go`：新增 `emailRule`（只依 email）與 `NewApplicationRateLimit`、`NewApplicationVerifyRateLimit`；全站日上限（Redis 計數 ＋ DB `COUNT` 降級）
- [ ] 3.5 `api/internal/httpapi/router.go`：`Deps` 新增 `ApplicationsPublic`、`Applications`、限流欄位；掛載 `/api/v1/public/*`（不經 TenantMW／Admin 中介層）；`wire.go` 建構注入
- [ ] 3.6 測試 `api/internal/application/*_test.go`（單元）與 `api/internal/httpapi/applications_integration_test.go`：新 email／重複未完成／冷卻中／已是後台帳號 → **回應位元組相同**；honeypot；Turnstile 失敗／故障；同意版本錯誤 422；per-IP 與 per-email 429＋`Retry-After`；token 單次使用、過期、重發使舊 token 失效；驗證前不可核准；並發兩個 verify 恰一個成功；日誌 PII 測試（slog buffer 找不到 email／姓名／電話）
- [ ] 3.7 `api/internal/app/wiring_integration_test.go`：確認 ⑤（Deps 全填）⑥（路由表）通過；**不新增領域事件／訂閱者**（⑦ 不動）

## 4. 核准與開店（design D9）

- [ ] 4.1 `api/internal/provision/service.go`：拆出 `ProvisionShopTx`／`AfterCommit`，`ProvisionShop` 改為組合兩者；跑既有整合測試（`accounts_integration_test.go` 的開店案例）確認逐位元組相同
- [ ] 4.2 `api/internal/application/service.go`：`Approve`（單一交易：條件式 UPDATE → `ProvisionShopTx` → 寫 `provisioned_shop_id` ＋ 事件；commit 後 `AfterCommit`）；owner 已有帳號需 `confirm_existing_account`
- [ ] 4.3 新增 `api/internal/httpapi/applications.go`：`GET /admin/shop-applications`（`status`、`q`、分頁）、`GET …/{id}`（含事件時間線與 outbox 訊息）、`POST …/{id}/approve`、`POST …/{id}/reject`（`decision_reason` 必填）、`POST …/{id}/verify-manually`、`POST …/{id}/messages/{mid}/mark-sent`；守衛 `shop_application.view`／`.review`，approve 另檢 `shop.create`＋`account.manage`
- [ ] 4.4 測試：核准成功（商家＋owner＋連結一次性回傳＋申請列 `approved` 同交易）、開店失敗整筆回滾且申請仍 `pending_review`、重複核准 409、未驗證 409、既有帳號未確認 409、權限組合 403、拒絕需理由、跨狀態轉移被拒
- [ ] 4.5 `api/internal/app/authguard_integration_test.go`：新路由皆有 RBAC 守衛，`expectedUnguardedAdminRoutes` 不變

## 5. 契約

- [ ] 5.1 `api/openapi/openapi.yaml`：新增全部新路由、請求／回應 schema（金額無關）、`/api/v1/public` 的 429／422 回應、`Retry-After` 標頭
- [ ] 5.2 `cd api && go test ./internal/httpapi/ -run TestOpenAPIV1SurfaceLock -openapi.update-surface`（只新增行；檢視 diff 確認沒有刪除）；`make openapi` 全綠

## 6. 清除與保存（design D8）

- [ ] 6.1 新增 `api/cmd/purge-applications/main.go`（預設 dry run、`-apply`、`-email <addr>` 個資請求；冪等；只印 id）；`Makefile` 新增 `purge-applications` 目標（dry run）——**此項為 repo 指令不屬於部署設定**
- [ ] 6.2 整合測試：未驗證 7 天、拒絕後保存期、核准後保存期、冷卻未結束不清、事件列保留且無 PII

## 7. 平台後台審核視圖（admin）

- [ ] 7.1 新增 `admin/views/platform-applications.js`：列表（狀態篩選、未驗證標示、風險旗標）、詳情（時間線、outbox 訊息與「複製／標記已寄出」、人工確認、核准對話框〔店名、狀態、既有帳號確認〕、拒絕對話框〔理由必填〕）；核准結果一次性顯示 `setup_url`（同 `platform-shops.js`，不寫入任何儲存）
- [ ] 7.2 `admin/app.js` 註冊表一列（`perm: "shop_application.view"`，平台層以 `platform_permissions` 判斷可見性）、`admin/index.html` 一個 `.tab-panel`；待處理數徽章
- [ ] 7.3 e2e：`e2e/happy-path.mjs` 新增一節（API 提交→取得 outbox 驗證連結→驗證→後台核准→owner 設密碼登入）；注意限流 key 在二次執行下的影響（e2e 用唯一 email、`REDIS_DB` 分離）
- [ ] 7.4 守住 tripwire #4′／#5 的量測並記錄在 CLAUDE.md 的 admin 條目

## 8. e2e 與測試（ecommerce 側）

- [ ] 8.1 e2e：`e2e/happy-path.mjs` 或新檔 `e2e/application-path.mjs` 新增申請流程（以 API 直接提交——官網在另一個 repo，此處不經瀏覽器表單）：API 提交→取得 outbox 驗證連結→驗證→後台核准→owner 設密碼登入；`CLAUDE.md` 同步斷言計數
- [ ] 8.2 契約範例：在 `openapi.yaml` 為申請端點的請求／回應與錯誤碼補完整範例，供官網 repo 的核心做契約測試（B3.4）
- [ ] 8.3 **不動** `web/`、`e2e` 中依賴 `WEB` 的斷言、`.github/workflows/*`：它們屬於 `retire-hosted-storefront-and-themes`

## 9. 文件、skill 與驗證（ecommerce 側）

- [ ] 9.1 `docs/deploy/platform-website-api-notes.md`（ecommerce 這側的營運說明）：`TURNSTILE_*`、`WEBSITE_URL`、`CORS_ALLOWED_ORIGINS` 加官網 origin、每日排程 `purge-applications`、人工模式（outbox）操作員 SOP；官網自身的部署 runbook 在官網 repo
- [ ] 9.2 更新 `.claude/skills/merchant-onboarding/SKILL.md` 與 `references/*`：把「申請流程」由「規劃中」改為現況（intake 來源、審核、核准後接 step 3）；`known-gaps.md` 移除對應項目
- [ ] 9.3 更新 `docs/PLATFORM-ROADMAP.md`、`README.md`、`CLAUDE.md`（新表與限流、PII 規則、官網在獨立 repo 的說明）；`openspec/project.md`
- [ ] 9.4 `docs/decisions/` 追加 ADR（官網採零建置獨立 repo；0002 已預留給 v1 例外，編號順延；補充「平台後台也應切成核心與設計層」的 follow-up）
- [ ] 9.5 驗證：`cd api && go build ./... && make lint && make test && make openapi`；`make test-int`（需 docker infra；串行）；`cd e2e && node commerce-path.mjs`（改動 wire.go 後必跑）與申請流程 e2e
- [ ] 9.6 手動：Turnstile 測試金鑰全流程（含官網 repo 的表單）；無 SMTP（outbox）與有 SMTP（`SMTPNotifier`）兩種模式各走一次；確認回應位元組在新／重複／冷卻三種情況相同

# B. 新的官網 repo（不在 ecommerce repo 內；本 change 只規範內容，建立 repo 需使用者核准）

## B0. 範圍檢查（放置規則）

- [ ] B0.1 官網 repo 內**不得**出現：server 程式、serverless function、資料庫、自己的 secrets、對 ecommerce 內容儲存的讀取；守門員（`check-layers`）加規則：設計層與核心只允許對 `config.js` 的 `apiBase` 發出申請端點的請求，其他網路呼叫一律拒絕
- [ ] B0.2 頁面內容（介紹、範本展示、價格、條款、隱私、文案）一律是 repo 內的靜態 HTML／資料檔，不在執行期抓取

## B1. 腳手架與守門員（design D1、Q12）

- [ ] B1.1 決定 repo 名稱與擁有者（Q12）；以 merchant template 的 `scripts/`（`check-scope.mjs`、`check-locked.mjs`、`check-layers.mjs`、`sync-core.mjs`、`lib/`）與 `template.json` 格式建立：`sites.website.designPaths`（各頁 HTML、`site.css`／tokens、文案、`assets/`、`sitemap.xml`、`robots.txt`）、`lockedPaths`（表單核心、`_headers`、守門員、`config.js`）、`manifests.website`（sha256 雜湊清單）
- [ ] B1.2 守門員：`check-design`（靜態掃描：設計層不得使用 `fetch`／`eval`／內嵌事件屬性／外部 URL／`<script>` 以外來源，沿用 `admin/docs/DESIGN-LAYER.md` §6 的規則）、`check-scope --base <ref>`、`check-locked`；CI（GitHub Actions）跑三者
- [ ] B1.3 `CLAUDE.md`（官網 repo 的 AI 操作手冊，仿 `DESIGN-LAYER.md`）：可編輯／不可編輯路徑、如何改文案、新增頁面的步驟（同時更新 sitemap）、永遠不得動的東西

## B2. 設計 token 與共用慣例

- [ ] B2.1 從 merchant template 取得設計 token（CSS 自訂屬性）與繁體中文文案慣例；官網可有自己的品牌值但沿用同一組 token 名
- [ ] B2.2 頁面共用結構（header／footer）採各頁自足或單一小 partial 的慣例，寫進 `CLAUDE.md`；不引入建置步驟

## B3. 鎖定核心：申請表單邏輯（design D2、D6）

- [ ] B3.1 `core/apply.js`（經典 script，與範本一致，無 ES module 打包）：欄位驗證（email、`business_summary` ≤ 1000 rune、`phone` 字元集）、honeypot 欄位、載入時間欄位、Turnstile 小工具載入與 token 取得、`POST {apiBase}/api/v1/public/shop-applications`、對 422／429（`Retry-After`）／503 的訊息對應、成功導向 `/apply/received/`；**不得**把 email 以外的任何資料寫入 storage；錯誤訊息不得洩漏申請是否已存在
- [ ] B3.2 `core/verify.js`：從 `location.hash` 讀 token、立刻 `history.replaceState` 清掉 fragment、`POST …/verify`、對 404／429 的畫面狀態；頁面設 `noindex` 與 `no-referrer`
- [ ] B3.3 `config.js`（商家／站台擁有，鎖定）：`apiBase`、Turnstile `siteKey`、`consentVersion`（等於 API 目前條款版本）
- [ ] B3.4 核心的單元測試（Node 內建 test runner，零依賴）：驗證規則、錯誤對應、fragment 清除；契約測試以 ecommerce 的 `openapi.yaml` 範例（A 的 8.2）為準
- [ ] B3.5 雜湊清單 `--update`（只在核心變更後，人工審核）

## B4. 設計層頁面與文案（design D11、D12）

- [ ] B4.1 頁面：`index.html`（`/`）、`how-it-works/`、`demo/`、`pricing/`（佔位，明示尚未生效）、`terms/`、`privacy/`（佔位，列出蒐集項目、目的、保存期間、權利與聯絡方式，**需法律審閱**）、`apply/`（表單標記，邏輯在核心）、`apply/verify/`、`apply/received/`（告知「若信箱收不到，我們會以人工方式聯絡」）
- [ ] B4.2 SEO：每頁獨立 `<title>`／description／canonical／`og:*`／`twitter:*`、`<html lang="zh-Hant">`、靜態 `sitemap.xml`／`robots.txt`、Organization JSON-LD；`/apply/verify`、`/apply/received` `noindex`
- [ ] B4.3 `/demo`：連結＋ `sandbox` iframe 指向 live demo（一個真的 merchant-template 部署，指向專用 demo shop；結帳啟用與否與重置方式依 Q4）

## B5. CSP、標頭與部署（design D7）

- [ ] B5.1 `_headers`（鎖定）：CSP（`script-src 'self' https://challenges.cloudflare.com`、`frame-src` 含 Turnstile 與 demo origin、`connect-src` 含 API origin、`frame-ancestors 'none'` 等，無 `'unsafe-inline'` script）、HSTS、`nosniff`、Referrer-Policy、Permissions-Policy；測試斷言 CSP 不含 `unsafe-inline` script
- [ ] B5.2 Cloudflare Workers static assets／Pages 部署設定（`wrangler.jsonc`，站台擁有）、網域與 DNS、Turnstile 站台建立（site key／secret 由誰建立見 Q9）；**這些是部署設定，需使用者核准並手動套用**
- [ ] B5.3 ecommerce 側的 `CORS_ALLOWED_ORIGINS` 加官網 origin（A 的 9.1 文件）

## B6. 營運

- [ ] B6.1 `docs/DEPLOY-RUNBOOK.md`（官網 repo）：部署、回滾、Turnstile 輪替、`_headers` 變更流程、核心同步
- [ ] B6.2 AI 編輯流程：官網文案變更走設計層分支＋守門員＋人工核可預覽（沿用商家站台的流程）

## B7. 驗證

- [ ] B7.1 `node scripts/check-design.mjs --base <ref>` 全綠、核心單元測試全綠
- [ ] B7.2 本機以靜態伺服器＋指向開發 API 跑完整申請流程（Turnstile 測試金鑰）；在 390px 與 1280px 檢查版面
- [ ] B7.3 以線上 header 檢查工具確認 CSP／HSTS／Referrer-Policy 實際送出
