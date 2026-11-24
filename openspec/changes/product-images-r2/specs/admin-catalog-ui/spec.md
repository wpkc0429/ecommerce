## ADDED Requirements

### Requirement: 商品編輯器的圖片管理

商品編輯器 MUST 提供圖片區塊：選檔後依序呼叫上傳意圖、直傳 presigned URL、建立圖片；顯示縮圖、可編輯 alt、可上下移動或拖曳重排（送出完整排列）、可刪除。圖片操作 MUST 經由圖片子資源端點即時生效，MUST NOT 依賴商品「儲存」按鈕，也 MUST NOT 把圖片放進商品 PUT 酬載。媒體未設定（`GET /admin/media/config` 回 `enabled: false`）時，圖片區塊 MUST 顯示「圖片儲存尚未設定」而不顯示上傳控件。新商品 MUST 先儲存（取得 id）才能加圖片，畫面需說明。使用者選取 HEIC／HEIF 檔時，前端 MUST 在瀏覽器內先轉成 JPEG 再走上傳流程（API 不接受 HEIC）；轉檔失敗時 MUST 顯示「無法轉換此檔案，請改用 JPEG／PNG」而不送出請求。

#### Scenario: 上傳並重排
- **WHEN** 商家為商品上傳兩張圖片並把第二張移到第一
- **THEN** 重新開啟編輯器時順序保持，公開詳情的順序相同

#### Scenario: 上傳 iPhone 的 HEIC
- **WHEN** 商家選取一張 `.heic` 照片
- **THEN** 前端先轉成 JPEG，上傳意圖的 `content_type` 為 `image/jpeg`，最終得到兩個 WebP 變體

#### Scenario: 媒體未設定
- **WHEN** 媒體未設定時開啟商品編輯器
- **THEN** 圖片區塊顯示未設定的說明，沒有檔案選擇控件

### Requirement: CMS 圖片欄位提供上傳

CMS 表單中 `x-editor: image` 的欄位在媒體已設定時 MUST 提供上傳按鈕：上傳完成後以 `urls.large` 填入欄位值；仍可手動清空。

#### Scenario: 上傳 hero 圖片
- **WHEN** 編輯者於 hero 的 image 欄位上傳圖片並儲存頁面
- **THEN** 欄位值為影像網域的 `large.webp` 網址，儲存成功
