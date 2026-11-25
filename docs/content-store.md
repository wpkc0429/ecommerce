# 內容儲存（headless content store）：文件、版本與公開讀取

> 對應 change `headless-content-store`（design D1、D2、D5、D6、D8）。**第二個切片已實作**：內容文件、版本歷史、
> 後台文件 API、公開讀取 API（含預覽與快取）、舊端點的相容層。**第三個切片已實作**：發佈 hook（§7）、排程發佈（§8）、稽核清除（§9）。
> **尚未實作**：後台內容畫面與商家 template 的後台核心（§10 列出畫面要用的 API）。schema 註冊與詞彙見 [`content-schema.md`](content-schema.md)。

## 1. 模型

一份**內容文件**以每店穩定的 `key`（`^[a-z0-9][a-z0-9-]{0,62}$`）識別。`key` **不是路由**：哪個 `key` 對應站台上的哪個畫面，由商家站台自己決定。

| 欄位 | 說明 |
| --- | --- |
| `type_key` | 指向作用中 schema 的 type（`global` 是店家全域內容）；不再綁主題 |
| `status` | `0` 草稿、`1` 已發佈（下架回 `0`，已發佈內容保留但不對外） |
| `draft_json` / `published_json` | 草稿與已發佈內容；各 ≤ 128 KiB；每店已發佈總量 ≤ 2 MiB；每店 ≤ 100 份 |
| `meta` | 自由欄位（SEO 之類），≤ 4 KiB |
| `draft_rev` | 每次儲存草稿 +1；樂觀鎖與後台 `ETag` 的依據 |
| `needs_attention` | schema 變更後草稿或已發佈內容不再通過驗證（已發佈內容照常提供；草稿須修到通過才能再儲存或發佈） |
| `declared` | 鍵是否仍被 schema 宣告（`false`＝孤兒，可手動刪除） |

**版本歷史**（`content_revisions`）：每次發佈寫一筆 `publish`，同交易裁剪到最近 **20** 筆；schema 變更讓文件有風險時，註冊交易為該文件寫一筆
`schema_snapshot`（保存當時的草稿與不同的已發佈內容；每文件保留最近 5 個 schema 版本），所以被移除或改名的欄位值永遠找得回來。
**還原只產生草稿，從不自動發佈**：回滾是兩步（`content.edit` 還原、`content.publish` 發佈），各有自己的權限與稽核。

**稽核**（`content_audit_log`）：`schema.register`、`schema.dry_run`、`doc.save`、`doc.publish`、`doc.unpublish`、`doc.delete`、`doc.restore`。
`detail` 只含版本號、`rev`、計數與布林；**不含內容、不含標題、不含信箱或網址**（整合測試把 log 導進 buffer、並檢查資料表，特徵值一個都找不到）。
保留 180 天，清除指令是 `cmd/purge-content-audit`（§9）；另有 `schema.*`、`doc.schedule_*`、`hook.*` 動作（§7、§8）。

## 2. 後台 API（`/api/v1/admin/shops/{shopID}/content/…`）

| 端點 | 權限 | 說明 |
| --- | --- | --- |
| `GET /documents` | `content.view` | 清單（不含內容） |
| `GET /documents/{key}` | `content.view` | 草稿、已發佈、`schema_node`；回應標頭 `ETag: "<draft_rev>"` |
| `PUT /documents/{key}` | `content.edit` | 建立或更新草稿。**更新帶 `If-Match: "<draft_rev>"`，建立帶 `If-None-Match: *`**；缺少回 428、不符回 412（`details.current_rev`）。回應是**實際存下的值**（rich text 可能被改寫）與新 `ETag` |
| `POST /documents/{key}/publish` | `content.publish` | 重新驗證草稿 → 寫 revision → 失效快取；可帶 `If-Match` 或 `{"expected_rev": N}` |
| `POST /documents/{key}/unpublish` | `content.publish` | 下架（`global` 不可） |
| `DELETE /documents/{key}` | `content.publish` | 只限集合成員與孤兒（schema 宣告的固定文件、`global` 不可刪，409） |
| `GET /documents/{key}/revisions`、`…/{rev}` | `content.view` | 清單（不含內容）／單筆內容 |
| `POST /documents/{key}/revisions/{rev}/restore` | `content.edit` | 放回**草稿**；可帶 `If-Match`；舊 schema 的快照若不再通過目前 schema 仍放回，但 `restored_valid:false`、`needs_attention` |
| `POST /documents/{key}/preview-token` | `content.view` | `{"scope":"document"｜"shop"}`，短效（`CONTENT_PREVIEW_TTL`，預設 10 分鐘） |

