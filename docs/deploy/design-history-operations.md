# 設計歷史：操作員手冊（切片 1＋2）

給負責部署平台的人。設計與契約見 `docs/design-history.md`。

## 1. 套用與授權

```bash
make migrate      # 兩支 migration：…_add_design_history 與 …_add_design_history_append_only_triggers（純新增、可逆）
make seed         # 建立 design.* 與 design_service.manage 權限節點並授予角色
```

**未 `make seed` 前 owner 看不到歷史、super_admin 簽不了憑證。** `/me` 的 `design.*` 是 UI 的可見性來源。
回滾：`make migrate-down` 兩次（先 trigger 再表；表內資料會一併消失，先 `-export-shop`）。

## 2. 為新商家簽發服務憑證（平台層，人工）

只有 `super_admin`（`design_service.manage`）能做，商家不能自助。

```bash
curl -sS -X POST "$API/api/v1/admin/design-service/credentials" \
  -H "Authorization: Bearer $PLATFORM_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"shop_id": 3, "name": "ai-edit-service", "allowed_cidrs": ["203.0.113.0/24"], "expires_in_days": 90}'
```

回應的 `key`（`dsk_…`）**只出現這一次**：直接放進 AI edit service 的祕密管理，不要貼進聊天或工單。
列表與撤銷：`GET …/credentials?shop_id=3`、`POST …/credentials/{id}/revoke`（立即生效）。
**輪替**：`POST …/credentials/{id}/rotate` 簽發新金鑰，舊的保留 7 天後到期；服務應在到期前 14 天提出輪替。
每店一把（爆炸半徑＝一家店）。與商家 repo 的 git token 分開保管。

機器路由族 `/api/v1/design-service/**` **不經 Cloudflare Access**（服務沒有 user 身分）。建議在部署層另加一條規則，
只允許服務的出口 IP 或 Access service token 打這個路徑前綴；並用 `allowed_cidrs` 在程式層再限制一次。

## 3. 排程（repo 沒有排程器）

```bash
# 每 5 分鐘：把卡住的請求標為 failed（先 dry run 看一次）
cd api && go run ./cmd/design-sweep            # dry run
cd api && go run ./cmd/design-sweep -apply
# 每日：驗證事件雜湊串（有任何斷鏈會以 exit 1 結束）
cd api && go run ./cmd/design-sweep -verify
# 每日：保存期限清除（先 dry run 看一次）
cd api && go run ./cmd/purge-design-history
cd api && go run ./cmd/purge-design-history -apply
```

Windows 工作排程器同理。截圖物件的孤兒報告（只讀；有孤兒或缺物件時 exit 1）：

```bash
cd api && go run ./cmd/design-sweep -orphans            # 全部商家
cd api && go run ./cmd/design-sweep -orphans -shop 3
```

清除指令在 commit 之後才刪物件（best-effort）：失敗會留下孤兒物件而不是不正確的資料列，下一次 `-orphans` 會列出，
孤兒的處置（確認後以 `mc rm`／R2 後台刪除）是人工的。

## 4. 保存期限與調整

預設值與環境變數見 `.env.example` 的 “design history” 區塊（`DESIGN_PROMPT_RETENTION`、`DESIGN_AUDIT_RETENTION` 等，皆有上下限，超出範圍拒絕啟動）。
個資請求或商家下架：`-export-shop N` 匯出 JSON（不含簽名網址），確認後 `-all-for-shop N -apply` 刪除（只影響該店；服務憑證不在其中，請另行撤銷）。

## 5. 沒有設定截圖儲存時（刻意）

`DESIGN_R2_*` 全空時：所有路由照掛，上傳與 `GET …/screenshots` 回 503 `design_storage_not_configured`（授權與請求存在性之後），
`submit` 同樣 503，所以沒有請求能走到 `awaiting_approval`（沒有 after 截圖就不能被核准，D18）。`DESIGN_R2_*` 只設一部分則**拒絕啟動**並列出缺少的鍵名。

## 6. 第二個 bucket：私有的截圖儲存（切片 2，只能由人做）

截圖含後台畫面，**絕不能公開**。R2 的公開存取是 bucket 層級的（`docs/media-r2-setup.md` 把圖片 bucket 整個綁成公開網域），
所以截圖要放在**另一個 bucket**，不是現有 bucket 的前綴。**這一節的 Cloudflare 步驟程式與 CI 都不會做，只由你在 Cloudflare 後台執行。**

