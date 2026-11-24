## Why

`web/themes/starter/sections/RichText.tsx` 以 `dangerouslySetInnerHTML` 直接輸出 `rich_text.html`，API 端也只做 JSON Schema 驗證（`internal/cms/schemaval.go` 的 `ValidatePayload` 只檢查「是字串」），任何能存頁面的人都能把 `<script>`、`onerror=`、`javascript:` 連結寫進商家前台。元件註解寫的是「Phase 1 信任後台作者，作者範圍擴大時再加消毒」——**那個前提即將失效**：

- 即將開放 `staff` 角色與商家自行邀請成員（change `merchant-staff-role`），後台作者不再只有平台指派的少數人；
- 後台 API 即將對外開放給每商家自己的 admin 前端（change `admin-api-public-exposure`），一個被盜的 editor token 就能在前台植入腳本；
- 後續 phase 的靜態店面會在發佈時把內容**烘進 HTML**，屆時已經沒有一個「渲染端」可以補救——寫入端必須是權威；
- 後續 phase 的 AI agent 只改設計層，內容欄位仍由人透過自動產生的表單撰寫，這些表單的 rich text 欄位也必須走同一道消毒。

## What Changes

- **寫入端消毒（權威）**：`internal/cms` 新增 `richtext` 消毒器（Go，`github.com/microcosm-cc/bluemonday` 自訂 allowlist）。`CreatePage`／`UpdatePage`／`UpdateShopContent` 在 `ValidatePayload` 通過之後，依 **schema 中標註 `"x-editor": "richtext"` 的字串欄位**走訪 payload，把該欄位改寫成消毒後的 HTML 再存入 `content_json`。觸發條件是 schema 標註而不是欄位名 `html`，所以任何主題、任何未來由 content-field schema 驅動的欄位都自動受保護。
- **消毒是改寫不是拒絕**：不合規的標籤／屬性被移除，請求仍成功，回應帶回**實際儲存的值**，後台編輯器重新載入即看到被移除的結果。
- **allowlist 單一來源**：`api/internal/cms/richtext/allowlist.json`（標籤、各標籤允許屬性、允許的 URL scheme）同時被 Go（`go:embed`）與 web（`npm run gen:types` 時複製成 `web/lib/richtext-allowlist.json`）消費，不維護兩份手寫清單。
- **渲染端消毒（縱深防禦）**：`RichText.tsx` 改為先以 `sanitize-html`（純 JS、不需 DOM，可在 RSC/SSR 執行）依同一份 allowlist 消毒再輸出；移除「信任後台作者」的註解。
- **既有資料回填**：新增一次性指令 `api/cmd/sanitize-content`，對所有 `pages.content_json`／`pages.published_json`／`shops.content_json` 依其當前主題 schema 重新消毒並寫回（冪等、可 dry-run、只輸出 id 與變動與否，不輸出內容）。寫回 `published_json` 後發佈 `PagePublished` 等既有事件使渲染快取失效。
- 本 change **不新增任何 v1 端點**；回應形狀不變（欄位值可能變短）。

## Capabilities

### New Capabilities
（無）

### Modified Capabilities
- `page-management`：新增「rich text 欄位於寫入時依 allowlist 消毒」需求，涵蓋頁面草稿與商家全域內容。
- `content-rendering`：新增「前台渲染 rich text 前再次消毒」需求；渲染 bundle 輸出的 rich text MUST 是已消毒的值。

## Impact

- **程式碼**：`api/internal/cms/service.go`（三個寫入點）、新增 `api/internal/cms/richtext/`（消毒器、allowlist、走訪器）、`api/cmd/sanitize-content/`、`web/themes/starter/sections/RichText.tsx`、`web/scripts/gen-types.mjs`（複製 allowlist）、`web/package.json`（`sanitize-html` ＋ `@types/sanitize-html`）、`api/go.mod`（`bluemonday`）。
- **資料**：既有內容經回填後可能變短；無 schema migration。
- **API 契約**：無新端點，`openapi.yaml`／`v1-surface.txt` 不變（`ContentJSON` 敘述可補一句「rich text 欄位會被消毒」）。
- **相依**：無。可最先上線，且 SHOULD 在 `merchant-staff-role` 與 `admin-api-public-exposure` 之前上線。
