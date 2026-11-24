## Why

商品今天沒有圖片。CMS 的 `x-editor: image` 欄位（hero 背景、feature icon、logo）只是一個任意字串，商家只能貼外部網址——圖片的可用性、大小、格式、EXIF（含 GPS）全部不受控，而且任何網址都被接受。花藝店的商品頁一定要多張圖，後續 phase 的靜態店面也需要一個穩定、可快取、與平台同網域的圖片來源。

## What Changes

- **物件儲存**：Cloudflare R2（S3 相容 API），以自訂網域（例如 `https://img.ksdevworks.online`）經 Cloudflare CDN 公開讀取。平台層設定 `R2_ACCOUNT_ID`／`R2_ACCESS_KEY_ID`／`R2_SECRET_ACCESS_KEY`／`R2_BUCKET`／`IMAGE_PUBLIC_BASE_URL`（另有選填 `R2_ENDPOINT` 覆寫，供本機 MinIO）。
- **上傳流程（直傳）**：
  1. admin 呼叫 `POST /api/v1/admin/shops/{shopID}/media/uploads`（宣告 `content_type`、`size_bytes`、`purpose`）→ API 建立一筆**上傳意圖**並回傳 10 分鐘有效的 presigned PUT URL（簽入 Content-Type 與 Content-Length）。
  2. 瀏覽器直接 PUT 到 R2 的 `incoming/` 前綴。
  3. admin 呼叫 `POST /api/v1/admin/shops/{shopID}/products/{productID}/images`（`upload_id`、`alt`）→ API 從 R2 讀回原檔、驗證、**轉成 WebP 並產生 thumb（長邊 480）與 large（長邊 1600）兩個尺寸**、寫回 R2 的不可變鍵、刪除原檔，並建立 `product_images` 紀錄。
- **轉檔在 API 端同步進行，不用 Worker**（理由見 design D1）。伺服器只接受 JPEG／PNG／WebP；**HEIC 由 admin 前端在瀏覽器內先轉成 JPEG**（每一個 admin 前端的責任，design D10）。
- **商品圖片是子資源**：列表、建立、更新 alt、整批重排（`PUT .../images/order`）、刪除；**不進入商品 PUT 酬載**（避開全量語意的「漏送即刪除」陷阱）。每商品上限 20 張。
- **公開 API**：公開商品列表與詳情 additive 新增 `images: [{id, alt, position, width, height, urls: {thumb, large}}]`，依 `position`、`id` 排序。URL 在讀取時由 `IMAGE_PUBLIC_BASE_URL` ＋ 儲存鍵組成——資料庫存鍵不存網址。
- **只接受影像網域的網址**：商品圖片只能經由上傳意圖建立（API 根本不接受網址）；CMS 的 `x-editor: image` 欄位與 rich text 的 `img[src]` 在媒體已設定時，寫入 MUST 為空字串或以 `IMAGE_PUBLIC_BASE_URL` 開頭，否則 422。另提供 `POST .../media/content-images`（`upload_id`）讓 CMS 圖片欄位走同一條轉檔管線。
- **R2 未設定時**：所有媒體路由**照常掛載**（路由表不得依設定而異，見 wiring-integrity 檢查 ⑥），但回 `503 media_not_configured`；admin 依 `GET /api/v1/admin/media/config`（新端點）隱藏上傳控件；公開回應的 `images` 為空陣列；CMS 圖片欄位維持本變更前的行為（不檢查網域）。本機開發以 docker compose 的 **MinIO** 當作 R2（同一份 S3 程式碼路徑，presign 行為一致）。
- 新增 `product_images` 與 `media_uploads` 兩張租戶資料表。

## Capabilities

### New Capabilities
- `product-images`：上傳意圖與 presigned 直傳、伺服器端轉檔與多尺寸、商品圖片子資源（排序、alt、刪除）、媒體設定缺失時的行為、影像網域限制。

### Modified Capabilities
- `product-catalog`：公開與後台商品回應含 `images`。
- `page-management`：CMS 圖片欄位與 rich text 圖片在媒體設定存在時只接受影像網域。
- `admin-catalog-ui`：商品編輯器的圖片管理區塊；CMS 圖片欄位的上傳按鈕。

## Impact

- **相依套件**：`github.com/aws/aws-sdk-go-v2/service/s3`（＋ presign）、`golang.org/x/image`（resize、webp 解碼）、`github.com/gen2brain/webp`（WASM 版 libwebp，**不需 cgo**，`Dockerfile` 的 `CGO_ENABLED=0` 不變）、`github.com/google/uuid`（若尚未引入）。
- **資料**：兩張新表的 migration；`internal/tenant` 的 `tenantOwned` 登記。
- **程式碼**：新增 `api/internal/media/`（ObjectStore 介面、S3 實作、轉檔管線、service）、`api/internal/httpapi/media.go`、`products.go`、`router.go`、`config.go`、`wire.go`、`cms`（圖片欄位網域驗證）、`admin/views/products.js`（或新拆 `admin/views/product-images.js`）、`admin/lib/schemaform.js`。
- **基礎設施**：`docker-compose.yml` 新增 MinIO 服務；R2 bucket 需設定 CORS（允許 admin origin PUT）與 `incoming/` 生命週期規則（1 天過期）；Cloudflare 自訂網域綁定 bucket。
- **API 契約**：新增約 8 條路由、`ProductImage` schema、`Product.images`；`v1-surface.txt` 只新增行。
- **相依 change**：建議在 `product-subtitle-public-categories` 之後（同改 `productJSON`）；收緊 rich text `img[src]` 需 `cms-rich-text-sanitization` 已上線（若未上線，該任務順延）。
