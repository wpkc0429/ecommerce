## 1. 消毒器（design D3–D6）

- [x] 1.1 `api/go.mod`：加入 `github.com/microcosm-cc/bluemonday`（v1.0.27；連帶 `golang.org/x/net` 轉為直接相依；`go.sum` 只新增 8 行，未做會刪除既有條目的 `go mod tidy`）
- [x] 1.2 新增 `api/internal/cms/richtext/allowlist.json`（標籤、各標籤屬性、屬性值 pattern、URL scheme、連內容移除的元素；含表格、不含 h1）
- [x] 1.3 新增 `api/internal/cms/richtext/richtext.go`：`go:embed` allowlist → bluemonday policy；`Sanitize(string) string`；`target=_blank` 以後處理補 `rel="noopener noreferrer"`（作者提供的 `rel` 一律不收）；`script/style/iframe/svg/...` 連內容移除
- [x] 1.4 schema 走訪器：**實作於 `api/internal/cms/sanitize.go`（`SanitizeRichText`）而非 `richtext/walk.go`**——它直接內嵌 `cms` 套件未匯出的 `hydrator`，重用其 `resolve`（`$ref`）、`sectionBranches`／`matchSectionBranch`（`oneOf` 依 `type` const），兩個走訪器因此對「哪個值屬於哪個 schema 節點」必然一致，不需要抽出或複製輔助函式。沒有改寫時回傳原位元組；`UseNumber` 保數字；`SetEscapeHTML(false)` 避免把 `<` 存成 `<`
- [x] 1.5 單元測試 `richtext/richtext_test.go`：23 個惡意向量（`onerror`、`javascript:` 大小寫／實體編碼／夾空白、`vbscript:`、`data:`、`http` 圖片、`<svg><script>`、`<noscript>` 逃逸、`style`／`class`／`id`、`onclick`、表單元素、h1、非法 target、非數字 colspan、作者 rel）＋ 10 個保留格式案例 ＋ rel 補齊 ＋ 冪等 ＋ allowlist 本身不含 h1／含表格／不含 style・class・id・rel・on*
- [x] 1.6 單元測試 `cms/sanitize_test.go`：sections `oneOf`＋`$ref` 走訪、陣列內物件的 richtext、`x-editor: text` 原樣、大整數不變、乾淨 payload 原位元組回傳、未知形狀不改寫、starter 形狀的 schema（h1 去殼、表格保留）

## 2. 寫入路徑（design D1／D2）

- [x] 2.1 `api/internal/cms/service.go`：`CreatePage`／`UpdatePage` 改走 `validateAndSanitize`（先驗 schema、再消毒、有改寫時再驗一次——消毒可能讓字串變長）
- [x] 2.2 `api/internal/cms/service.go`：`UpdateShopContent` 同上（依 `theme.ConfigSchema`）
- [x] 2.3 `PublishPage` 不改，已加註解說明（複製已消毒的 `content_json`；舊資料由回填處理）
- [x] 2.4 整合測試：**寫在新檔 `api/internal/httpapi/richtext_integration_test.go`**（重用 `newCMSEnv`）——更新與建立頁面的回應／DB 已消毒、`x-editor: text` 原樣、全域內容（測試中為 starter 主題加一個 richtext 欄位）、發佈後渲染 bundle 已消毒。**已實跑通過**（首次執行時兩條斷言比對的是原始 JSON 位元組——引號被跳脫、jsonb 會重新序列化——已改為比對解析後的 html，屬測試本身的錯誤，實作未改）

## 3. 回填指令（design D7）

