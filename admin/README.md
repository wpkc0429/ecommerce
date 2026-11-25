# 平台後台（`admin/`）

繁體中文、無建置步驟的靜態 SPA（原生 ES modules，無打包器、無框架、無 npm 相依），透過 `api/` 的後台 API 運作。它是**平台後台與共用的 merchant admin**（商家自己的後台來自 `merchant-template` 的 `admin/`）。畫面是「設計層」程式碼，只呼叫白名單的 `ctx`；一切與安全或功能有關的東西在**鎖定的核心**。

> **安全（必讀）：** 這個 `admin/` 住在 `ecommerce` monorepo 裡，而那個 repo 同時含 Go API 原始碼與部署檔。**AI 的設計工作階段必須在「只含 `admin/`」的 sparse checkout 或 worktree 裡執行**，且結果必須通過範圍檢查（`node scripts/check-design.mjs --base <ref>`：只有 `admin/design/**` 可變）。AI 絕不取得整個 repo 的寫入權，也沒有任何憑證。精確指令（已實測）：
>
> ```bash
> BASE=$(git rev-parse origin/main)
> git worktree add --no-checkout ../ecommerce-admin-design -b design/admin-<request-name> "$BASE"
> cd ../ecommerce-admin-design
> printf '/admin/\n' | git sparse-checkout set --no-cone --stdin   # 非 cone 模式：cone 會帶出 repo 根目錄的檔案
> git checkout && ls                                               # 只應看到 admin
> ```
>
> 或獨立的 sparse clone：`git clone --filter=blob:none --sparse --no-checkout <url> dir`，再 `printf '/admin/\n' | git sparse-checkout set --no-cone --stdin` 與 `git checkout -b design/admin-<name> origin/main`。完整說明、殘餘風險與「平台成長後把 `admin/` 拆成獨立 repo」的建議：[docs/DESIGN-LAYER.md](docs/DESIGN-LAYER.md) 第 0 節。

## 結構與分層

```
index.html  app.js  styles.css  nginx.conf  Dockerfile  .dockerignore  package.json

【核心：鎖定，AI 不可改，由 scripts/core.manifest.json 的雜湊保護】
lib/      API／驗證／權限／路由／設計層載入器：api.js（token、refresh、Access 重載）、session.js、rbac.js、router.js、
          money.js、format.js、upload.js（HEIC 與直傳）、branding.js、design.js（唯一的設計層載入器）、
          design-schema.js（契約，載入器與守門員共用）、public-api.json（ctx 白名單，由 gen-public-api.mjs 產生）、
          meta/*.js（純資料：路由、導覽、不可逆動作的固定警告）
core/     ctx 與動作：context.js、groups/*.js（每個領域一個模組：data／actions／logic／enums／ui／format）、
          members/、platform/、auth.js（連結 token 只留在閉包）、routes.js、guard.js、host.js（掛載＋備援）、
          warnings.js、errors.js
ui/       UI 原件：kit.js（安全 DOM 建構器 h()、確認對話框…）、schemaform.js
vendor/   heic2any（MIT）
design-defaults/  設計層的「原廠備份」：copy.js + copy/*.js、routes.js、views/、parts/、tokens.css、theme.css
scripts/  守門員與工具（見下）

【設計層：design/ 是 AI 與設計師唯一能改的地方】
design/tokens.css  design/theme.css  design/copy.js  design/routes.js  design/views/  design/parts/  (design/assets/)

docs/     DESIGN-LAYER.md（契約與 agent 手冊）、AI-EDIT-SERVICE-NOTES.md、DESIGN-LAYER-PROVENANCE.md
```

舊的 `admin/views/`、`lib/dom.js`、`lib/schemaform.js` 已不存在：畫面搬到 `design-defaults/views/`（與 `design/views/` 的拷貝），原 `lib/dom.js` 的 `rows`／`renderPager`／`repeater`／`fillSelect` 搬進 `ui/kit.js`，`schemaform` 搬到 `ui/schemaform.js`。

- 設計層只透過 `ctx` 做事（`lib/public-api.json`）。權限、狀態規則、驗證、商品 PUT 的全量保護、確認與**固定警告**都在核心動作裡。
- 路由是 hash router：`#/<view>[/<param>]`（`lib/router.js`）；路由 id、權限在 `lib/meta/*.js`，`design/routes.js` 只決定每個 id 用哪個畫面檔。
- 設計的畫面壞了（載入失敗、丟錯、20 秒沒完成）：核心改掛 `design-defaults/` 的內建畫面並在 console 警告，不會擋住登入。
- `lib/` 不得 import `ui/`、`core/`、`design/`、`app.js`，不建 DOM；`ui/`、`core/`、`app.js` 不得 `fetch`／碰 token 或儲存／寫 `/api/v1`（`scripts/check-layers.mjs`）。

## 執行

```bash
cd admin
node scripts/serve.mjs                 # 靜態伺服器，預設 http://0.0.0.0:3001（PORT／HOST 可改）；只提供後台需要的檔案
node scripts/serve.mjs --csp           # 同上，並送出 nginx.conf 的正式 CSP（用來確認設計遵守它）
CSP_CONNECT_EXTRA="http://127.0.0.1:9000" node scripts/serve.mjs --csp   # 本機 MinIO 上傳圖片時要多放行的 connect-src
```

（舊方式 `npm run dev`＝`python3 -m http.server 3001` 與 `make admin-dev` 仍可用，但不會送 CSP，也不會限制只提供後台檔案。）API 位址：localhost 預設 `http://localhost:8080`，可用 `localStorage.apiBase` 覆寫；非 localhost 用 `location.origin`（同源，refresh token 走 HttpOnly cookie）。**access token 只存在記憶體**。

