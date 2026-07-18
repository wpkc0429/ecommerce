## Context

`themes.config_schema`、`theme_pages.page_schema` 是 ent `field.JSON`，Postgres 端存為 `jsonb`。Postgres 的 `jsonb` 儲存格式**不保留物件鍵的原始撰寫順序**（內部依鍵長度＋字典序排列），但**保留陣列元素順序**。這個限制已經被記錄在 `openspec/project.md` Important Constraints：「jsonb 不保留物件鍵順序：schema 驅動表單的欄位順序非撰寫順序（如需固定順序需另訂 x-editor 排序約定）」——本次就是兌現這句話承諾的「另訂約定」。

`api/internal/httpapi/pages.go`、`themes.go` 把 ent 讀出的 `json.RawMessage`（schema 欄位）原封不動塞進 HTTP 回應（不重新反序列化成 Go map 再 marshal），所以 schema 文件一旦寫進 DB，admin 收到的 bytes 就是 Postgres 決定的鍵順序，而非主題設計者在來源檔案裡撰寫的順序。`admin/app.js` 的 `buildForm`/`buildField` 用 `Object.entries(schema.properties)` 迭代，於是表單欄位順序繼承了這個不可預期的順序。

讀者對象：後端/前端工程師（`admin/app.js` 無建置步驟、需與 Go 端各自實作但邏輯一致）。本 change 編號獨立（D1、D2...），不與 phase1 design.md 衝突。

## Goals / Non-Goals

**Goals:**

- 定義一個 schema 擴充關鍵字，讓主題撰寫者能明確指定後台表單欄位的呈現順序，且此順序不受 jsonb 儲存重排影響。
- 提供決定性（非隨機）的 fallback，讓「未標註順序的欄位」也有可預期的排列方式。
- 主題建立/更新時就近校驗這個新關鍵字的合法性（沿用既有 422 metaschema 驗證路徑），而不是留到 admin 渲染階段才默默出錯。
- 幫 starter 主題補上示範標註，作為往後其他主題作者的參考範例。

**Non-Goals:**

- 不改變 `content_json`/`published_json` 的資料格式、驗證邏輯或 hydration 輸出結構——這條路徑處理的是頁面/商家「內容」payload，跟「schema 如何描述表單呈現」是兩件事。
- 不新增 API 或 DB 欄位——`x-editor-order` 是 schema 文件內的 metadata，既有的 schema 傳輸路徑（`json.RawMessage` 逐位元組透傳）已經足夠讓它抵達 admin。
- 不處理 `admin/app.js` 以外的其他 UX 問題、不重構既有表單渲染以外的程式碼。
- 不建立前端測試框架（`admin/` 目前無 build/test 工具鏈，僅 `python3 -m http.server` 開發伺服器）。

## Decisions

### D1. 排序關鍵字：逐欄位整數 `x-editor-order`（非物件層級陣列）

**選擇**：在每個欄位自身的 schema 節點上標註 `"x-editor-order": <整數>`，數字小的排前面，例如：

```json
{
  "properties": {
    "title":    { "type": "string", "x-editor-order": 0, "default": "" },
    "subtitle": { "type": "string", "x-editor-order": 1, "default": "" }
  }
}
```

**放棄的替代方案：物件層級陣列**，例如 `"x-editor-order": ["title", "subtitle", "image"]` 掛在物件節點（而非欄位節點）上。誠實比較（兩種方案技術上都可行，jsonb 只重排物件鍵、不重排陣列元素，所以陣列方案的元素順序同樣不受 jsonb 影響）：

| | 逐欄位整數（選用） | 物件層級陣列（放棄） |
|---|---|---|
| 技術可行性 | 可行 | **同樣可行**——陣列元素順序不受 jsonb 重排影響 |
| 局部編輯成本 | 新增/刪除單一欄位只需改動該欄位自身的標註；插入到中間不需要 renumber（可用 `0.5`、或允許不連續整數如 `10`/`20`/`30` 預留間隙） | 插入到中間常需要整批重寫陣列（把後續欄位名稱往後挪），或忍受「陣列順序其實是後補的、跟欄位定義位置不同步」的心智負擔 |
| 與現有慣例一致性 | 與 `x-editor`（型別標註）同屬「掛在欄位節點上的 metadata」，撰寫者在同一個地方看到某欄位的所有標註 | 排序資訊獨立於欄位定義之外，撰寫/閱讀 schema 時要在兩處對照 |
| 巢狀場景（`$defs` 裡的 section block、object 裡的 object） | 每層各自標註，語意單純（「這個 object 底下的欄位怎麼排」局部決定） | 每一層 object 都要各自維護一份陣列，一樣要分層維護，優勢不明顯 |
| 遺漏欄位的處理 | 個別欄位可以先不標註，其餘欄位不受影響 | 陣列若漏列某欄位，容易被誤讀為「刻意排最後」而非「忘記加」，語意較模糊 |

