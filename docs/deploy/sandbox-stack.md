# Sandbox 堆疊（admin 截圖的唯一來源）

來源：`openspec/changes/design-history/design.md` D11／D12 與決定 Q5（admin 截圖**只**從 sandbox 取得）。
隱私規則 R1–R5 見 `docs/design-history-privacy.md`，服務契約見 `docs/design-service-contract.md`，營運手冊見 `design-history-operations.md` 第 7 節。

## 它是什麼

一組**獨立、拋棄式**的部署，跟主要的本機堆疊（`docker-compose.yml`、`ecommerce` 資料庫、:8080、:3001）**沒有任何共用物**：

| 項目 | sandbox | 主堆疊（不得共用） |
|---|---|---|
| Compose 專案 | `ecommerce-sandbox`（`docker/sandbox/docker-compose.yml`） | `ecommerce` |
| Postgres | 自己的**容器與 volume**，127.0.0.1:**55432**，資料庫 `ecommerce_sandbox`，使用者 `sandbox` | :5432，`ecommerce` |
| Redis | 自己的容器 127.0.0.1:**56379**，邏輯 DB **7**，`requirepass` | :6379，DB 0／1 |
| MinIO | 自己的容器 127.0.0.1:**59000**（主控台 **59001**），bucket `sandbox-media`（公開讀）與 `sandbox-design-history`（私有） | :9000／9001 |
| API | `APP_ENV=development`、127.0.0.1:**18080**、mock 金流、**不設 SMTP**（＝完全不寄信的 e-mail sink） | :8080 |
| admin 靜態檔 | `admin/scripts/serve.mjs`，127.0.0.1:**13001** | :3001 |

所有連接埠只綁 127.0.0.1。要改埠請用 `SANDBOX_PG_PORT`／`SANDBOX_REDIS_PORT`／`SANDBOX_MINIO_PORT`／`SANDBOX_MINIO_CONSOLE_PORT`／`SANDBOX_API_PORT`／`SANDBOX_ADMIN_PORT`；與主／e2e 堆疊的埠相同會被拒絕。
狀態（隨機產生的憑證、PID、日誌、編譯出的二進位）放在 `%TEMP%\ecommerce-sandbox`（或 `SANDBOX_HOME`），**不在 repo 內**，`down` 時全部刪除。

資料集是 `make seed-demo` 的合成資料（`cmd/seed -demo`），帳號固定且公開（只存在於拋棄式資料庫內）：

- 平台管理員 `sandbox-platform@example.com` ／ `sandbox-platform-pw-0001`
- 商家 owner `demo-owner@example.com` ／ `demo-owner-change-me`；店員 `demo-staff@example.com` ／ `demo-staff-change-me`
- 顧客 `demo-customer@example.com` ／ `demo-customer-change-me`；publishable key `pk_demoseedstorefrontkeynotsecret`

## 啟動、停止、重設

```bash
make sandbox-up       # 或：node scripts/sandbox/sandbox.mjs up   （編譯 → 容器 → migrate → seed -demo → API → admin；約 10 秒，可重複執行）
make sandbox-env      # 印出 origin、帳號、bucket 名稱（JSON，不含基礎設施密碼）
make sandbox-smoke    # 上傳煙霧測試（見下）
make sandbox-reset    # down + up：回到完全相同的乾淨資料集
make sandbox-down     # 停程序、刪容器與 volume、刪憑證與二進位
node scripts/sandbox/sandbox.mjs selftest   # 守衛的單元測試（不需 docker）
```

沒有 `make` 的機器（例如這台 Windows）直接用 `node scripts/sandbox/sandbox.mjs <指令>`。拍完就 `down`：它是「按需啟動、拍完即毀」的設計。

## 守衛（`scripts/sandbox/guard.mjs`，有 `guard.test.mjs`）

