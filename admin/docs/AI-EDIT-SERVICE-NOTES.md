# 給日後 AI 編輯服務的需求（尚未建置）

這個服務讓平台操作員用對話請 AI agent 修改**平台後台的設計層**（`admin/design/**`），顯示預覽，經人類核准後才發佈。本 repo 已為它做好準備；以下是它可以倚賴的保證，以及它必須做到的事。agent 自己的操作手冊是 [DESIGN-LAYER.md](DESIGN-LAYER.md)（原文交給它）與 `.claude/skills/platform-admin-design/SKILL.md`。（改寫自 `merchant-template` 的同名文件，差異在於：本後台在 `ecommerce` monorepo 內，那個 repo 還有 Go API 與部署檔。）

## 本 repo 的保證

* agent 能改的一切都在 `admin/design/`。其餘全部鎖定並有雜湊清單（`scripts/core.manifest.json`）。
* `node scripts/check-design.mjs --base <ref>`（在 `admin/` 內執行）是唯一的閘門。結束碼 `0` 通過、`1` 有違規（報告在 stderr，或用 `--json` 取得機器可讀報告）、`2` 用法／環境錯誤（什麼都沒判定）。無相依套件、Node 18+、數秒完成。
* 設計在執行期壞掉會退回內建畫面並在 console 警告，不會擋住登入。
* agent 只看得到 `lib/public-api.json` 列的 `ctx`；沒有 token、沒有網路、沒有儲存。不可逆動作的確認與固定警告在核心。

## 安全邊界：沙箱先於掃描（最重要）

`ecommerce` repo 含 Go API 原始碼、migration、`docker/`、`docker-compose.yml`、`.github/`、`docs/`、`e2e/`。**範圍檢查是事後的攔截網，不是沙箱。**服務必須：

1. **只給 agent 一個只含 `admin/` 的工作目錄**：`git worktree add --no-checkout` ＋ `printf '/admin/\n' | git sparse-checkout set --no-cone --stdin`，或 `git clone --filter=blob:none --sparse --no-checkout` ＋ 同樣的 sparse 設定（精確指令見 DESIGN-LAYER.md 第 0 節，已實測）。**不要用 cone 模式**（會帶出 repo 根目錄的檔案）。agent 看不到 `api/`、部署檔、`.env`。
2. **沙箱不含任何憑證**：沒有 git 遠端的寫入權（推送由服務在核准後、用另一個身分做）、沒有部署金鑰、沒有雲端權杖、沒有資料庫字串、沒有 Cloudflare 存取。檔案工具限定在 `admin/design/`（寫入其他路徑一律當場拒絕；範圍檢查是備援）。
3. 每次 agent 回合之後、以及發佈前對**確切的 commit** 再各跑一次：`cd admin && node scripts/check-design.mjs --base "$BASE"`。
4. **長期**：平台後台成長後把 `admin/` 搬到自己的 repo（見 DESIGN-LAYER.md 第 0 節，含搬遷要帶走的清單），讓 agent 的存取範圍等於整個 repo。

## 流程

1. **每個請求一個分支**：記錄 `BASE=$(git rev-parse origin/main)`，從它建立 `design/admin-<request-id>`（`design/admin-` 前綴會觸發 CI `Admin guards` job 的範圍檢查）。agent 在該分支的 sparse checkout 工作。
2. **預覽前先過閘門**：每個 agent 回合後跑 `node scripts/check-design.mjs --base "$BASE"`。非零就**不要**部署預覽；把 stderr 報告（規則、檔案、行號）回饋給 agent，重試次數要有上限（3 次足矣）。越界的 diff 一律拒絕、不要「修補」：丟棄該分支狀態、告訴操作員沒有任何變更。
3. **預覽**：只部署分支的靜態檔（`index.html app.js styles.css lib core ui vendor design design-defaults`；`docs`、`scripts`、`e2e` 不屬於它們，`.dockerignore` 也排除）到一個**拋棄式預覽主機**，**必須以嚴格 CSP 提供**（見下）。預覽連到**拋棄式的 API**（自己的資料庫與 Redis DB），**絕不連正式資料**；預覽主機同樣置於 Cloudflare Access 之後。操作員以手機與桌機寬度檢視、用自己的身分登入。本機等價：`node scripts/serve.mjs --csp`。
4. **核准**：發佈＝服務在操作員明確點擊後，對確切 commit 重跑閘門，再合併分支。保留前一個 commit 讓「復原」是一次 revert。**絕不讓 agent 推送正式分支**；合併後的部署由既有的 `image-admin.yml` 流程完成。
5. **稽核**：每個請求記錄操作員、提示、base 與 head SHA、diff、閘門報告、預覽網址、誰何時核准。

