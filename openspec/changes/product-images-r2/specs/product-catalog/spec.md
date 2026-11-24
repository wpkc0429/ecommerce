## ADDED Requirements

### Requirement: 商品回應含圖片

公開與後台的商品列表與詳情 MUST 為每個商品包含 `images` 陣列，元素為 `{id, alt, position, width, height, urls: {thumb, large}}`，依 `position` 遞增、再依 `id` 遞增排序；無圖片或媒體未設定時 MUST 為空陣列（MUST NOT 缺鍵或為 `null`）。`urls` MUST 於回應時由 `IMAGE_PUBLIC_BASE_URL` 與儲存鍵組成，資料庫 MUST NOT 儲存完整網址。

#### Scenario: 公開詳情含排序後的圖片
- **WHEN** 已發佈商品有兩張圖片且第二張被重排到第一
- **THEN** 公開詳情的 `images[0].id` 為原本第二張的 id

#### Scenario: 無圖片
- **WHEN** 已發佈商品沒有任何圖片
- **THEN** 回應含 `"images": []`

#### Scenario: 更換影像網域不需遷移
- **WHEN** `IMAGE_PUBLIC_BASE_URL` 由 `https://img-old.test` 改為 `https://img.ksdevworks.online` 並重啟
- **THEN** 既有圖片的 `urls` 改以新網域開頭
