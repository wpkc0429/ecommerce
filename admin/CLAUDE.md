# CLAUDE.md

平台後台（`ecommerce` monorepo 的 `admin/`）：繁體中文，無建置的靜態站（原生 ES modules），透過 `../api` 的後台 API 運作。根目錄的 `../CLAUDE.md` 有全專案脈絡與 admin 的歷史決策；README.md 有結構、執行、測試、部署的完整說明，這裡只記慣例。以下指令一律在 `admin/` 目錄下執行。

**安全：AI 的設計工作階段只能在「只含 `admin/`」的 sparse checkout／worktree 裡執行**（指令見 `docs/DESIGN-LAYER.md` 第 0 節），不給 repo 範圍的寫入權、不給任何憑證，結果必須過 `node scripts/check-design.mjs --base <ref>`。這個 repo 還有 Go API 與部署檔，設計工作階段不該看得到它們。設計工作階段的操作手冊：`../.claude/skills/platform-admin-design/SKILL.md`。

## 指令

```bash
node scripts/serve.mjs [--csp]                 # 開發伺服器（預設 0.0.0.0:3001；--csp 送出正式 CSP）
node scripts/check-design.mjs --base <ref>     # 守門員（範圍、掃描、schema、ctx 清單、固定警告、部署/CSP、核心雜湊、分層）；0／1／2
node scripts/check-design.mjs --update         # 人類專用：核心變更審過之後重寫 scripts/core.manifest.json
node scripts/test-guard.mjs                    # 守門員測試（離線）
node scripts/scan-views.mjs design             # 只掃設計層
node scripts/check-layers.mjs                  # 分層檢查
node scripts/gen-public-api.mjs                # 人類專用：重新產生 lib/public-api.json
node scripts/sync-design.mjs                   # 人類專用：design-defaults/{views,parts} -> design/（會覆寫 design/）
cd ../e2e && node happy-path.mjs               # 瀏覽器 e2e（需拋棄式環境，配方見 ../CLAUDE.md）
cd ../e2e && node parity.mjs capture <dir> | compare <a> <b>   # 390／1280 逐頁截圖與逐像素比對
```

沒有 build、沒有 npm 相依、沒有 linter。CI job `Admin guards`（`../.github/workflows/ci.yml`）跑 `check-design` 與 `test-guard`，`design/admin-*` 分支另跑 `--base` 範圍檢查。

## 分層規則：鎖定的核心 vs 設計層

AI **只能改 `design/**`**。其餘全是鎖定的核心（`scripts/core.manifest.json` 以雜湊保護，**預設鎖定**：`admin/` 下除 `design/`、`docs/`、`e2e/` 之外的每個檔案，包含這個 CLAUDE.md 與 README.md）。完整契約：`docs/DESIGN-LAYER.md`；AI 編輯服務需求：`docs/AI-EDIT-SERVICE-NOTES.md`；來歷：`docs/DESIGN-LAYER-PROVENANCE.md`。

- **核心**：`app.js`、`index.html`、`styles.css`、`nginx.conf`、`Dockerfile`、`lib/`（API／session／RBAC／router／money／format／upload／`design.js` 載入器／`design-schema.js`／`public-api.json`／`meta/*.js`）、`core/`（`context.js`、`groups/*`、`members/`、`platform/`、`auth.js`、`routes.js`、`guard.js`、`host.js`、`warnings.js`、`errors.js`）、`ui/`（`kit.js`、`schemaform.js`）、`vendor/`、`design-defaults/`、`scripts/`。
- **設計層** `design/`：`tokens.css`、`theme.css`、`copy.js`、`routes.js`、`views/`、`parts/`、`assets/`。畫面只透過 `ctx`（`lib/public-api.json`）做事，不得 import 核心，不得出現 fetch／storage／eval／document／innerHTML 等。
- **安全相關一律在核心的動作裡**（`core/groups/*.js`），不可搬進畫面：權限檢查（`core/guard.js` 的 `need`／`needAny`／`needPlatform`，在請求之前）、狀態規則、驗證、商品 SKU 保護、上傳上限與 HEIC、**不可逆動作的確認對話框**。
- **`styles.css` 是鎖定的**：外觀只經 `design/tokens.css` 與 `design/theme.css`；做不到就升級給人類。
- **不可逆動作的警告固定在核心**：`lib/meta/<group>.js` 的 `warned`（`core/warnings.js` 讀取；`ROUTE_IDS` 與 `WARNED_CONFIRM_IDS` 都由 `lib/meta` 衍生，單一來源）。對話框 = 設計層的 `confirm.<id>.title`／`.text`／`.yes`／`.no` + 核心的固定警告（永遠顯示，`data-testid="confirm-warning"`）。`validateCopy` 與守門員拒絕任何 `confirm.*.*warn*` 鍵，`css/hides-dialog` 擋隱藏 CSS。
- **嚴格 CSP 是部署要求**：`nginx.conf` 帶（`always`），`check-design.mjs` 的 deploy 步驟檢查；`connect-src` 只有 `'self'` 與 `https://*.r2.cloudflarestorage.com`，非 R2 部署要自己加主機。
- **新增 `ctx` 函式 = 核心變更**（只有人類，流程見 `../.claude/skills/core-change/SKILL.md`）：改 `core/groups/`、`node scripts/gen-public-api.mjs`、`node scripts/check-design.mjs --update`、`node scripts/sync-design.mjs`、更新 `docs/DESIGN-LAYER.md`。新文案 key 加進 `design-defaults/copy/<檔>.js`（`copy.js` 彙整它們）。AI 不跑這些，不要讓設計層繞過去。
- `lib/` 不得 import `ui/`、`core/`、`design/`、`app.js`，不建 DOM；`ui/`、`core/`、`app.js` 不得 `fetch`／XHR／碰 token 或 storage／寫 `/api/v1`（`scripts/check-layers.mjs`）。

