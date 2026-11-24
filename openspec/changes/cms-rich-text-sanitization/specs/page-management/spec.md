## ADDED Requirements

### Requirement: Rich text 欄位於寫入時依 allowlist 消毒

系統 MUST 在頁面草稿建立／更新與商家全域內容更新時，對 payload 中所有「schema 標註為 `"x-editor": "richtext"`」的字串欄位，依單一 allowlist 進行 HTML 消毒，並儲存**消毒後**的值。觸發條件 MUST 是 schema 標註，MUST NOT 依賴欄位名稱。消毒 MUST 是改寫（移除不允許的標籤、屬性與 URL），MUST NOT 因含不允許內容而拒絕請求；回應 MUST 回傳實際儲存的值。

allowlist MUST 允許表格元素（`table`、`thead`、`tbody`、`tfoot`、`tr`、`th`、`td`、`caption`）且 MUST NOT 允許 `h1`。allowlist MUST 排除所有事件處理屬性（`on*`）、`style`、`script`／`style`／`iframe` 元素（連同其內容），以及 `javascript:`／`data:`／`vbscript:` 等非允許 scheme 的 URL；`target="_blank"` 的連結 MUST 帶 `rel="noopener noreferrer"`。allowlist MUST 只有一個來源檔，API 與 web 渲染端共用。

#### Scenario: 事件屬性被移除
- **WHEN** 編輯者存入 rich text `<p>hi<img src="https://x.test/a.png" onerror="alert(1)"></p>`
- **THEN** 請求成功，儲存與回應的值不含 `onerror`，`<p>hi</p>` 與合法的 `img` 保留

#### Scenario: script 元素連同內容移除
- **WHEN** 編輯者存入 `<p>a</p><script>alert(1)</script>`
- **THEN** 儲存的值為 `<p>a</p>`，不含 `alert(1)` 文字

#### Scenario: javascript scheme 被移除（含大小寫與實體編碼變形）
- **WHEN** 編輯者存入 `<a href="JaVaScRiPt:alert(1)">x</a>` 或 `<a href="&#106;avascript:alert(1)">x</a>`
- **THEN** 儲存的值中該連結不帶 `href`

#### Scenario: 表格保留、h1 移除
- **WHEN** 編輯者存入 `<h1>大標</h1><table><tr><td colspan="2">x</td></tr></table>`
- **THEN** 儲存的值不含 `<h1>` 標籤（文字「大標」保留），表格與 `colspan` 保留

#### Scenario: 非 richtext 欄位不受影響
- **WHEN** 編輯者在 `x-editor: text` 的欄位存入字串 `<b>標題</b>`
- **THEN** 該字串原樣儲存（純文字欄位由渲染端以文字輸出，不是 HTML）

#### Scenario: 商家全域內容同樣受保護
- **WHEN** 主題 `config_schema` 中有 `x-editor: richtext` 欄位，商家更新全域內容時該欄位含 `<script>`
- **THEN** 儲存的 `shops.content_json` 該欄位不含 `<script>`

### Requirement: 既有內容可一次性回填消毒

系統 MUST 提供一個冪等的一次性指令，依各頁面與商家當前主題 schema 對既有 `content_json`、`published_json` 與 `shops.content_json` 重新消毒並寫回；預設 MUST 為 dry-run；輸出 MUST NOT 包含內容本身。寫回已發佈內容後 MUST 經由既有領域事件使渲染快取失效。

#### Scenario: 回填冪等
- **WHEN** 回填指令實跑一次後再執行第二次
- **THEN** 第二次回報零筆變動
