# 商品圖片：Cloudflare R2 啟用與部署 runbook

（change product-images-r2）

圖片存在 **Cloudflare R2**（S3 相容），瀏覽器以 presigned PUT 直傳、API 讀回後轉成兩個 WebP 變體，
公開讀取走你綁在 bucket 上的自訂網域（經 Cloudflare CDN 快取）。

**沒有設定這些之前，系統完全可用，只是不能上傳圖片**：所有媒體路由照常存在、回 `503 media_not_configured`；
公開商品回應的 `images` 是 `[]`；後台的上傳控件不顯示（後台讀 `GET /api/v1/admin/media/config`）。
所以可以先部署程式、之後再依本文件啟用，不需要停機。

## 一、你要做的事（依序）

### 1. 建立 bucket

Cloudflare Dashboard → R2 → Create bucket（例如 `ecommerce-media`）。位置選離使用者最近的即可。
**不要**開啟 `r2.dev` 公開網址（正式環境用自訂網域，見第 3 步）。

### 2. 建立 API Token（給 API 用）

R2 → Manage R2 API Tokens → Create API token：

- 權限：**Object Read & Write**
- 範圍：**只限這個 bucket**（Specify bucket）
- 建立後記下 **Access Key ID** 與 **Secret Access Key**（Secret 只顯示一次），以及頁面上的 **Account ID**。

> Secret 只放在伺服器的環境變數（`.env`／VPS 的 secret 管理），**不要**進 git、不要貼進任何文件。

### 3. 綁定自訂網域（公開讀取）

bucket → Settings → Custom Domains → Connect Domain，例如 `img.ksdevworks.online`
（網域必須在同一個 Cloudflare 帳號底下）。這個網域就是 `IMAGE_PUBLIC_BASE_URL`。

### 4. 設定 CORS（瀏覽器直傳必要）

bucket → Settings → CORS Policy，**允許所有會呼叫上傳的 admin origin**——包含共用的後台
SPA，**以及 Bouquet 等自帶的自訂後台前端**：

```json
[
  {
    "AllowedOrigins": ["https://admin.ksdevworks.online", "https://<另一個自訂後台的 origin>"],
    "AllowedMethods": ["PUT"],
    "AllowedHeaders": ["Content-Type"],
    "MaxAgeSeconds": 3600
  }
]
```

漏掉一個 origin＝那個後台的上傳在瀏覽器裡失敗（Network 面板看到 CORS 錯誤），其他都正常，很難從 API 這端察覺。
新增一個自帶後台前端時，記得回來加它的 origin。

### 5. 生命週期規則

bucket → Settings → Object lifecycle rules：新增規則，prefix `incoming/`，**1 天後刪除**。
`incoming/` 存放尚未被轉檔的原檔（API 轉檔成功後會立刻刪除原檔；這條規則兜住處理失敗或使用者放棄的上傳）。

### 6. 設定環境變數並重啟 API

| 變數 | 值 |
| --- | --- |
| `R2_ACCOUNT_ID` | 第 2 步的 Account ID |
| `R2_ACCESS_KEY_ID` | 第 2 步的 Access Key ID |
| `R2_SECRET_ACCESS_KEY` | 第 2 步的 Secret Access Key |
| `R2_BUCKET` | 第 1 步的 bucket 名稱 |
| `IMAGE_PUBLIC_BASE_URL` | 第 3 步的網域，**`https://` 開頭、不帶結尾斜線**，例如 `https://img.ksdevworks.online` |

選填：

| 變數 | 預設 | 說明 |
| --- | --- | --- |
| `R2_ENDPOINT` | `https://<R2_ACCOUNT_ID>.r2.cloudflarestorage.com` | 只有本機 MinIO 才需要覆寫 |
| `IMAGE_EXTRA_ALLOWED_ORIGINS` | 空 | 逗號分隔的額外「可信影像網域」前綴（遷移期用，見下） |
| `MEDIA_MAX_BYTES` | `15728640`（15 MiB） | 單檔上限 |
| `MEDIA_MAX_PIXELS` | `40000000` | 像素上限（防解壓縮炸彈） |
| `MEDIA_PROCESS_CONCURRENCY` | `2` | 同時轉檔的請求數（見下「記憶體」） |
| `MEDIA_WEBP_QUALITY` | `80` | WebP 品質 1–100 |
| `MEDIA_UPLOAD_URL_TTL` | `10m` | presigned PUT 網址有效期 |

**五個必要變數是全有或全無**：只設一部分，API 會拒絕啟動並列出缺少的鍵名（部分設定必然是錯的，
與其讓它在第一次上傳時才壞，不如開機就說）。五個全空＝停用。`APP_ENV=production` 下
`R2_SECRET_ACCESS_KEY` 若是 `minioadmin` 這類已知預設值也會拒絕啟動。

### 7. 驗證

1. `GET /api/v1/admin/media/config`（需登入）回 `{"enabled": true, "public_base_url": "https://img…"}`。
2. 後台開一個商品 → 圖片區塊出現「上傳圖片」→ 傳一張 JPEG。
3. 公開 `GET /api/v1/shop/products/{slug}` 的 `images[0].urls.large` 可以直接在瀏覽器開啟，
   `Content-Type: image/webp`、`Cache-Control: public, max-age=31536000, immutable`。