結論：兩種方案都能解決 jsonb 順序問題，但逐欄位整數在「schema 隨時間局部演進」（本專案的常態——主題會持續新增欄位、design D6/content-rendering 的 hydration 本就是為了吸收這種演進）情境下維護成本更低，且與 `x-editor` 命名/放置慣例一致，故採用。使用者（KS）本身也傾向此方案。

**整數間隙建議**（撰寫慣例、非強制規則）：新欄位可用 10 的倍數（0、10、20...）預留插入空間，避免頻繁 renumber；本次 seed 示範會採用連續整數（0、1、2...）以求簡單，兩種寫法程式邏輯都支援（只看相對大小，不要求連續或特定間隔）。

### D2. Fallback：未標註欄位排在最後、彼此字母序

未標註 `x-editor-order` 的欄位：

1. 排在所有已標註欄位之後。
2. 彼此之間依欄位鍵名（原始字串，非顯示用 `title`）字母序排列。

**理由**：舊 schema（尚未補標註）或撰寫者忘記標註新欄位時，不應該直接壞掉或退回原本「jsonb/Go map 隨機順序」的問題行為——字母序雖然不是撰寫者的本意順序，但至少是**決定性**的（同一份 schema 每次渲染順序一致），比現狀（不可預期）更好，且不需要額外標註就能運作，符合漸進式採用（可以只標註最重要的前幾個欄位，其餘保持字母序）。

**放棄的替代方案**：未標註欄位維持「目前的行為」（即繼續依 jsonb/Go map 順序）——放棄理由：這正是本次要修正的問題本身，讓 fallback 繼續不確定性等於沒有解決任何東西，且無法寫出決定性的測試斷言。

### D3. 校驗：併入既有 `ValidateSchemaDoc`，遞迴走訪整份文件

`x-editor-order` 可能出現在任意巢狀層級（頂層 `properties`、巢狀 object 的 `properties`、`$defs` 裡的 section block 定義、`items.properties` 等）。與其假設固定路徑，新函式改為**遞迴走訪整份已解析 JSON 文件**（`any` 型別的 map/slice 樹），只要遇到 map 中存在 `"x-editor-order"` 鍵，就檢查其值是否為「數學上的整數」（`float64` 且等於自己的 `math.Trunc`）；不合法則累積一筆 `Detail{Pointer, Message}`，JSON Pointer 指向該欄位路徑下的 `/x-editor-order`。

掛載點：`ValidateSchemaDoc`（`schemaval.go`）在既有 `CompileSchema` metaschema 檢查通過後，追加這個新檢查。這條函式已經是 `CreateTheme`/`UpdateTheme`/`CreateThemePage`/`UpdateThemePage` 四個既有呼叫點共用的入口，新規則自動套用到全部四處，不需要另外改 `service.go`。

**放棄的替代方案**：把 `x-editor-order` 的型別規則寫進 JSON Schema 本身的 metaschema（例如用 `$vocabulary` 擴充或另訂一份 meta-schema 檔）——過度工程；`santhosh-tekuri/jsonschema` 對未知關鍵字本就寬容（不會因為多了 `x-editor-order` 而編譯失敗），維持這個寬容性、只在應用層加一層輕量語意檢查，改動面最小、也最貼近 `x-editor` 目前的「不驗證值、只是慣例」處理方式再加一點點必要的健檢（避免非整數值悄悄流到前端造成 JS 排序出錯或 `NaN` 比較的難以除錯行為）。

### D4. `FieldOrder` helper：Go/JS 各自實作、邏輯手動保持一致

`api/internal/cms/fieldorder.go` 新增 `FieldOrder(props map[string]any) []string`，輸入某個 schema 節點的 `properties` map，輸出排序後的鍵名切片（規則同 D1/D2）。用途：

