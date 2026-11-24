# 自帶後台前端接入說明

給「不用平台的共用後台 SPA、自己做後台」的前端（例如 Bouquet 的自訂後台）。本文件是**活文件**，
後台 API 的其他接入說明會陸續加在這裡；目前有「圖片上傳」（change product-images-r2）與「密碼設定連結」（change platform-account-provisioning）兩章。

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


---

## 密碼設定／重設連結（`#/set-password/{token}`）

平台管理員建立後台帳號、開店、或替既有帳號產生重設連結時，API 回的是一條**一次性連結**，管理員從不知道密碼。
連結形如：

```
{ADMIN_PUBLIC_URL}/#/set-password/{token}
```

**這個形狀是給每一個 admin 前端的契約**（共用的 admin SPA、Bouquet 自訂後台……）：任何 admin 前端
**MUST 在自己的根路徑處理 `#/set-password/{token}`**。API 端不知道、也不假設是哪一個前端在處理它。
（商家成員邀請用同一種做法：`#/accept-invite/{token}`，見本文後段。）

### 為什麼 token 放在 fragment

`#` 之後的部分瀏覽器**不會**送給任何伺服器、proxy 或 Referer，所以 token 不會出現在 nginx、Cloudflare 或
任何存取日誌裡。頁面也**不得**把 token 寫進 `localStorage` 或任何持久儲存——只放在記憶體（closure）。

### `ADMIN_PUBLIC_URL` 指向誰

API 的環境變數 `ADMIN_PUBLIC_URL` 必須是**共用 merchant admin SPA 的網域**，**不是平台管理員自己的 admin 網域**
（後者的存取政策只放行平台擁有者，商家 owner 打開連結會被擋下）。商家若使用自帶的 admin 前端，該前端的網域需要
由平台日後登記後才會被用作連結基底；在那之前連結指向共用 SPA。

### 呼叫順序

1. 路由到 `#/set-password/{token}` 時，**不需要登入**。先
   `POST /api/v1/admin/auth/password-setup/inspect`，body `{ "token": "…" }`
   → `200 { email, kind, expires_at }`（`kind` 是 `setup` 或 `reset`）。把 `email` 顯示給使用者確認是自己的帳號。
2. 使用者輸入兩次新密碼（**至少 12 個字元**）後
   `POST /api/v1/admin/auth/password-setup`，body `{ "token": "…", "password": "…" }` → `204`。
3. 成功後**導回登入頁並預填 email**，不要自動登入（伺服器不簽發 token）。同時清掉網址上的 token
   （`history.replaceState`）。

### 錯誤處理

| 回應 | 意思 | 畫面 |
| --- | --- | --- |
| `404` | token 無效、過期或已使用——**一律是同一個 404，不區分原因** | 「連結無效或已過期，請聯絡管理員重新產生」，**不顯示密碼輸入框** |
| `422` | 密碼少於 12 字元（`details[0].pointer` 是 `/password`）。**不會消耗 token**，可修正後重送 | 顯示訊息、保留表單 |
| `429` | 來源 IP 超過速率限制，`Retry-After` 標頭是秒數 | 提示稍後再試 |

兩個端點都依來源 IP 限流。token 單次使用：成功設定之後，同一條連結再用一律 404；管理員對同一帳號重新產生連結後，
**先前未使用的連結也會失效**。設定成功會撤銷該帳號所有既有的 refresh token（其他裝置需重新登入）。

### 管理員那一側：`delivery` 要先看

產生連結的回應（建立帳號、開店、重設）都帶 `delivery`：

- `"manual"`：回應含 `setup_url`（或 `reset_url`）與 `expires_at`。**只顯示這一次**並提供複製按鈕；
  離開畫面後取不回來（只能重新產生）。提示管理員以安全管道交給本人。
- `"email"`（日後接上寄信之後）：**回應不含連結**。判斷請以 `delivery` 為準，不要以「有沒有 `setup_url` 欄位」判斷。

在搬到 VPS、接上帳號信件之前，`delivery` 一律是 `"manual"`。

### 停用帳號

停用帳號後，它**已簽發、尚未過期**的 access token 在下一個請求就會被拒絕：受 RBAC 守衛的端點回 `403`，
`GET /api/v1/admin/me` 回 `401`，refresh 也回 `401`。前端遇到這些回應照既有的「登出並回登入頁」處理即可。

