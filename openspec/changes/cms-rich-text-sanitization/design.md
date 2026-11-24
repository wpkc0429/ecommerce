## Context

- `rich_text` 區塊的 schema 在 `api/internal/seed/schemas/starter/sections_defs.json`：`"html": { "type": "string", "x-editor": "richtext" }`。`x-editor` 已經是主題 schema 的正式詞彙（`text`／`image`／`list`／`richtext`），被 `admin/lib/schemaform.js` 用來選控件。
- 寫入路徑三條：`cms.Service.CreatePage`、`UpdatePage`（存 `content_json`）、`UpdateShopContent`（存 `shops.content_json`，驗 `config_schema`）。`PublishPage` 是把 `content_json` 複製成 `published_json`，再驗一次 schema，不是新的使用者輸入。
- 渲染：`/api/v1/render/page` 回傳 hydrate 過的 `published_json`；web 的 `RichText.tsx` 直接 `dangerouslySetInnerHTML`。渲染 bundle 是用戶端無關的 SDUI 契約（native app 也消費），所以只在 web 端消毒保護不到其他消費端。

## Goals / Non-Goals

**Goals:**
- 任何寫進資料庫的 rich text 都已經過 allowlist 消毒；渲染 bundle 的所有消費者拿到的都是安全的 HTML。
- 觸發條件由 schema 宣告（`x-editor: richtext`），新主題／未來 content-field schema 不需要改 Go 程式就受保護。
- web 渲染端有第二道同規則的消毒。

**Non-Goals:**
- 富文本編輯器（WYSIWYG）本身——後台仍是 textarea。
- 其他 `x-editor` 型別的驗證（`image` 欄位的網域限制屬於 change `product-images-r2`）。
- CSP header（屬於 web 部署設定，記為後續；本 change 不依賴它）。

## Decisions

### D1：寫入端是權威，渲染端是縱深防禦
寫入端消毒的理由是**消費端不只一個**：web SSR、未來 native app、後續 phase 在發佈時烘成靜態 HTML 的店面——後者發佈之後就沒有渲染端可以補救。只做渲染端＝每個新消費端都必須記得消毒；只做寫入端＝allowlist 有洞時沒有第二道。兩者用**同一份 allowlist**，所以縱深防禦不會產生「寫入端允許、渲染端刪掉」的不一致。

### D2：改寫而不是 422 拒絕
被拒絕的使用者無法知道「哪一個屬性」不行（HTML 錯誤定位到字元位置對 textarea 使用者沒有意義），而貼上自 Word／Google Docs 的內容幾乎必然帶 `style`／`class`／`span`。改寫讓儲存永遠成功；回應帶回實際儲存值。（實作時發現後台編輯器原本**不會**在儲存後重新載入，因此本 change 一併讓頁面編輯器以 PUT 回應、全域內容表單以重新載入重建表單，畫面顯示實際存下的值。）代價：作者可能沒注意到內容被改，記為 Risk。

### D3：觸發條件是 schema 的 `x-editor: richtext`，不是欄位名
走訪器（`cms.SanitizeRichText`，內嵌 `hydrator`）同時持有 schema 與 payload，遇到 `oneOf`（sections 陣列）時依 `type` const 選定分支、遇到 `$ref` 解析 `$defs`——與 `cms/hydrate.go` 已有的走訪邏輯同構，盡量重用其解析輔助函式。以欄位名 `html` 判斷會漏掉任何不叫 `html` 的 rich text 欄位，也會誤傷剛好叫 `html` 的純字串欄位。

### D4：Allowlist（初版）
- 標籤：`p br strong b em i u s blockquote ul ol li h2 h3 h4 a img hr`，以及表格 `table thead tbody tfoot tr th td caption`。**不含 `h1`**：頁面標題由主題負責，一頁只該有一個 `h1`（已定案）。
- 屬性：`a[href title target rel]`、`img[src alt width height]`、`th[colspan rowspan scope]`、`td[colspan rowspan]`；**不允許 `style`、`class`、`id`、任何 `on*`**。
- URL：`href` 允許 `http`／`https`／`mailto`／`tel` 與相對路徑；`img[src]` 只允許 `https`（`product-images-r2` 上線後收緊為只允許影像網域，見該 change）。
- `target="_blank"` 一律補 `rel="noopener noreferrer"`。
- 移除 `script`／`style`／`iframe` 時**連內容一起移除**（不是只剝標籤留下腳本文字）。

### D5：allowlist 單一來源為 JSON
`api/internal/cms/richtext/allowlist.json` 由 Go `go:embed` 讀取並建出 bluemonday policy；`web/scripts/gen-types.mjs`（已是「由 api 的 schema 檔產生 web 檔案」的既有機制）同時把它複製到 `web/lib/richtext-allowlist.json`，`RichText.tsx` 以它組 `sanitize-html` 選項。新增一條 Go 單元測試：以同一組惡意向量（`<img onerror>`、`<a href="javascript:">`、`<svg><script>`、大小寫混用 `JaVaScRiPt:`、HTML entity 編碼的 scheme）斷言輸出；web 端無測試框架，以 `npm run typecheck` ＋ e2e 一條斷言守住。

### D6：bluemonday 而不是 `x/net/html` 手寫
手寫 tokenizer 消毒器是經典的錯誤來源（實體解碼、命名空間、`<noscript>` 解析差異）。bluemonday 是 Go 生態中維護中的 allowlist 消毒器，以 `x/net/html` 解析後重新序列化，輸出必為良構 HTML。web 端選 `sanitize-html` 而不是 `isomorphic-dompurify`：後者在 SSR 需要 jsdom（重、冷啟動慢），前者純 JS。

### D7：既有資料以一次性指令回填，不以 migration
消毒需要主題 schema（決定哪些欄位是 richtext）與 Go 程式碼，不適合寫成 SQL migration。`cmd/sanitize-content` 冪等（第二次執行零變動）、預設 `-dry-run`，只印出 `page_id`／`shop_id` 與「changed/unchanged」，**不印內容**（內容可能含商家資料）。回填 `published_json` 後要讓渲染快取失效：重用 `internal/events` 的既有事件，不直呼 Redis（專案規則）。

## Risks / Trade-offs

- [作者沒注意到內容被改] → 後台儲存後以存下的值重建表單；admin 的 rich text 欄位旁加一行說明「不支援的標籤與樣式會被移除」。
- [allowlist 太窄，商家需要 iframe 影片] → allowlist 是 JSON，擴充是一個 review 得到的 diff；`iframe` 若要開放必須另限 `src` 網域，不在本 change。
- [回填改動已發佈內容] → 只會**移除**不安全的部分；先以 dry-run 在正式資料上看變動列數。
- [Go 與 JS 兩個消毒器對同一 allowlist 的解讀細節不同] → 寫入端已消毒，渲染端再消毒通常是 no-op；差異只可能讓渲染端多刪，不會多放。

## Migration Plan

1. 部署 API（新寫入立即受保護）→ 2. `cmd/sanitize-content -dry-run` 檢視 → 3. 實跑 → 4. 部署 web。回滾：API 回滾後新寫入不再消毒，已消毒資料不需還原。

## Decided（原 Open Questions，已由使用者定案）

- allowlist **支援表格**（`table/thead/tbody/tfoot/tr/th/td/caption`），**不支援 `h1`**。
- `img[src]` 在影像網域上線前允許任意 `https`；`product-images-r2` 上線後收緊為只允許影像網域（該 change 的任務 5.2）。
