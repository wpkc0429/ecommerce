# page-management — 商家頁面 CRUD、Schema 校驗與草稿/發佈

## ADDED Requirements

### Requirement: Page creation bound to current theme
建立頁面時 `type_key` MUST 存在於商家當前主題的 `theme_pages`，否則回 422。`slug` MUST 符合 `^[a-z0-9-]+$`（儲存前小寫正規化）、於同一 shop 內唯一（DB unique 約束）、且 MUST NOT 使用保留字（`home` 以外的系統保留字如 `api`、`admin`、`preview`、`_next`；`home` 僅由系統建立）。

#### Scenario: 建立成功
- **WHEN** 於套用主題 `starter`（含 `landing_page` 頁型）的商家建立 `type_key = 'landing_page'`、`slug = 'summer-sale'` 的頁面
- **THEN** 頁面以 `status = 0`（草稿）建立

#### Scenario: 不支援的 type_key
- **WHEN** 建立 `type_key` 不在當前主題頁型清單中的頁面
- **THEN** 回 422

#### Scenario: 同店 slug 重複
- **WHEN** 於同一商家建立與既有頁面相同的 slug
- **THEN** 回 422（不同商家間相同 slug 允許）

#### Scenario: 保留字 slug 被拒
- **WHEN** 建立 `slug = 'api'` 的頁面
- **THEN** 回 422

### Requirement: Payload schema validation on save
儲存 `pages.content_json` 或 `shops.content_json` 時，系統 MUST 分別依當前主題的 `page_schema` / `config_schema` 驗證 payload；失敗 MUST 回 422，錯誤明細附 JSON Pointer 路徑。

#### Scenario: 型別錯誤附定位
- **WHEN** schema 定義 `banner.images` 為陣列而 payload 傳入字串
- **THEN** 回 422，`details` 含 `/banner/images` 的錯誤說明

#### Scenario: 合法 payload 儲存
- **WHEN** payload 完整符合 page_schema
- **THEN** 寫入 `content_json`（工作副本），`updated_at` 更新

### Requirement: Draft and publish workflow
頁面編輯 MUST 僅寫入 `content_json`（工作副本），不影響線上輸出；「發佈」動作 MUST 在校驗通過後將 `content_json` 複製到 `published_json`、設 `status = 1` 並使該頁快取失效；「下架」MUST 設 `status = 0` 並使該頁快取失效。前台渲染一律讀 `published_json`。

#### Scenario: 編輯不影響線上
- **WHEN** 已發佈頁面的 `content_json` 被修改但未發佈
- **THEN** 前台渲染仍輸出原 `published_json` 內容

#### Scenario: 發佈生效
- **WHEN** 執行發佈動作
- **THEN** `published_json` 更新為工作副本、該頁快取被清除，下一請求輸出新內容

#### Scenario: 下架
- **WHEN** 對已發佈頁面執行下架
- **THEN** `status = 0`，前台該 slug 回 404

### Requirement: Shop global content editing
商家全域內容（`shops.content_json`）的更新 MUST 依當前主題 `config_schema` 校驗（422 規則同上），成功後立即生效並觸發該商家整租戶快取失效（version bump）。

#### Scenario: 全域內容更新
- **WHEN** 商家更新 Logo 圖片網址且通過校驗
- **THEN** 租戶快取版本遞增，所有頁面下一請求的 `shop.content` 皆為新值

### Requirement: Auto-created home page
建立商家時系統 MUST 自動建立 `type_key = 'home'`、`slug = 'home'` 的頁面；前台路徑 `/`（剝離 path_prefix 後為空）MUST 解析為 `slug = 'home'`。

#### Scenario: 建店即有首頁
- **WHEN** 平台建立新商家並套用主題
- **THEN** 該商家存在 slug 為 `home` 的草稿首頁，發佈後 `/` 可渲染

### Requirement: SEO meta storage
`pages.meta` SHALL 儲存 `seo_title`、`seo_keywords`、`seo_description` 等 SEO 欄位，並 MUST 隨渲染 bundle 的 `page.seo` 輸出。

#### Scenario: SEO 輸出
- **WHEN** 頁面 meta 含 `seo_title` 並已發佈
- **THEN** 渲染 bundle 的 `page.seo.seo_title` 為該值