## 守門員與測試

```bash
node scripts/check-design.mjs --base <ref>   # 全部檢查（範圍、掃描、schema、ctx 清單、固定警告、部署／CSP、核心雜湊、分層）。0 通過／1 違規／2 用法或環境錯誤
node scripts/check-design.mjs                # 同上但略過範圍檢查
node scripts/test-guard.mjs                  # 守門員自己的測試（敵意範例必須被攔下），離線、無瀏覽器
node scripts/scan-views.mjs design           # 只掃設計層
node scripts/check-layers.mjs                # 只跑分層檢查
node scripts/gen-public-api.mjs              # （人類）重新產生 lib/public-api.json
node scripts/check-design.mjs --update       # （人類）核心變更審過之後重寫 scripts/core.manifest.json
node scripts/sync-design.mjs                 # （人類）把 design-defaults/ 同步成 design/（會覆寫 design/）
```

CI：`.github/workflows/ci.yml` 的 job **`Admin guards`**（Node-only、很快）跑 `check-design.mjs`、`test-guard.mjs`，並對 `design/admin-*` 分支以 `--base origin/<base>` 做範圍檢查。

**瀏覽器 e2e**（Playwright，在 repo 根目錄的 `e2e/`）需要拋棄式環境，配方在根目錄 `CLAUDE.md` 的「在另一組 port／乾淨資料庫上跑 happy-path」：`CREATE DATABASE ecommerce_e2e`，用覆寫的 `DATABASE_URL` 跑 `go run ./cmd/migrate` 與 `go run ./cmd/seed -demo`；第二個 API 以 `API_ADDR=127.0.0.1:8180 REDIS_DB=1 PAYMENT_DEFAULT_PROVIDER=mock` 啟動（`REDIS_DB` 必須與主 API 不同）；admin 用本目錄的靜態伺服器（`node scripts/serve.mjs`，:3001；e2e 要用 `http://localhost:3001` 以符合 MinIO CORS）；最後 `cd e2e && E2E_API_BASE_URL=http://127.0.0.1:8180 E2E_ADMIN_BASE_URL=http://localhost:3001 node happy-path.mjs`。跑完停掉程序、`DROP DATABASE ecommerce_e2e`、只清 Redis DB 1、只清 e2e 的 MinIO bucket；絕不碰主資料庫。不可逆動作的確認現在是頁內 `<dialog data-testid="confirm-dialog">`（原本是原生 `confirm()`），e2e 以 `lib.mjs` 的 `armConfirm(page)` 在點擊前預先武裝、自動按 `confirm-yes`。

**視覺一致性（parity）**：`e2e/parity.mjs` 對平台（super admin）與商家 owner 兩種角色，在 390px 與 1280px 逐頁截圖並逐像素比對。

```bash
cd e2e
node parity.mjs capture <outDir>             # 在同一個資料庫狀態下，對「舊」與「新」後台各拍一次（以 E2E_ADMIN_BASE_URL 指向）
node parity.mjs compare <dirA> <dirB>
```

Stage B 的結果：66 張截圖中 65 張逐像素相同，唯一的差異是 before 拍攝的時間點假象。

## 部署

Docker 映像（`Dockerfile`）：`nginx:stable-alpine`，`COPY nginx.conf` 為 `default.conf`，`COPY . /usr/share/nginx/html/` 後移除 web root 裡的 `nginx.conf`。`.dockerignore` 排除 `Dockerfile`、`.dockerignore`、`package.json`、`docs`、`scripts`、`e2e`、`node_modules`，所以文件與守門員腳本不會進映像；新增 ES 模組不需要改 Dockerfile。CI 的映像建置在 `.github/workflows/image-admin.yml`。

**`nginx.conf` 的嚴格 CSP 是部署要求，不是選項**（`check-design.mjs` 的 deploy 步驟檢查它，且 `Dockerfile` 不得移除或改寫）：`script-src 'self' 'wasm-unsafe-eval'`（HEIC 解碼器）、`style-src 'self'`、`img-src 'self' https: data: blob:`、`worker-src 'self' blob:`、`object-src 'none'`、`frame-ancestors 'none'`、`base-uri 'none'`、`form-action 'self'`。**`connect-src` 預設只有 `'self' https://*.r2.cloudflarestorage.com`**（同源後台 API ＋ 商品圖片預簽直傳 R2）。**非 R2 的部署（本機 MinIO、其他 S3 主機）必須把該主機加進 `nginx.conf` 的 `connect-src`**（再重新建置映像），否則圖片上傳會被瀏覽器擋下；預覽環境同理（`CSP_CONNECT_EXTRA`）。正式拓撲與存取控制見 `docs/deploy/admin-exposure-runbook.md` 與 `docs/admin-frontend-integration.md`（repo 根目錄的 `docs/`）。

## 設計變更與核心變更

- 改外觀與用字（AI 或設計師）：只動 `design/`，agent 手冊與契約是 [docs/DESIGN-LAYER.md](docs/DESIGN-LAYER.md)，操作程序是 `.claude/skills/platform-admin-design/SKILL.md`。日後的 AI 編輯服務需求：[docs/AI-EDIT-SERVICE-NOTES.md](docs/AI-EDIT-SERVICE-NOTES.md)。
- 核心變更（新增 `ctx` 函式、不可逆動作、新文案 key、守門員、CSP）：只有人類，流程在 `.claude/skills/core-change/SKILL.md`。
- 檔案來歷與逐檔差異：[docs/DESIGN-LAYER-PROVENANCE.md](docs/DESIGN-LAYER-PROVENANCE.md)。