1. R2 → Create bucket，例如 `ecommerce-design-history`。**不要**：Connect domain（自訂網域）、Public access → r2.dev subdomain。保持兩者皆未啟用。
2. R2 → Manage API tokens → Create API token：權限 **Object Read & Write**，Specify bucket 只選這個 bucket；**不要**與圖片 bucket 的 token 共用
   （共用等於圖片 token 外洩就能讀所有截圖；production 設定會拒絕 `DESIGN_R2_ACCESS_KEY_ID` 等於 `R2_ACCESS_KEY_ID`）。
3. 把 `DESIGN_R2_BUCKET`、`DESIGN_R2_ACCESS_KEY_ID`、`DESIGN_R2_SECRET_ACCESS_KEY` 放進 API 的祕密管理（帳號與端點沿用 `R2_ACCOUNT_ID`／`R2_ENDPOINT`）。
   不得貼進聊天、工單、日誌。可選：`DESIGN_SCREENSHOT_URL_TTL`（預設 5m）、`DESIGN_SHOP_QUOTA_BYTES`（預設 2 GiB）。
4. 重啟 API。日誌的 “design screenshot storage not configured” 那行消失即已啟用。

**驗證（task 9.3，每次建立或變更 bucket 設定後都要做）**：匿名請求必須被拒絕。

```bash
# 取一個真實物件鍵（簽名網址去掉問號之後的部分），不帶簽名直接讀：必須是 403 或 404，絕不能是 200
curl -s -o /dev/null -w '%{http_code}
' "https://<account>.r2.cloudflarestorage.com/<bucket>/shops/<shop>/design/<request>/<id>.png"
# 匿名列出 bucket：必須是 403
curl -s -o /dev/null -w '%{http_code}
' "https://<account>.r2.cloudflarestorage.com/<bucket>/"
# 若曾開過 r2.dev 或綁過網域，也要對它們試一次（必須 403／404／無法解析）
```

本機 MinIO 的驗證已寫進 `e2e/design-history-path.mjs`（私有 bucket 的簽名網址可讀，去掉簽名／亂改簽名／匿名列出皆被拒絕；2026-11-26 通過）。
本機 bucket 是 `ecommerce-design-history`（開發）與 `ecommerce-design-history-e2e`（e2e，可整桶清空），`docker-compose.yml` 以 `:none`（無匿名存取）建立；
**已存在的 MinIO volume 不會重跑建立**，請手動：`docker exec <minio> mc mb --ignore-existing local/ecommerce-design-history` 後 `mc anonymous set none`。

**風險（殘留）**：bucket 若被誤綁公開網域，鍵含 uuid 但全部截圖都會公開。沒有程式能從內部證明 bucket 是私有的，所以靠上面的驗證與部署檢查清單。

## 7. sandbox 堆疊（admin 截圖的唯一來源，Q5）

admin 截圖**只能**來自拋棄式的 sandbox 部署：另一個 API ＋ 另一個資料庫，資料集是固定的合成資料（`make seed-demo` 等價：虛構顧客、地址、訂單；固定帳號 owner／staff），
**絕不對真實商家的 live 後台截圖**。平台在上傳時檢查 `capture_manifest.dataset = sandbox-v1` 且 `api_origin_class = sandbox`（否則 422 `live_data_capture_refused`），
storefront 截圖只收匿名、`sandbox-v1`（sandbox 來源）或 `public-live`（公開來源，商品與內容本來就公開）。**平台無法證明截圖真的來自 sandbox**：這是契約加縱深防禦，
所以：

- sandbox 堆疊的 repo 內建版本（2026-11-27）：`make sandbox-up`／`sandbox-down`／`sandbox-reset`（`scripts/sandbox/`、`docker/sandbox/`），獨立 Postgres／Redis／MinIO 容器與連接埠、`ecommerce_sandbox` 資料庫、`seed -demo`、拒絕主資料庫與 production 類環境；完整說明、守衛與禁忌見 `docs/deploy/sandbox-stack.md`。第一版與主堆疊跑在同一台機器，之後隨平台搬遷。
- **伺服器端 sandbox origin 清單（2026-11-27）**：平台設定 `DESIGN_SANDBOX_ORIGINS`（逗號分隔的精確 `http(s)://host[:port]`）。admin 截圖的 `X-Capture-Manifest` 必須帶 `origin` 且等於清單中的一項，否則 422 `live_data_capture_refused`。**清單為空時，production 類 `APP_ENV`（含未設定）拒絕全部 admin 截圖**，所以上線前要先設；dev／test 為空時照原本的 manifest 規則收。值來自 `make sandbox-env` 的 `design_sandbox_origins`（本機預設 `http://127.0.0.1:13001` 等），搬到別台主機時改成它的真實 origin。說明與限制見 `docs/deploy/sandbox-stack.md`。
- 商家看的預覽（連真實 API）與擷取預覽（連 sandbox）是同一份靜態檔、兩個部署。
- 擷取腳本（template 後續任務 T6）必須拒絕在 API origin 不在 sandbox 允許清單時啟動，並對 `data-pii` 元素遮罩（T7）。