### `GET /api/v1/admin/me` 的 `platform_permissions`

回應新增 `platform_permissions`：呼叫者在**平台層**的有效權限節點（依名稱排序；沒有平台角色者是 `[]`）。
只用來決定要不要畫平台視圖（「商家」「帳號」分頁）；伺服器端的授權不讀它。

## 商家成員邀請連結（`#/accept-invite/{token}`）

商家 owner（`user.manage_members`）以 `POST /api/v1/admin/shops/{shopID}/invitations`（`{ email, role_id }`）邀請成員。
連結形如 `{ADMIN_PUBLIC_URL}/#/accept-invite/{token}`，與 `#/set-password/{token}` 是**同一種契約**：任何 admin 前端
**MUST 在自己的根路徑處理 `#/accept-invite/{token}`**；token 在 fragment、只放記憶體、不得寫入任何持久儲存；
有效 **7 天**（`ADMIN_INVITE_TOKEN_TTL`）、單次使用。`delivery` 先看（同密碼連結）：`"manual"` 時回應含 `invite_url`
（只顯示一次），`"email"` 時不含連結。

**邀請永遠不能替既有帳號設定密碼。** 邀請時 API 不建立任何帳號；接受時才分流：

1. 路由到 `#/accept-invite/{token}` 時**不需要登入**。先 `POST /api/v1/admin/auth/invitations/inspect`
   （`{ token }`）→ `200 { shop_name, role_name, email, requires_login, expires_at }`。
2. `requires_login = false`（該 email 沒有任何後台帳號）：讓使用者輸入兩次密碼（至少 12 字元），
   `POST /api/v1/admin/auth/invitations/accept`（`{ token, password }`）→ `204`。**不會自動登入**，導回登入頁並預填 email。
3. `requires_login = true`（該 email 已有後台帳號，不論是否已設定密碼）：**不得**呼叫上面那支（會是 `409`、不做任何變更）。
   請使用者以**那個帳號**登入，再 `POST /api/v1/admin/invitations/accept`（已認證，`{ token }`）→ `204`；
   呼叫者 email 必須等於邀請 email，否則 `403`（邀請仍可由本人使用）。成功後重新換發 token（`/auth/refresh`），
   `GET /admin/me` 的 `sids` 就會含新商家。

| 回應 | 意思 |
| --- | --- |
| `404` | token 無效、過期、已使用或已撤回——**一律同一個 404** |
| `409` | （未認證接受）email 已有帳號，請先登入；（已認證接受）已是該商家成員 |
| `422` | 密碼少於 12 字元；**不消耗 token** |
| `429` | 來源 IP 超過速率限制 |

邀請建立端點本身依「商家＋呼叫者」限流，回應對「尚無帳號的 email」與「已有帳號的 email」鍵集合相同。
inspect 的 `requires_login` 會讓**持有連結者**得知該 email 是否有後台帳號（manual 交付下持有者就是邀請者；接上信件後
持有者是受邀者本人，這個殘留消失）。

### 成員資格狀態

`POST /api/v1/admin/shops/{shopID}/users/{userID}/disable|enable`（`user.manage_members`）停用／啟用的是**該店的成員資格**
（`shop_user.status`），不是帳號：被停用者對該店的下一個請求即 `403`，其他商家與登入本身不受影響；重新換發後 `sids`
不再含該店。`GET /admin/shops/{shopID}/users` 的每列帶 `membership_status`（1 啟用／0 停用），`roles[]` 與
`GET /admin/shops/{shopID}/roles` 的每個角色帶 `merchant_assignable`：商家層呼叫者只能邀請、指派 `merchant_assignable = true`
的角色，且不得對持有不可委派角色（例如另一位 owner）的成員、或對自己做停用／角色變更。`merchant_owner` 只有平台能授予或移除。

### 推進出貨為「已退貨」另需 `shipment.return`

`PUT …/shipments/{shipmentID}` 的 `status = "returned"` 除了 `shipment.update` 另需 `shipment.return`，缺少時回 `403`
（出貨、訂單、點數皆不變）；`delivered` 只需 `shipment.update`。UI 應依 `GET /shops/{shopID}/me` 的 `permissions`
決定是否畫「標記退貨」按鈕。
