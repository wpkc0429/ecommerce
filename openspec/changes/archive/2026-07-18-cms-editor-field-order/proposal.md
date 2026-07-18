## Why

`themes.config_schema`、`theme_pages.page_schema` 存於 Postgres jsonb 欄位；jsonb 不保留物件鍵的原始撰寫順序。`openspec/project.md` 的 Important Constraints 已預先記錄這個已知限制：「jsonb 不保留物件鍵順序：schema 驅動表單的欄位順序非撰寫順序（如需固定順序需另訂 x-editor 排序約定）」。後果是 `admin/app.js` 的 `buildForm`/`buildField` 依 schema 的 `properties` 用 `Object.entries()` 迭代渲染表單欄位時，順序取決於 jsonb 內部順序（非決定性、與主題設計者撰寫順序無關），UX 差且不可預期。本次把這筆已記錄的技術債兌現：定義排序擴充關鍵字，讓表單欄位順序可由主題撰寫者控制。

## What Changes

- 新增 schema 擴充關鍵字 `x-editor-order`（沿用 `x-editor` 家族命名慣例）：掛在欄位自身 schema 節點上的整數，數字小的排前面；缺少標註的欄位排在所有已標註欄位之後，彼此依鍵名字母序排列（決定性 fallback，取代目前的 jsonb/map 隨機順序）。
- `api/internal/cms` 新增 `FieldOrder(props map[string]any) []string`：依上述規則排序一個 schema 節點的 `properties` 鍵。
- `ValidateSchemaDoc`（`schemaval.go`）新增遞迴檢查：`x-editor-order` 若存在但非合法整數（字串、浮點小數、物件等），視為 schema 撰寫錯誤，走既有 422 錯誤路徑（`ValidationError` + JSON Pointer），令 `CreateTheme`/`UpdateTheme`/`CreateThemePage`/`UpdateThemePage` 自動涵蓋此規則。
- `admin/app.js` 的表單渲染邏輯（`buildForm`、`buildField` 的 object 分支、`buildListField.addRow`、`buildSectionsField.addSection`）改依 `x-editor-order` 排序欄位；資料回收路徑（`_collect`）不受影響。
- `api/internal/seed/schemas/starter/config_schema.json`、`sections_defs.json` 補上 `x-editor-order` 作為示範/驗收案例。
- 不影響 `content_json`/`published_json` 的驗證與 hydration 輸出結構——這是 schema *本身*多一個描述性 metadata 關鍵字，`hydrate.go`、`ValidatePayload`、資料庫 schema/migration 皆不變動。

## Capabilities

### New Capabilities
（無）

### Modified Capabilities
- `theme-system`：MODIFIED「Theme schema validity」需求——該需求已定義「平台撰寫規範：...以 `x-editor` 標註後台表單控件型別」，`x-editor-order` 是同一撰寫規範家族的延伸（表單欄位排序標註），擴充該需求敘述並新增對應 Scenario，而非另開新 capability 或新 Requirement——避免讀者需要在多處拼湊「schema 撰寫規範」全貌。

## Impact

- 新增：`api/internal/cms/fieldorder.go`、`api/internal/cms/fieldorder_test.go`
- 修改：`api/internal/cms/schemaval.go`（掛驗證呼叫）、`admin/app.js`（表單渲染排序）、`api/internal/seed/schemas/starter/config_schema.json`、`api/internal/seed/schemas/starter/sections_defs.json`
- 不影響：`hydrate.go`、`service.go`、`httpapi/*`、ent schema / migration、`content_json`/`published_json` 資料格式
