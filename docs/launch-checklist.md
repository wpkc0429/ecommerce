# 上線前檢查清單（bouquet 花店店面 + 平台）
原則：不趕時間，完整、UIUX 好、通過資安掃描才上線。標記：[O]=需要店主、[S]=AI subagent 可做、[O+S]=兩者配合。

## 0. 本次已實際執行的唯讀掃描結果（2026-11-26）
- 工具現況：已安裝 = go、golangci-lint(v2.15)、npm、docker。未安裝 = govulncheck、gosec、gitleaks、trufflehog、trivy、nuclei、testssl、ZAP（均未安裝任何東西）。
- `go vet ./...`（ecommerce/api）：無輸出，通過。
- `golangci-lint run -E gosec,errcheck,staticcheck`：**無法使用**，程式 panic（golangci-lint 以舊版 Go 編譯，載入 Go 1.26.4 toolchain 的型別時崩潰）。需要用 Go 1.26 重編 golangci-lint 或改用 gosec 單機版。
- `npm audit --package-lock-only`（ecommerce/e2e，唯一有 lockfile 的相關專案）：0 個漏洞。店面與平台官網、admin 皆無 package.json/lockfile（無建置、無 npm 依賴，供應鏈風險低；但 vendor/ 內的第三方檔案如 HEIC 解碼器需人工查版本）。
- 機密掃描（快速 grep，非完整歷史）：命中的檔案是 .env.example、文件、ecpay-sim、e2e 測試用的 ECPay 變數名稱，屬預期；`ecommerce/.env` 存在於本機，需確認被 .gitignore 且從未進過 git 歷史（完整確認要靠 gitleaks，見 A）。
- 安全標頭檢視：
  - merchant-template-clean/public/_headers：CSP 嚴格（script-src 'self'、object-src 'none'、frame-ancestors 'none'、base-uri 'none'），有 nosniff/Referrer-Policy/X-Frame-Options。缺 HSTS 與 Permissions-Policy；connect-src 為 `https:`（未釘死 API origin）。
  - platform-website/public/_headers：最完整（HSTS、Permissions-Policy、COOP、Turnstile 白名單），但 connect-src/frame-src 仍是 `REPLACE_WITH_API_ORIGIN/DEMO_ORIGIN` 佔位，上線前必須換真值並跑 `node scripts/check-layers.mjs --update`。
  - ecommerce/admin/nginx.conf：CSP 嚴格、無 inline；connect-src 僅 'self' + *.r2.cloudflarestorage.com。缺 HSTS（由 Cloudflare 補即可）。
  - **bouquet/public 沒有 `_headers`**，而且 index.html 有 inline `<script>`（第 37 行，載入 config.local.js 的 document.write）、app.js 有 innerHTML。若直接套 template 的 CSP 會壞。這是上線阻擋項（見 D 的第 2 步）。
  - bouquet 的 config.js 仍是 `REPLACE_WITH_PLATFORM_API_BASE` / `REPLACE_WITH_PUBLISHABLE_KEY` 佔位。

## A. 資安掃描計畫
| 項目 | 工具/指令 | 現在可跑? | 涵蓋 | 負責 |
|---|---|---|---|---|
| Go 已知漏洞 | `govulncheck ./...`（ecommerce/api） | 否，需安裝（`go install golang.org/x/vuln/cmd/govulncheck@latest`，會連網寫入 GOPATH，需店主同意） | 依賴與標準庫已知 CVE | [O 同意安裝] 後 [S] |
| Go 靜態安全 | `gosec ./...` | 否，需安裝；或修好 golangci-lint 後啟用 gosec | SQL 拼接、弱雜湊、檔案權限、SSRF/命令注入模式 | [S] |
| Go vet / lint | `go vet`、golangci-lint | vet 可（已通過）；lint 目前 panic | 基本正確性 | [S] |
| Go 測試含競態 | `go test -race ./...`（需 Postgres/Redis 的整合測試用 docker compose） | 可（本機有 docker），屬寫入本機測試 DB，不碰 production | 回歸、OpenAPI 合約測試 | [S] |
| 依賴稽核 | `npm audit --package-lock-only`（e2e）、`go list -m -u all`（僅看更新） | 可，已跑 | e2e 依賴 | [S] 已完成 |
| 機密掃描（含 git 歷史） | `gitleaks detect` 或 trufflehog，對 ecommerce、bouquet、merchant-template、platform-website | 否，未安裝；替代：`git log -p -S` 針對 `.env`、`BEGIN PRIVATE KEY`、`HashKey` | 歷史中的金鑰、ECPay HashKey/IV、R2 金鑰、JWT secret | [S]，結果由 [O] 判斷是否要輪替 |
| 映像/容器 | `trivy image`（ecommerce api、admin Dockerfile）、`trivy fs` | 否，未安裝 | OS 套件 CVE、Dockerfile 設定（是否 root） | [S] |
| 動態掃描 DAST | OWASP ZAP baseline、nuclei 對**本機/預備環境**，絕不對 production | 否，未安裝；需先有 staging（可用本機 compose + tunnel 的測試網域） | XSS、標頭、錯誤頁洩漏、常見錯誤設定 | [O 核准目標] + [S] |
| TLS/標頭 | testssl.sh 或 securityheaders.com / SSL Labs（上線網域） | 否；網域上線後才有意義 | TLS 版本、HSTS、憑證鏈 | [O]（提供網域）+ [S] |
| CSP 檢查 | `node scripts/check-layers.mjs`（merchant-template、platform-website 內建） | 可，唯讀 | CSP 未被弱化、與 config.js 一致 | [S] |

**現在可零風險先做**：(1) go vet / go test（本機 DB）；(2) 對四個 repo 做 `_headers`/nginx 人工審查與 check-layers；(3) 對照 `ecommerce/api/openapi/openapi.yaml`，逐一列出每個 endpoint 的 authn、權限、rate limit，找出沒有保護的寫入端點；(4) 用 git 指令做歷史機密搜尋；(5) 跑 openapi 合約測試。

