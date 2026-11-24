## ADDED Requirements

### Requirement: CMS 圖片欄位只接受影像網域

當媒體儲存已設定時，頁面草稿與商家全域內容中所有 schema 標註為 `"x-editor": "image"` 的字串欄位，其值 MUST 為空字串，或以 `IMAGE_PUBLIC_BASE_URL` 加上 `/` 為前綴（另可由 `IMAGE_EXTRA_ALLOWED_ORIGINS` 列出額外允許的前綴，同樣以含結尾斜線的前綴比對）；不符者 MUST 回 422，錯誤 pointer 指向該欄位。rich text 中不符此規則的 `img[src]` MUST 於消毒時被移除。媒體儲存未設定時，本需求 MUST NOT 生效（行為與本變更前相同）。

#### Scenario: 外部網址被拒
- **WHEN** 媒體已設定，編輯者把 hero 的 `image` 設為 `https://example.com/a.jpg`
- **THEN** 回 422，pointer 指向該 section 的 `image`

#### Scenario: 相似網域無法繞過
- **WHEN** `IMAGE_PUBLIC_BASE_URL` 為 `https://img.ksdevworks.online`，值為 `https://img.ksdevworks.online.evil.test/a.webp`
- **THEN** 回 422

#### Scenario: 影像網域網址被接受
- **WHEN** 值為 `https://img.ksdevworks.online/shops/1/content/<uuid>/large.webp`
- **THEN** 儲存成功

#### Scenario: 未設定媒體時不限制
- **WHEN** 媒體未設定，值為 `https://example.com/a.jpg`
- **THEN** 儲存成功