角色：`staff` 全部 403；`editor` 有 view＋edit（不能發佈、下架、刪除、註冊 schema）；`merchant_owner` 與平台 `super_admin` 全有。`page.*` 是相容期的等價別名（舊頁面端點仍接受）。
限流（每商家＋呼叫者）：儲存與還原每分鐘 60、發佈／下架／刪除每分鐘 20。

可建立的鍵：format 2 只能建立 schema 宣告的鍵（`documents`）或符合 `collections` 前綴的鍵（受 `max` 限制）；format 1（舊主題回填的 schema，沒有宣告）可建立任何合法鍵，需在 body 帶 `type_key`。
`link`、`image`、`richtext` 欄位在寫入時被檢查或消毒（見 `content-schema.md`）；發佈前會重新驗證。

## 3. 公開讀取 API（商家站台用）

三個端點，掛在 `/api/v1/shop/content`，只需 `X-Publishable-Key`（識別碼不是憑證）。**只回已發佈的內容。**

```
GET /api/v1/shop/content                    → {schema_version, documents:[{key,type_key,title,published_at,etag}]}
GET /api/v1/shop/content?include=content    → 每筆另帶 content 與 meta（整批取回；上界 2 MiB）
GET /api/v1/shop/content?keys=home,faq      → 只取指定鍵（最多 50 個）
GET /api/v1/shop/content/{key}              → {key,type_key,title,schema_version,published_at,content,meta}
GET /api/v1/shop/content/{key}/preview      → 草稿（標頭 X-Preview-Token）
```

- **hydrate**：`content` 一律經 schema hydrate（補預設值、丟棄 schema 未定義的鍵），所以站台永遠拿到**完整形狀**。已發佈內容在新 schema 下不再通過驗證時**仍照原樣提供**，`schema_version` 是發佈當時的版本；type 已不在 schema 內的文件照存的內容提供。
- **404 逐位元組相同**：「鍵不存在」「只有草稿」「已下架」「鍵格式不符」回同樣的 404（內容與標頭都相同）；跨商家永遠 404。
- **快取標頭**：`ETag: "<回應主體 SHA-256 前 16 hex>"`（含引號）、`If-None-Match` → `304`（無主體）、`Cache-Control: public, max-age=60, stale-while-revalidate=300`
  （`CONTENT_PUBLIC_MAX_AGE`，預設 60 秒）、`Vary: X-Publishable-Key, Origin`、`X-Cache: HIT|MISS`。清單的每筆 `etag` 與單一文件端點的 `ETag` 一致，可用來決定哪些文件要重抓。
