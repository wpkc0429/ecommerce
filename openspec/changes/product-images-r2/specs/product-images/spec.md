## ADDED Requirements

### Requirement: 上傳意圖與 presigned 直傳

系統 SHALL 提供 `POST /api/v1/admin/shops/{shopID}/media/uploads`，請求含 `content_type`（MUST ∈ `image/jpeg`、`image/png`、`image/webp`）、`size_bytes`（MUST 為正且不超過上限，預設 15 MiB）與 `purpose`（`product_image` 或 `content_image`）。`purpose=product_image` MUST 要求 `product.edit`，`content_image` MUST 要求 `page.edit`；跨商家 MUST 回 403。成功時 MUST 建立一筆屬於該商家的上傳意圖，並回傳 `upload_id`、presigned PUT URL、必須帶的標頭與到期時間（預設 10 分鐘）。物件鍵 MUST 由伺服器產生，MUST 位於 `incoming/` 前綴且含不可猜測的隨機成分；presigned URL MUST 簽入 Content-Type 與 Content-Length。

#### Scenario: 取得上傳網址
- **WHEN** 具 `product.edit` 者請求 `{"content_type":"image/jpeg","size_bytes":204800,"purpose":"product_image"}`
- **THEN** 回 201，含 `upload_id`、`upload_url`、`headers`（含 `Content-Type`）與 `expires_at`

#### Scenario: 不接受的格式或過大
- **WHEN** `content_type` 為 `image/svg+xml`，或 `size_bytes` 超過上限
- **THEN** 回 422，且不建立上傳意圖

#### Scenario: HEIC 由伺服器拒絕
- **WHEN** `content_type` 為 `image/heic`
- **THEN** 回 422，訊息指出需先轉成 JPEG 或 WebP

#### Scenario: 權限不足
- **WHEN** 不具 `product.edit` 者以 `purpose=product_image` 請求
- **THEN** 回 403

### Requirement: 建立商品圖片時於伺服器端轉檔並產生多尺寸

系統 SHALL 提供 `POST /api/v1/admin/shops/{shopID}/products/{productID}/images`（`product.edit`），請求含 `upload_id` 與選填 `alt`（≤ 300 rune）。系統 MUST 只接受屬於本商家、未過期、未被消費、`purpose=product_image` 的上傳意圖；MUST 讀回物件並以實際內容（而非宣告）驗證為允許的影像格式且像素數不超過上限；MUST 依 EXIF 方向轉正後產生 `thumb`（長邊 ≤ 480）與 `large`（長邊 ≤ 1600）兩個 WebP 變體（只縮不放、保持比例），輸出 MUST NOT 含 EXIF 中繼資料；變體 MUST 寫入不可變鍵並設長效快取標頭；原檔 MUST 於成功後刪除。上傳意圖的消費 MUST 是原子的：同一 `upload_id` 第二次使用 MUST 回 409。新圖片 MUST 排在該商品既有圖片之後。單一商品圖片數 MUST NOT 超過上限（預設 20），超過回 409。

回 201 時圖片 MUST 已可經公開網址讀取（不存在「處理中」狀態）。

#### Scenario: 上傳 JPEG 得到兩個 WebP 變體
- **WHEN** 已 PUT 一張 3000×2000 的 JPEG 至 presigned URL 後建立圖片
- **THEN** 回 201，回應 `urls.thumb` 與 `urls.large` 皆以 `IMAGE_PUBLIC_BASE_URL` 開頭並以 `.webp` 結尾，`width`=1600、`height`=1067，原檔已不存在

#### Scenario: 小圖不放大
- **WHEN** 原圖為 300×200
- **THEN** `thumb` 與 `large` 皆為 300×200

#### Scenario: 內容與宣告不符
- **WHEN** 宣告 `image/jpeg` 但實際上傳的是 HTML 檔
- **THEN** 回 422，不建立圖片紀錄

#### Scenario: 重用上傳意圖
- **WHEN** 以同一 `upload_id` 建立圖片兩次
- **THEN** 第二次回 409

#### Scenario: 他店的上傳意圖
- **WHEN** 以 shop B 的 `upload_id` 對 shop A 的商品建立圖片
- **THEN** 回 422（視同不存在），不讀取該物件

#### Scenario: EXIF 被移除
- **WHEN** 原圖含 GPS EXIF
- **THEN** 兩個變體皆不含 EXIF 區塊

### Requirement: 商品圖片的管理與排序

系統 SHALL 提供商品圖片的列表（`product.view`）、修改 alt（`product.edit`）、刪除（`product.edit`）與整批重排（`PUT .../images/order`，`product.edit`）。重排請求的 `image_ids` MUST 恰好是該商品目前全部圖片 id 的一個排列，否則 MUST 回 422 且不做任何變更。圖片 MUST NOT 經由商品 PUT 酬載建立、修改或刪除；商品 PUT 不論是否含任何圖片相關鍵 MUST NOT 影響既有圖片。刪除商品 MUST 一併刪除其圖片紀錄。列表 MUST 依 `position` 遞增、再依 `id` 遞增排序。

#### Scenario: 重排
- **WHEN** 商品有圖片 [A, B, C]，送出 `{"image_ids": [C, A, B]}`
- **THEN** 之後的列表與公開回應依序為 C、A、B

#### Scenario: 不完整的排列被拒
- **WHEN** 商品有圖片 [A, B, C]，送出 `{"image_ids": [C, A]}`
- **THEN** 回 422，順序不變

#### Scenario: 商品 PUT 不影響圖片
- **WHEN** 有 2 張圖片的商品以不含圖片資訊的完整酬載更新
- **THEN** 該商品仍有原本 2 張圖片，順序不變

### Requirement: 媒體儲存未設定時明確降級

物件儲存的必要設定 MUST 為全有或全無：部分設定時服務 MUST 拒絕啟動。全部未設定時，所有媒體路由 MUST 仍然掛載（路由表 MUST NOT 依設定而異），並回 `503`、錯誤碼 `media_not_configured`；`GET /api/v1/admin/media/config` MUST 回報 `enabled: false`；公開商品回應的 `images` MUST 為空陣列。

#### Scenario: 未設定時上傳
- **WHEN** 未設定任何 R2 變數時請求 `POST .../media/uploads`
- **THEN** 回 503，`error.code` 為 `media_not_configured`

#### Scenario: 部分設定
- **WHEN** 只設定 `R2_BUCKET` 與 `R2_ACCOUNT_ID`
- **THEN** 服務啟動失敗，錯誤訊息列出缺少的鍵名

### Requirement: CMS 內容圖片走同一條轉檔管線

系統 SHALL 提供 `POST /api/v1/admin/shops/{shopID}/media/content-images`（`page.edit`），以 `purpose=content_image` 的上傳意圖產生與商品圖片相同規格的兩個 WebP 變體，回傳 `urls.thumb`、`urls.large`、`width`、`height`。上傳意圖的驗證與消費規則同商品圖片。

#### Scenario: 產生內容圖片網址
- **WHEN** 具 `page.edit` 者以已上傳的 `upload_id` 呼叫
- **THEN** 回 201，`urls.large` 以 `IMAGE_PUBLIC_BASE_URL` 開頭
