# theme-system — 平台主題/頁型管理與 JSON Schema 定義

## ADDED Requirements

### Requirement: Theme schema validity
`themes.config_schema` 與 `theme_pages.page_schema` MUST 為合法的 JSON Schema（draft 2020-12）；主題建立或更新時系統 MUST 先以 metaschema 驗證，不合法者回 422。平台撰寫規範：物件層級 SHALL 設 `additionalProperties: false`、葉節點 SHALL 提供 `default`、以 `x-editor` 標註後台表單控件型別。

#### Scenario: 不合法 schema 被拒
- **WHEN** 平台管理員上傳 `type: "strng"` 之類無法通過 metaschema 的 config_schema
- **THEN** 回 422 並附錯誤位置

### Requirement: Section-based official theme structure
平台官方主題的 `page_schema` MUST 以 `sections` 陣列搭配區塊型別辨識欄位（`oneOf` + `type`）定義頁面主體結構，且 `config_schema` MUST 含 `tokens`（design tokens：色彩、字體、間距等）區段——使主題成為可由 AI 以純資料生成的形態，並作為 web 與原生 APP 共用的 SDUI 契約。

#### Scenario: starter 主題結構
- **WHEN** 檢視 seed 的 `starter` 主題
- **THEN** 各頁型 `page_schema` 的主體為 `sections` 陣列（每項含 `type` 辨識欄位），`config_schema` 含 `tokens` 區段

### Requirement: Theme page type registry
每個主題 SHALL 以 `theme_pages` 註冊其支援的頁型（`type_key` + `component_key` + `page_schema`）；同一主題內 `type_key` MUST 唯一（DB unique 約束）。

#### Scenario: 重複 type_key 被拒
- **WHEN** 對同一主題新增第二個 `type_key = 'home'` 的頁型
- **THEN** 寫入失敗，API 回 422

### Requirement: Theme activation gating
商家 MUST 僅能套用 `is_active = true` 的主題；平台下架主題（`is_active = false`）MUST NOT 影響既有已套用該主題的商家。

#### Scenario: 套用未開放主題被拒
- **WHEN** 商家嘗試將 `theme_id` 切換為 `is_active = false` 的主題
- **THEN** 回 422

#### Scenario: 下架不影響既有商家
- **WHEN** 平台將某主題設為 `is_active = false`，而 shop A 已套用該主題
- **THEN** shop A 前台渲染不受影響

### Requirement: Theme switching with compatibility precheck
商家切換主題時，系統 SHALL 回報新主題不支援的既有頁面清單（`type_key` 於新主題無對應者）；切換成功 MUST 觸發該商家整租戶快取失效（version bump）；不受支援頁面於前台 MUST 回 404、於後台 MUST 標示為不相容。

#### Scenario: 換版預檢
- **WHEN** 商家從主題 A（含 `landing_page` 頁型）切換到不含該頁型的主題 B
- **THEN** 回應列出所有 `type_key = 'landing_page'` 的頁面為不相容，切換後該些頁面前台 404

#### Scenario: 換版即時生效
- **WHEN** 商家完成主題切換
- **THEN** 租戶快取版本遞增，下一請求以新主題的 schema 與 component_key 渲染

### Requirement: Theme update propagation
平台更新主題（schema 或元件版本）時，系統 MUST 對所有 `shops.theme_id` 指向該主題的商家執行快取 version bump，使變更於下一請求生效。

#### Scenario: 主題升級後全商家失效
- **WHEN** 平台更新主題 `starter` 的 `config_schema`（新增含 `default` 的欄位）
- **THEN** 所有套用 `starter` 的商家快取版本遞增，後續渲染輸出含新欄位的預設值