- **伺服端快取**：重用渲染快取（Redis；`cache:shop:{id}:v{ver}:content:{key|_list|_all}`，singleflight，Redis 故障退回 DB）。**事件驅動失效**：發佈／下架／刪除 → 刪該鍵與兩個清單鍵；schema 註冊、全域內容更新、商家停用 → 整店版本號 +1。所以發佈後伺服端立即可見；瀏覽器端最多再舊 `max-age` 秒。舊頁面端點（`/pages/*`、`/content`）寫入的內容走同樣的失效路徑。
- **預覽**：後台簽發 token（`POST …/preview-token`），站台以 **`X-Preview-Token` 標頭**（不放網址：避免進日誌與 Referer）呼叫 `…/{key}/preview`。token 綁商家與範圍（單一鍵或整店），不能跨店或跨文件、不能當 access token，也不能由頁面預覽 token 冒用；使用時再檢查持有者仍有 `content.view`。回應 `Cache-Control: no-store`、`X-Robots-Tag: noindex, nofollow`。網址上的 `token` 參數被忽略。
- **限流**：每商家每 IP 每分鐘 120 次（`RATE_LIMIT_CONTENT_PUBLIC_LIMIT`／`_WINDOW`），超過回 `429`＋`Retry-After`。
- **CORS**：與其他公開端點相同（登記過的 storefront origin）；`Allow-Headers` 含 `X-Publishable-Key, X-Preview-Token, If-None-Match, If-Match`，`Expose-Headers` 含 `ETag`（**漏掉任一個，瀏覽器端的條件請求或預覽就壞**，測試斷言）。

## 4. 商家站台消費指南

**建議（design D8）：執行期抓取為預設，搭配設計層的靜態預設文案；建置期快照是選用的最佳化。**

| | 建置期抓取（烘焙進 HTML／資料檔） | 執行期抓取（瀏覽器載入時） |
| --- | --- | --- |
| 優點 | 首次繪製最快、無閃爍、不依賴 API 可用性、文字在 HTML 內（SEO 最好） | 發佈後立即可見（≤ `max-age`）；**不需要建置步驟也不需要 hook**，符合零建置範本；店面本來就在執行期呼叫 API，沒有新增可用性相依 |
| 缺點 | 需要建置步驟與發佈 hook（**hook 尚未實作**）；發佈到上線有重建延遲；hook 失敗時內容過期 | 首次繪製要等一個請求（先畫預設文案再替換）；SEO 較弱 |

**執行期的作法**：

1. 站台的設計層保留預設文案（頁面永遠可用）。
2. 載入時 `GET /api/v1/shop/content?include=content`（一個請求）；記住回應的 `ETag`，之後以 `If-None-Match` 重抓，304 就沿用。
3. 把結果覆蓋在預設值上；失敗（網路、5xx、429）**靜默沿用預設值或上次成功的內容**，不要讓頁面壞掉。
4. 某個 `key` 不在清單內（未發佈）→ 用預設文案，不要當錯誤。
5. 依 `type_key` 與 `schema_version` 決定呈現；`content` 的形狀由商家自己的 `content.schema.json` 決定，hydrate 保證每個宣告的欄位都在。

**呈現規則（縱深防禦，伺服器已在寫入時檢查，但消費端不得假設它是唯一一道）**：

- 文字：只以 `textContent` 或文字節點呈現。
- `richtext` 欄位：只能呈現**消毒後的 DOM**（allowlist 與伺服器同一份：`api/internal/cms/richtext/allowlist.json`；不可用 `innerHTML` 直接塞原字串）。
- `link` 欄位：只放進 `href`，且再檢查一次 scheme（`https:`、`http:`、`mailto:`、`tel:`、以 `/` 或 `#` 開頭的站內路徑；拒絕 `javascript:`、`data:`、`vbscript:`）。
- `image` 欄位：只放進 `src`；CSP `img-src` 只放行影像網域。
- 站台 CSP 不放 `'unsafe-inline'` script；內容永遠不進 `<script>`、事件屬性或 `style`。
- 預覽模式：站台讀 `?content_preview=<token>`、以 `X-Preview-Token` 標頭打預覽端點，並顯示「預覽中」橫幅；token 短效，不要存進任何持久儲存。

**用 ETag 還是等 `max-age`**：瀏覽器自己會遵守 `max-age=60`，所以多數情況什麼都不用做；需要「剛發佈就看到」的預覽或管理員畫面，用預覽端點（永不快取）。

## 5. 相容層（現有端點行為不變）

`/admin/shops/{id}/pages*`、`/content`、`/page-types` 與 `/api/v1/render/page`、`/render/preview` 的行為與回應位元組不變（既有整合測試、契約測試與 e2e 不改斷言）。