## 預覽與正式環境都必須有嚴格 CSP：必須，不是選項

`nginx.conf` 已對所有回應送出嚴格 CSP（`always`），`Dockerfile` 安裝它，`check-design.mjs` 的 "deploy" 步驟在缺少或弱化時失敗。**未帶此 CSP 的預覽不得給操作員看，服務也不得發佈未在該 CSP 下提供過的預覽。**不是由本 repo 的映像建出的預覽主機必須完全重現該政策（可以在 `connect-src`／`img-src` 加自己的預覽與 API 來源，絕不加 `unsafe-inline`／`unsafe-eval`）。本機驗證：`node scripts/serve.mjs --csp`（送出與 `nginx.conf` 同一份政策）。

```
default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self';
img-src 'self' https: data: blob:; connect-src 'self' https://*.r2.cloudflarestorage.com;
worker-src 'self' blob:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'
```

`connect-src` 的 `https://*.r2.cloudflarestorage.com` 是因為商品圖片直傳 R2 的預簽 PUT。**預覽環境若用本機 MinIO 或其他 S3 主機，必須明確把它加進預覽的 `connect-src`**（`CSP_CONNECT_EXTRA="http://127.0.0.1:9000" node scripts/serve.mjs --csp`），這只放寬預覽、不要放寬正式。有了這份政策，一個漏過掃描的設計仍然不能把資料送到 API 與 bucket 以外的地方、不能執行動態程式碼、不能被嵌入框架。

## 預算與限制

* 每個請求與每天的 token、工具呼叫次數設上限；閘門失敗後的重試上限（3）；diff 大小上限（閘門已限制：300 個檔、4 MiB、單一 JS 128 KiB、素材 512 KiB）。
* 每個 agent 回合有時間限制；預算內過不了閘門就放棄並告知操作員。
* agent 除模型 API 與（唯讀的）repo 來源外不得有網路。

## 無秘密、有紀錄

* agent 的環境變數、檔案與提示裡**不放任何秘密**：沒有 `.env`、沒有 API 金鑰、沒有 session 或 access token、沒有真實顧客資料。預覽用的拋棄式 API 其測試帳號也只屬於該拋棄式環境。
* 紀錄（服務端）：提示、agent 的檔案工具呼叫、閘門報告、預覽主機的存取。**紀錄不得含**預覽使用者的 token、`Authorization`、一次性連結或個資；預覽的 console `[design]` 警告可以回饋給 agent，但先過濾。
* agent 對預覽的觀察（截圖、console）若含資料，用拋棄式資料。

## 服務絕對不能做的事

* 以能碰到其他 repo、平台 API 正式環境或正式主機的憑證執行 agent。
* 閘門失敗、diff 超出 `admin/design/**`、或沒有操作員核准時發佈。
* 執行 `check-design.mjs --update`、`gen-public-api.mjs`、`sync-design.mjs`（會改核心雜湊與契約；只有人類在審過的核心變更中做，見 `.claude/skills/core-change/SKILL.md`）。新增任何 `ctx` 名稱同理。
* 提供沒有嚴格 CSP 的預覽，或讓 agent 改 `nginx.conf`、`Dockerfile`、`lib/meta/*.js`（固定警告）或 `styles.css`。
* 把掃描當沙箱：它是縱深防禦（見 DESIGN-LAYER.md 第 0 節「殘餘風險」）；人類核准預覽與 CSP 是設計的一部分。

## 值得做的選配

* 對預覽跑煙霧檢查：以平台管理員與商家 owner 登入、開每個路由、斷言沒有 `[design]` 以外的 console 錯誤且 390px 不水平溢位（`e2e/parity.mjs capture` 可當逐頁截圖的基礎）。
* 把預覽的 `[design]` console 警告回饋給 agent（「你的 `reports` 畫面丟錯，目前顯示內建畫面」）。
* 提供「重設為預設」：`git checkout <初始 commit> -- admin/design/`。
