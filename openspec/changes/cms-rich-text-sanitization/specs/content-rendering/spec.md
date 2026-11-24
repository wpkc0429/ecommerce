## ADDED Requirements

### Requirement: 前台渲染 rich text 前再次消毒

storefront 渲染 rich text 區塊時 MUST 先以與 API 寫入端相同的 allowlist 消毒，再輸出為 HTML；MUST NOT 將未經消毒的字串交給 `dangerouslySetInnerHTML`。渲染 bundle API 回傳的 rich text 值 MUST 是寫入端消毒後的值（即不得在 bundle 中出現 allowlist 之外的標籤或屬性）。

#### Scenario: 渲染端阻擋繞過寫入端的內容
- **WHEN** 資料庫中的 `published_json` 因任何原因（例如回填前的舊資料）含 `<img src=x onerror=alert(1)>`
- **THEN** 前台輸出的 HTML 不含 `onerror` 屬性

#### Scenario: 合法格式保留
- **WHEN** rich text 為 `<h2>標題</h2><p><strong>粗體</strong> 與 <a href="/about">連結</a></p>`
- **THEN** 前台輸出保留這些標籤與 `href`