## 8. Cloudflare 規則建議（人工，repo 外）

- `/api/v1/design-service/**` 不經 Access（服務沒有 user 身分）：用 WAF 自訂規則只允許服務的出口 IP，或要求 Access service token；程式層另有 `allowed_cidrs`。
- 上傳是 PUT 且本文可到 2 MiB：不要對這個路徑前綴開啟會緩衝或改寫本文的轉換；`Content-Type: image/png` 與 `X-Capture-Manifest` 標頭必須原樣通過。
- 簽名網址指向 R2 的 S3 端點（不是你的網域）：商家後台若要顯示截圖，其 CSP `img-src` 需放行該主機（template 任務 T3，是人工審查的核心變更）。
- 截圖 bucket 不要加任何 Cache Rule／Transform Rule／Worker 路由。

## 9. 保存期限（Q1）

已發佈請求的截圖 365 天、其餘（rejected／failed／cancelled）30 天、prompt 全文 365 天、請求與事件 730 天；`cmd/purge-design-history` 會刪 bucket 內的物件（資料列保留並設 `purged_at`，簽名讀取對已清除者回 `url: null`）。
調整：`DESIGN_SCREENSHOT_RETENTION`、`DESIGN_SCREENSHOT_RETENTION_UNPUBLISHED`（有上下限，超出拒絕啟動）。

## 10. 新商家的服務憑證（與 `merchant-onboarding` 同步，平台層、人工）

見第 2 節；入職檢查清單多一步：商家要用 AI 重新設計時，由平台管理員簽發該店的 `dsk_` 憑證（每店一把）、交給服務的祕密管理、記下到期日。
未簽發憑證的商家不受影響（沒有請求就沒有任何寫入）。

## 11. 版本預覽（切片 4，D25）

- **Migration**：`20261126103356_add_design_version_previews`（純新增一張表與四個索引，可逆；沒有新權限節點、不需 re-seed）。主 DB 由操作者自己 `make migrate`。
- **新設定**（`internal/config/design.go`，超出範圍拒絕啟動）：`DESIGN_VERSION_PREVIEW_TTL`（30m，1m–2h）、`DESIGN_VERSION_PREVIEW_HOURLY_CAP`（6，每店每人每小時）、`DESIGN_VERSION_PREVIEW_DAILY_CAP`（30，每店每 UTC 日）、`DESIGN_VERSION_PREVIEW_RETENTION`（90 天，1 天–10 年）。
- **排程**：沿用第 3 節的 `design-sweep -apply`（每 5 分鐘）——它現在也把逾時的版本預覽寫成 `expired`、把逾租約的 `claimed` 寫成 `failed(service_lost)`（輸出多兩行；只碰 `design_version_previews`）。沒有排程時，讀取仍會即時顯示 `expired`，但有效預覽數不會被釋放，也不會被「新請求」以外的方式清掉。
- **清除**：`purge-design-history`（每日）多一行 `version preview usage rows`；只刪早於保存期限的使用列，**不碰請求、事件、截圖**；冪等；`-shop N` 可限定；`-export-shop` 輸出使用列（不含 origin）；`-all-for-shop` 一併刪。
- **常見狀況**：商家說「按了預覽一直在等」→ 服務沒有輪詢或沒有該店憑證（`GET …/version-previews?status=queued` 看佇列）；`failed(service_lost)` → 服務認領後沒有在 5 分鐘內回報；429 → 達每小時或每日上限，等 `Retry-After`；409 `revert_below_core_floor` → 該設計早於一次需要設計層跟著改的 core 同步，這是預期的拒絕。
- **用量查詢**（誰在何時預覽過哪一版）：`SELECT created_at, requested_by_user_id, target_tag, status FROM design_version_previews WHERE shop_id = N ORDER BY created_at DESC;`——admin 的版本預覽會讓發起者看到他本來就有權看的真實資料，這張表是「誰開過」的紀錄。
- 日誌只含預覽 id、shop、站台與狀態，**不含 origin**。

## 12. 預覽 origin 與唯讀工作階段（2026-11-28，操作員步驟）

預覽主機（AI 服務部署改版預覽／版本預覽的地方）必須使用**專屬的預覽 origin**，平台對從這些 origin 登入的工作階段在伺服器端強制唯讀。**沒有 schema 變更、不需 migration、不需 re-seed。**

