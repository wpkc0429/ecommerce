## MODIFIED Requirements

### Requirement: Theme schema validity
`themes.config_schema` 與 `theme_pages.page_schema` MUST 為合法的 JSON Schema（draft 2020-12）；主題建立或更新時系統 MUST 先以 metaschema 驗證，不合法者回 422。平台撰寫規範：物件層級 SHALL 設 `additionalProperties: false`、葉節點 SHALL 提供 `default`、以 `x-editor` 標註後台表單控件型別、以 `x-editor-order`（整數，數字小者排前面）標註後台表單欄位的呈現順序。`x-editor-order` MAY 省略；省略該標註的欄位 SHALL 排在所有已標註欄位之後，並以欄位鍵名字母序排列。`x-editor-order` 若存在但值非合法整數，視同不合法 schema，主題建立或更新時 MUST 回 422。

#### Scenario: 不合法 schema 被拒
- **WHEN** 平台管理員上傳 `type: "strng"` 之類無法通過 metaschema 的 config_schema
- **THEN** 回 422 並附錯誤位置

#### Scenario: 不合法欄位排序標註被拒
- **WHEN** 平台管理員上傳的 schema 中某欄位的 `x-editor-order` 為字串（如 `"1"`）而非整數
- **THEN** 回 422，錯誤詳情附該欄位 `x-editor-order` 的 JSON Pointer 位置

#### Scenario: 後台表單依標註順序渲染
- **WHEN** 主題 schema 對欄位 A、B、C 分別標註 `x-editor-order` 為 2、0、1
- **THEN** 後台編輯表單渲染順序為 B、C、A

#### Scenario: 未標註欄位退回字母序排在最後
- **WHEN** 主題 schema 中欄位 A 標註 `x-editor-order` 為 0，欄位 D、B 皆未標註
- **THEN** 後台表單渲染順序為 A、B、D（未標註欄位排在已標註欄位之後，彼此依鍵名字母序排列）
