## Why

ADR `docs/decisions/0001-headless-platform-and-merchant-template.md` 的「修正」記錄了一個產品事實：商家會用後台**表單**（不是透過 AI）修改小東西——文字、banner、活動文案、促銷圖片、店家資訊、FAQ。這類內容必須存在**商家站台程式碼之外**，否則每次文案變更都是一次程式碼變更，並與 AI 對設計層的修改衝突。正確的家是 ecommerce 的 CMS **以 headless 方式使用**：透過它的公開讀取 API（`GET /api/v1/render/page` 今天已可用 publishable key 讀取，另有 `/render/preview`）。

今天的 CMS 是為「平台託管店面」設計的，不適合直接當通用內容儲存：

- **schema 來自主題**：頁面的 JSON Schema 在 `theme_pages.page_schema`，全域內容的在 `themes.config_schema`；商家自己的站台無法定義自己的內容形狀。
- **定位用巢狀語意的 slug**：頁面以 `slug`（且 `home` 是保留字、`type_key` 綁主題頁型）定位，這是路由概念，不是內容鍵。
- **只有一份草稿與一份已發佈快照**：沒有版本歷史、無法回滾；沒有樂觀鎖。
- **沒有「發佈後通知商家站台重建」的機制**。
- **權限是 `page.*`**，語意是「頁面」不是「內容」。
- **商家站台與商家後台沒有消費它的契約**：`merchant-template` 的後台沒有內容畫面，店面的文案寫死在 `public/content.js`。

本 change 把 CMS 的內容功能**保留並改造**成通用的 headless **內容儲存**，並定義 merchant template 要如何消費它。

## What Changes

- **資料模型**：每家商家的內容文件（`content_documents`，以穩定的 `key` 定位，每份文件有 `type_key`、草稿、已發佈快照、版本計數）、每店 schema（`content_schemas`，版本化）、有界版本歷史（`content_revisions`）、稽核（`content_audit_log`）。現有 `pages` 與 `shops.content_json` 遷移為文件（`global` 為保留的鍵）；`home` 不再是保留 slug，只是慣例鍵；slug 的路由語意消失。
- **schema 所有權**：由**商家站台**的設計層提供 `content.schema.json`，經管理端 API 註冊（人工觸發、可先 dry run）；定義允許的詞彙（`x-editor`：`text`／`textarea`／`richtext`／`image`／`link`／`list`／`color`／`select` 等）、大小與深度上限，以及對 schema 本身的驗證（防 DoS 與夾帶）；schema 版本化，既有內容永不遺失，移除欄位前警告。
- **API**：公開讀取（publishable key；讀一份文件、列出已發佈鍵、整批讀取、`ETag`／快取標頭、預覽 token）；管理端 CRUD、草稿／發佈／下架、樂觀鎖、版本歷史與還原；新權限 `content.view`／`content.edit`／`content.publish`／`content.manage`（staff 角色預設不給）。
- **發佈 hook**：發佈後可選擇通知商家站台重建（例如每店一個 Cloudflare deploy hook URL）；SSRF／濫用防護（主機 allowlist、無重導向、逾時、簽章、限流、去抖動）。
- **排程發佈**（2026-11-25 使用者推翻「第一版不做」）：草稿可排定在未來某個時間發佈，可改期、取消；排程綁定草稿版本（排程之後的編輯使排程失效）；到點重新驗證，失敗則不發佈並標 `needs_attention`；走與手動發佈相同的服務路徑；執行者是 `cmd/content-scheduler`（一次性，cron）與選用的 API 內 ticker（advisory lock）。見 design D11。
- **稽核清除**：`cmd/purge-content-audit`（180 天、預設 dry run）；見 design D12。
- **相容**：`/admin/shops/{id}/pages*`、`/content`、`/page-types`、`/render/page`、`/render/preview` 在遷移後成為**薄的相容轉接層**，行為不變（v1 表面鎖原樣保留，不需要 v1 例外）。
- **消費模式與 merchant template 的後續工作**（只描述，不在本 change 實作）：執行期抓取＋靜態預設值為建議；template 的後台核心要新增 `ctx.data`／`ctx.actions` 內容函式與核心擁有的 schema 驅動表單建構器，店面核心新增 `ShopCommerce.content`。
- **與 `retire-hosted-storefront-and-themes` 的關係**：該 change **已核准**，其第一階段交付本 change 的第一個切片（**每店 schema 儲存**，tasks 群組 1）；本 change 的其餘部分在其後。

## Capabilities

### New Capabilities
- `content-store`：每店 schema、內容文件、版本歷史、權限與稽核。
- `content-delivery`：公開讀取 API、預覽、快取與 ETag、相容轉接層。
- `content-publish-hook`：發佈通知（deploy hook）與其防護。
- `content-scheduled-publish`：排程發佈（綁定草稿版本、到點重新驗證、錯過視窗、冪等；2026-11-25 加入）。

### Modified Capabilities
- `page-management`、`content-rendering`：行為不變，其需求改由新 capability 擴充；實際的 spec 增量在實作時以 MODIFIED 對齊（本提案不在這裡動它們，避免與 `retire-hosted-storefront-and-themes` 同時修改同一批 spec）。

## Impact

- **資料**：新表 `content_schemas`（群組 1，由退役 change 交付）、`content_documents`、`content_revisions`、`content_audit_log`、`content_publish_hooks`、`content_publish_schedules`；全部帶 `shop_id`，登記為 `tenantOwned`。回填 migration 把 `pages`／`shops.content_json` 轉為文件；`pages` 表與 `shops.content_json` 欄在清理階段（群組 10）才移除，且需要 Q 的決定。
- **程式碼**：新 `api/internal/content/`（服務、驗證、版本、稽核）、`api/internal/contentschema/`（群組 1）、`api/internal/content/hook/`；`internal/cms/` 的驗證／hydration／消毒／圖片網域碼被**重用**（移動或匯出，不重寫）；`internal/render/` 的快取與失效器擴充；`httpapi/content_public.go`、`httpapi/content_admin.go`、`httpapi/pages.go` 改為轉接層；`seed.go`（`content.*` 權限、demo schema）；`admin/views/pages.js`、`content.js` 改打新端點（或保留舊端點到清理階段）。
- **API 契約**：新增約 17 條路由（additive）；`openapi.yaml` 與 `v1-surface.txt` 只新增；既有端點不變。`wiring_integration_test` 的 ⑤⑥⑦ 需涵蓋新 Deps 與新事件訂閱者。
- **部署**：新環境變數 `CONTENT_*`（見 design）；發佈 hook 的主機 allowlist 是平台層設定。
- **merchant-template（後續，不在本 change）**：見 design「merchant template 後續工作」與 tasks 群組 11。
- **開放問題**：已於 2026-11-25 由使用者決定（「照建議調整」），見 design「Decisions record」；Q9（Cloudflare deploy hook 主機）已於第三切片查證並決定；Q11 已由使用者推翻（做排程發佈）。
