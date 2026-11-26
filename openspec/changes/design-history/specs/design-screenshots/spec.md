## ADDED Requirements

### Requirement: 截圖 MUST 存放在私有 bucket，只經 API 簽名讀取

截圖 SHALL 存放在與公開影像 bucket 分開的物件儲存 bucket（不綁自訂網域、不啟用公開網址、專用且只限該 bucket 的憑證）。讀取 MUST 只經 `GET /api/v1/admin/shops/{shopID}/design/requests/{requestID}/screenshots`（`design.view`），每張回傳有效期不超過 5 分鐘的簽名網址，回應 `Cache-Control: no-store`；簽名網址 MUST NOT 寫入資料庫或日誌。設定為全有或全無：部分設定 MUST 拒絕啟動；未設定時所有截圖路由 MUST 仍掛載並在授權檢查之後回 503 `design_storage_not_configured`。

#### Scenario: 簽名讀取
- **WHEN** owner 讀取截圖列表
- **THEN** 每張有 `url` 與 `url_expires_at`（距今 ≤ 5 分鐘），回應標頭含 `Cache-Control: no-store`

#### Scenario: 未設定儲存
- **WHEN** 未設定私有 bucket 時上傳截圖
- **THEN** 回 503 `design_storage_not_configured`

#### Scenario: 部分設定
- **WHEN** 只設定 `DESIGN_R2_BUCKET`
- **THEN** 服務啟動失敗並列出缺少的鍵名

### Requirement: 截圖的標準與上傳驗證

截圖 SHALL 為 PNG，視口寬度 MUST 為 390 或 1280，高度 MUST 不超過 10,000，單張 MUST 不超過 2 MiB；`phase` MUST 為 `before` 或 `after`，`role` MUST 為 `owner`、`staff`、`anon` 之一，`view_key` MUST 符合 `^[a-z0-9-]+$`。上傳 MUST 由服務以 `PUT …/screenshots/{phase}/{role}/{viewport}/{view_key}` 直接送位元組；系統 MUST 完整解碼、寬度等於視口，並重新編碼後儲存（去除中繼資料）、對存入位元組計算 SHA-256；若請求帶 `X-Content-SHA256` 必須等於上傳位元組的雜湊。每請求 MUST 不超過 80 張與 40 MiB，每店 MUST 不超過 `DESIGN_SHOP_QUOTA_BYTES`。同一槽以相同位元組重送 MUST 回 200 不新增；不同位元組 MUST 新增 `attempt` 而保留舊列。

#### Scenario: 合法上傳
- **WHEN** 服務上傳寬 390 的 PNG 到 `before/owner/390/orders`
- **THEN** 回 201，列含 `sha256`、`width = 390`、`attempt = 1`，物件存在於私有 bucket

#### Scenario: 寬度與視口不符
- **WHEN** 上傳寬 800 的 PNG 並宣告視口 390
- **THEN** 回 422 `invalid_screenshot`

#### Scenario: 非影像內容
- **WHEN** 上傳內容是 HTML 但標頭宣告 `image/png`
- **THEN** 回 422，不建立列與物件

#### Scenario: 雜湊不符
- **WHEN** `X-Content-SHA256` 不等於上傳位元組的雜湊
- **THEN** 回 422

#### Scenario: 超過配額
- **WHEN** 該店累計大小已達上限
- **THEN** 回 409 `quota_exceeded`

#### Scenario: 槽位與嘗試上限
- **WHEN** 請求已有 80 個槽位仍上傳新槽位
- **THEN** 回 409 `slot_cap_exceeded`；重拍既有槽位不受影響
- **WHEN** 該請求的上傳嘗試總數已達上限（預設 240）
- **THEN** 回 409 `attempt_cap_exceeded`

#### Scenario: 重送與重拍
- **WHEN** 同一槽先後以相同位元組、再以不同位元組上傳
- **THEN** 第一次重送回 200 不新增列，第二次新增 `attempt = 2`，列表預設只回每槽最新 attempt

### Requirement: 固定視圖集與決定性

系統 SHALL 以 `set_version` 標示視圖集版本；before 與 after MUST 為相同 `set_version` 且槽位集合相等才算「完整」。擷取 MUST 以固定視口、`deviceScaleFactor = 1`、等待網路閒置、字型就緒與無載入指示、關閉動畫後進行。系統 MAY 儲存服務計算的 `diff_ppm`，但 MUST NOT 以它作為任何閘門。

#### Scenario: 視圖集版本不同
- **WHEN** after 的 `set_version` 與 before 不同時送審
- **THEN** 回 409 `screenshots_incomplete`

### Requirement: Admin 截圖 MUST 只來自 sandbox 資料集

`site = admin` 的截圖，其 `capture_manifest.dataset` MUST 為 `sandbox-v1`，否則系統 MUST 回 422 `live_data_capture_refused`。`site = storefront` 的截圖只可來自公開資料（`dataset` 為 `sandbox-v1` 或 `public-live`），視圖集 MUST NOT 包含訂單確認頁或帶有使用者輸入值的頁面。

#### Scenario: 對 live 後台截圖
- **WHEN** 服務為 admin 上傳 `dataset = public-live` 的截圖
- **THEN** 回 422 `live_data_capture_refused`

### Requirement: 提示詞中的個資 MUST 在入庫時遮蔽

建立請求時，系統 MUST 把 prompt 中的 email、台灣手機號碼與通過 Luhn 檢查的 13 至 19 位數字取代為 `[redacted]`，在 `prompt_flags` 記錄偵測到的類別，並剔除控制字元與雙向覆寫字元（U+202A 至 U+202E、U+2066 至 U+2069）；`prompt_sha256` MUST 對遮蔽後文字計算。prompt 長度 MUST 不超過 4000 字。

#### Scenario: 遮蔽
- **WHEN** prompt 含 `wang@example.com` 與 `0912345678`
- **THEN** 儲存的文字中兩者皆為 `[redacted]`，`prompt_flags` 含 `email` 與 `phone`

#### Scenario: 惡意字元
- **WHEN** prompt 含 `<script>` 與 U+202E
- **THEN** U+202E 被剔除，`<script>` 原樣存為文字，API 回應 `Content-Type: application/json` 且帶 `X-Content-Type-Options: nosniff`