4. R2 的 `incoming/` 底下沒有殘留該次上傳的原檔。

## 二、運作細節

- **只接受 JPEG／PNG／WebP**。HEIC（iPhone 原生格式）由**每一個 admin 前端**在瀏覽器內先轉成 JPEG
  （見 `docs/admin-frontend-integration.md`）；伺服器收到 HEIC 回 422。
- 輸出：`thumb`（長邊 ≤ 480）與 `large`（長邊 ≤ 1600）兩個 WebP，只縮不放。EXIF（含 GPS）在重新編碼時被移除——
  bucket 是公開的，這是隱私需求。
- 每個商品最多 20 張圖片。
- 資料庫只存**儲存鍵**不存網址；網址在回應時由 `IMAGE_PUBLIC_BASE_URL` 組成。**換網域只改這個環境變數**，不需要資料遷移。
- （內容儲存，保留並改造，見 ADR 0001 的修正）CMS 內容的圖片欄位（schema 標註 `x-editor: image`）在媒體啟用後，只接受空字串或以 `IMAGE_PUBLIC_BASE_URL + "/"` 開頭的網址
  （含結尾斜線的前綴比對，`https://img.ksdevworks.online.evil.test` 過不了），否則 422；rich text 裡不符的 `<img>` 會被移除。
  既有 CMS 內容若含外部圖片網址，啟用後再存檔會 422——把那些網址列進 `IMAGE_EXTRA_ALLOWED_ORIGINS` 作為過渡，
  之後換成上傳的圖片。
- 刪除圖片／商品時，資料列在交易內刪除、物件在 commit 後 best-effort 刪除。刪除失敗只留下一行 WARN log
  （含 `shop_id` 與物件鍵），結果是 bucket 裡多一個孤兒檔（花一點儲存費，不影響正確性）。孤兒清理指令記為後續。

### 記憶體

轉檔在 API 程序內同步進行。一張 4,000 萬像素的圖解碼約 160 MB，`MEDIA_PROCESS_CONCURRENCY=2` 時峰值約 320 MB。
**VPS 記憶體小於 1 GB 時，把 `MEDIA_PROCESS_CONCURRENCY` 設為 `1`。** 超過併發上限的請求是排隊等待，不是 503。
處理硬上限 60 秒；失敗時上傳意圖不被消費，可以直接重試。

## 三、本機開發與測試（MinIO）

本機用 docker compose 的 **MinIO** 當作 R2，API 走**同一份 S3 程式碼路徑**（只差 endpoint），所以 presigned PUT、
CORS 這些最容易出錯的地方在本機就會被走到。

```bash
make up   # 啟動 PostgreSQL + Redis + MinIO（--wait 到健康）
```

`.env` 需要（見 `.env.example`，值請自己產生隨機的本機專用值）：

```
MINIO_ROOT_USER=<隨機>
MINIO_ROOT_PASSWORD=<隨機，至少 8 字元>
R2_ACCOUNT_ID=local
R2_ACCESS_KEY_ID=<同 MINIO_ROOT_USER>
R2_SECRET_ACCESS_KEY=<同 MINIO_ROOT_PASSWORD>
R2_BUCKET=ecommerce-media
R2_ENDPOINT=http://127.0.0.1:9000
IMAGE_PUBLIC_BASE_URL=http://127.0.0.1:9000/ecommerce-media
```

- MinIO 只綁 `127.0.0.1`（API `:9000`、console `:9001`）；沒有預設密碼（未設定 `MINIO_ROOT_*` 時 compose 拒絕啟動，與 Postgres／Redis 同規則）。
- 啟動時自動建立兩個 bucket：`ecommerce-media`（開發）與 `ecommerce-media-e2e`（e2e 專用，可以整桶清空而不碰開發圖片），
  皆為匿名可讀（對應 R2 的公開自訂網域）。
- 瀏覽器直傳的 CORS 以 `MINIO_API_CORS_ALLOW_ORIGIN` 設定（預設允許 `http://localhost:3001`、`http://127.0.0.1:3001`）。
  MinIO 沒有 bucket 層級的 CORS API，這是它的全伺服器等價物。
- `make test-int` 不需要 MinIO：整合測試用記憶體內的 `media.FakeStore`（presign 回 `httptest` 網址，同樣強制簽入的
  Content-Type／Content-Length）。只有 e2e 需要 MinIO。
- 映像說明：MinIO 已不在 Docker Hub／Quay 發佈映像，`docker-compose.yml` 釘的是 `bitnamilegacy/minio:2025.7.23`
  （最後一個仍可拉取的社群版建置）。它只是本機開發用的替身，不是任何部署的依賴；若日後換成別的 S3 相容替身，
  只要維持 path-style 與 CORS／匿名讀取即可，API 不需要改。

## 四、回滾

移除上面五個環境變數並重啟＝停用（媒體路由回 503、公開 `images` 變 `[]`、後台隱藏上傳控件）。
資料表與已上傳的物件保留，重新設定後原樣回來。
