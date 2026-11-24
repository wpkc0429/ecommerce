# 自帶後台前端接入說明

給「不用平台的共用後台 SPA、自己做後台」的前端（例如 Bouquet 的自訂後台）。本文件是**活文件**，
後台 API 的其他接入說明會陸續加在這裡；目前只有「圖片上傳」一章（change product-images-r2）。

> 完整端點與欄位以 `api/openapi/openapi.yaml` 為準；這裡只講**流程與容易踩的點**。

---

## 圖片上傳

### 流程（四步，順序固定）

```
選檔 ─▶ ① HEIC? 先轉 JPEG ─▶ ② POST …/media/uploads ─▶ ③ PUT upload_url ─▶ ④ POST …/images
```

0. 啟動時（與切換商家時）呼叫 `GET /api/v1/admin/media/config`（已認證即可）。
   `enabled=false` 代表這個部署還沒設定物件儲存：**隱藏上傳控件**，不要讓使用者點了才看到 503。
   回應另有 `max_bytes`（單檔上限，預設 15 MiB）、`accepted_types`、`max_images_per_product`（20）。

1. **HEIC／HEIF 必須由前端先轉成 JPEG**（見下節）。伺服器只收 `image/jpeg`、`image/png`、`image/webp`，
   收到 HEIC 回 422（訊息要求先轉檔）。這是**每一個** admin 前端的責任，API 不為任何單一前端特判。

2. `POST /api/v1/admin/shops/{shopID}/media/uploads`
   ```json
   { "content_type": "image/jpeg", "size_bytes": 204800, "purpose": "product_image" }
   ```
   `purpose` 是 `product_image`（需要 `product.edit`）或 `content_image`（CMS 圖片欄位，需要 `page.edit`）。
   回 201：
   ```json
   { "upload_id": "…uuid…", "upload_url": "https://…", "headers": { "Content-Type": "image/jpeg" }, "expires_at": "…" }
   ```
   `size_bytes` 必須是**實際要送的位元組數**——它被簽進網址，送出不同大小的檔案會被儲存端以簽章錯誤拒絕。

3. `PUT upload_url`，body 是檔案本身，**標頭原樣帶回傳的 `headers`**（至少 `Content-Type`）。
   - **不要**帶 `Authorization`：這是直傳到物件儲存，不經過 API，簽章已經在網址裡；多帶反而會讓簽章驗證失敗。
   - 不要自己加別的標頭。`Content-Length` 由瀏覽器依 body 自動設定。
   - 網址 10 分鐘內有效。
   - 這一步會被瀏覽器的 CORS 擋住，如果你的 admin origin 沒有被列進 bucket 的 CORS 允許清單
     （部署端要設，見 `docs/media-r2-setup.md`）。

4. 建立圖片：
   - 商品圖片：`POST /api/v1/admin/shops/{shopID}/products/{productID}/images`，body `{ "upload_id": "…", "alt": "…" }`
   - CMS 圖片欄位：`POST /api/v1/admin/shops/{shopID}/media/content-images`，body `{ "upload_id": "…" }`
     → `{ urls: {thumb, large}, width, height }`，把 `urls.large` 填進欄位。

   伺服器此時才讀回原檔、**以實際內容**驗證、轉成 WebP（`thumb` 長邊 480、`large` 長邊 1600，只縮不放）、
   移除 EXIF、寫入後刪除原檔。**回 201 時圖片已可由公開網址讀取**，沒有「處理中」狀態。
   - `409`：這個 `upload_id` 已經用過（重送請拿新的 upload_id，或直接重新整理列表），或商品已有 20 張。
   - `422`：upload_id 不存在／過期／不屬於這個商家／用途不符，或檔案內容不是 JPEG／PNG／WebP、像素過大。
     失敗**不會消耗** upload_id——同一個 upload_id 可以重試（例如檔案其實還沒傳完）。

### HEIC 轉檔（前端責任）

iPhone 的原生照片格式是 HEIC。選到 `image/heic`／`image/heif`（或副檔名 `.heic`／`.heif`）時，**上傳前**在瀏覽器內轉成 JPEG：

- 品質 `0.92`，**保留原始解析度**（不要縮放——縮放與 WebP 輸出是伺服器的事）。
- Safari 可直接用 `createImageBitmap(file)` → canvas → `canvas.toBlob(cb, "image/jpeg", 0.92)`。
- 其他瀏覽器沒有原生 HEIC 解碼，可用 [heic2any](https://www.npmjs.com/package/heic2any)（MIT）：
  `heic2any({ blob, toType: "image/jpeg", quality: 0.92 })`。共用後台把它 vendored 在 `admin/vendor/`，
  只在需要時才載入（約 1.3 MB）。
- **轉檔後重新檢查大小上限**（`max_bytes`）：HEIC 轉 JPEG 可能比原檔大。超過就顯示說明、**不要送請求**。
- 轉檔失敗：顯示說明（請在手機上轉成 JPEG 後再傳），不要送任何請求。
- `POST …/media/uploads` 的 `content_type` 與 `size_bytes` 要用**轉檔後**的 Blob。

共用後台的參考實作：`admin/lib/upload.js`（約 100 行，沒有相依）。

### 商品圖片是子資源，**不在商品 PUT 裡**

| 動作 | 端點 | 權限 |
| --- | --- | --- |
| 列表 | `GET …/products/{productID}/images` | `product.view` |
| 新增 | `POST …/products/{productID}/images` | `product.edit` |
| 改 alt | `PUT …/products/{productID}/images/{imageID}`，`{ "alt": "…" }`（≤ 300 字元） | `product.edit` |
| 整批重排 | `PUT …/products/{productID}/images/order`，`{ "image_ids": [3,1,2] }` | `product.edit` |
| 刪除 | `DELETE …/products/{productID}/images/{imageID}` | `product.edit` |

- 重排的 `image_ids` 必須**恰好**是該商品目前全部圖片 id 的一個排列（否則 422、不做任何變更）。
  永遠從伺服器剛讀到的列表算新順序，**不要**從畫面反推——兩個分頁同時操作時，部分排序會留下重複的位置。
- **商品的 `PUT` 完全不看圖片**：商品 PUT 是全量語意（漏送的 SKU 會被刪），圖片刻意不放進那個酬載，
  所以舊版前端或第三方前端存檔不會刪光圖片。反過來也一樣：改圖片不需要「儲存」商品。
- 商品回應（列表與詳情，後台與公開）都有 `images: [{id, alt, position, width, height, urls: {thumb, large}}]`，
  依 `position`、`id` 遞增，永遠存在（沒有圖片是 `[]`）。
- 新增的圖片排在最後；每個商品最多 20 張。

### 未設定媒體儲存時

所有上面的端點照常存在，回 `503`、`error.code = "media_not_configured"`；`GET /admin/media/config` 回
`enabled: false`。處理方式只有一個：隱藏上傳控件並顯示「媒體儲存尚未設定」的說明。
