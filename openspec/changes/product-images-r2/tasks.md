## 1. 設定（design D2）

- [x] 1.1 `api/internal/config/config.go`：新增 `MediaConfig{AccountID, AccessKeyID, SecretAccessKey, Bucket, PublicBaseURL, Endpoint, ExtraAllowedOrigins, MaxBytes, MaxPixels, ProcessConcurrency, WebPQuality, UploadURLTTL}` 與 `Enabled()`；全有或全無，部分設定回錯並列出缺少的鍵名；`PublicBaseURL` 必須是不帶結尾斜線的 `https://`（dev/test 允許 `http://`）
- [x] 1.2 `config.validateSecrets`：production 下拒絕 `R2_SECRET_ACCESS_KEY` 為 `minioadmin` 等已知預設
- [x] 1.3 config 單元測試：全空停用、全有啟用、部分設定錯誤、URL 結尾斜線正規化
- [x] 1.4 `.env.example`：新增 `R2_*`／`IMAGE_PUBLIC_BASE_URL`／`IMAGE_EXTRA_ALLOWED_ORIGINS`／`MEDIA_*`，預設指向本機 MinIO（`R2_ENDPOINT=http://127.0.0.1:9000`）並註明正式值
- [x] 1.5 `docker-compose.yml`：新增 MinIO（只綁 `127.0.0.1`、無預設密碼，比照 Postgres/Redis 的規則）＋ 一次性 init 容器建立 bucket、設定匿名讀取、bucket CORS（允許 `http://localhost:3001` PUT）；`Makefile` 的 `up` 一併等待其健康檢查

## 2. 資料模型（design D3／D5）

- [x] 2.1 `api/internal/ent/schema/media.go`：`MediaUpload`（uuid id、shop_id、object_key、content_type、size_bytes、purpose、created_by、expires_at、consumed_at nullable；索引 `(shop_id, id)`）與 `ProductImage`（shop_id、product_id FK CASCADE、image_key unique、position、alt、width、height、created_at；索引 `(product_id, position, id)`）；`Product` 新增 `images` edge（CASCADE 放在 assoc edge 上，見 catalog.go 的註解）
- [x] 2.2 CHECK：`media_uploads_purpose_check`、`media_uploads_size_check`、`product_images_position_check (position >= 0)`
- [x] 2.3 `go generate ./internal/ent`；`make migrate-gen name=add_product_images`；up→down→up
- [x] 2.4 `api/internal/tenant`：兩張表登記進 `tenantOwned`（`enttenancy_internal_test.go` 會要求）

## 3. 物件儲存與轉檔（design D1／D2／D4）

- [x] 3.1 `api/go.mod`：`aws-sdk-go-v2/service/s3`、`aws-sdk-go-v2/feature/s3/manager`（若需要）、`golang.org/x/image`、`github.com/gen2brain/webp`
- [x] 3.2 新增 `api/internal/media/store.go`：`ObjectStore` 介面；`s3store.go`：R2/MinIO 實作（region `auto`、path-style 供 MinIO、`PresignPut` 簽入 Content-Type 與 Content-Length）；`fakestore.go`：記憶體實作＋ `httptest` presign 端點（測試用）
- [x] 3.3 新增 `api/internal/media/process.go`：`DecodeConfig` 先驗像素上限 → 解碼（jpeg/png/webp/gif 首幀）→ EXIF 轉正 → CatmullRom 縮放 → WebP 編碼；輸出 `[]Variant{name, bytes, w, h}`
- [x] 3.4 `process_test.go`：只縮不放、比例、EXIF 方向（以內建 fixture）、輸出無 EXIF、超像素拒絕、內容嗅探拒絕 HTML、WebP 可被 `x/image/webp` 解回
- [x] 3.5 新增 `api/internal/media/service.go`：`CreateUpload`、`ConsumeUpload`（條件式 UPDATE `consumed_at IS NULL` ＋ 影響列數）、`AttachProductImage`、`CreateContentImage`、`UpdateAlt`、`Reorder`（完整排列檢查、單一交易）、`DeleteImage`；commit 後 best-effort 刪除物件（design D8）；semaphore 控制併發；60 秒處理逾時
- [x] 3.6 啟用時於啟動預熱一次 WebP 編碼器（WASM 初始化）
- [x] 3.7 `catalog.DeleteProduct` 之後 best-effort 刪除 `shops/{shop}/products/{pid}/` 前綴物件

## 4. HTTP（design D6／D9）