## 不可破壞的約定

- **契約**：遵守 `../docs/admin-frontend-integration.md`。**access token 只在記憶體**；同源（非 localhost）用 cookie 模式（登入 `refresh_transport:"cookie"`，refresh／logout 帶 `X-Admin-Refresh: 1`）；跨源（localhost 開發）用 body 模式，refresh token 放 localStorage；單飛 refresh；Access 過期（fetch TypeError 或 HTML 回應）整頁重載一次（30 秒防迴圈）；任何 403 之後探測 `/admin/me`，401 回登入頁。
- **連結 token**（set-password／accept-invite）只在 `core/auth.js` 的閉包與網址 fragment，不進 `ctx.route.params`、不寫任何儲存。**一次性連結**（開通、重設、邀請）只顯示一次（`core/platform/linkonce.js`），不儲存、不記錄；`delivery` 為 `manual` 才有連結。
- **金額絕不縮放**：整個 `admin/` 不存在 `/100` 或 `*100`；顯示走 `lib/money.js`（`ctx.format.money`），輸入是整數；不用 `Intl.NumberFormat` 的 currency。守著它的是 `e2e/happy-path.mjs` 的金額斷言。
- **點數不是金額**：點數走 `formatPoints`／`formatPointsDelta`／`formatPercent`，絕不經 `formatMoney`。訂單金額三件組組合式呈現（`小計 ＋ 運費 ＝ 應付總額`，有折扣時含折扣）。
- **商品 PUT 是全量語意**：`catalog.upsertSKUs` 會刪除未出現在酬載中的既有 SKU，`category_ids` 同樣全量取代。現在由核心的 `saveProduct`（`core/groups/catalog.js`）強制：永遠送完整 `skus`，畫面不能繞過，也沒有「只存基本資料」的入口。
- **圖片是商品的子資源**：走專屬端點、即時生效，不進商品 PUT；重排是完整排列；HEIC 在瀏覽器轉 JPEG（`lib/upload.js`）。
- **權限是體驗層**：`can(node)` 來自 `GET /admin/shops/{id}/me`（平台層節點來自 `GET /admin/me`）；API 的 RBAC 才是授權。切換商家必須重新取得。
- **hash router**：`#/<view>[/<param>]`（`lib/router.js`），hash 未變時 `navigate` 回傳 false、呼叫端自己重畫；深層連結只涵蓋「這一筆」，篩選不入 hash；開機時指向不存在／無權限／`transient` 的畫面安靜回落。
- **篩選一律在伺服器端**（列表是分頁的）；顧客名冊是 PII：不含 `password_hash`／電話，訪客身分如實標示、不提供依 `registered` 批次篩選。
- **各視圖的職責邊界**：顧客詳情不擁有點數（調整表單與流水帳只在 `points`）；報表口徑與「不是現金流量表」要寫在畫面上，不重算 API 的數字。
- 介面文字一律繁體中文；390px 與桌機都要能用，不得水平捲動。

## 行數絆線（tripwire）在新結構下怎麼量

#1／#2／#3 原樣。**#4′**：一次 change 中「鎖定核心」（`lib/` ＋ `core/` ＋ `ui/` ＋ `app.js`）的淨增，不得超過 `design-defaults/views|parts` 淨增的 1/3；**兩種量法（帳面與扣除一次性償還後）都要報**。**#5**：`design-defaults/views/*.js` 或 `parts/*.js` 單檔超過約 400 行就先拆檔。量法：`cat lib/*.js lib/meta/*.js core/*.js core/*/*.js ui/*.js app.js | wc -l` 對 `cat design-defaults/views/*.js design-defaults/parts/*.js | wc -l`（Stage B 基線：核心 3,110 行、預設畫面與零件 3,253 行，最大 `views/products.js` 352）。

## 測試注意

- 絕不碰主 `ecommerce` 資料庫、使用者的 tunnel／Cloudflare；絕不 `docker compose down -v`。拋棄式 API 要調高登入限流、`REDIS_DB` 與主 API 不同。
- e2e 要用 `http://localhost:3001`（MinIO CORS）；截圖放 scratchpad／`E2E_OUT`，不進 git。
- 不可逆動作的確認是頁內對話框，e2e 用 `armConfirm(page)` 預先武裝（要在點擊之前）。