**人工審查項目**（全部 [S] 先出報告、[O] 做最終判斷）
1. 認證/Session：JWT/refresh token 儲存（admin 為 HttpOnly cookie）、過期與撤銷、登入暴力破解 rate limit、密碼雜湊（argon2/bcrypt）、邀請連結一次性。
2. RBAC 與租戶隔離：每個 ent 查詢是否帶 shop_id；以兩間商家帳號做越權測試（IDOR：改 URL 的 ID、改 header 的 shop）；平台層路徑對商家 origin 是否一律 403。
3. SSRF：publish hooks / webhook 呼叫是否擋內網、loopback、metadata IP（169.254.169.254）、DNS rebinding、重導向跟隨。
4. 上傳：presigned URL 的 content-type/大小限制、檔名與路徑、HEIC 轉檔、是否可上傳 SVG/HTML 造成儲存型 XSS、R2 公開網域是否 nosniff。
5. CORS：只放行已登記的 origin、無萬用字元、無 Allow-Credentials 給店面；admin 與店面分離。
6. 付款回呼：ECPay CheckMacValue 以 timing-safe 比對、金額與訂單比對、冪等（重複通知）、僅信任 server-to-server 回呼、不靠前端導回判定付款成功。
7. 日誌 PII：不記姓名、電話、地址、信箱、token、CheckMacValue；錯誤回應不含堆疊。
8. 備份/還原：Postgres 定期 dump + MinIO/R2 物件備份，**實際做過一次還原演練**，備份加密並存放在另一處。
9. 其他：訂單查詢是否可被猜（用不可猜 token）、Turnstile 伺服器端驗證、郵件注入、Cookie flags（Secure/SameSite）、依賴 vendor 檔版本。

## B. UI/UX 審查計畫
- 視窗：390px（手機，主要）、768px、1280px 桌機；各頁截圖比對。[S] 用 Chrome 擴充或 Playwright 自動截圖並列出問題，[O] 看後定調風格。
- 無障礙：對比度 AA（正文 4.5:1）、鍵盤全程可操作（Tab 順序、focus 可見、彈窗可用 Esc 關閉並還原焦點）、表單 label、圖片 alt、按鈕與連結語意、aria-live 錯誤訊息、觸控目標 ≥44px。工具：axe（瀏覽器內）+ 手動鍵盤測。[S]
- 結帳流程走查：商品 → 購物車 → 收件資訊 → 配送日期/時段（花店特有：可配送日、截單時間）→ 付款（ECPay 沙盒）→ 完成頁/信件。每步驟測空車、庫存不足、無效日期、付款取消/失敗/逾時、重複點擊、返回鍵。[S] 走查，[O] 用真實手機實測一單。
- 狀態：每個畫面都要有載入（骨架/提示）、錯誤（可重試）、空狀態（購物車空、分類無商品、搜尋無結果）、離線/API 失敗訊息。[S]
- 效能：Lighthouse 手機版目標 Performance ≥90、Accessibility ≥95、Best Practices ≥95、SEO ≥90；LCP <2.5s、CLS <0.1、INP <200ms；圖片 WebP/AVIF、尺寸合適、lazy load。[S]（Chrome 內跑 Lighthouse 或 Playwright）
- zh-TW 文案：用語一致（購物車/結帳/訂單）、避免簡體與機器翻譯腔、價格格式 NT$、日期 yyyy/mm/dd、錯誤訊息友善、法定頁面（隱私權、退換貨、配送說明、聯絡方式）齊全。[S] 初審，[O] 定稿（品牌語氣）。
- 其他：SEO 與分享（title、description、OG 圖、favicon）、404 頁、花店實拍圖片品質。[O+S]

## C. 基礎設施就緒度
- 平台主機：目前是本機 + Cloudflare Tunnel，意味電腦關機/斷網/睡眠就下線。上線前決定：維持本機（需固定開機、UPS、自動重啟 docker、Tunnel 當服務執行）或搬到 VPS/雲端。[O 決策]
- CORS：把花店正式網域登記到平台 allowed-origins（用途 storefront），`CORS_ALLOWED_ORIGINS` 只含平台 admin；店面 admin 網域用途 `admin`。[O 提供網域，S 準備指令]
- DNS：店面、店面 admin、API、圖片網域（R2 自訂網域）、平台官網；Cloudflare Access 兩個應用（merchant admin、platform admin）設定與測試。[O]
- SMTP：訂單確認信、邀請信的寄件服務與網域 SPF/DKIM/DMARC，測試送達率。[O 申請，S 驗證]
- Turnstile：建立 site key/secret，平台官網申請表單與（如適用）店面結帳。[O]
- R2：正式 bucket、自訂公開網域、CORS、金鑰最小權限、生命週期；MinIO 僅限本機。[O]
- ECPay：正式商店代號與 HashKey/IV（按商家存放）、回呼 URL 為公開 HTTPS、沙盒全流程通過後才切正式。[O]
- 監控/備份：健康檢查（uptime 監控）、錯誤告警、磁碟空間、Postgres 每日備份與還原演練、金鑰輪替計畫、log 輪替。[O+S]
- bouquet 設定：config.js 改真值、publishable key、新增 `_headers`（見下）、Workers 部署（wrangler 僅靜態資產）。[O 部署，S 準備]

## D. 建議順序與分工
1. [S] 補掃描工具前先做：go test、openapi 合約對照 authz/rate limit 報告、歷史機密搜尋、四站標頭審查。零風險，可立刻開始。
2. [S] bouquet 補 `_headers`：移除 inline script（改成外部檔）、檢查 innerHTML 是否有跳脫、套用 merchant-template 的嚴格 CSP，再以 check-layers 驗證。這是修改，需 [O] 同意後進行。
3. [O] 同意安裝 govulncheck/gosec/gitleaks/trivy；[S] 執行並整理報告，修高/中風險。
4. [S] 人工審查 A 的 9 項，產出發現清單；[S] 修；[O] 審核核心變更。
5. [S] UI/UX：390px 與桌機走查、無障礙、Lighthouse、zh-TW 文案初審；[O] 用手機實測並定稿。
6. [O] 決定平台主機、網域、DNS、SMTP、Turnstile、R2、ECPay 正式帳號；[S] 寫部署與回滾步驟。
7. [S] 建立 staging（獨立網域），跑 ZAP/nuclei 與 ECPay 沙盒完整結帳；[O] 簽核。
8. [O] 備份還原演練通過、監控上線後，才切正式；先小量真實訂單試營運，再公開。