- `pages` 與 `shops.content_json` 仍是資料表，但**每一次寫入都在同一個交易裡與內容文件雙向同步**（`internal/contentstore`）：舊端點寫入 `pages` 後同交易同步到文件（`slug`→`key`、`content`→`draft`、發佈寫 revision）；新文件 API 寫文件後同交易寫回 `pages`（`global` 寫回 `shops.content_json`）。`shops.content_json` 因此是 `global` 文件已發佈內容的**唯讀投影**。
- 兩邊永遠相等：整合測試在兩個方向各驗證一次（`TestLegacyEndpointsAndDocumentsStayConsistent`）；繞過相容層直接寫表的資料（直接 SQL、舊工具）由 `EnsureDocuments` 在下一次後台文件操作或 schema 註冊時自動補齊。
- 兩個有意的差異（皆為收緊、不影響合法資料）：舊頁面端點的 slug 現在必須是合法的 `key`（≤ 63 字元、不以 `-` 開頭），且 `global` 為保留字；`x-editor: link` 欄位開始檢查 scheme（舊 format 1 schema 沒有這種欄位，所以沒有既有資料受影響）。
- 舊端點的退役不在這個 change：保留相容端點，沒有第二次 v1 例外（使用者 2026-11-25 的決定）。

## 6. 接下來（商家 template 的後台核心要建什麼）

唯讀的發現，給 merchant template（它在另一個 repo，守門員流程之內）：

1. `ctx.data`／`ctx.actions` 的內容函式要對應上面的後台 API：`If-Match`／`If-None-Match` 的處理（412 → 「他人已更新，重新載入」、428 是程式錯誤）、422 的 JSON Pointer 對應到欄位、儲存後以**回應**重建表單（rich text 可能被改寫）。
2. 表單建構器要支援 `x-editor`：`text`、`textarea`、`richtext`、`image`、`link`、`color`、`select`、`list`、`sections`，並顯示 `needs_attention`、`declared`、`restored_valid`、revisions 與還原（還原後提示「只放回草稿」）。
3. 預覽：呼叫 `preview-token` 後開啟 `{站台}/?content_preview=<token>`；站台核心用標頭打預覽端點。
4. 店面核心：`ShopCommerce.content.{all,get}` 用 `include=content`＋ETag，失敗退回預設；`richText()`／`link()` 以同一份 allowlist 再消毒。
5. 設計層要出貨 `content.schema.json`；改 schema 後**必須由人**在後台觸發註冊（dry run → 確認移除 → 註冊），AI 與 CI 不得自動推送。


## 7. 發佈 hook（第三切片；design D7）

發佈或下架後通知商家站台重建（典型：Cloudflare deploy hook）。**預設關閉**，每店至多一個，只有 `content.manage`（owner、super_admin）能設定。
操作員步驟（建立 Cloudflare hook、dry run、測試、環境變數、失敗處理）見 [`deploy/content-store-operations.md`](deploy/content-store-operations.md) §1。

| 端點（`/admin/shops/{shopID}/content/publish-hook…`） | 說明 |
| --- | --- |
| `GET` | `PublishHookStatus`：`configured`、`enabled`、`host`、`url_masked`（`https://host/…末四碼`）、`last_status`、`last_http_status`、`last_attempt_at`、`last_success_at`、`consecutive_failures`、`disabled_reason`、`failed`、`pending`、`daily_count`／`daily_cap`。**永不回完整 URL 或 secret** |
| `PUT`（`{url?, enabled?}`；`?dry_run=true`） | 設定或更換 URL（加密儲存，之後不可讀回）、啟停。dry run 只驗證形狀與 allowlist，不儲存也不呼叫。URL 不合規 422（訊息不回顯 URL） |
| `DELETE` | 刪除 |
| `POST …/test` | 對已儲存的 URL 單次測試呼叫（不影響失敗計數與每日上限）；只回 `{ok, result, http_status}` |
| `POST …/fire` | 手動重建：略過去抖動，佔用每日上限（滿了 429 `hook_daily_cap_reached`），停用時 409 |
| `POST …/signing-secret` | 產生新的簽章 secret，**只在這個回應顯示一次** |