**為什麼要獨立的 origin 與 cookie 範圍**：預覽頁是 AI 產生的程式碼，不可信。若與商家的一般後台共用 origin，預覽頁就能使用（或從 localStorage／cookie 取得）能寫入的一般工作階段。獨立 origin 讓瀏覽器的同源政策替我們隔離權杖與 cookie：唯讀 refresh cookie（`__Secure-admin_preview_refresh`）是 host-only（沒有 `Domain`）、`Secure`、`HttpOnly`、`SameSite=Strict`，不會送到其他主機。**不要**把預覽放在商家一般後台的同一主機或其子路徑上，也不要設定 `Domain=` 涵蓋父網域的 cookie。

**設定（`.env`）**

| 鍵 | 預設 | 說明 |
|---|---|---|
| `ADMIN_PREVIEW_ORIGINS` | 空＝功能關閉 | 逗號分隔。精確：`https://p-3f9a.preview.example.test`；萬用字元（僅此一種形式）：`https://*.preview.example.test`。 |
| `ADMIN_PREVIEW_SESSION_TTL` | `30m` | 1 分鐘～30 分鐘（且不超過 `DESIGN_VERSION_PREVIEW_TTL`）；工作階段的絕對壽命。 |

**格式規則（啟動時嚴格驗證，不合就拒絕啟動，錯誤訊息只指出鍵與項目）**：必須小寫；`https://`（只有開發環境允許 `http://localhost|127.0.0.1|[::1]` 精確項目）；只有 scheme＋主機＋選填連接埠，不可有 userinfo、路徑、查詢、片段、結尾點；萬用字元只能是整個第一個標籤（`https://*.base`），**只匹配恰好一層**（`a.preview.example.test` 符合；`a.b.preview.example.test`、`preview.example.test` 本身、`evil-preview.example.test` 都不符），底網域至少兩個標籤（拒絕 `https://*.com`），被匹配的標籤只能是小寫英數與連字號、不得以 `xn--`（punycode）開頭；連接埠必須與設定逐字相同（設定沒寫連接埠＝只匹配預設連接埠）。**預覽 origin 不得與 `CORS_ALLOWED_ORIGINS`、`ADMIN_PUBLIC_URL`、`WEBSITE_URL`、`PLATFORM_ADMIN_HOSTS` 重疊**（啟動即失敗）；用 `POST …/allowed-origins` 登記 `purpose=admin` 的 origin 若符合預覽樣式會得到 422。

**登記步驟**：①選一個你控制 DNS 的專屬網域（例如 `preview.<你的網域>`），把萬用字元記錄指向預覽主機；②在 `.env` 設 `ADMIN_PREVIEW_ORIGINS=https://*.preview.<你的網域>`；③重啟 API；④在 Cloudflare Access（若有）讓預覽網域的 `/api/v1/admin` 與一般後台使用相同或更嚴的政策（repo 外、人工）；⑤驗證：從預覽 origin 登入，回應含 `"scope":"preview_ro"`；對任何寫入路由回 403 `preview_read_only`。要關閉功能：清空 `ADMIN_PREVIEW_ORIGINS` 並重啟（既有唯讀權杖立即失效）。

**行為摘要**：從預覽 origin 登入／換發 → 唯讀工作階段（`scope=preview_ro`，綁定該 origin，最長 `ADMIN_PREVIEW_SESSION_TTL`，換發不延長）；預覽 origin 的所有非安全請求（除 login／refresh／logout）→ 403 `preview_read_only`；明列的敏感 GET 也被拒；一般權杖從預覽 origin → 403；唯讀權杖從其他 origin → 401。CORS 對預覽 origin 只開放 `/admin/shops/{id}/…` 與共用的 auth／me／branding／media config。

**AI 服務與預覽主機要做的事**見 `docs/design-service-contract.md` 第 12 節。**稽核**：日誌 `admin_preview_session`（登入／換發／登出）與 `admin_preview_denied`，只含使用者 id 與 origin 短標籤。**已知邊界**：預覽 origin 的辨識依 `Origin`（同源 GET 則依 `Host`）；非瀏覽器客戶端可以不送 `Origin`，那等於不是預覽請求；這不構成繞過，因為預覽頁是瀏覽器程式碼（瀏覽器一定附上正確的 `Origin`，且預覽頁只拿得到唯讀權杖），而唯讀權杖在沒有 `Origin` 時是 401。預覽 origin 上的儲存前端呼叫（`/api/v1/shop/**` 寫入，例如會員登入、購物車、結帳）同樣被拒，預覽只能瀏覽公開讀取。