## 假設
- 平台 API 與 bouquet 皆未上正式網域；ecommerce/.env 內為本機值。
- 只做靜態與唯讀分析；掃描 production 或 Cloudflare 設定未包含。
- 店面沿用 merchant-template 的鎖定核心 + 設計層架構；bouquet 可能略早於 template 的 CSP 要求。
- 沒有 npm 建置，故依賴風險集中在 Go 模組與 vendor 檔。

---

## 已完成的稽核 (date 2026-11-26)
範圍：D 的第 1 步，三項唯讀稽核。僅讀程式碼、git 歷史與設定檔；未安裝任何工具、未連網、未碰 Cloudflare / production / 主資料庫。
分工標記：[S] = AI subagent 可自行修（核心變更仍需 [O] 審核）；[O] = 需要店主決定或提供資料。
結果摘要：高 3、中 8、低 8。**沒有發現已被證實、可立即利用的線上漏洞**；高風險項目都是上線前必須處理的設定或阻擋項。其中 H1 若實際部署拓撲如下所述，屬於現行風險，需店主確認。

### 一、發現（依嚴重度）

#### 高（上線阻擋）
**[已修 c73bd12] H1. TRUSTED_PROXIES 預設為空，Cloudflare Tunnel 後面所有訪客共用同一個 IP，rate limit 變成全域共用桶**
- 證據：`.env.example:51-54`（`TRUSTED_PROXIES=` 空值）、`api/internal/httpapi/clientip.go:36-43`（信任清單為空時一律用 TCP peer）。cloudflared 與 API 同機時 peer 恆為 127.0.0.1（或 docker 閘道）。
- 影響：任何一個匿名者打滿 admin 登入（20 次/5 分）、會員登入、申請表、訪客結帳的 IP 桶，所有真實使用者都會收到 429（拒絕服務）；per-IP 防暴力也失去意義；log 的 client_ip 也全是同一個。
- 修法：正式環境設 `TRUSTED_PROXIES=127.0.0.1/32,::1/128`（cloudflared 在 docker 網路時填該網段）；`config.Load` 對 production 空值給警告或拒絕啟動；加整合測試。 [O 確認拓撲並設定 .env] + [S 寫啟動檢查與測試]
- 處置（c73bd12）：不預設信任標頭（避免偽造 IP）；production 空值且收到 CF-Connecting-IP／X-Forwarded-For 時記一次 WARN；已加偽造／可信對端測試。**店主仍須在本機 .env 設 `TRUSTED_PROXIES=127.0.0.1/32,::1/128`**（API 在 Docker、cloudflared 在主機時改填 Docker 橋接網段）。

**H2. merchant-template 的 `_headers` 以 `form-action 'self'` 擋掉 ECPay 付款跳轉，結帳會無法付款**
- 證據：`merchant-template-clean/public/_headers:26`（`form-action 'self'`）；`public/commerce/checkout.js:29-40`（建立 form、`form.action = f.action`、`form.submit()` 送往綠界）。bouquet 的 `public/commerce/checkout.js:29-40` 相同，且之後要套同一份 CSP。
- 影響：瀏覽器封鎖表單送往綠界網域，使用者看到空白或 CSP 錯誤。
- 修法：`form-action 'self' https://payment.ecpay.com.tw`（沙盒期另加 `https://payment-stage.ecpay.com.tw`，正式只留正式網域）；`check-layers.mjs` 同步調整；用 ECPay 沙盒實測。這是 template 核心（locked）變更，須走 core-change 流程。 [S 準備] + [O 核准核心變更]

**H3. bouquet/public 沒有 `_headers`（無 CSP、HSTS 等），且 index.html 有 inline script**
- 證據：`bouquet/public/` 無 `_headers`；`bouquet/public/index.html:37-43`（inline `<script>`）；`index.html:26-28`（Google Fonts，第三方樣式與字型）；`public/app.js:1331`（`app.innerHTML = view()`，字串拼接，靠 `esc()`，`app.js:104-110`）；`app.js` 有 40 多處 `style="..."` 屬性。
- 影響：結帳店面（含訪客個資、付款跳轉）沒有 CSP 作為 XSS 最後防線。
- 修法：見「四、bouquet 標頭提案」。 [O 同意修改 bouquet] + [S 實作與驗證]

#### 中
**[已修 c73bd12] M1. `/healthz` 對匿名者回傳內部錯誤字串**：`api/internal/httpapi/router.go:156-160`（`err.Error()` 直接寫進回應，DB 連線錯誤可能含主機、埠、使用者名稱）。修法：對外只回固定的 unhealthy，細節寫 log；或不經 tunnel 公開 /healthz。 [S]

**M2. HTTP server 只設了 ReadHeaderTimeout**：`api/cmd/server/main.go:36-40`，沒有 ReadTimeout / IdleTimeout / WriteTimeout / MaxHeaderBytes，慢速 body 可長時間占用連線。修法：加 `ReadTimeout`（約 30s）、`IdleTimeout`（約 120s）、`MaxHeaderBytes`；WriteTimeout 需考慮截圖上傳與 hook 測試耗時再定。 [S]

**[已修 c73bd12] M3. mock 付款供應商在 production 仍註冊，顧客可用 body 的 `provider` 指定它**：`api/internal/app/wire.go:433-451`（mock 與 ecpay 同時註冊）、`httpapi/payment.go:137-145` 與 `payment/service.go:124-126`（`provider` 由請求決定）、`config/config.go:361`（`PAYMENT_DEFAULT_PROVIDER` 預設 "mock"）。mock webhook 靠 `PAYMENT_MOCK_WEBHOOK_SECRET` 的 HMAC 保護，production 啟動會拒絕預設值（`config/secrets.go:118`；`APP_ENV` 預設為 production，`config.go:350`），目前不可偽造。但若 `APP_ENV` 設成 development，或 default provider 沒改，風險立刻變成「可偽造付款完成」或「真實訂單走 mock」。修法：production 不註冊 mock，或強制 `PAYMENT_DEFAULT_PROVIDER=ecpay` 並拒絕請求指定 mock。 [S]