`test`／`fire` 每商家每分鐘共 3 次，設定類每呼叫者每分鐘 30 次。

**出站規則**（唯一的出站路徑 `hook.Sender.Post`）：只允許 https、埠 443、無 userinfo／fragment、非 IP 字面值、≤ 512 字元，主機必須在 allowlist
（預設只有 `api.cloudflare.com` 的兩種 deploy hook 路徑，`CONTENT_HOOK_ALLOWED_HOSTS` 只能新增）；每次發送自行解析 DNS，任何一個答案落在私有／loopback／link-local
（含 `169.254.169.254`）／CGNAT／multicast／保留／IPv4-mapped／NAT64／6to4 就整個拒絕（`blocked`），並**只連線到剛檢查過的 IP**（DNS rebinding 無效）；不跟隨重導向
（3xx＝失敗）；整體 5 秒；回應只讀 4 KiB 然後丟棄，**永不回傳給呼叫者或寫入日誌**。請求主體 `{event, shop_id, key?, rev?, at, coalesced?}` **不含內容**，
標頭 `X-Content-Timestamp`、`X-Content-Signature: sha256=HMAC-SHA256(secret, timestamp + "." + body)`。

**去抖動與上限**：同商家 30 秒內的多次發佈合併成一次呼叫（持續發佈最多拖 5 分鐘）；兩次呼叫最短間隔 60 秒（未到保留為待送）；每日（UTC）上限 30 次（用完丟棄並記 `rate_limited`）。
狀態存在 `content_publish_hooks` 的 `pending_*` 欄位，所以重啟不丟、多實例與一次性排程指令不會重複呼叫。事件處理器只入隊（有界、滿了丟棄並 log），**不拖慢發佈請求**。
失敗（逾時、5xx、429）重試 2 次（2 秒、4 秒）；**連續 5 次失敗自動停用**（`failed: true`），手動重新啟用才恢復。稽核 `hook.set`／`hook.test`／`hook.fire`／`hook.disable`／`hook.delete`／`hook.secret_rotate`，只記主機與結果類別。

## 8. 排程發佈（第三切片；design D11）

草稿可以排定在未來發佈。**排程綁定你審閱過的那一版草稿**：到點時只有 `draft_rev` 仍相同才發佈；之後任何草稿儲存（含還原）立即讓排程變成 `superseded`（`result: edited`），
不會有「沒審閱過的修改在凌晨上線」。要發佈新版就重新排程。

| 端點（`/admin/shops/{shopID}/content/…`） | 權限 | 說明 |
| --- | --- | --- |
| `PUT /documents/{key}/schedule`（`If-Match: "<draft_rev>"`，`{publish_at}`） | `content.publish` | 排程（201）或改期（200，同一個 `id`、重新綁定到現在的 `draft_rev`）。缺 `If-Match` 428、不符 412 |
| `DELETE /documents/{key}/schedule` | `content.publish` | 取消（204；沒有等待中的 404；處理中 409） |
| `GET /documents/{key}/schedule` | `content.view` | 目前（或最近一次）的排程 |
| `GET /schedules?scope=upcoming｜recent&limit=` | `content.view` | 本店即將發佈（預設）或最近結束的排程 |
| 文件清單與詳情 | `content.view` | 多一個 `scheduled_publish_at`（UTC 或 `null`） |

- **時間**：`publish_at` 是帶偏移的 RFC 3339（無偏移 422，不猜時區）；儲存為 UTC、進位到下一個整分鐘；至少 2 分鐘後、至多 90 天（`CONTENT_SCHEDULE_MIN_LEAD`／`_MAX_AHEAD`）。
  「到點」由**資料庫的 `now()`** 判斷，所以多台機器的時鐘偏差不影響。
