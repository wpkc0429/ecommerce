## Context

- API 是 `CGO_ENABLED=0` 的靜態 binary（`api/Dockerfile`，alpine runtime），目前跑在本機經 Cloudflare Tunnel 對外，之後會搬到 VPS。
- 專案慣例：設定相依的路由不得出現（wiring-integrity ⑥ 要求生產 router 與契約 router 逐條相同；`ecpay-sim` 因此是獨立 binary）；回傳集合必須明訂順序；商品 PUT 是全量語意（admin-commerce-catalog D8）；`decodeJSON` 有 1 MiB 上限；新表必須分類進 `tenantOwned`。
- R2：S3 相容（SigV4，region `auto`，endpoint `https://<account_id>.r2.cloudflarestorage.com`），支援 presigned GET/PUT，**不支援 presigned POST policy**（所以無法用 `content-length-range`，改在 PUT 簽入確切 Content-Length）。公開讀取以 bucket 綁定自訂網域，走 Cloudflare CDN 快取。

## Goals / Non-Goals

**Goals:** 多張、可排序、有 alt 的商品圖片；WebP 雙尺寸；直傳不經 API；公開 API 回傳；只接受自家影像網域；R2 未設定時明確降級。

**Non-Goals:**
- SKU 層級圖片（每個 SKU 一張圖）——資料模型留了空間（`product_images` 可日後加 `sku_id`），本 change 不做。
- 任意尺寸即時轉換（`/cdn-cgi/image/` 或 Cloudflare Images）。
- 孤兒物件的定期清理排程（見 D8，以 best-effort 刪除 ＋ 生命週期規則處理）。
- 伺服器端 HEIC／AVIF 解碼（HEIC 由前端轉檔，見 D10）。
- 影片。

## Decisions

### D1：轉檔在 API 端同步進行，不用 Cloudflare Worker
候選：(a) API 在「建立圖片」請求內讀回原檔轉檔；(b) R2 事件通知 → Queue → Worker 轉檔；(c) Cloudflare Images／Image Transformations；(d) 瀏覽器用 canvas 轉 WebP 後直傳。

選 (a)，理由：
1. **可測試性**：轉檔管線是普通 Go 程式碼，進 `make test`／CI；Worker 是第二個部署目標、第二套語言與測試基礎設施，而本專案的規則是「需要進 CI 的檢查必須寫成 Go 測試」。
2. **搬 VPS 不受影響**：(b)(c) 把核心邏輯綁在 Cloudflare 專屬服務上；R2 本身是 S3 相容，(a) 換成任何 S3／MinIO 都只改設定。
3. **Workers 的資源限制**：WASM 版 libwebp 在 Worker 上解碼一張 12MP JPEG ＋ 兩次編碼，CPU 時間遠超免費方案的 10ms，付費方案也需要調高上限；(c) 有每月轉換額度與計費。
4. **一致性**：同步完成代表「建立圖片」回 201 時，圖片**已經可用**；(b) 需要「處理中」狀態、輪詢或回呼，admin 與公開 API 都要多處理一個中間態。
5. (d) 被否決：瀏覽器輸出不可信，API 仍要解碼驗證；Safari 的 WebP 編碼支援不一致；EXIF 去除與尺寸規則會散落在前端。

代價：API 程序承擔 CPU 與記憶體。以 **處理併發上限**（`MEDIA_PROCESS_CONCURRENCY`，預設 2，semaphore，超過的請求等待而非 503）、**輸入上限**（15 MiB、4,000 萬像素，先 `image.DecodeConfig` 檢查再完整解碼，防解壓縮炸彈）、與 **請求逾時**（處理 60 秒硬上限）界定。商家上傳圖片是低頻的後台操作，不在顧客的關鍵路徑上。

### D2：`media.ObjectStore` 介面 ＋ S3 實作；本機用 MinIO，未設定回 503
介面只有 `PresignPut(key, contentType, size, ttl)`、`Get(key, maxBytes)`、`Put(key, body, contentType, cacheControl)`、`Delete(keys...)`。正式與本機都走 `aws-sdk-go-v2` 的 S3 實作，差別只有 endpoint（`R2_ENDPOINT` 覆寫為 `http://127.0.0.1:9000`）——**沒有「本機檔案系統」的第二份實作**，否則 presign 這條最容易出錯的路徑在本機永遠不會被走到。

設定是全有或全無：五個必要值全部有 → 啟用；全部空 → 停用；部分有 → **拒絕啟動**（部分設定必然是錯誤，與 `REPORT_DEFAULT_TIMEZONE` 同待遇）。`APP_ENV=production` 下 `R2_SECRET_ACCESS_KEY` 納入 `validateSecrets` 的已知預設值檢查（MinIO 的 `minioadmin`）。停用時 `media.Service` 仍被建構（`Enabled()==false`），所有媒體路由照常掛載、回 `503 media_not_configured`——路由表不因設定而異（檢查 ⑥），也不需要 nil 分支。