**[已修 c73bd12] M4. 除登入/註冊/申請/結帳/內容公開讀取外，沒有基線 rate limit**（對照表見二）：店面商品/分類/運送公開讀取（`router.go:379-394`）、會員購物車與訂單（`router.go:339-361`）、訪客訂單 token 路由（`router.go:372-376`）、`/render/*`（`router.go:414-418`）、`GET /admin/branding`（`branding.go:129`，匿名且會查 DB）、付款 webhook（`router.go:404-412`，簽章驗證前先解析 provider 並可能查 DB）、已登入後台的所有寫入路由（僅邀請有限制，`team.go:31`）。修法：Cloudflare WAF/Rate Limiting 先擋；API 端加一個以 IP 與 shop+IP 為鍵的寬鬆全域中介層（例如 300 次/分），webhook 以 IP 單獨設限。 [O 設 Cloudflare 規則] + [S 寫中介層]

**M5. rate limiter 與帳號鎖定在 Redis 故障時 fail-open**：`api/internal/ratelimit/limiter.go:61-101`（`Allow` 出錯回 Allowed，Peek/Record/Reset 同）；`httpapi/adminexposure_integration_test.go:570`（`TestAdminAccountLockoutFailsOpen`）證明是刻意設計。可接受，但上線前要有 Redis 監控與告警，並知道 Redis 掛掉等於登入防暴力失效。 [O 決策/監控] + [S 寫告警文件]

**M6. ecommerce/admin 的 nginx 與映像**：`ecommerce/admin/nginx.conf:16-19`——`img-src` 開放整個 `https:`；沒有 HSTS / Permissions-Policy / COOP / CORP；沒有任何 `Cache-Control`（index 與靜態檔靠瀏覽器啟發式快取，更新可能延遲）。`ecommerce/admin/Dockerfile:13-15` 以 `COPY .` 整個目錄進 web root，`.dockerignore` 只排除 `docs scripts e2e node_modules` 等，未排除 `CLAUDE.md`、`README.md`，內部文件會被當靜態檔公開。修法：`.dockerignore` 加 `*.md`；`index.html` 設 `Cache-Control: no-cache`；補 HSTS（若不由 Cloudflare 補）、Permissions-Policy、`Cross-Origin-Opener-Policy: same-origin`、`Cross-Origin-Resource-Policy: same-origin`；`img-src` 釘死為影像網域（像 merchant-template 用環境變數注入）。 [S]

**M7. merchant-template 的 `_headers` 缺項**：`merchant-template-clean/public/_headers:26-29`——缺 HSTS、Permissions-Policy、COOP；`connect-src 'self' https:` 與 `img-src ... https:` 未釘死（檔頭 `:19-20` 已承認是已知限制）；無快取標頭。修法：HSTS（`max-age=31536000; includeSubDomains`）、Permissions-Policy（沿用 platform-website 的清單）、`Cross-Origin-Opener-Policy: same-origin`、在 `new-merchant.mjs` 依每家店的 API/影像網域替換 `connect-src`/`img-src`。屬 locked 核心變更。 [S 準備] + [O 核准]

**M8. template 的 admin CSP 少了 `'wasm-unsafe-eval'`，與 ecommerce/admin 不一致**：`merchant-template-clean/admin/deploy/nginx.conf.template:31`（`script-src 'self'`），而同一套 `heic2any` 解碼器在 `ecommerce/admin/nginx.conf:13-16` 需要 `'wasm-unsafe-eval'`。HEIC 上傳在 template 版本可能被 CSP 擋。該檔另缺 HSTS / Permissions-Policy / COOP（快取 `no-cache` 已有，`:36`，無雜湊檔下合理）。修法：先在瀏覽器實測，必要時加上並同步 `check-design.mjs`。 [S 實測] + [O 核准]

#### 低
- **L1. 平台官網 `_headers` 仍是佔位**：`platform-website/public/_headers:20`（`REPLACE_WITH_API_ORIGIN`、`REPLACE_WITH_DEMO_ORIGIN`）。上線前換真值並跑 `check-layers.mjs --update`；另缺 `Cache-Control`（建議 `no-cache`）與 `Cross-Origin-Resource-Policy`。 [O 給網域] + [S]
- **L2. bouquet 的 `public/config.local.js` 在會被 wrangler 整個上傳的資料夾內**：`bouquet/wrangler.jsonc`（`assets.directory: ./public`）；`.gitignore:11` 只擋 git，wrangler 不讀 .gitignore。檔內是本機測試 API 位址與 publishable key（非機密，但會公開並暴露測試堆疊）。修法：新增 `public/.assetsignore`（內容 `config.local.js`）。 [S]
- **L3. bouquet 的 `.gitignore` 沒有 `.env` / `.dev.vars`**（只有 `public/config.local.js` 與 `admin/.env*`）。補上。 [S]
- **L4. `decodeJSON` 未啟用 `DisallowUnknownFields`**：`api/internal/httpapi/middleware.go:218-225`。檢視請求 DTO 後未發現可寫入 `shop_id`/`member_id`/價格等的大量賦值（`accounts.go:157,183`、`design_credentials.go:74` 的 `shop_id` 皆在平台權限之後且為明示欄位）。啟用僅為縱深防禦，需先確認不破壞前端相容。 [S]
- **L5. 會員登入沒有帳號層級鎖定**（後台有，`adminauth.go:150-165`）：`ratelimitmw.go:163-168` 只有 shop+IP 與 shop+IP+email，換 IP 即繞過，屬撞庫風險。修法：加 per-(shop,email) 失敗計數，需評估被拿來鎖他人帳號的風險。 [S 實作] + [O 決策]
- **L6. 預設 CORS 來源含 localhost**：`api/internal/config/config.go:386`（`CORS_ALLOWED_ORIGINS` 預設 `http://localhost:3001,http://localhost:3000`）。production 要明確覆寫為平台 admin 網域。 [O]
- **L7. webhook 小問題**：`payment/service.go:232`（`res.Amount != 0 &&`，金額為 0 時略過比對；內容已簽章，風險低，應改為必比對）；`httpapi/payment.go:254-261`（未知 provider/店面未設定憑證回 404，可探測哪些店面設了 ECPay）。 [S]
- **L8. OpenAPI 缺機器可讀的權限/限流註記**：`api/openapi/openapi.yaml` 的 `x-permission`、`x-rate-limit` 為 0 筆，路由表只能靠程式碼人工對照。建議加註記並寫測試比對（`openapi_contract_test.go:302` 已確保每條路由都在 spec）。另 `render/preview` 的 token 放 query string（`httpapi/render.go:82`），代理層日誌可能留下（token 有效期短）。 [S]