- **到點**：重新驗證草稿（schema 可能已換版）。失敗 → 不發佈、文件 `needs_attention`、排程 `failed`（`result: invalid`，`failure.pointers` 最多 5 個 JSON Pointer，不含內容）、稽核 `doc.schedule_failed`。
  排程者已被停用／移出／失去 `content.publish` → `failed`（`permission_revoked`）。
- **錯過視窗**：排程器（或 API）停機後恢復，遲到在 1 小時內（`CONTENT_SCHEDULE_GRACE`）照常發佈（`late_seconds` 記錄），超過 → `missed`、不發佈、稽核 `doc.schedule_missed`，由人決定是否補排。
- **同一條路徑**：排程器呼叫 `content.Service.Publish`，所以 revision、`doc.publish` 稽核（帶 `scheduled: true`、`schedule_id`）、快取失效與發佈 hook 都與手動發佈相同。
- **冪等與並發**：每筆以條件式 UPDATE 認領；整輪另有 Postgres advisory lock（兩個排程器同時跑，第二個整輪跳過）；崩潰遺留的 `running`（> 10 分鐘）重置為 `pending`，再處理時若已發佈就記 `already_published`，不重複寫 revision。
- **其他使排程失效的事件**：手動發佈同一版（`superseded`／`published_manually`）、刪除文件（`cancelled`／`document_deleted`）。每份文件同時至多一筆等待中的排程。
- **執行者**：`cmd/content-scheduler`（一次性，cron／工作排程每分鐘；`make content-scheduler`）與 API 內選用的 ticker（`CONTENT_SCHEDULER_ENABLED=1`，預設關）。見 [`deploy/content-store-operations.md`](deploy/content-store-operations.md) §2。

## 9. 稽核清除（第三切片；design D12）

`cmd/purge-content-audit`（`make purge-content-audit`＝dry run）：`content_audit_log` 保留 180 天（`CONTENT_AUDIT_RETENTION`），已結束的排程列保留 90 天（`CONTENT_SCHEDULE_RETENTION`）；
預設 dry run、`-apply` 才刪、分批、冪等、`-shop N` 限一家、只印計數與時間範圍；等待中與處理中的排程永不清除。見營運手冊 §3。

## 10. 給下一個 task（後台內容畫面）的 API 清單

畫面要用到的全部後端能力都已存在（這個切片沒有建任何 UI）：

| 畫面 | 端點 | 注意 |
| --- | --- | --- |
| schema 檢視／註冊 | `GET/PUT …/content/schema`、`…/schema/versions` | `dry_run`、`confirm_removals`（`content.manage`） |
| 文件清單 | `GET …/documents`（含 `needs_attention`、`declared`、`scheduled_publish_at`） | |
| 編輯器 | `GET/PUT …/documents/{key}`（`If-Match`／`If-None-Match: *`；412 → 重新載入；428 是程式錯誤；422 的 `details[].pointer` 對應欄位） | 以**回應**重建表單 |
| 發佈／下架／刪除 | `POST …/publish`（`If-Match`）、`…/unpublish`、`DELETE` | 確認對話框 |
| 排程 | `PUT/DELETE/GET …/documents/{key}/schedule`、`GET …/schedules` | `If-Match` 帶審閱過的 `draft_rev`；顯示 `status`／`result`／`failure.pointers`；時間以 UTC 傳、以商家時區顯示（`GET …/settings` 沒有，時區在報表回應的 `timezone`） |
| 版本 | `GET …/revisions`、`…/{rev}`、`POST …/restore` | 還原只放回草稿 |
| 預覽 | `POST …/preview-token`（`scope: document｜shop`） | |
| 發佈 hook | `GET/PUT/DELETE …/publish-hook`（`?dry_run=true`）、`POST …/test`、`…/fire`、`…/signing-secret` | 狀態只有遮罩 URL；`failed: true` 要顯示成最終失敗並提供重新啟用；設定表單的 URL 欄位是只寫 |

權限節點：`content.view`（讀）、`content.edit`（草稿、還原）、`content.publish`（發佈、下架、刪除、排程）、`content.manage`（schema、hook）；畫面可見性來自 `GET /shops/{shopID}/me`。