1. **資料庫名稱鎖死**：交給任何 sandbox 程序的 `DATABASE_URL` 必須恰為 `127.0.0.1:{sandbox 埠}/ecommerce_sandbox`、使用者 `sandbox`；指到 `ecommerce`、`ecommerce_e2e`、主 `.env` 讀到的任何資料庫名稱或埠一律拒絕啟動。
2. **拒絕 production 類環境**：呼叫端環境的 `APP_ENV`／`NODE_ENV` 為 production／staging／live 即拒絕；sandbox 程序自己的 `APP_ENV` 固定 `development`。
3. **環境是白名單，不是繼承**：sandbox 程序（server／migrate／seed）拿到的環境變數只有守衛組出的那一組；呼叫者 shell 裡的 `SMTP_*`、`ECPAY_*`、`R2_*`、`CF_*`、`DATABASE_URL` 都進不去。最後一道檢查拒絕任何 `SMTP_`／`ECPAY_`／`CF_`／Stripe 等鍵，要求 `PAYMENT_DEFAULT_PROVIDER=mock`，且 `R2_ENDPOINT`／`IMAGE_PUBLIC_BASE_URL` 必須是 loopback（只能是 sandbox 自己的 MinIO，不可能是 Cloudflare R2）。
4. **不讀主 `.env`**：`config.Load()` 會往上走五層找 `.env` 補齊未設定的變數（空字串不算已設定）。sandbox 程序的工作目錄固定在 repo 之外，且向上五層任一層有 `.env` 就拒絕啟動。
5. **只能刪自己的東西**：`sandbox-reset`／`down` 不連任何資料庫、不執行 `DROP DATABASE`；它對 compose 專案 `ecommerce-sandbox` 執行 `down -v`，並先列出 docker 標示屬於該專案的容器與 volume，只要有一個名稱不是 `ecommerce-sandbox-*`／`ecommerce-sandbox_*` 就中止。主堆疊的容器與 volume 在結構上碰不到（Postgres 是另一個實例，不是同一實例上的另一個資料庫）。
6. **埠不得與主／e2e 堆疊重疊**，且 sandbox 內部互不重複。

## 平台如何判斷「這是 sandbox」（誠實說明）

平台**沒有**「sandbox 部署」的旗標，但從 2026-11-27 起有一份**伺服器端的 sandbox origin 清單**：設定 `DESIGN_SANDBOX_ORIGINS`（逗號分隔的 `http(s)://host[:port]`，不含路徑；大小寫會被正規化，比對是逐字相等）。
`design.checkCaptureRules`（`api/internal/design/screenshots.go`）對 admin 截圖要求：`dataset=sandbox-v1`、`api_origin_class=sandbox`，**且** `capture_manifest.origin`（新的選填欄位，admin 必填）等於清單中的某一項，否則 422 `live_data_capture_refused`。
- 清單為空：`APP_ENV` 為 production 類（含未設定，fail closed）時 **所有 admin 截圖一律被拒**；development／test 時只照 manifest 規則收（origin 可省略）。清單非空時任何環境都強制比對。
- storefront 截圖不受清單影響（它拍的是公開資料）。上傳時 manifest 只保存已知欄位，`origin` 現在是其中之一。
- 這仍然不是證明：manifest 是服務自己宣告的，清單擋住的是「宣告了不在清單內的 origin」與「production 忘了設定清單」。真正的邊界仍是 sandbox 的守衛與擷取腳本的 allowlist（template T6，`scripts/design-capture.mjs`，已實作，且 T6 要在 manifest 帶上 `origin`＝實際截圖的 admin origin）。
- 平台部署要設的值：`make sandbox-env` 的 `design_sandbox_origins`（預設 `http://127.0.0.1:13001`、`http://localhost:13001`、`http://127.0.0.1:18080`、`http://localhost:18080`）；若 sandbox 搬到別的主機，填它的真實 https origin。sandbox 自己的 API 啟動時已由 `buildSandboxEnv` 設好同一份清單，所以 `make sandbox-smoke` 會實際走過這個檢查。
產生標頭值：`node scripts/sandbox/sandbox.mjs manifest`（base64 的 `{"tool":"design-capture",…,"dataset":"sandbox-v1","api_origin_class":"sandbox","origin":"http://127.0.0.1:13001",…}`）。

## AI 編輯服務怎麼取得 origin 與測試帳號