### 二、路由族 authz / 租戶 / rate limit 對照表
總結：後台所有 `{shopID}` 取自 URL（`authz.go:14-24`），再以 RBAC 快照判斷（`authz.go:29-59`），沒有信任 body/header 的 shop id；店面租戶只由 `X-Publishable-Key` 解析（`tenantmw.go:24-41`，無 Host / X-Site-Domain 後援）；會員 JWT 的 audience 綁定該店（`middleware.go:255-271`）。資料層查詢普遍同時帶 `ShopID`（例：`order/service.go:637-640,732-735,860-868`、`design/service.go:133,158,222`）。

| 路由族 | 掛載位置 | 驗證中介層 | 權限節點 | 租戶範圍 | rate limit | 備註 |
|---|---|---|---|---|---|---|
| 健康檢查 `GET /healthz` | `router.go:156` | 無 | 無 | 無 | 無 | M1 |
| 公開申請 `/api/v1/public/shop-applications`、`/verify` | `router.go:310-314`、`applications_public.go:46-47` | 無（Turnstile 在 handler；CORS 只放官網來源 `middleware.go:187-189`） | 無 | 無 | 有（IP 寬/緊、email 24h，`config/application.go:107-109`）；body 上限 `limitBody` | 良好 |
| 店面公開讀取 `/api/v1/shop/products`、`categories`、`shipping-methods` | `router.go:379-394` | TenantMW（publishable key） | 無 | key 解析出的 shop | 無 | M4 |
| 店面內容公開讀取 `/shop/content*` | `content_public.go:44-47` | TenantMW | 無 | shop | 有（預設 120/分，`config.go:425`） | `preview` 需 token |
| 店面會員驗證 `/shop/auth/register,login,refresh,logout` | `router.go:326-331` | TenantMW | 無 | shop | register/login/refresh 有（shop+IP、shop+IP+email）；logout 無 | L5 |
| 店面會員 `/shop/me,cart,orders,checkout,payments,shipments,points` | `router.go:339-361` | TenantMW + MemberMW（aud=shop） | 無（自助，handler 以 memberID 過濾） | shop + member | 無 | M4；測試 `tenancy_integration_test.go:221` |
| 訪客結帳 `/shop/guest/checkout` | `router.go:368-377`、`guest.go:85` | TenantMW | 無 | shop | 有（shop+IP、shop+IP+email） | 良好 |
| 訪客訂單 `/shop/guest/order*` | `guest.go:91-98` | TenantMW + OrderAccessMW（aud=order:shop，限單一訂單，24h） | 無 | shop + token 內訂單 id | 無 | 測試 `guest_integration_test.go:562,683,699` |
| 後台認證 `/admin/auth/login,refresh,logout` | `router.go:173-177` | AdminAccess、AdminHost 前置；login 無 JWT | 無 | 無 | login、refresh 有（IP、IP+email、帳號鎖定）；logout 無 | refresh cookie HttpOnly+Secure+Strict（`adminauth.go:80-86`），需 X-Admin-Refresh（`:234`） |
| 後台未登入輔助 `/admin/auth/password-setup*`、`/auth/invitations/*`、`GET /admin/branding` | `router.go:178-186` | AdminAccess、AdminHost | 無（token 即憑證） | token／註冊的 admin host | password-setup、invitations 有（IP）；branding 無 | 例外清單鎖定於 `app/authguard_integration_test.go:22-42` |
| 商家後台 `/admin/shops/{shopID}/**`（products、categories、orders、payments、shipping、points、members、reports、settings、pages、content*、publishable-keys、allowed-origins、ecpay-credentials、media、roles、users、invitations、design） | `router.go:197-264` | AdminAccess、AdminHost、AdminMW（JWT aud=admin）、AdminBindEmail | 每條皆有節點，例 `product.view/create/edit/delete`（`products.go:32-36`）、`payment_credential.manage`（`paymentcredentials.go:38-39`）、`user.manage_members`（`team.go:30`） | URL `{shopID}` + RBAC 跨店守衛（`authz.go:48`） | 僅邀請建立有（`team.go:31`）；content hook、設計核准在 handler 內另有 limiter | M4；`authguard_integration_test.go:53` 逐路由驗證「無角色帳號必得 403」，未守衛者僅 4 條且有理由 |
| 後台自我資訊 `GET /admin/shops/{id}/me`、`POST media/uploads`、`GET /admin/me`、`GET /admin/media/config` | `roles.go:36`、`media.go:36,49` | AdminMW | `RequireShopAccess`（只做成員檢查；uploads 在 handler 依用途再判權限）；`me`、`media/config` 無 | URL shopID／帳號 | 無 | `me`、`media/config` 為文件化例外 |
| 平台後台 `/admin/shops`、`accounts`、`roles`、`permissions`、`shop-applications`、`shop-provisioning`、`setup-status`、`design-service/credentials` | `router.go:268-289` | AdminAccess、AdminHost（平台 host 限制）、AdminMW | `RequirePlatformPermission`：`shop.*`、`account.*`、`user.manage_roles`、`shop_application.*`、`design.PermServiceManage`；要求平台管理員（`authz.go:152`） | 平台層（body 的 `shop_id` 只供平台操作） | 無 | CORS 僅平台來源（`middleware.go:199-201`） |
| 設計服務機器路由 `/api/v1/design-service/shops/{shopID}/**` | `router.go:299-305` | DesignServiceAuth（`dsk_` 憑證、雜湊查找、CIDR、到期、撤銷，`design_service_auth.go:44-74`） | 憑證綁定單一 shop；不能用於 /admin | `{shopID}` 必須等於憑證 shop，否則 404（`:67`） | 有（寫入 120/分、上傳 60/分、失敗 30/分/IP，`:26-30,78-99`） | 良好；測試 `design_integration_test.go`、`design_screenshots_integration_test.go` |
| 付款 webhook `POST /api/v1/webhooks/payments/{provider}[/{shopID}]` | `router.go:404-412`、`payment.go:250-299` | 無 JWT；簽章（ECPay CheckMacValue `ecpay.go:320`；mock 用 `hmac.Equal`，`mockprovider.go:74`） | 無 | `{shopID}` 僅用來選該店憑證（簽章通過前不可信） | 無 | 64KB body 上限；金額比對 `service.go:232`；M4、L7 |
| 渲染 `GET /api/v1/render/page`、`/preview` | `router.go:414-418`、`render.go:36-50,82` | page：publishable key；preview：短效 token + 權限複查（`render.go:93`） | preview 需 `page.view`/`content.view` | shop | 無 | M4、L8 |

