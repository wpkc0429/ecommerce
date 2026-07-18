## 1. `internal/cms`：`FieldOrder` helper + `x-editor-order` 校驗（design D1/D2/D3/D4）

- [x] 1.1 新增 `api/internal/cms/fieldorder.go`：`FieldOrder(props map[string]any) []string`——依 `x-editor-order`（`float64`，整數值）升冪排序已標註欄位（同值以鍵名字母序打破平手），未標註欄位排在最後、彼此依鍵名字母序排列。
- [x] 1.2 同檔新增未匯出的遞迴校驗函式（如 `validateEditorOrder(raw json.RawMessage) error`）：走訪整份已解析 JSON 文件（任意巢狀層級），遇到 map 含 `"x-editor-order"` 鍵時檢查值是否為合法整數（`float64` 且等於 `math.Trunc(f)`）；不合法則累積 `Detail{Pointer, Message}`（Pointer 指向該欄位路徑 + `/x-editor-order`），全部走完後若有錯誤回傳 `*ValidationError`。
- [x] 1.3 `api/internal/cms/schemaval.go` 的 `ValidateSchemaDoc` 在既有 `CompileSchema` 檢查通過後，追加呼叫 1.2 的校驗函式，讓 `CreateTheme`/`UpdateTheme`/`CreateThemePage`/`UpdateThemePage` 既有的 422 路徑自動涵蓋新規則。
- [x] 1.4 確認 `hydrate.go`、`ValidatePayload`、`service.go`、`httpapi/*`、ent schema/migration 皆未變動（design Non-Goals）。

## 2. Go 測試（design D1/D2/D3；spec theme-system「Theme schema validity」新增 Scenario）

- [x] 2.1 新增 `api/internal/cms/fieldorder_test.go`：`TestFieldOrderSortsByAnnotation`——欄位 A/B/C 分別標註 `x-editor-order` 為 2、0、1，`FieldOrder` 輸出須為 `["B","C","A"]`。
- [x] 2.2 同檔新增 `TestFieldOrderFallbackAlphabetical`——欄位 A 標註 `x-editor-order: 0`，欄位 D、B 皆未標註，輸出須為 `["A","B","D"]`（未標註者排最後、彼此字母序）。
- [x] 2.3 同檔視需要補充邊界案例（如同值 tie-break、全部未標註時整體退化為字母序）。
- [x] 2.4 `api/internal/cms/schemaval_test.go` 新增 `TestValidateSchemaDocRejectsInvalidEditorOrder`——`x-editor-order` 為字串（如 `"1"`）的 schema 呼叫 `ValidateSchemaDoc` 須回傳 `*ValidationError`，且 `Details` 含指向該欄位 `/x-editor-order` 的 Pointer。
- [x] 2.5 同檔新增巢狀案例——`x-editor-order` 校驗需能在 `$defs` 內的 section block 定義、巢狀 object 的 `properties` 中被找到並正確驗證（不只頂層 `properties`）。

## 3. `admin/app.js`：表單渲染依標註排序（design D4）

- [x] 3.1 新增小工具函式（比照既有 `resolveRef`/`sectionBranches` 風格），輸入一個 `properties` 物件（key → schema node，可能含 `$ref`），回傳依 `x-editor-order` 排序後的鍵陣列（規則與 Go 端 `FieldOrder` 一致：已標註者升冪排前、同值字母序 tie-break；未標註者排最後、字母序）；需對每個 key 的 schema node 先 `resolveRef` 才能讀到 `x-editor-order`（欄位定義可能是 `$ref`）。
- [x] 3.2 `buildForm` 改用該工具函式決定 `Object.entries(props)` 的迭代順序。
- [x] 3.3 `buildField` 的 object 分支（巢狀 object 欄位）改用該工具函式。
- [x] 3.4 `buildListField.addRow`（一般陣列的物件欄位，如 nav/footer links 的 `label`/`href`）改用該工具函式。
- [x] 3.5 `buildSectionsField.addSection`（section 區塊的欄位，`type` 判別欄位除外）改用該工具函式。
- [x] 3.6 確認 `_collect`（`buildListField`/`buildSectionsField` 的資料回收路徑）未被修改——順序不影響其語意。

## 4. seed 主題 schema 示範（design D1；驗收案例）

- [x] 4.1 `api/internal/seed/schemas/starter/config_schema.json`：`tokens`、`header`、`footer` 底下各欄位（含巢狀 `nav`/`links` 陣列的 item 欄位）補上 `x-editor-order`，反映合理的編輯順序。
- [x] 4.2 `api/internal/seed/schemas/starter/sections_defs.json`：`hero`、`rich_text`、`feature_grid`、`cta` 各 block 定義的欄位（`type` 判別欄位除外，不需要標註排序）補上 `x-editor-order`。
- [x] 4.3 `make seed`（或 `cd api && go run ./cmd/seed`，視本機是否有 docker infra 而定）可重跑不出錯（idempotent，既有 `seedStarterTheme` 邏輯保證）。

## 5. 驗證與收尾

- [x] 5.1 `cd api && go build ./...` 通過
- [x] 5.2 `cd api && golangci-lint run` 0 issues
- [x] 5.3 `cd api && go test ./...` 綠燈（含新增的 `fieldorder_test.go`/`schemaval_test.go` 案例）
- [x] 5.4 若本機有 docker infra：`INTEGRATION=1 go test -p 1 -count=1 ./...` 綠燈，確認既有 `cms_integration_test.go` 等未受影響
- [x] 5.5 人工驗證：透過 `make seed`/`make seed-demo` 灌好的實際 demo 商家（shop id=1），啟動 API 伺服器並以 `demo-owner@example.com` 登入，`curl GET /admin/shops/1/content` 取得**真實** jsonb 儲存後的 `config_schema`（觀察到頂層鍵順序為 `footer, header, tokens`——證實 jsonb 重排問題確實存在於這條路徑），將回應內容餵給從 `admin/app.js` 逐字複製出的 `orderedKeys`/`resolveRef` 函式執行，確認排序後的欄位順序（頂層 `tokens, header, footer`；`tokens` 內 `color_primary, color_background, color_text, font_family, spacing_unit, radius`；`header` 內 `site_title, logo_url, nav`；`nav.items` 內 `label, href`；`footer` 內 `text, links`）完全符合 4.1 標註的撰寫順序，證明修正在真實 API 回應（含 jsonb 重排）下端到端生效，而不只是單元測試層級成立：起 admin（`cd admin && npm run dev` 或直接開 `index.html`）+ API，登入後開啟套用 starter 主題商家的頁面編輯器/商家內容編輯頁，肉眼確認表單欄位順序符合 4.1/4.2 標註的順序（design D4 承認的兩邊手寫實作一致性靠此步驟把關）
