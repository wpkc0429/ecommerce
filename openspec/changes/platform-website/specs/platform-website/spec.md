## ADDED Requirements

### Requirement: 官網為靜態輸出且不渲染任何商家

平台官網 SHALL 由 `web/` 建置為純靜態輸出，執行期 MUST NOT 依賴 Node 伺服器、Host 標頭或渲染 bundle API。官網 MUST NOT 呼叫 `/api/v1/render/*`。

#### Scenario: 建置產物
- **WHEN** 執行 `npm run build`
- **THEN** 產生靜態輸出目錄，內含各頁 HTML、`sitemap.xml`、`robots.txt` 與 `_headers`

### Requirement: 頁面清單

官網 SHALL 提供：`/`（介紹）、`/how-it-works`、`/demo`（merchant template 的 live demo）、`/pricing`（佔位）、`/terms`（佔位）、`/privacy`（佔位）、`/apply`、`/apply/verify`、`/apply/received`。`/terms`、`/privacy`、`/pricing` 的佔位頁 MUST 明示內容尚未生效，不得呈現為正式條款。

#### Scenario: 佔位標示
- **WHEN** 開啟 `/privacy`
- **THEN** 頁面明示為佔位內容並列出將蒐集的資料項目

### Requirement: SEO 基礎

每頁 MUST 有獨立的 `<title>`、description、canonical 與 Open Graph 標籤，`<html lang="zh-Hant">`；站台 MUST 提供 `sitemap.xml` 與 `robots.txt`；`/apply/verify` 與 `/apply/received` MUST 為 `noindex`，且 `/apply/verify` 的 Referrer-Policy MUST 為 `no-referrer`。

#### Scenario: 驗證頁不被索引
- **WHEN** 檢視 `/apply/verify` 的輸出
- **THEN** 含 `noindex` 且不出現在 `sitemap.xml`

### Requirement: 安全標頭

官網的託管設定 MUST 送出 CSP（`script-src` 只允許自身與 Turnstile 網域，不含 `'unsafe-inline'`）、`Strict-Transport-Security`、`X-Content-Type-Options: nosniff`、`Referrer-Policy`、`Permissions-Policy`，且 `frame-ancestors 'none'`。

#### Scenario: CSP 不含 unsafe-inline script
- **WHEN** 檢視 `_headers` 的 CSP
- **THEN** `script-src` 不含 `'unsafe-inline'`

### Requirement: 申請表單的行為

`/apply` 表單 MUST 顯示 Turnstile 小工具、對 422／429／503 呈現可理解的訊息、送出成功導向 `/apply/received`；成功頁 MUST 說明「若未收到驗證信會由人工聯絡」，且 MUST NOT 透露該 email 先前是否申請過。

#### Scenario: 429
- **WHEN** API 回 429
- **THEN** 表單顯示稍後再試並依 `Retry-After` 提示，不清空已填欄位