特別檢查結論：
- **信任用戶端 shop/tenant id**：未發現。後台只信 URL `{shopID}` 並經 RBAC；店面只信 publishable key（識別碼、非憑證，`tenantmw.go:11-15`）；webhook 的 `{shopID}` 只選憑證；`X-Site-Domain` 已移除（`tenantmw.go:21-23`）。
- **mass-assignment**：未發現實際風險，見 L4。
- **錯誤洩漏**：統一 envelope，`httpx.Internal` 為固定字串（`httpx.go:94-96`），`recoverer` 不外洩堆疊（`middleware.go:324-343`）。不一致處：`router.go:159`（M1）；`applications.go:257-259` 與 `settings.go:119` 回傳 service/驗證錯誤字串，前者限平台管理員、後者為欄位驗證，可接受。
- **SSRF（附帶）**：publish hook 連線釘住解析後 IP、擋私有/loopback/metadata、不跟隨重導向（`content/hook/policy.go:200-228`、`sender.go:118,142`）；`CONTENT_HOOK_DEV_INSECURE` 在 production 被拒（`config/contentops.go:101-102`）。
- **上傳（附帶）**：只接受 jpeg/png/webp（`media/service.go:38`），HEIC 在客戶端轉檔，有大小上限（`:168`），未見 SVG。
- **既有測試證據**：`openapi_contract_test.go:302`、`app/authguard_integration_test.go:53`、`tenancy_integration_test.go:49,115,221`、`clientauth_integration_test.go:369,401`、`ratelimit_integration_test.go:131-331`、`adminexposure_integration_test.go:210-796`。**缺口**：沒有「A 店管理員逐一打 B 店所有 `/shops/{id}/**` 路由必得 403」的通用測試（目前是無角色帳號的 403 測試加各模組個別測試），建議新增。 [S]

### 三、git 歷史機密搜尋結果
範圍：ecommerce 107 commits、bouquet 46、platform-website 5、merchant-template-clean 18（`git grep` 對 `git rev-list --all`，輸出已遮罩，只保留檔案、commit、行號與前 4 字元）。
- **未發現**：AWS（AKIA）、sk_live、GitHub token（ghp_/github_pat_）、Slack（xox）、PRIVATE KEY、JWT（eyJ…）、Cloudflare token。四個 repo 歷史中從未提交 `.env`、`.dev.vars`、`*.pem`、`*.key`（僅 `ecommerce/.env.example`）。
- `ecommerce/.env`：被 `ecommerce/.gitignore:1` 忽略，`git log --all -- .env` 為 0 筆，從未進入歷史；本機檔案存在（2026-11-24，3379 bytes）。結論：沒有外洩，無需輪替。
- 命中但屬預期（已逐類確認）：
  - ECPay HashKey/HashIV 類：命中分布於 `payment_integration_test.go`、`openapi_conformance_integration_test.go`、`openspec/.../ecpay-payment-provider/design.md:329`；唯一值共 4 個，其中 2 個是綠界公開的沙盒測試值，另 2 個是測試用假值（前綴 `ec`，長度 15/16）。非真實憑證。
  - `postgres://` 含密碼：`.env.example`、`.github/workflows/ci.yml`、`api/internal/config/secrets_test.go`，皆為 dev/CI 弱密碼或範本，production 啟動守衛會拒絕（`config/secrets.go:111-115`）。
  - generic password/token：`accounts_integration_test.go`、`authflow_integration_test.go`、`team*_integration_test.go`、`e2e/*.mjs`、bouquet 與 merchant-template-clean 的 `admin/e2e/run.mjs`、platform-website 的 `tests/fixtures/contract.json:15`（Turnstile 假 token）。皆為測試資料。
- 方法限制：regex 掃描，熵值型或未知格式的機密不保證抓得到；店主同意安裝後仍建議跑 gitleaks/trufflehog 做第二道。bouquet 的 `.gitignore` 缺 `.env`（L3）。

### 四、標頭/CSP 審查
| 項目 | bouquet/public | platform-website/public/_headers | merchant-template-clean/public/_headers | ecommerce/admin/nginx.conf | merchant-template admin（nginx.conf.template） |
|---|---|---|---|---|---|
| CSP | 無 | 嚴格（`:20`） | 嚴格，但 `connect-src https:`、`img-src https:`（`:26`） | 嚴格，`img-src https:`（`:16`） | 嚴格，img/connect 由環境變數注入（`:31`） |
| HSTS | 無 | 有（`:21`） | 無 | 無 | 無 |
| Permissions-Policy | 無 | 有（`:25`） | 無 | 無 | 無 |
| Referrer-Policy | 無 | no-referrer | strict-origin-when-cross-origin | no-referrer | no-referrer |
| COOP / CORP | 無 | COOP 有、CORP 無 | 無 | 無 | 無 |
| connect-src 釘死 | 不適用 | 佔位待換（L1） | 否（M7） | 部分（self + R2 萬用，`:16`） | 是（`ADMIN_UPLOAD_ORIGIN`） |
| frame-ancestors | 無 | none | none | none | none |
| form-action | 無 | self | **self（H2，擋 ECPay）** | self | none |
| 快取（index vs 帶雜湊資產） | 無（Cloudflare 預設） | 無 | 無 | 無 | 全部 `no-cache`（`:36`） |