- [x] 4.1 新增 `api/internal/httpapi/media.go`：`MediaHandler.MountShopAdmin`（uploads、content-images、商品圖片五條路由）與 `MountAdmin`（`GET /admin/media/config`）；`purpose` 對應權限於 handler 內以 `Engine.Snapshot` 判定；停用時一律 `503 media_not_configured`（`httpx` 新增該錯誤碼）
- [x] 4.2 `router.go`：`Deps.Media`；掛在 `/shops/{shopID}` 群組與 admin 已認證群組；確認 `PUT .../images/order` 與 `PUT .../images/{imageID}` 在 chi 中的解析順序
- [x] 4.3 `products.go`：`productJSON` 輸出 `images`（永遠存在）；`catalog` 的商品查詢 `WithImages(order by position, id)`；URL 由 `media.URLFor(key, variant)` 組成
- [x] 4.4 `internal/app/wire.go`：建構 `media.Service`（停用時也建構）並注入；`wiring_integration_test.go` 檢查 ⑤⑥ 通過（若 `Deps` 新欄位為介面，在 overrides 登記）

## 5. CMS 網域限制（design D7）

- [x] 5.1 `api/internal/cms`：沿用 rich text 的 schema 走訪器，對 `x-editor: image` 欄位做前綴檢查（含結尾斜線），不符回 `ValidationError` 並帶 JSON pointer；媒體停用時跳過
- [x] 5.2 若 `cms-rich-text-sanitization` 已上線：rich text allowlist 的 `img[src]` 改為只允許影像網域前綴（由設定注入，不寫死在 allowlist.json）；否則此任務順延至該 change
- [x] 5.3 整合測試：外部網址 422、相似網域 422、影像網域通過、停用時不限制

## 6. Admin

- [x] 6.1 新增 `admin/views/product-images.js`（由 `products.js` 匯入，比照 `order-shipments.js` 的拆法，避免 `products.js` 超過 400 行）：上傳（presign → `fetch PUT` → attach）、縮圖、alt、上下移動、刪除
- [x] 6.1a HEIC 轉檔（design D10）：vendored `admin/vendor/heic2any.min.js`（記錄版本與授權）；Safari 走 `createImageBitmap` → canvas → `toBlob('image/jpeg', 0.92)`，其他瀏覽器走 heic2any；轉檔後重新檢查 15 MiB 上限；失敗時顯示說明且不送請求
- [x] 6.2 `admin/lib/schemaform.js`：`x-editor: image` 欄位在媒體啟用時加上傳按鈕
- [x] 6.3 開機時呼叫 `GET /admin/media/config` 並存在 shell state；量測 #4′（`lib/`＋`app.js` 淨增 vs `views/` 淨增）與 #5，記入 CLAUDE.md

## 7. API 契約

- [x] 7.1 `openapi.yaml`：新路由、`MediaUpload`／`ProductImage`／`MediaConfig` schema、`Product.images`、`503 media_not_configured`
- [x] 7.2 重新產生 `v1-surface.txt`，確認只新增
- [x] 7.3 `openapi_conformance_integration_test.go`：以 `FakeStore` 走完整上傳流程並驗證回應

## 8. 測試

- [x] 8.1 `api/internal/httpapi/media_integration_test.go`（`FakeStore`）：presign 成功／格式與大小 422／HEIC 422／權限 403／跨店 403；attach 成功（變體尺寸、原檔已刪）；重用 409；他店 upload 422；過期 422；內容不符 422；每商品上限 409；重排完整排列與不完整 422；刪除；商品 PUT 不影響圖片；商品刪除 cascade；停用 503；`media/config`
- [x] 8.2 公開商品回應含排序後的 `images`、無圖時 `[]`、換 `IMAGE_PUBLIC_BASE_URL` 後 URL 改變
- [x] 8.3 併發：同一 `upload_id` 兩個並行 attach，恰好一個成功

## 9. E2E（需 MinIO）

- [x] 9.1 `e2e/fixtures/`：一張小 JPEG（含 EXIF 方向 6）
- [x] 9.2 `e2e/happy-path.mjs` 商品目錄段：在編輯器上傳兩張、重排、改 alt → 公開詳情順序與 alt 正確、`urls.large` 可 GET 且 `Content-Type: image/webp`
- [x] 9.3 `e2e/commerce-path.mjs`：API 層走 presign → PUT → attach（不需瀏覽器）

## 10. 文件與驗收

- [x] 10.1 `docs/storefront-integration.md`：`images` 欄位、變體尺寸、URL 不可變可長期快取
- [x] 10.2 新增 `docs/media-r2-setup.md`（部署 runbook：bucket、自訂網域、CORS（允許**所有** admin origin，含 Bouquet 自訂前端）、生命週期、API Token 權限、VPS 記憶體 < 1GB 時併發設 1）
- [x] 10.2a `docs/admin-frontend-integration.md`（由 `admin-api-public-exposure` 建立；若本 change 先上線則在此建立）：上傳流程章節——presign → PUT（必帶回傳的 headers）→ attach；HEIC 必須由前端先轉 JPEG
- [x] 10.3 `CLAUDE.md`：轉檔在 API 端的理由、存鍵不存網址、圖片不進商品 PUT、停用 503 且路由照掛、網域前綴比對含斜線
- [x] 10.4 `make test`、`make test-int`、`make lint`、`make openapi`；`make up`（含 MinIO）＋ `node happy-path.mjs`、`node commerce-path.mjs` 全綠