營運者在同一台機器啟動 sandbox（`make sandbox-up`）後，把 `make sandbox-env` 的輸出交給服務（環境變數或設定檔；內容只有 loopback origin 與上面的公開合成帳號，沒有任何真實資料或基礎設施密碼）：

- `api_origin`（`http://127.0.0.1:18080`）、`admin_origin`（`http://127.0.0.1:13001`）、`allowed_api_origins`：擷取腳本（template T6，`scripts/design-capture.mjs`）只可對這些 origin 啟動；admin SPA 的 API 位址由腳本寫進 localStorage `apiBase`。
- `logins.owner`／`logins.staff`：admin 的 owner／staff 截圖；`publishable_key`：storefront 的匿名截圖。
- 服務上傳到**真正的平台**用的是平台簽發給它的 `dsk_` 憑證，與 sandbox 無關；sandbox 自己的設計歷史 bucket 只用來在本機做上傳煙霧測試。
- sandbox origin 是**平台層設定**（`DESIGN_SANDBOX_ORIGINS`），不是每個商家登記的東西；預覽 origin 仍是每個請求用 `PUT …/preview` 登記的。擷取腳本的 allowlist 與平台清單應是同一份（取自 `make sandbox-env`）。

## 上傳煙霧測試（`make sandbox-smoke`）

對 sandbox API 執行 `e2e/design-history-path.mjs`（假服務走完整條設計歷史流程，真 PNG 進 sandbox 的私有 MinIO bucket），其中包含：admin 截圖帶 `sandbox-v1` 標記被接受（201）、帶 `public-live` 被拒（422 `live_data_capture_refused`）、完全沒有 manifest 被拒（422）。它另外向 sandbox 的 Postgres 確認 `current_database()` 是 `ecommerce_sandbox`。

## R1–R5 對照

| 規則 | sandbox 的落點 |
|---|---|
| R1 admin 只從 sandbox 截 | 這個堆疊是唯一合法來源；沒有真實資料可拍；平台上傳檢查 `sandbox-v1` |
| R2 storefront 只拍公開資料、不登入 | sandbox 的 storefront 只有合成商品；匿名 key 是固定 demo key |
| R3 遮罩個資 | 資料本來就是合成的；擷取腳本仍須對 `data-pii` 遮罩（T6 已做；T7 在核心標記 `data-pii` 未做）作為縱深 |
| R4 截圖私有儲存、短效簽名網址 | 平台的設計歷史 bucket；sandbox 內的 bucket 為 `:none` 私有 |
| R5 保存期限與清除 | 平台 `purge-design-history`；sandbox 本身 `down` 即全毀 |

## 絕對不要

- 不要把 sandbox 的 `DATABASE_URL` 指向主資料庫，或在 sandbox 程序裡放真實的 SMTP／金流／R2／Cloudflare 憑證（守衛會拒絕，但不要嘗試繞過）。
- 不要對真實商家的 admin 截圖，不要拿 sandbox 的帳號去登入任何真實部署（它們是公開的合成帳號）。
- 不要把 sandbox 的埠綁到 0.0.0.0、不要放到公網或 Cloudflare Tunnel 後面。
- 不要手動對 `docker/sandbox/docker-compose.yml` 跑 `docker compose`（專案名稱與環境由腳本提供）。
- 不要把 `%TEMP%\ecommerce-sandbox`（含隨機憑證）放進 repo 或備份。

## 搬到平台上之後

這份是「先放在同一台 Windows 機器上」的第一版。搬遷時保留三件事：獨立的 Postgres 實例（或至少獨立資料庫＋獨立帳號）、白名單環境、`ecommerce_sandbox` 名稱檢查；compose 檔與 `guard.mjs` 可直接沿用，改的只是埠與主機。

## 預覽 origin（版本預覽 e2e 用）

`make sandbox-up` 另外設定 `ADMIN_PREVIEW_ORIGINS`（`sandbox-env` 的 `preview_origin`，預設 `http://127.0.0.1:13002`）。**沒有任何東西在這個埠上監聽**；它只是 e2e 登入時送出的 `Origin` 值，用來驗證預覽 origin 的唯讀工作階段（`make sandbox-smoke` 會跑 `e2e/design-version-preview-path.mjs`）。