- [x] 3.1 回填邏輯在 `api/internal/cms/backfill.go`（`Service.BackfillRichText`，可測）；指令 `api/cmd/sanitize-content/main.go`：預設 dry run，`-apply` 才寫回，`-v` 列出未變動列；逐頁依當前主題 page_schema、逐商家依 config_schema；無主題／頁型不在當前主題者回報 skipped；只印 id 與 changed/unchanged/skip
- [x] 3.2 寫回 `published_json` 後發佈 `events.PagePublished`、寫回商家內容後發佈 `events.ShopContentUpdated`；指令內以 `render.Invalidator` 訂閱（不直呼 Redis）；Redis 不通時照寫並 WARN
- [x] 3.3 整合測試（同 2.4 的檔案）：植入舊式惡意內容 → dry run 不寫不發事件 → apply 兩份都消毒且發出 `PagePublished` → 報告不含內容 → 第二次零變動。**已實跑通過**
- [x] 3.4 `Makefile`：新增 `sanitize-content` target（**只跑 dry run**；`-apply` 必須手動下）

## 4. Web 渲染端（design D1／D5）

- [x] 4.1 `web/package.json`：加入 `sanitize-html@^2.18.2` 與 `@types/sanitize-html@^2.16.2`（lockfile 另有 `nanoid`、`source-map-js` 兩個 patch 級升版，為 postcss 去重所致）
- [x] 4.2 `web/scripts/gen-types.mjs`：同時把 allowlist 複製成 `web/lib/richtext-allowlist.json`（`$comment` 標明產生檔）
- [x] 4.3 新增 `web/lib/sanitize.ts`：以 allowlist 組 `sanitize-html` 選項（屬性值 pattern 在 `transformTags` 套用、`target=_blank` 補 rel、`drop_with_content` 對應 `nonTextTags`）並匯出 `sanitizeRichText()`
- [x] 4.4 `web/themes/starter/sections/RichText.tsx`：改用 `sanitizeRichText(html)`；刪除「Phase 1 信任後台作者」註解
- [x] 4.5 `npm run gen:types`（`schema-types.ts` 僅行尾差異）、`npm run typecheck`、`npm run build` 皆通過；另以 tsc 編譯 `lib/sanitize.ts` 到 scratchpad 跑 10 個向量，行為與 Go 端一致

## 5. Admin

- [x] 5.1 `admin/lib/schemaform.js`：`richtext` 控件下方加說明文字。**另修正 design D2 的錯誤前提**：頁面編輯器與全域內容表單原本**不會**在儲存後重新載入，現改為以 PUT 回應（`views/pages.js`）／重新 `load()`（`views/content.js`）重建表單，畫面顯示實際存下的值。#4′：`lib/`＋`app.js` +6、`views/` +8（帳面 75%，已記入 CLAUDE.md；那 6 行是說明文字，不是抽象）

## 6. E2E

- [x] 6.1 `e2e/happy-path.mjs` 新增 ⑥b（7 項斷言）：API 存入含 `onerror`／`<script>`／`javascript:`／表格的 rich text → 儲存成功且存下的值已消毒、表格保留 → 發佈 → 前台 DOM 有表格、`window.__xss` 未被設定、沒有 `[onerror]` 與 `javascript:` 連結。**已實跑通過**（獨立資料庫 `ecommerce_e2e`、API :8180／REDIS_DB=1／mock、web :3100、admin :3001；全程 268 項 ✓、0 項 ✗）

## 7. 文件與驗收

- [x] 7.1 `CLAUDE.md`：新增本 change 的架構要點，e2e 斷言數 261 → 268；`api/openapi/openapi.yaml` 兩處 `content_json` 敘述補上消毒說明（`v1-surface.txt` 不變）
- [x] 7.2 `make test` ✅；`INTEGRATION=1 go test -p 1 -count=1 ./...` ✅ 全部套件通過（含契約檢查 ③ 與接線檢查 ⑤⑥⑦）；`golangci-lint` 本機未安裝、依指示不安裝，由 CI 執行
- [x] 7.3 在獨立資料庫上 `seed -demo` ＋ 三服務 ＋ `node happy-path.mjs`：PASSED，268 項斷言（主資料庫未動；結束後已停止服務、DROP `ecommerce_e2e`、FLUSHDB Redis DB 1）
