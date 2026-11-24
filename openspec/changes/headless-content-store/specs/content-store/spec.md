## ADDED Requirements

### Requirement: 內容文件以每店穩定的 key 定位

系統 SHALL 以 `content_documents` 儲存每家商家的內容文件，以 `(shop_id, key)` 唯一定位；`key` MUST 符合 `^[a-z0-9][a-z0-9-]{0,62}$`，`key` MUST NOT 帶有路由語意，且 `home` MUST NOT 是保留字。每份文件 MUST 有 `type_key`、草稿、可為空的已發佈快照、`status`（草稿或已發佈）與單調遞增的 `draft_rev`。每店文件數 MUST NOT 超過 100，單份草稿或快照 MUST NOT 超過 128 KiB，每店已發佈內容總量 MUST NOT 超過 2 MiB。

#### Scenario: 重複的 key
- **WHEN** 同一商家建立第二份 `key` 相同的文件
- **THEN** 以更新既有文件處理，不產生第二列

#### Scenario: 不合規則的 key
- **WHEN** 以 `Home` 或 `-x` 或 64 字元以上的字串建立文件
- **THEN** 回 422，pointer 指向 `key`

#### Scenario: 超過大小上限
- **WHEN** 草稿序列化後超過 128 KiB
- **THEN** 回 422 且不儲存

### Requirement: 每店 schema 由商家站台註冊且詞彙受限

系統 SHALL 允許具 `content.manage` 的使用者以 `PUT /api/v1/admin/shops/{shopID}/content/schema` 註冊該店的 `content.schema.json`。註冊 MUST 在寫入前驗證：主體不超過 128 KiB、JSON 無重複鍵且深度受限、結構上限（深度 8、節點 2,000、每個 object 50 個屬性、enum 100 個值）、只使用允許的詞彙（`$ref` 只能是本地 `#/$defs/…`、每個 object 的 `additionalProperties` 為 `false`、每個 string 宣告 `maxLength`、每個 array 宣告 `maxItems` 且不超過 200）、無 `$ref` 環且展開節點數不超過 5,000、可被 draft 2020-12 編譯、且 schema 接受自己的預設值。任何一項失敗 MUST 回 422 並附 JSON pointer，且 MUST NOT 寫入。

#### Scenario: 遠端參照被拒
- **WHEN** schema 含 `"$ref": "https://example.com/x.json"`
- **THEN** 回 422，不寫入

#### Scenario: 參照放大被拒
- **WHEN** schema 的 `$ref` 展開後節點數超過 5,000
- **THEN** 回 422，不寫入

#### Scenario: 缺少上限被拒
- **WHEN** 某個 string 欄位沒有 `maxLength`，或某個 array 沒有 `maxItems`
- **THEN** 回 422，pointer 指向該欄位

#### Scenario: dry run
- **WHEN** 以 `?dry_run=true` 註冊合法 schema
- **THEN** 回相容性報告且不寫入新版本

### Requirement: schema 版本化且既有內容永不遺失

每次內容不同的註冊 MUST 產生新的 schema 版本；內容與作用中版本相同（sha256 相同）的註冊 MUST NOT 增加版本。註冊 MUST NOT 改寫任何文件。若新 schema 會使既有文件的某些鍵被丟棄，註冊 MUST 在缺少 `confirm_removals=true` 時回 409 並附報告；註冊時對任何將失去資料或不再通過驗證的文件，系統 MUST 在同一交易內保存其草稿（與不同時的已發佈內容）為 `schema_snapshot` 版本。不再通過驗證的文件 MUST 標為 `needs_attention`，其草稿修正前 MUST NOT 能發佈，但其已發佈內容 MUST 繼續提供。

#### Scenario: 移除欄位的警告
- **WHEN** 新 schema 移除一個既有文件有值的欄位且未帶 `confirm_removals`
- **THEN** 回 409，報告列出文件 key 與欄位路徑，且不寫入

#### Scenario: 確認後註冊不遺失資料
- **WHEN** 帶 `confirm_removals=true` 註冊
- **THEN** 新版本生效，該文件有一筆 `schema_snapshot` 保存舊草稿，已發佈內容仍可讀取

#### Scenario: 重複註冊
- **WHEN** 註冊與作用中版本 sha256 相同的 schema
- **THEN** 回 `changed: false` 且版本不變

### Requirement: 寫入閘門驗證並消毒內容

每次儲存草稿 MUST 依該文件 type 的 schema 驗證（422 附 JSON pointer）、檢查 `x-editor: image` 欄位的網域規則、檢查 `x-editor: link` 欄位只含允許的 scheme（`https`、`http`、`mailto`、`tel`、站內相對路徑）並拒絕 `javascript:`、`data:` 與控制字元、並依 allowlist 改寫 `x-editor: richtext` 欄位；回應 MUST 帶實際存下的值。發佈 MUST 重新驗證草稿。

#### Scenario: 危險連結
- **WHEN** `link` 欄位值為 `javascript:alert(1)`
- **THEN** 回 422，pointer 指向該欄位

#### Scenario: richtext 被改寫
- **WHEN** richtext 欄位含 `<script>`
- **THEN** 儲存成功，回應中的值已移除 `<script>`

### Requirement: 儲存使用樂觀鎖

`PUT …/documents/{key}` MUST 要求 `If-Match: "<draft_rev>"`（首次建立用 `If-None-Match: *`）；`draft_rev` 不符 MUST 回 412 且不寫入；成功 MUST 使 `draft_rev` 加一。

#### Scenario: 並發儲存
- **WHEN** 兩個請求以同一個 `draft_rev` 同時儲存
- **THEN** 恰一個成功，另一個回 412

### Requirement: 有界版本歷史與還原

每次發佈 MUST 寫入一筆 `publish` 版本，系統 MUST 在同交易內只保留每份文件最近 20 筆 `publish` 版本。還原 MUST 只把某版本內容放回草稿，MUST NOT 自動發佈；還原需要 `content.edit`，發佈需要 `content.publish`。

#### Scenario: 超過 20 筆
- **WHEN** 同一文件發佈第 21 次
- **THEN** 最舊的 `publish` 版本被刪除，列表剩 20 筆

#### Scenario: 還原不發佈
- **WHEN** 還原某個版本
- **THEN** 草稿內容變為該版本，線上已發佈內容不變

### Requirement: 內容權限節點

系統 SHALL 新增權限節點 `content.view`、`content.edit`、`content.publish`、`content.manage`。`merchant_owner` MUST 全有；`editor` MUST 只有 `content.view` 與 `content.edit`；`staff` 與其他角色 MUST NOT 預設擁有任何 `content.*`。`page.*` 在相容期內 MUST 與對應的 `content.*` 等價。

#### Scenario: staff 沒有內容權限
- **WHEN** 以 staff 角色呼叫任一內容管理端點
- **THEN** 回 403

#### Scenario: editor 不能發佈
- **WHEN** 以 editor 角色發佈文件
- **THEN** 回 403

### Requirement: 內容操作留下稽核紀錄

每次 schema 註冊、dry run、儲存、發佈、下架、刪除、還原與 hook 設定／測試／觸發 MUST 寫入 `content_audit_log`（含 actor、動作、文件 key 與結果類別）；稽核與日誌 MUST NOT 含內容本身與 hook URL。

#### Scenario: 日誌不含內容
- **WHEN** 儲存含特徵字串的內容
- **THEN** 應用程式日誌與稽核 `detail` 中找不到該字串