- 供 Go 測試直接驗證排序規則正確性（不需要透過 HTTP/DB 往返）。
- 作為這個排序規則的「規格即程式碼」單一真相來源，未來若新增後端消費者（例如 D6 提到的 `json-schema-to-typescript` 型別產生流程要保留欄位順序）可以直接重用。

`admin/app.js` 因為是無建置步驟的 vanilla JS（`design D1`：無法與 Go 共用程式碼、也不引入打包工具鏈），另外實作一個對等的小函式（比照檔案裡現有 `resolveRef`/`sectionBranches` 的寫法風格），套用到 4 個既有用 `Object.entries(...properties)` 迭代欄位的渲染路徑：`buildForm`、`buildField` 的 object 分支、`buildListField.addRow`、`buildSectionsField.addSection`。兩邊排序規則需保持一致（Go 測試 + 本次人工開 admin 頁面比對驗證），`_collect`/資料回收路徑不動（物件屬性的寫入順序不影響 JSON 語意，改了只增風險不增價值）。

**放棄的替代方案**：後端新增一個「已排序」API 欄位（例如 `page_schema` 之外再回傳 `field_order: [...]`）——放棄理由：schema 本身已經內嵌了排序資訊（`x-editor-order`），沒有額外欄位運算/傳輸的必要；`FieldOrder` 停留在 Go 端當作測試與未來重用的工具函式即可，不需要在 HTTP 層曝露。

### D5. Capability 歸屬：MODIFIED `theme-system` / 「Theme schema validity」

延伸既有 Requirement 而非新開一條——`x-editor` 系列標註（控件型別、現在加上排序）本質上都是「平台撰寫規範」的一部分，讀者查 `theme-system` spec 應該能在同一條 Requirement 底下看到 schema 撰寫規範的完整圖像，不需要在多個 Requirement 或 capability 之間拼湊。

## Risks / Trade-offs

- [Go 端 `FieldOrder` 與 JS 端排序邏輯分屬兩份手寫實作，未來若其中一邊修改排序規則忘了同步另一邊，會出現「後端驗證通過但前端顯示順序跟預期不同」的漂移] → 影響面小（純 UX 排序，不影響資料正確性/校驗結果），且 admin 是刻意選擇的無建置架構（`design D1`），這是既有的架構取捨延伸，非本次新增風險；`FieldOrder` 的 Go 測試把規則明文化，未來改動時容易對照。
- [整數間隙慣例（如跳號預留插入空間）不是強制規則，撰寫者仍可能寫出連續整數導致插入新欄位要 renumber] → 兩種寫法邏輯上都合法（只比較相對大小），只是撰寫時的權衡，留給主題撰寫者自行選擇，不在校驗層強制。
- [遞迴走訪整份 schema 文件檢查 `x-editor-order` 型別，理論上對超大/病態 schema 有效能疑慮] → 現有 `hydrate.go` 已有 `maxHydrateDepth` 這類防禦性設計案例，但 schema 文件本身是主題撰寫者手動撰寫、僅在建立/更新主題時執行一次（非熱路徑），量級遠小於渲染路徑，不需要額外深度限制。

## Migration Plan

無 DB migration、無 API 契約變更。部署順序：

1. 部署新程式碼（`ValidateSchemaDoc` 新增檢查、`FieldOrder` 新函式、`admin/app.js` 排序邏輯）——完全向後相容：既有 schema 沒有 `x-editor-order` 標註時，走 D2 的 fallback（字母序排最後），不會因為升級而報錯或崩潰。
2. 更新 `api/internal/seed/schemas/starter/*.json` 補上標註後，重跑 `make seed`（idempotent，既有 `seedStarterTheme` 邏輯本就是「查無則建立、查有則更新」，可安全重跑）讓 starter 主題的 DB 資料帶上新標註。
3. 既有已上線、尚未補標註的自訂主題不受影響（fallback 生效），主題撰寫者可自行決定何時、以何種優先順序補上標註。

回滾：純程式碼回滾即可（無 schema/migration 變更可回滾）；即使 DB 裡已經有主題 schema 含 `x-editor-order` 標註，舊版程式碼會把它當成未知關鍵字忽略（`x-editor` 系列關鍵字本就不影響 payload 校驗），不會造成相容性問題。

## Open Questions

- 是否要提供一個 CLI/lint 工具掃描既有主題 schema、列出尚未標註 `x-editor-order` 的欄位，協助主題撰寫者批次補標註——本次範圍只交付機制本身與 starter 示範，工具化留待有多個第三方主題撰寫者的實際需求出現後再評估。