所有站台皆無建置、無檔名雜湊。建議：`index.html` 與所有 `.js/.css` 用 `Cache-Control: no-cache`（重新驗證，發布即生效）；只有圖片/字型等不可變檔案才設 `public, max-age=31536000, immutable`，且要改用帶雜湊檔名。

#### bouquet 標頭提案（僅提案，**未修改 bouquet**）
1. 移除 inline script：刪除 `bouquet/public/index.html:37-43`，改為 `<script src="commerce/local-config.js"></script>`（放在 `config.js` 之後、`commerce/api.js` 之前，與 `merchant-template-clean/public/index.html:35` 一致），並新增 `bouquet/public/commerce/local-config.js`，內容同 `merchant-template-clean/public/commerce/local-config.js`（僅 localhost/127.0.0.1 才 `document.write('<script src="config.local.js"><\/script>')`）。
2. Google Fonts：`index.html:26-28` 載入 fonts.googleapis.com / fonts.gstatic.com。二選一：(a) 自行託管字型（建議，隱私與 CSP 都較乾淨，之後 `style-src 'self'`、`font-src 'self'`）；(b) 暫時放行下方兩個網域。
3. 新增 `bouquet/public/_headers`（方案 b 的暫時版；上線時把 `<API_ORIGIN>`、`<IMAGE_ORIGIN>` 換成 `config.js` 的 `apiBase` 與 R2 影像網域）：
```
/*
  Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; style-src-attr 'unsafe-inline'; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: https://<IMAGE_ORIGIN>; connect-src 'self' https://<API_ORIGIN>; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://payment.ecpay.com.tw https://payment-stage.ecpay.com.tw; object-src 'none'; upgrade-insecure-requests
  Strict-Transport-Security: max-age=31536000; includeSubDomains
  X-Content-Type-Options: nosniff
  X-Frame-Options: DENY
  Referrer-Policy: strict-origin-when-cross-origin
  Permissions-Policy: accelerometer=(), camera=(), geolocation=(), gyroscope=(), microphone=(), payment=(), usb=()
  Cross-Origin-Opener-Policy: same-origin
  Cache-Control: no-cache
/assets/*
  Cache-Control: public, max-age=86400
```
   說明：`style-src-attr 'unsafe-inline'` 是因 `app.js` 把商品圖當 `style="background-image:..."`（`imgCss`，`app.js:119-121`，已 `encodeURI` + `esc`），style 屬性無法執行程式碼。正式上線只保留正式綠界網域（沙盒測完移除 stage）。HSTS 確認所有子網域都走 HTTPS 後才用 `includeSubDomains`，之後再評估 preload。
4. `public/.assetsignore` 加入 `config.local.js`（L2）；`.gitignore` 補 `.env`、`.dev.vars`（L3）。
5. 驗證：套用後在 390px 與桌機各跑一次完整結帳（含 ECPay 沙盒跳轉），Console 不得有 CSP 違規；`app.js:1331` 的 `innerHTML` 要再做逐點 `esc()` 審查（已有約 108 處 `esc(`，但未逐一證明沒有漏網）。

### 五、修復分工彙整
| 編號 | 嚴重度 | [S] 可自行修 | 需要 [O] |
|---|---|---|---|
| H1 | 高 | 啟動檢查、測試 | 確認拓撲並設 `TRUSTED_PROXIES` |
| H2 | 高 | 調整 `_headers`、check-layers、沙盒實測 | 核准 template 核心變更 |
| H3 | 高 | 改 index.html、新增 `_headers`/local-config.js/.assetsignore、驗證 | 同意修改 bouquet；字型自託管或放行；提供 API/影像網域 |
| M1 M2 M3 | 中 | 全部 | 審核 |
| M4 | 中 | 全域限流中介層 | Cloudflare WAF/限流規則 |
| M5 | 中 | 告警文件 | 決策與監控 |
| M6 M7 M8 | 中 | nginx、`_headers`、.dockerignore | 核准 template 核心變更 |
| L1-L8 | 低 | 多數 | L1、L5、L6 需決策/網域 |

### 六、下一步建議
1. 先修 H2（影響真實付款），再做 H3（D 的第 2 步，需同意）與 H1（部署設定）。
2. 補通用跨租戶測試，並把 M1-M3 的小修併成一個 PR。
3. 其餘照 D 的順序；govulncheck、gosec、gitleaks 已跑（見「掃描工具結果」），trivy 等仍待同意。

## 掃描工具結果 (2026-11-26)

工具以 `go install` 安裝到 `C:\Users\ks\go\bin`（未改 PATH、未改 repo）：govulncheck v1.10.0（DB 2026-11-24）、gosec v2.30.0、gitleaks v8.30.1。執行環境 GOTOOLCHAIN=go1.26.4（需把該 toolchain 的 bin 放在該次指令 PATH 前，否則 go1.27 的 `go list` 會與 govulncheck 衝突）。

### 指令與數量
| 工具 | 指令（於 ecommerce/api） | 結果 |
|---|---|---|
| govulncheck | `govulncheck ./...` | 修前：28 個可達（x/net 與 stdlib 合計，部分同時涉及兩者）；另有 4 個在匯入套件、18 個在依賴模組但未呼叫。修後（x/net 升 v0.62.0）：21 個可達，全為 stdlib |
| gosec | `gosec -exclude-dir=ent -exclude-generated -no-fail ./...`（預設不含 `_test.go`；排除 ent 產生碼） | 139 檔 / 33,884 行，14 筆：HIGH 11（G115×5、G404×2、G704×2、G101×2）、MEDIUM 3（G705×2、G304×1）；全部判定為誤報或低風險 |
| gitleaks | `gitleaks git --redact=100 --no-banner -f json .`（全歷史，輸出已遮蔽） | ecommerce 34、bouquet 8、platform-website 1、merchant-template-clean 16；**全為 generic-api-key，無真實機密**，無 AWS/Stripe/Cloudflare/私鑰類命中 |