整合測試用 `media.FakeStore`（記憶體實作，presign 回一個 `httptest.Server` 的 URL），因此 `make test-int` 不需要 MinIO；e2e 需要 MinIO（`docker-compose.yml` 新服務，`make up` 一併啟動）。

### D3：上傳意圖表，API 永不接受網址或任意鍵
`media_uploads`（租戶資料）：`id`（uuid）、`shop_id`、`object_key`（`incoming/{shop_id}/{uuid}`，由 API 產生）、`content_type`、`size_bytes`、`purpose`（`product_image`／`content_image`）、`created_by`（user id）、`expires_at`、`consumed_at`。建立圖片時只接受 `upload_id`：查得到、屬於本店、未過期、未消費、`purpose` 相符，才去讀物件；消費以條件式 UPDATE（`consumed_at IS NULL`）＋ 影響列數仲裁，重送同一個 `upload_id` 第二次回 409。這讓「只接受影像網域」對商品圖片是**結構性的**——沒有任何欄位放得下外部網址。

presign 簽入 `Content-Type` 與 `Content-Length`：瀏覽器送出不同的值 R2 會拒絕簽章。讀回時仍以 `maxBytes` 截斷並以內容嗅探（`http.DetectContentType` ＋ 實際解碼）驗證，不信任宣告。

### D4：輸出規格
- 兩個變體：`thumb`（長邊 ≤ 480）、`large`（長邊 ≤ 1600）；**只縮不放**；保持比例、不裁切（裁切是主題 CSS 的事，`object-fit`）。
- WebP lossy，品質 80（`MEDIA_WEBP_QUALITY` 可調）。
- 先依 EXIF Orientation 轉正再縮放；重新編碼天然丟棄 EXIF／GPS——**這是隱私需求**，因為整個 bucket 經自訂網域公開。原檔處理完立刻刪除；`incoming/` 另設 1 天生命週期規則兜底（處理失敗或使用者放棄的上傳）。
- 鍵：`shops/{shop_id}/products/{product_id}/{image_uuid}/{thumb|large}.webp`；內容圖片 `shops/{shop_id}/content/{image_uuid}/{variant}.webp`。鍵含 uuid、內容永不改寫，所以寫入時設 `Cache-Control: public, max-age=31536000, immutable`。
- 縮放用 `golang.org/x/image/draw.CatmullRom`；解碼支援 JPEG／PNG／WebP（`x/image/webp`）；GIF 取第一幀（列為次要）。
- WebP 編碼用 `github.com/gen2brain/webp`：libwebp 編成 WASM、以 wazero 執行，**不需 cgo**，保住 `CGO_ENABLED=0` 的靜態 binary。`chai2010/webp` 需要 cgo（會改 Dockerfile 與 CI）；純 Go 的 `nativewebp` 只有無損模式，照片體積比 JPEG 還大。

### D5：資料庫存鍵不存網址
`product_images`：`id`、`shop_id`、`product_id`（FK，product 刪除時 CASCADE）、`image_key`（`shops/.../{uuid}`，不含變體與副檔名）、`position`（int）、`alt`（≤ 300 rune，預設 `''`）、`width`／`height`（large 變體實際尺寸）、`created_at`。URL 在序列化時以 `IMAGE_PUBLIC_BASE_URL + "/" + image_key + "/{variant}.webp"` 組成。換網域（例如搬 VPS 後改用別的 CDN）只改一個環境變數，不需資料遷移。

### D6：圖片是子資源，不進商品 PUT
商品 PUT 會刪除未出現在酬載中的 SKU（D8）。若圖片也進同一酬載，任何沒載入圖片的舊版 admin 或第三方 admin 前端一存檔就會刪光圖片——而且不會噴錯。子資源端點：
- `GET  .../products/{productID}/images`（`product.view`）
- `POST .../products/{productID}/images`（`product.edit`；`{upload_id, alt}`；新圖排最後）
- `PUT  .../products/{productID}/images/{imageID}`（`product.edit`；只改 `alt`）
- `PUT  .../products/{productID}/images/order`（`product.edit`；`{image_ids: [...]}` 必須**恰好是**該商品目前全部圖片的排列，否則 422——不允許部分排序，避免兩個分頁同時操作時默默留下重複的 position）
- `DELETE .../products/{productID}/images/{imageID}`（`product.edit`）
- `POST .../media/uploads`（依 `purpose` 要求 `product.edit` 或 `page.edit`，在 handler 內以 `Snapshot.Allows` 判定）
- `POST .../media/content-images`（`page.edit`；`{upload_id}` → `{urls: {thumb, large}, width, height}`）
- `GET /api/v1/admin/media/config`（已認證即可；`{enabled, public_base_url, max_bytes, accepted_types}`，供 admin 決定是否顯示上傳控件）

