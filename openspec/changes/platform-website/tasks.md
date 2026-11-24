> 前置：design「Open Questions」Q1–Q11 已由使用者回答。以下假設採用建議答案（Q1 Next 靜態輸出、Q2 方案 A）；答案不同時調整對應群組。

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

## 8. 官網（`web/`，design D1／D7／D9／D11／D12）

- [ ] 8.1 **移除租戶渲染**（方案 A）：刪 `web/app/[[...path]]/`、`web/app/preview/`、`web/themes/`、`web/components/Maintenance.tsx`、`web/lib/api.ts`（render 呼叫）、`web/lib/sanitize.ts`、`web/lib/richtext-allowlist.json`、`web/scripts/gen-types.mjs`；`package.json` 移除 `sanitize-html`、`json-schema-to-typescript` 與 `gen:types` script
- [ ] 8.2 `web/next.config.mjs`：`output: "export"`、`trailingSlash: true`；`web/Dockerfile` 與 `.github/workflows/image-web.yml`：改為靜態產物（nginx 映像或刪除，依 Q9）；**僅在使用者核准後**修改該 workflow
- [ ] 8.3 頁面：`web/app/page.tsx`（`/`）、`how-it-works`、`demo`、`pricing`（佔位）、`terms`（佔位）、`privacy`（佔位）、`apply/page.tsx`（表單＋Turnstile 小工具＋錯誤對應 422／429／503＋honeypot＋載入時間）、`apply/verify/page.tsx`（讀 fragment、POST、`noindex`、`no-referrer`）、`apply/received/page.tsx`；共用 `web/components/*`、`web/lib/apply.ts`（呼叫 `NEXT_PUBLIC_API_BASE`）
- [ ] 8.4 SEO：`web/app/layout.tsx` metadata（`lang="zh-Hant"`、canonical、OG）、每頁 `generateMetadata`、`web/app/sitemap.ts`、`web/app/robots.ts`（`force-static`）、JSON-LD
- [ ] 8.5 `web/public/_headers`：CSP（含 Turnstile 與 demo 來源）、HSTS、`nosniff`、Referrer-Policy、Permissions-Policy；以 `next build` 產物驗證 CSP 不需 `'unsafe-inline'` script
- [ ] 8.6 `ci.yml` 的 web job：移除「Generated types are current」步驟，其餘（`npm ci`、`npm run build`、typecheck）保留——**需使用者核准 workflow 變更**
- [ ] 8.7 測試：`web/` 加元件／表單狀態測試（若採 vitest 需新增依賴，依 Q1 決定）或以 e2e 瀏覽器斷言取代：表單錯誤對應、成功導向 `/apply/received`、`/apply/verify` 無 token 的畫面
- [ ] 8.8 e2e：`e2e/happy-path.mjs` 移除 ⑤⑥⑥b⑦ 中所有 `WEB` 相依的斷言與 `E2E_WEB_BASE_URL`（保留 ①–④ 直到 `retire-tenant-rendering`）；`CLAUDE.md` 的「另一組 port」段落改寫

## 9. 文件與營運

- [ ] 9.1 新增 `docs/deploy/platform-website-runbook.md`：Turnstile 站台建立、`CORS_ALLOWED_ORIGINS`、官網託管與 `_headers`、DNS、`WEBSITE_URL`、每日排程 `purge-applications`、人工模式（outbox）操作員 SOP
- [ ] 9.2 更新 `.claude/skills/merchant-onboarding/SKILL.md` 與 `references/*`：把「申請流程」由「規劃中」改為現況（intake 來源、審核、核准後接 step 3）；`known-gaps.md` 移除對應項目
- [ ] 9.3 更新 `docs/PLATFORM-ROADMAP.md`、`README.md`（`web/` 說明）、`CLAUDE.md`（`/web` 條目、新表與限流、PII 規則）；`openspec/project.md`（Storefront 條目）
- [ ] 9.4 `docs/decisions/` 追加 ADR（Q1／Q2 的最終選擇）

## 10. 驗證

- [ ] 10.1 `cd api && go build ./... && make lint && make test && make openapi`
- [ ] 10.2 `make test-int`（需 docker infra；串行）
- [ ] 10.3 `cd web && npm ci && npm run build`（靜態輸出、typecheck）；檢查 `out/` 內含 `sitemap.xml`、`robots.txt`、`_headers`
- [ ] 10.4 `cd e2e && node commerce-path.mjs`（改動 wire.go 後必跑）與新增的申請流程 e2e
- [ ] 10.5 手動：Turnstile 測試金鑰全流程；無 SMTP（outbox）與有 SMTP（`SMTPNotifier`）兩種模式各走一次；確認回應位元組在新／重複／冷卻三種情況相同