### 發現
**高**
- V1（owner 需要）Go 標準庫 go1.26.4 有 21 個可達漏洞（net/http、crypto/tls、crypto/x509、html/template、encoding/xml、net/url…），最高修復版 go1.26.11（GO-2026-6859 HTTP/1 伺服器崩潰、GO-2026-6809 x509 中繼憑證）。可達路徑例：`internal/content/hook/sender.go:154`（http.Client.Do）、`internal/media/s3store.go:46`。修法：`api/go.mod` 的 `go` 改 1.26.11、Dockerfile 用 `golang:1.26.11-alpine`（目前 `golang:1.26-alpine` 會浮動）、CI 重建映像。**未由 subagent 處理**：golangci-lint v2.15.0 以 go1.26.4 建置，go 指令改版會使 lint 失敗，需連同 lint 升級一起核准。

**中**
- V2（已修）golang.org/x/net v0.47.0 的 13 個漏洞（GO-2026-5025~5030、6610~6617、6860、6866 等），其中 `internal/cms/richtext/imageprefix.go:22` 的 `html.ParseFragment`（CMS 富文本淨化）可達 GO-2026-5030（重複屬性 XSS）、5025-5029、6860、6866。已升 v0.62.0（crypto 連帶 v0.58.0）。

**低**
- G115 `internal/points/service.go:313`（int64→int32 點數）：`AwardBasisAmount/pointsPerMinorUnit` 理論可溢位；建議加上限檢查。subagent 可做。
- G115 `internal/media/service.go:421` 影像寬高 int→int32：已受影像尺寸上限約束，誤報。
- G115 `internal/design/screenshots.go:310,377` viewport int→int16：請求端已驗證範圍，建議在轉型前加 clamp 以消警。subagent 可做。
- G404 `internal/ratelimit/limiter.go:91,162`：math/rand 僅用於 Redis sorted-set 成員唯一後綴，非安全用途，誤報。
- G704 `cmd/ecpay-sim/main.go:223,229`：本機 ECPay 模擬器，URL 來自表單且僅開發用，誤報；確保此 binary 不進 production 映像。
- G101 `internal/config/secrets.go:21,26`：公開的 dev 金鑰/mock webhook 預設值，檔內註明且非 dev 環境會拒絕啟動，誤報。
- G705 `internal/httpapi/content_public.go:110,221`：輸出已序列化 JSON 且 Content-Type 為 application/json，誤報。
- G304 `internal/config/config.go:723`：`.env` 讀取固定相對路徑，非使用者輸入，誤報。

### gitleaks 分類（值皆未印出）
- 真實機密：**0**。不需輪替。
- 公開沙盒值：ECPay 官方公開測試商店（MerchantID 3002607 與其公開 HashKey/HashIV），出現於 `.env.example`、`config.go`、`ecpay-sim/main.go`、`e2e/lib.mjs`、多個 `_test.go`、openspec 封存文件（ecommerce 歷史 commit 1345cb5、0217aa5）。
- 測試/開發固定值：`secrets.go` dev 加密金鑰、`cryptbox_test.go` 測試 base64 金鑰、`media_test.go`、`design_integration_test.go` 的 prompt 字串、`resolver_integration_test.go`/`cms_integration_test.go` 的 64 位 hex 測試金鑰（皆為 dev/CI 專用，正式環境被拒）。
- 完整性雜湊誤報：`admin/scripts/core.manifest.json`、`scripts/*.manifest.json`（bouquet、platform-website、merchant-template-clean 全部命中）、`api/migrations/atlas.sum`。建議新增 `.gitleaks.toml` allowlist（path 比對上述檔案）以便日後接 CI：owner 同意後 subagent 可做。
- 待人工確認（低）：`openspec/changes/archive/2026-07-31-guest-checkout/tasks.md:186` 一個 `eyJ` 開頭字串，看似範例 token 佔位，建議 owner 目視確認一次。

### 分工
| 項目 | 狀態 | 分工 |
|---|---|---|
| V1 go 1.26.11 + Dockerfile + lint 升級 | 未做 | owner 核准後 subagent 可做 |
| V2 x/net v0.62.0 | 已修 | 完成 |
| G115 points/screenshots 加檢查 | 未做 | subagent 可做 |
| `.gitleaks.toml` allowlist 與 CI 接入 | 未做 | subagent 可做（需同意） |
| tasks.md:186 目視確認 | 未做 | owner |

### 修復進度（2026-11-26，本機提交，未推送）
- V1 已修（8385df9）：`api/go.mod` go 1.26.11、Dockerfile `golang:1.26.11-alpine`、CI 的 golangci-lint 釘 v2.15.0（goinstall，以 job 的 Go 建置）、文件版本號。govulncheck 可達：修前 21 → 修後 0。本機 golangci-lint v2.15.0 已用 go1.26.11 重建（舊檔留在 `C:\Users\ks\go\bin\golangci-lint.go1.26.4.exe.bak`），不再需要 GOTOOLCHAIN=go1.26.4；`go generate ./internal/ent` 仍需 1.26.x（用 `GOTOOLCHAIN=go1.26.11`）。api 映像 `docker build` 本機成功（未推送）。驗證：go vet、golangci-lint 0 issues、`INTEGRATION=1 go test -p 1 -timeout 40m ./...` 全綠（29 個套件 ok，httpapi 1258s）。
- G115 已修（baad182）：`points.awardFor`、`design.viewportInt16` 加範圍檢查與單元測試，合法輸入行為不變。
- gitleaks 已設定（73d2098）：`.gitleaks.toml` 以「完全相同的值」allowlist 公開 ECPay 沙盒與 dev/測試固定值，並以路徑 allowlist 雜湊清單（`*manifest.json`、`atlas.sum`）。全歷史 `gitleaks git --redact=100 --no-banner .` 現只剩 1 筆：`openspec/changes/archive/2026-07-31-guest-checkout/tasks.md:186`（待 owner 目視，未 allowlist）。因該筆會使 CI 失敗，暫不接入 CI；手動執行：`gitleaks git --redact=100 --no-banner .`。