**不新增權限節點**：圖片是商品內容的一部分，`product.edit` 已涵蓋（editor 本來就能改商品）。

### D7：CMS 圖片欄位的網域限制只在媒體已設定時生效
媒體未設定時沒有任何辦法產生影像網域的網址，強制限制等於讓 CMS 圖片欄位無法使用。已設定時：`x-editor: image` 欄位（以 schema 標註觸發，與 rich text 消毒同一個走訪器）的值必須是 `""` 或以 `IMAGE_PUBLIC_BASE_URL + "/"` 開頭，否則 422（pointer 指到欄位）；rich text 的 `img[src]` 不符者被移除（消毒是改寫，與 `cms-rich-text-sanitization` D2 一致）。另可用 `IMAGE_EXTRA_ALLOWED_ORIGINS` 列出額外允許的前綴（遷移期用）。比對是**前綴含結尾斜線**，避免 `https://img.ksdevworks.online.evil.test` 繞過。

### D8：刪除是 best-effort，交易後執行
刪除圖片／商品時，資料列在交易內刪除，物件在 commit 之後刪除（失敗只留 WARN log，含 `shop_id` 與鍵，不重試）。理由：物件刪除失敗的後果是孤兒檔（花錢、不影響正確性），而把網路呼叫放進交易會讓 R2 故障變成商品無法刪除。孤兒清理指令記為後續。

### D10：HEIC 是前端的責任，API 不接受
iPhone 的原生格式是 HEIC。伺服器端支援需要另一個 WASM 解碼器（libheif，體積與專利授權都是負擔），而所有目標瀏覽器都能在 canvas 上輸出 JPEG／WebP。所以：
- API 的 `content_type` 白名單**維持** `image/jpeg`、`image/png`、`image/webp`，HEIC／HEIF 回 422（錯誤訊息明示「請先轉成 JPEG 或 WebP」）。
- **admin 前端在上傳前於瀏覽器內把 HEIC／HEIF 轉成 JPEG**（品質 0.92，保留原始解析度；最終 WebP 仍由伺服器產生，前端不做縮放）：Safari 可直接以 `createImageBitmap` 解碼 HEIC；其他瀏覽器使用 vendored 的 `heic2any`（放在 `admin/vendor/`，原生 ES module 載入，不引入建置步驟）。轉檔後的檔案大小同樣受 15 MiB 上限約束。
- 這是**每一個 admin 前端**的責任（共用 merchant admin SPA 與 Bouquet 的自訂前端皆然），寫進 `docs/admin-frontend-integration.md` 的上傳流程章節；API 不為任何單一前端特判。

### D9：`images` 在公開回應永遠存在
即使媒體未設定或商品沒有圖片，`images` 也是 `[]`，不是缺鍵或 `null`——消費端不需要分辨三種「沒有」。

## Risks / Trade-offs

- [轉檔吃滿 API 記憶體] → 併發上限 2 ＋ 像素上限 40MP；一張 40MP RGBA 解碼約 160MB，2 併發約 320MB 峰值。搬 VPS 時若記憶體低於 1GB，部署設定把 `MEDIA_PROCESS_CONCURRENCY` 設為 1（列入 `docs/media-r2-setup.md`）。
- [同步請求在慢速 R2 上逾時] → 60 秒硬上限；失敗時 upload 意圖不被消費，可重試。
- [WASM 編碼器首次呼叫的初始化延遲] → 於啟動時（媒體已啟用）預熱一次。
- [既有 CMS 內容含外部圖片網址，媒體啟用後再存檔會 422] → 錯誤訊息指出欄位；`IMAGE_EXTRA_ALLOWED_ORIGINS` 作為過渡；目前沒有正式商家依賴 CMS 外部圖片。
- [R2 bucket CORS 未設定 → 瀏覽器直傳失敗] → 列入部署 runbook，e2e 以 MinIO 同樣設定 CORS，確保這條路徑在本機被走到。
- [整個 bucket 公開] → 原檔處理後立即刪除 ＋ 生命週期規則；`incoming/` 鍵含 uuid 不可猜測。

## Migration Plan

1. 建 R2 bucket、自訂網域 `img.ksdevworks.online`、bucket CORS（允許 admin origins 的 `PUT`，`Content-Type` 標頭）、`incoming/` 1 天生命週期、API Token（Object Read & Write，限該 bucket）。
2. migration：`product_images`、`media_uploads`。
3. 部署 API 並設定 `R2_*`、`IMAGE_PUBLIC_BASE_URL`；未設定時行為等同停用，可先部署程式後設定。
4. 回滾：移除設定即停用；資料表保留無害。

## Decided（原 Open Questions，已由使用者定案）

- 尺寸只有 `thumb`（長邊 480）與 `large`（長邊 1600）兩種，不加 medium。
- 伺服器不接受 HEIC；admin 前端在瀏覽器內轉成 JPEG 再上傳（D10）。
- 上限：每商品 20 張、單檔 15 MiB。
