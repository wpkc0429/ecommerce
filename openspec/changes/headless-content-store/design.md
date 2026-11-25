## Context（已對照程式碼驗證）

- **現行資料**：`pages`（`api/internal/ent/schema/cms.go`）：`shop_id`、`type_key`、`title`、`slug`（`^[a-z0-9-]+$`，≤150，`UNIQUE(shop_id, slug)`）、`status`（0/1）、`content_json`（草稿）、`published_json`（已發佈快照）、`meta`。`shops.content_json`（`ent/schema/tenancy.go:33`）是全域內容，**即時生效、沒有草稿**（`UpdateShopContent`）。schema：`theme_pages.page_schema`（依 `type_key`）、`themes.config_schema`。
- **現行寫入閘門**：`cms.Service.validateAndSanitize`（`cms/sanitize.go`）＝`ValidatePayload`（`santhosh-tekuri/jsonschema/v6`，draft 2020-12，422 附 JSON pointer）→ `CheckImageFields`（`x-editor: image`，前綴比對含結尾斜線，媒體未設定時不檢查）→ `sanitizeRichText`（`x-editor: richtext`，bluemonday allowlist 單一來源 `cms/richtext/allowlist.json`；改寫不拒絕；有改寫再驗一次）。`cms/hydrate.go`：填預設值、**丟棄 schema 沒定義的鍵**、section 陣列以 `properties.type.const` 分支、`maxHydrateDepth = 64`。`cms/fieldorder.go`：`x-editor-order`。
- **現行讀取**：`GET /api/v1/render/page?path=`（`httpapi/render.go`）：金鑰優先，否則 `X-Site-Domain`／Host；`render.Cache`（Redis，`cache:shop:{id}:v{ver}:page:{slug}`，24h TTL，singleflight，Redis 故障退回 DB）；`render.Invalidator` 訂閱 `PagePublished`／`PageUnpublished`／`ShopContentUpdated`…；`GET /render/preview?token=`：`auth.IssuePreview`（HS256、`aud=preview`、`PREVIEW_TOKEN_TTL` 預設 10 分鐘、claims＝user＋shop＋slug），使用時檢查持有者仍有 `page.view`，`Cache-Control: no-store`。
- **現行權限**：`page.view/create/edit/delete/publish`；`merchant_owner` 全有；`editor` 有 view／create／edit（無 delete／publish）；`staff` 無。
- **現行後台**：`admin/views/pages.js`、`content.js`、`lib/schemaform.js`（JSON Schema→表單；`x-editor`：`color`、`richtext`、`image`〔媒體啟用時有上傳鍵〕，`type: array`＝`repeater`，`sections`）、`lib/upload.js`（`purpose: content_image`）。
- **現行約束**：JSON 請求主體上限 1 MiB（`httpapi/middleware.go` `decodeJSON`）；沒有通用稽核表；`ratelimit.Limiter`（Redis 滑動視窗，fail open）；`internal/cryptbox`（AES-256-GCM，`PAYMENT_CREDENTIALS_ENCRYPTION_KEY`）；`events.Dispatcher`（同步、commit 後、handler 無 error）；`notify.Service` 是「有界 channel ＋ worker、不持久化」的先例；`/api/v1/shop/*` 群組用 `TenantMW`（金鑰優先，否則 Host——Host 後備在 `retire-hosted-storefront-and-themes` 移除）。
- **現行 e2e 事實**：無真實商家的 CMS 內容；`seed -demo` 種了 `starter` 主題與 demo 商家的 `home` 草稿（`internal/seed/schemas/starter/*.json`）。

**放置規則（使用者於 2026-11-24 決定）**：需要後端運算或伺服器端狀態的東西歸 ecommerce；很少改動的內容放在站台自己的 repo（官網 repo、或商家店面設計層）；商家或操作員**經常用表單編輯**的內容才放在本內容儲存。所以平台官網的介紹、價格、條款等文案**不**放這裡（它們是官網 repo 的靜態內容）；本內容儲存服務的是商家店面的可編輯文字、banner、活動文案、促銷圖、店家資訊與 FAQ。

**與已核准的退役 change 的關係**：`retire-hosted-storefront-and-themes` 已於 2026-11-24 核准直接移除；它的第一階段交付本 change 的群組 1（每店 schema 儲存），所以本 change 不是它的前置條件，只有群組 1 是；其餘群組隨使用者核准後進行。

## Goals / Non-Goals

**Goals**：①商家站台能擁有自己的內容形狀（schema）；②商家以表單編輯、草稿／發佈、可預覽、可回滾；③公開讀取快、可快取、不洩漏草稿；④所有輸入在寫入時被驗證與消毒；⑤schema 變更永不遺失資料；⑥發佈可選擇觸發商家站台重建；⑦現有端點在遷移期間行為不變。
**Non-Goals**：多語系內容（`key` 可自行加語系後綴，系統不管）；內容的全文搜尋；內容的關聯或查詢語言；二進位資產管理（圖片走既有 `media` 流程）；排程發佈（**已改為做，見 D11**）；多人同時編輯的合併（只做樂觀鎖）；在 ecommerce 內渲染內容（這正是被退役的東西）。

## Decisions

### D1：資料模型

**`content_schemas`**（每店 schema，版本化；**群組 1，由 `retire-hosted-storefront-and-themes` 第一階段交付**）
`id`、`shop_id`、`version`（int ≥ 1，`UNIQUE(shop_id, version)`）、`schema_json`（jsonb）、`sha256`（varchar(64)，正規化 JSON 的雜湊，用來判斷「重複註冊」）、`source`（`theme_backfill`｜`registered`）、`created_by_user_id`（可 NULL）、`created_at`。作用中的版本＝該店最大 `version`。儲存格式：
- **format 1**（群組 1 的回填格式）：`{"format":1,"types":{"<type_key>":<JSON Schema>},"global":<JSON Schema>,"component_keys":{…}}`——`component_keys` 只為退役 change 階段 4 之前的 bundle 過渡，之後忽略。format 1 **沒有文件宣告**：任何符合 `types` 的鍵都可建立（等同今天的行為）。
- **format 2**（商家註冊的 `content.schema.json`，D3）：`types` ＋ `documents`（宣告固定鍵）＋ `collections`（宣告動態鍵前綴）。讀取端同時支援兩種格式；第一次成功註冊 format 2 之後，該店用 format 2。

**`content_documents`**（內容文件）
`id`、`shop_id`、`key`（`^[a-z0-9][a-z0-9-]{0,62}$`，穩定、**不是路由**，`UNIQUE(shop_id, key)`）、`type_key`（varchar(50)，指向作用中 schema 的 `types`，**無 FK**）、`title`（varchar(150)，只給後台列表用）、`status`（0 草稿／1 已發佈，CHECK）、`draft_json`、`published_json`（可 NULL）、`meta`（jsonb，≤4 KiB，SEO 之類的自由欄位）、`draft_rev`（bigint，每次儲存草稿 +1，**樂觀鎖與 ETag 的依據**）、`published_rev`（發佈當時的 `draft_rev`）、`schema_version`（草稿最後一次驗證所用的 schema 版本）、`published_schema_version`、`needs_attention`（bool，schema 變更後草稿或已發佈內容不再通過驗證）、`declared`（bool，鍵是否仍被 schema 宣告）、`created_at`、`updated_at`、`published_at`、`updated_by_user_id`。索引：`(shop_id, status)`。
上限：`draft_json`／`published_json` 各 ≤ 128 KiB（寫入時檢查，422）；每店文件數 ≤ 100；每店已發佈內容總量 ≤ 2 MiB（發佈時檢查，使「整批讀取」有上界）。

**`content_revisions`**（有界版本歷史）
`id`、`shop_id`、`document_id`、`rev_no`（每文件遞增）、`kind`（`publish`｜`schema_snapshot`）、`json`、`title`、`meta`、`schema_version`、`actor_user_id`、`created_at`。**每次發佈寫一筆 `publish`**（內容＝剛發佈的快照）；**同交易內**刪除該文件最舊的 `publish`，只保留最近 **20** 筆。`schema_snapshot` 由 D4 的 schema 註冊產生，不受 20 筆限制，但每文件只保留最近 5 個 schema 版本的快照。
還原＝把某筆 revision 的 `json` 放回**草稿**（`content.edit`），**不自動發佈**（發佈另需 `content.publish`），所以回滾是兩步、各有各的權限與稽核。

**`content_audit_log`**（稽核）
`id`、`shop_id`、`actor_user_id`（可 NULL，不設 FK：稽核要比帳號活得久）、`action`（`schema.register`｜`schema.dry_run`｜`doc.save`｜`doc.publish`｜`doc.unpublish`｜`doc.delete`｜`doc.restore`｜`hook.set`｜`hook.test`｜`hook.fire`）、`doc_key`、`detail`（jsonb：版本號、`rev`、移除欄位數、hook 結果類別；**不含內容本身、不含 URL 全文**）、`created_at`。保留 180 天（`CONTENT_AUDIT_RETENTION`），清除由 `cmd/purge-content-audit`（預設 dry run，同 `cmd/sanitize-content`）。今天**沒有**通用稽核表，所以這是新表；保持小而專用。

全部新表帶 `shop_id`，登記進 `tenantOwned`（`enttenancy_internal_test.go` 會要求）。

#### 現行資料到新模型的對應

| 現行 | 新 | 說明 |
| --- | --- | --- |
| `pages.slug` | `content_documents.key` | slug 今天已是扁平的（`^[a-z0-9-]+$`，**沒有巢狀**）；對應是恆等。不合新規則的（>63 字元或以 `-` 開頭）：遷移**拒絕並列出**（目前沒有真實資料；預期為 0）。slug 不再有任何路由語意——商家站台自己決定哪個 `key` 對應哪個畫面 |
| `home`（保留 slug，建店自動建立） | 慣例鍵 `home`（**不再保留**，不再自動建立） | 新店不再自動建立 `home`；schema 的 `documents` 宣告它，註冊時由系統建立空草稿（hydrate 預設值）。`createHomePage` 在清理階段移除；`setup-status.home_published` 改為「`home` 文件已發佈」（欄位不變） |
| `pages.type_key` | `content_documents.type_key` | 概念保留：type 是 schema 中的具名形狀，不再綁主題 |
| `pages.title`、`meta` | 同名 | |
| `pages.content_json` / `published_json` / `status` | `draft_json` / `published_json` / `status` | 恆等 |
| `shops.content_json`（全域內容，即時生效） | 鍵 `global` 的文件，`type_key = 'global'` | 遷移時 `status = 1`、`published_json = draft_json = shops.content_json`（保持「即時」可見）；之後它和其他文件一樣有草稿／發佈。相容轉接層的 `PUT /content` 先寫草稿再立即發佈，保留「即時生效」語意 |
| `themes.config_schema`／`theme_pages.page_schema` | `content_schemas`（format 1 → format 2） | 群組 1 已回填 |

**遷移路徑（相容期間端點照常運作）**：①（群組 1）schema 儲存；②（群組 2）新表＋回填 migration（冪等，`INSERT … ON CONFLICT DO NOTHING`，回填後 `pages` 與新表並存）；③（群組 4）`cms.Service` 的頁面方法改為委派給 `content.Service`（讀寫都走新表），舊端點的轉接層把 `slug↔key`、`content↔draft_json`、`page id↔document id` 對應回去，回應位元組不變（現有整合測試即回歸）；④（群組 10，清理）確認沒有流量後移除 `pages` 表與 `shops.content_json`——**僅在使用者決定（Q）後**；在此之前 `pages` 保持只讀（寫入已轉向）。回滾：③ 之前可直接 `git revert`；③ 之後資料已在新表，回滾＝把轉接層切回舊程式碼並以 `pages` 為準，因為 ③ 期間**雙寫**（新表為主，同交易寫回 `pages`／`shops.content_json`），所以回滾不丟資料。

### D2：公開讀取 API

端點（掛在 `/api/v1/shop/content`，經 `TenantMW`；publishable key 識別商家；CORS 沿用 `shop_allowed_origins` 的 storefront 用途）：

| 端點 | 說明 |
| --- | --- |
| `GET /shop/content` | 列出已發佈文件：`{schema_version, documents:[{key,type_key,title,published_at,etag}]}`；加 `?include=content` 則每筆帶 hydrate 後的 `content` 與 `meta`（上限由「每店已發佈總量 ≤ 2 MiB」保證）。供建置期與執行期整批抓取 |
| `GET /shop/content/{key}` | 讀一份已發佈文件：`{key, type_key, title, schema_version, published_at, content, meta}`。**不存在與未發佈回完全相同的 404**（不洩漏草稿存在） |
| `GET /shop/content/{key}/preview` | 預覽草稿：標頭 `X-Preview-Token`（**不放 query**：避免進日誌與 Referer）；回 hydrate 後的草稿；`Cache-Control: no-store`、`X-Robots-Tag: noindex`；使用時檢查 token 持有者仍具 `content.view` 且商家未停用（沿用 `/render/preview` 的做法） |

- **hydrate**：回應的 `content` 一律經 `cms.Hydrate`（填預設值、丟棄 schema 未定義的鍵），所以商家站台總是拿到**完整形狀**。已發佈內容若在新 schema 下不再通過驗證，**仍照原樣（hydrate 後）提供**，並帶 `schema_version`（發佈當時的版本）讓站台自己判斷——不因 schema 變更而讓線上內容消失。
- **快取標頭**：`ETag: "<sha256 前 16 hex of 回應主體>"`，支援 `If-None-Match`→304；`Cache-Control: public, max-age=60, stale-while-revalidate=300`（`CONTENT_PUBLIC_MAX_AGE`，預設 60）；`Vary: X-Publishable-Key, Origin`。預覽一律 `no-store`。
- **伺服端快取**：重用 `render.Cache` 的版本化 key 與 singleflight，新命名空間 `cache:shop:{id}:v{ver}:content:{key}` 與 `…:content:_list`、`…:content:_all`；發佈／下架刪除該 key 與兩個列表 key；schema 註冊、商家停用 `BumpShops`。新領域事件 `ContentPublished{ShopID,Key}`、`ContentUnpublished{ShopID,Key}`、`ContentSchemaRegistered{ShopID,Version}`；失效器為既有 `render.Invalidator` 的新 case（檢查 ⑦ 的訂閱者集合不變，因為是同一個 handler）；發佈 hook 是**新增**的訂閱者（`wiring_integration_test` 的 `expectedSubscribers` 需有意識更新）。
- **限流**：公開讀取端點掛既有 per-IP 規則（`ipRule`）；因為有 ETag 與快取，限額寬鬆（預設每 IP 每分鐘 120）。
- **相容轉接層**：`/render/page`、`/render/preview` 與 `/admin/shops/{id}/pages*`、`/content`、`/page-types` 行為與回應位元組不變（`slug`＝`key`；`/render/page` 的 `path` 正規化沿用 `NormalizeSlug`；bundle 的 `shop.theme`／`component_key` 欄位的處置由退役 change 階段 4 決定並已列入其 v1 例外）。

### D3：schema 所有權與詞彙

**誰擁有**：商家站台。站台的設計層出貨 `content.schema.json`（路徑為設計層的一部分；template 後續工作 T1）。註冊是**人工觸發**的動作（商家 owner 或平台管理員，`content.manage`），不是 CI 自動推送：因為 schema 檔在 AI 可編輯的設計層，人工確認是「AI 不能自己改內容形狀」的最後一道關；dry run 先給出相容性報告。

`PUT /admin/shops/{shopID}/content/schema`：body＝`content.schema.json`；`?dry_run=true` 只回報告、不寫入；`?confirm_removals=true` 才允許有「欄位移除」警告的註冊。回應：`{version, changed, report:{documents:[{key, status:"ok"|"needs_attention"|"orphaned", removed_paths:[…], invalid:[{pointer,message}]}]}}`。相同 `sha256` 的重複註冊回 200、`changed:false`、不增加版本。

**`content.schema.json`（format 2）形狀**：
```json
{
  "format": 2,
  "types": {
    "home": { "title": "首頁", "schema": { "type": "object", "additionalProperties": false, "properties": { … } } },
    "faq":  { "title": "常見問題", "schema": { … } }
  },
  "documents":   { "home": { "type": "home" }, "faq": { "type": "faq" }, "global": { "type": "global" } },
  "collections": { "campaign-": { "type": "campaign", "max": 20 } }
}
```
`documents` 的鍵是固定文件（註冊時自動建立缺的空草稿）；`collections` 的鍵是前綴，商家可在後台建立 `campaign-spring` 之類的文件（受 `max` 限制）。`types[*].schema` 是 JSON Schema（draft 2020-12 的受限子集）。

**允許的詞彙（allowlist；未列者一律拒絕）**
- 結構：`type`（`object`｜`array`｜`string`｜`integer`｜`number`｜`boolean`）、`properties`、`required`、`items`、`additionalProperties`（**每個 object 必須是 `false`**）、`enum`、`const`、`default`、`title`、`description`、`$defs`、`$ref`（**只允許 `#/$defs/…` 的本地參照**）、`oneOf`（**只允許「區塊聯集」**：每個分支都是 object 且 `properties.type.const` 為字串，與現行 sections 慣例相同）。
- 約束：`minLength`／`maxLength`（**每個 string 必須宣告 `maxLength`**，一般 ≤ 2,000；`richtext` ≤ 50,000）、`minimum`／`maximum`、`minItems`／`maxItems`（**每個 array 必須宣告 `maxItems` ≤ 200**）、`pattern`（長度 ≤ 200；Go `regexp` 是 RE2，無災難性回溯，但仍限制長度）、`format`（只允許 `date`、`date-time`）。
- 編輯器提示 `x-editor`（string 專用）：`text`（預設）、`textarea`、`richtext`、`image`、`link`、`color`、`select`（搭配 `enum`）；`x-editor` 於 array：`list`（一般陣列，預設）、`sections`（區塊聯集）；`x-editor-order`（整數，既有）；`x-help`（≤ 200 字，純文字）。**`link`**：值必須是 `https://…`、`http://…`、`mailto:`、`tel:`、站內相對路徑（以 `/` 或 `#` 開頭）；拒絕 `javascript:`、`data:`、`vbscript:`、含控制字元；上限 2,048 字元——**這是新的檢查**（現行程式碼沒有 `link` 種類），實作為 `CheckLinkFields`，與 `CheckImageFields` 同構。`image`：沿用既有圖片網域規則。
- **禁止**：`$id`、`$anchor`、`$dynamicRef`、`$dynamicAnchor`、遠端或相對 `$ref`、`$schema`（除非等於 draft 2020-12）、`patternProperties`、`propertyNames`、`unevaluated*`、`dependent*`、`if/then/else`、`allOf`／`anyOf`／`not`、`contentEncoding`／`contentMediaType`、其他 `format`、任何未列出的 `x-*`、重複的物件鍵。

**schema 本身的驗證（註冊時依序執行，任何一步失敗回 422 並附 pointer，不寫入）**
1. 請求主體 ≤ 128 KiB（比一般 1 MiB 更小）；JSON 解析時**拒絕重複鍵**並在解析前限制巢狀深度 ≤ 12。
2. 結構上限：深度 ≤ 8（`properties`／`items` 的層數）、節點總數 ≤ 2,000、每個 object ≤ 50 個屬性、`enum` ≤ 100 個值（每值 ≤ 200 字元）、`types` ≤ 50、`documents` ≤ 100、`collections` ≤ 20、字串型 `title`／`description` ≤ 200／500 字元。
3. 詞彙檢查：以上 allowlist 走一遍（含「每個 object `additionalProperties:false`」「每個 string 有 `maxLength`」「每個 array 有 `maxItems`」）。
4. `$ref` 圖檢查：只允許本地 `$defs`、偵測環（DFS），並計算**展開後節點數**（`$ref` 展開不得讓有效節點數超過 5,000，防止 billion-laughs 式放大）。
5. 以既有 `cms.CompileSchema`（draft 2020-12 metaschema）編譯。
6. **自洽檢查**：對每個 type 產生預設 hydrate 結果並驗證通過（schema 必須接受自己的預設值），整體有 2 秒的 context 預算。
7. 鍵名檢查：`documents` 與 `collections` 的鍵符合 `key` 規則，沒有衝突，且引用的 `type` 存在。
8. 文字欄位（`title`／`description`／`x-help`）在後台一律以純文字呈現（`textContent`），schema 不能夾帶 HTML——這由後台核心保證（template 後續工作 T2），API 另外拒絕含 `<` 的這些欄位（保守起見）。

### D4：schema 版本與既有內容

- 每次**內容不同**的註冊產生新版本（`sha256` 不同）。**註冊從不改寫任何文件**。
- 註冊時對該店每份文件，以新 schema 驗證 `draft_json` 與 `published_json`，並計算「被新 schema 丟棄的鍵路徑」（hydrate 會丟棄的鍵）：
  - 全通過、無移除：`ok`。
  - 有鍵路徑將被丟棄（欄位移除或改名）：**警告**，需要 `confirm_removals=true` 才能註冊；回報列出每份文件與路徑。
  - 不再通過驗證：`needs_attention=true`（後台顯示；草稿必須修到通過才能再儲存或發佈；**已發佈快照照常提供**）。
  - 文件的鍵不再被宣告：`declared=false`（`orphaned`）：仍保留、仍照常提供已發佈內容；後台提示；商家可手動下架或刪除。
- **永不遺失資料**：對任何「有移除」或「不再通過」的文件，在註冊交易內寫一筆 `kind='schema_snapshot'` 的 revision，保存當時的 `draft_json`（以及 `published_json` 若不同）。因為草稿儲存會以表單重建（只含新 schema 的欄位），舊欄位的值只存在於 snapshot 中，所以還原仍能找回。`schema_snapshot` 不受 20 筆限制（每文件保留最近 5 個 schema 版本）。
- 型別變更（例如 `string`→`integer`）視為「不再通過」處理。`enum` 縮小同。新增必填欄位：既有文件變 `needs_attention`（預設值可緩解：有 `default` 的必填欄位在 hydrate 後通過，這是建議作法，並在 template 的 `DESIGN-LAYER` 文件中寫明）。
- 回到舊 schema（以舊內容再次註冊）：產生新版本號，內容與舊版相同，因此重複註冊判斷用「與**目前作用中**版本的 sha256 比較」。

### D5：管理端 API 與權限

路徑前綴 `/api/v1/admin/shops/{shopID}/content`（守衛為商家層 RBAC）：

| 端點 | 權限 | 說明 |
| --- | --- | --- |
| `GET /schema` | `content.view` | 作用中 schema ＋ 版本 ＋ 各 type 的標題（供表單） |
| `PUT /schema` | `content.manage` | 註冊（D3），`dry_run`、`confirm_removals` |
| `GET /documents` | `content.view` | 列表：`key`、`type_key`、`title`、`status`、`needs_attention`、`declared`、`draft_rev`、`updated_at`、`updated_by` |
| `GET /documents/{key}` | `content.view` | `{draft, published, type, schema_node, draft_rev, status, …}` |
| `PUT /documents/{key}` | `content.edit` | 建立或更新草稿；**`If-Match: "<draft_rev>"`**（首次建立用 `If-None-Match: *`），不符回 412；驗證＋消毒（D6），回**實際存下的值**與新 `draft_rev`；只能建立被宣告的鍵或符合 `collections` 前綴的鍵 |
| `POST /documents/{key}/publish` | `content.publish` | 驗證草稿（對作用中 schema）→ 快照 → 寫 revision → 事件；body 可帶 `expected_rev` |
| `POST /documents/{key}/unpublish` | `content.publish` | 下架 |
| `DELETE /documents/{key}` | `content.publish` | 只允許 `collections` 的文件（已宣告的固定文件不可刪，只能清空） |
| `GET /documents/{key}/revisions` | `content.view` | 列表（不含內容） |
| `GET /documents/{key}/revisions/{rev}` | `content.view` | 讀取內容 |
| `POST /documents/{key}/revisions/{rev}/restore` | `content.edit` | 放回草稿（不發佈） |
| `POST /documents/{key}/preview-token` | `content.view` | 簽發預覽 token（D2）；`scope: "document"｜"shop"`（`shop` 讓站台的預覽模式能預覽所有草稿） |
| `GET/PUT /publish-hook`、`POST /publish-hook/test`、`POST /publish-hook/fire` | `content.manage` | D7 |

**權限節點**：新增 `content.view`、`content.edit`、`content.publish`、`content.manage`。授予：`super_admin`（`*`）全有；`merchant_owner` 全有；`editor` 只有 `content.view`、`content.edit`（與今天 `page.view/create/edit` 對等）；**`staff` 與其他角色預設不給**（seed 測試以相等比對斷言，同 `merchant-staff-role`）。`page.*` 在相容期內**等價**：相容端點仍以 `page.*` 守衛；seed 把 `page.*` 的授予複製成對應的 `content.*`（`page.view→content.view`、`page.create/edit→content.edit`、`page.publish/delete→content.publish`），自訂角色由 migration 同樣複製。清理階段（群組 10）才決定是否移除 `page.*`（Q）。`content.manage` 不給 editor 的理由：schema 註冊與 hook 是**整合動作**（同 `publishable_key.*`、`payment_credential.manage` 的立場），不是內容編輯。

**一個存取面的取捨**：`content.edit` 能在已宣告的鍵上寫任何通過 schema 的內容；因為 `richtext` 被消毒、`link` 被限制 scheme、`image` 被限制網域，剩餘的惡意面是**文字內容本身**（釣魚文案），那是商家內部權限問題，不是平台安全問題。

### D6：寫入閘門（重用現行消毒與檢查）

文件寫入（`PUT`）與還原後的儲存都走同一個函式 `content.Service.validateAndSanitize`，**是 `cms.validateAndSanitize` 的搬移與泛化**（輸入改為 schema 節點與 payload，不再綁 `Theme`）：`ValidatePayload` → `CheckImageFields` → **新增 `CheckLinkFields`** → `sanitizeRichText`（`richtext.SanitizeWithImagePrefixes`）→ 有改寫再驗一次。422 帶 JSON pointer，行為與今天一致（回應含實際存下的值，後台以回應重建表單）。發佈**不再消毒**（複製已消毒草稿），但**發佈前重新驗證**（schema 可能已變）。`cmd/sanitize-content` 改為走文件表。`allowlist.json` 仍是單一來源。

### D7：發佈 hook

**目的**：發佈後通知商家站台重建（例如 Cloudflare deploy hook），讓「建置時抓取」或「烘焙快照」的站台更新。**預設關閉**；每店一筆 `content_publish_hooks`：`shop_id`（唯一）、`url_ciphertext`（`cryptbox` 加密，**URL 本身是祕密**——Cloudflare deploy hook 的 URL 即憑證）、`host`（明文，供稽核與 allowlist）、`signing_secret_ciphertext`（伺服器產生、加密儲存）、`enabled`、`last_status`（`ok`｜`http_error`｜`timeout`｜`blocked`）、`last_http_status`、`last_attempt_at`、`consecutive_failures`。後台檢視 API 只回報 `host`、URL 的**遮罩**（`https://host/…末四碼`）與狀態，**絕不回完整 URL 或 secret**（設定為只寫；證明落在回應位元組的整合測試）。

**SSRF 與濫用防護（全部適用；出站請求只能走這一條程式路徑）**
1. **主機 allowlist**：平台層設定 `CONTENT_HOOK_ALLOWED_HOSTS`（逗號分隔，精確主機名或 `*.suffix`；**預設只含 Cloudflare 的 deploy hook 主機**，實作時以 Cloudflare 官方文件核對確切主機，例如 Pages 的 `api.cloudflare.com`）。商家自訂主機需平台管理員加入 allowlist。設定時與每次發送時都檢查。
2. URL 規則：只允許 `https`、埠 443（或明確允許的埠）、無 userinfo、無 fragment、長度 ≤ 512、主機不得是 IP 字面值。
3. **解析後再檢查 IP**：每次發送自己解析 DNS，拒絕任何落在私有、loopback、link-local（含 `169.254.169.254`）、CGNAT、multicast、保留區段的位址（IPv4 與 IPv6，含 IPv4-mapped）；並以**自訂 `DialContext` 只連線到剛驗證過的 IP**（防 DNS rebinding：驗證與連線之間不再次解析）。
4. **不跟隨重導向**（`CheckRedirect` 回 `http.ErrUseLastResponse`；3xx 視為失敗）。
5. **逾時與大小**：整體逾時 5 秒；只讀回應主體前 4 KiB 然後丟棄；**回應內容永不回傳給呼叫者或寫進日誌**（只留狀態碼與類別，避免把內網資訊吐回給設定 URL 的人）。
6. **簽章**：請求主體 `{event:"content.published"｜"content.unpublished", shop_id, key, rev, at}`（**不含內容**）；標頭 `X-Content-Timestamp`、`X-Content-Signature: sha256=HMAC(secret, timestamp + "." + body)`。（Cloudflare deploy hook 不驗簽也不看主體——簽章是給自訂主機的；設定畫面如實說明。）
7. **去抖動與限流**：發佈事件不直接送出，而是「安排一次」：同一商家在 30 秒內的多次發佈合併成一次呼叫（`CONTENT_HOOK_DEBOUNCE`）；每商家最短間隔 60 秒、每日上限 30 次（`CONTENT_HOOK_DAILY_CAP`）；手動 `fire` 與 `test` 另有每商家每分鐘 3 次的限流。限流用 `ratelimit.Limiter`（Redis），**Redis 故障時 fail open 但仍受 DB 欄位 `last_attempt_at` 的最短間隔約束**。
8. **執行模型**：訂閱 `ContentPublished`／`ContentUnpublished` 的 handler **只入隊**（同 `notify.Service`：有界 channel、固定 worker、不做 I/O、滿了丟棄並 log），**不拖慢發佈請求**；worker 使用與請求無關的 context；失敗最多重試 2 次（指數退避，只重試逾時與 5xx）；**不持久化**（行程重啟可能丟失一次觸發——「手動重建」按鈕是備援；失敗連續 5 次自動停用並在後台顯示）。
9. 稽核：`hook.set`／`hook.test`／`hook.fire` 寫 `content_audit_log`（只記主機與結果類別）。日誌**不記 URL**。
10. 誰能設定：只有 `content.manage`；allowlist 才是真正的安全邊界（即使 owner 惡意，也只能打 allowlist 上的主機）。

### D8：消費模式與建議

| | 建置期抓取（烘焙進 HTML／資料檔） | 執行期抓取（瀏覽器載入時） |
| --- | --- | --- |
| 優點 | 首次繪製最快、無閃爍；不依賴 API 可用性；文字在 HTML 中（SEO 最好）；API 負載最低 | 發佈後立即可見（≤ `max-age`）；**不需要建置步驟也不需要 hook**，符合零建置範本；商家站台本來就在執行期呼叫 API 取得商品／購物車，沒有新增可用性相依 |
| 缺點 | 需要建置步驟（範本是零建置，要加一個建置期腳本）；發佈到上線有重建延遲（約 1–2 分鐘）；需要 hook；hook 失敗時內容過期 | 首次繪製要等一個請求（用預設文案先畫，再替換）；SEO 較弱（範本是 hash 路由的 SPA，本來就弱）；內容 XSS 風險落在執行期（但有消毒與 CSP） |

**建議：執行期抓取為預設，搭配設計層的靜態預設文案；發佈 hook 與建置期快照是選用的最佳化。** 具體：站台的 `content.js`（設計層）保留預設文案；鎖定核心的 `ShopCommerce.content` 在載入時呼叫 `GET /shop/content?include=content`（一個請求、`ETag`／304），把結果覆蓋在預設值上，失敗時靜默使用預設值（頁面永遠可用）。若某店很在意 SEO 或首次繪製，才啟用 hook 並在建置指令中跑核心提供的 `fetch-content` 腳本產生 `content.snapshot.json` 作為第一次繪製的資料。理由：範本是零建置、商家期待「按發佈就看得到」、且店面已經依賴 API。

### D9：安全

- **消毒重用**：`cms/richtext` 與 allowlist 單一來源；寫入端是權威。商家站台是第二道防線（template 後續工作 T4：核心提供 `ShopCommerce.content.richText(html)`，依同一份 allowlist 產生 DOM；設計層**不得**用 `innerHTML`，既有守門員已禁止）。`web/lib/sanitize.ts` 隨 `web/` 刪除，所以這個職責轉到範本核心。allowlist 到範本的同步以雜湊檢查（CI 比對兩邊 `allowlist.json` 的 sha256）。
- **圖片網域**：沿用 `CheckImageFields`／`ImageURLAllowed`；圖片上傳沿用 `POST …/media/content-images`（`purpose: content_image`），權限改為 `content.edit`（相容期也接受 `page.edit`）。
- **商家站台上的 XSS**：內容只以文字節點或消毒後的 DOM 片段呈現；`link` 值只在 `href` 且已通過 scheme 檢查（核心再檢查一次）；`image` 值只在 `src`，CSP `img-src` 只放行影像網域；站台 CSP 不放 `'unsafe-inline'` script；內容永遠不進 `<script>`、事件屬性或 `style`。
- **稽核**：D1 的 `content_audit_log`；所有寫入記 actor。**日誌**只准帶 `shop_id`、`doc_key`、`rev`、`schema_version`、動作與結果類別，不記內容、不記 hook URL（以 slog buffer 的 PII 整合測試證明）。
- **限流**：公開讀取（D2）、管理端寫入（每商家＋呼叫者，沿用 `AdminInviteRateLimit` 的形狀：儲存每分鐘 60、發佈每分鐘 20、schema 註冊每小時 20、`dry_run` 每分鐘 10）、hook（D7）。
- **租戶隔離**：新表登記 `tenantOwned`；公開端點在 tenant ctx 下查詢；所有以 key 為條件的查詢明寫 `shop_id`。預覽 token 帶 `shop_id`＋`aud=content-preview`，拒絕跨店重用（測試）。
- **DoS**：D3 的 schema 限制、文件 128 KiB／100 份、列表上界 2 MiB、validate 的 context 逾時 2 秒。
- **隱私**：內容文件是公開行銷內容，不應存放個資；文件 `title`／`meta` 不入日誌。

### D10：merchant template 後續工作（本 change **不實作**；都在 merchant-template repo，守門員流程之內）

**T1 設計層與契約**：`template.json` 的 `sites.storefront.designPaths` 加 `public/content.schema.json`；`public/content.js` 保留為預設文案；`CLAUDE.md` 與 `admin/docs/DESIGN-LAYER.md` 補充內容文件與 schema 的規則（改 schema ＝ 改 `content.schema.json` ＝ 之後必須由人在後台觸發「同步內容結構」，AI 不得假設已註冊）。

**T2 後台核心（`admin/core/`、`admin/lib/`、`admin/ui/`，鎖定）**：
- `ctx.data`（皆先檢查 `content.view`）：`getContentSchema()`、`listContentDocuments()`、`getContentDocument(key)`（回 `{key, type, title, status, needsAttention, declared, draft, published, rev, schemaNode}`）、`listContentRevisions(key)`、`getContentRevision(key, rev)`。
- `ctx.actions`：`saveContentDocument(key, form, {rev})`（核心依 schema 在客戶端先驗證並收集、送出、對 412/422 回 `ActionError`〔`kind: "stale"｜"invalid"`，`e.fields` 為 pointer→訊息〕、回傳**伺服器實際存下的值**）、`publishContentDocument(key)`／`unpublishContentDocument(key)`／`restoreContentRevision(key, rev)`／`deleteContentDocument(key)`（**皆由核心顯示確認對話框**，同其他不可逆動作；`restore` 說明「只放回草稿」）、`uploadContentImage(file, {onStage,onProgress})`（重用核心上傳管線與 HEIC 轉換）、`previewContentDocument(key)`（核心簽發 preview token 並開啟 `{siteOrigin}/?content_preview=<token>` 的新分頁；`siteOrigin` 來自核心設定）、`syncContentSchema({dryRun, confirmRemovals})`（讀取**同源**的 `/content.schema.json` 後 PUT；顯示 dry run 報告；移除警告需明確確認）。權限檢查在每個函式內，同 `ctx.actions` 現況。
- **核心擁有的 schema 驅動表單建構器**：`ctx.ui.contentForm(schemaNode, value, opts)` 回 `{el, read(), validate(), setErrors(fields)}`；`ctx.ui.contentField(schemaNode, pointer, value)` 回單一欄位控件，供設計層自行排版。控件由 `x-editor` 決定（`text`／`textarea`／`richtext`〔核心的最小編輯器，輸出會被伺服器消毒〕／`image`〔含上傳、網域提示〕／`link`／`color`／`select`／`list`／`sections`）。**驗證邏輯鎖在核心**：`required`、長度、`pattern`、`enum`、`link` scheme、`image` 網域（依 `media()` 事實）、陣列上限——設計層控制的只有**版面**（欄位分組、順序、區塊標題、說明文字來自 `copy.js`、`layout` 選項的欄位順序），**不能移除必填欄位**（必填欄位沒被 `contentForm` 渲染時，核心會自動補渲染，與商品表單「省略 title 會被核心驗證擋下」同一原則）。
- 路由（`core/routes.js`）：`content`（`/content`，`content.view`）、`contentDocument`（`/content/:key`，`content.view`）、`contentSchema`（`/content/schema`，`content.manage`）、`contentHook`（`/content/publish-hook`，`content.manage`）；`design-defaults/views/*` 提供預設畫面，`design/routes.js` 可覆寫；`lib/public-api.json` 與守門員 `check-design` 的 `ctx` 清單、`copy.js` 的 `content.*` 鍵、`core.manifest.json` 一併更新；`admin/e2e` 增加內容流程。
**T3 店面核心（`public/commerce/`，鎖定）**：`ShopCommerce.content.{all(), get(key), onChange}`（ETag、失敗退回 `SHOP_CONTENT` 預設、不拋出）；`check-layers` 允許其 `fetch`；`public-api.json` 新增。
**T4 渲染安全**：`ShopCommerce.content.richText(html)`（依 allowlist 產生 DOM）與 `ShopCommerce.content.link(value)`（再檢查 scheme）；allowlist 同步與雜湊檢查；設計層守門員規則維持禁止 `innerHTML`。
**T5 發佈與預覽**：選用的 `scripts/fetch-content.mjs`（建置指令用；鎖定）；`?content_preview=<token>` 預覽模式（核心讀 token、以 `X-Preview-Token` 呼叫預覽端點、頁面顯示「預覽中」橫幅）；`docs/CORE-CHANGELOG.md` 標示 `Design-layer action`（設計層需要提供 `content.schema.json`）；`sync-core` 與 `new-merchant.mjs`（新店產生預設 schema 與註冊指引）；`merchant-onboarding` skill 同步。

## Risks / Trade-offs

- **範圍大**：以群組切開，每群組可獨立部署；群組 1 已由退役 change 先行交付。
- **雙寫期**：群組 4 的雙寫可能在極端情況下造成新舊不一致；以「新表為主，同交易寫回舊欄位」與回歸測試控制；清理階段才拿掉。
- **schema 由 AI 可編輯的檔案提供**：以人工觸發註冊＋dry run＋移除警告緩解；若使用者希望 CI 自動註冊，需另行決定（Q3）。
- **內容為公開資料的假設**：publishable key 是識別碼不是憑證，任何知道 key 的人都能讀已發佈內容；設計上內容必須是可公開的。草稿永遠不經公開端點。
- **hook 不持久化**：行程重啟可能丟一次觸發；手動重建是備援。
- **`link`／`select` 等新 `x-editor` 種類**需要後台 `schemaform.js`（保留的編輯器）同步支援，否則平台後台仍可用文字框降級。

## Migration Plan

見 D1「遷移路徑」。部署順序：群組 1（隨退役 change）→ 群組 2–3（新表與回填，無行為改變）→ 群組 4（切換，雙寫）→ 群組 5–9（新 API、hook、後台）→ 觀察期後群組 10（清理，需 Q 的決定）。群組 11（template）在 merchant-template repo 獨立進行。

## Decisions record（原 Open Questions；**DECIDED 2026-11-25，decided by the user：「照建議調整」**，採用各題的建議答案；僅 Q9 仍開放）

1. **Q1 `page.*` 權限：DECIDED 2026-11-25（decided by the user）**——新增 `content.*`，`page.*` 在相容期間保留為**等價別名**（兩者互通，持有其一即可通過對應的相容端點）；`page.*` 日後與相容端點一併退役——那是另一次、需要自己 v1 例外的決定，不在本 change 內（見 Q2）。與 `retire-hosted-storefront-and-themes` 保留 `page.*` 的決定一致。
2. **Q2 清理與相容端點：DECIDED 2026-11-25（decided by the user）**——保留相容端點（薄轉接層）；**不做第二次 v1 例外**；`Shop.content_json` 保留為由 `global` 文件投影的**唯讀值**（`Page` 回應同理），只移除 `pages` 表的寫入路徑與舊儲存，不移除任何 v1 欄位或路由。
3. **Q3 schema 註冊：DECIDED 2026-11-25（decided by the user）**——只能由人經 admin API 註冊（先 dry run）；**不**由 CI 或 AI 編輯服務自動推送，因為 AI 不得改變內容的形狀。要自動化需另開 change 與專用憑證。
4. **Q4 `content.manage` 不拆**：DECIDED 2026-11-25（decided by the user）。
5. **Q5 `editor` 有 view 與 edit、沒有 publish**：DECIDED 2026-11-25（decided by the user）；商家要放寬由自訂角色處理。
6. **Q6 公開快取 `max-age=60`、`stale-while-revalidate=300`**：DECIDED 2026-11-25（decided by the user）。
7. **Q7 版本歷史保留 20 筆發佈（`schema_snapshot` 另計）**：DECIDED 2026-11-25（decided by the user）。
8. **Q8 每店 100 份文件／2 MiB 已發佈總量，可由平台設定覆寫**：DECIDED 2026-11-25（decided by the user）。
9. **Q9 Cloudflare deploy hook 的確切主機：DECIDED 2026-11-25（查證於官方文件，decided by the user 的要求）**——預設 allowlist ＝ `api.cloudflare.com`，且只放行 `/client/v4/workers/builds/deploy_hooks/<id>` 與 `/client/v4/pages/webhooks/deploy_hooks/<id>`；證據與文件網址見下方「第三切片的決定」。
10. **Q10 內容預覽 token TTL 30 分鐘**（與 `PREVIEW_TOKEN_TTL`＝10 分鐘分開設定）：DECIDED 2026-11-25（decided by the user）。
11. **Q11 排程發佈：~~第一版不做~~ → 2026-11-25 由使用者推翻，進本 change**（DECIDED 2026-11-25，decided by the user；語意見 D11）。

**仍開放**：無（Q9 已於第三切片查證並決定）。


## 第三切片的決定（發佈 hook、排程發佈、稽核清除；DECIDED 2026-11-25，decided by the user）

使用者 2026-11-25 明確要求依序做：①發佈 hook ②**排程發佈** ③稽核清除 ④後台內容畫面（④ 在下一個 task、於 platform-admin 的 core/design 分流內做，本切片只提供它需要的 API 並寫成文件）。**Q11（「第一版不做排程發佈」）因此被使用者本人推翻**：DECIDED 2026-11-25（decided by the user）——排程發佈進本 change，見 D11。

### Q9 的答案（DECIDED 2026-11-25；查證於官方文件）

Cloudflare 的兩種 deploy hook 都是對 **`api.cloudflare.com`**（埠 443、`POST`、不需 `Authorization` 標頭，URL 內的識別碼就是憑證）送出：

| 產品 | URL 形狀 | 官方文件 |
| --- | --- | --- |
| Workers Builds | `https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/<DEPLOY_HOOK_ID>` | https://developers.cloudflare.com/workers/ci-cd/builds/deploy-hooks/ 與 https://developers.cloudflare.com/changelog/post/2026-04-01-deploy-hooks/（文件原文：`curl -X POST "https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/<DEPLOY_HOOK_ID>"`；「No `Authorization` header is needed. The unique identifier embedded in the URL acts as the authentication credential.」；回應含 `build_uuid`；限流為每 Worker 每分鐘 10 次、每帳號每分鐘 100 次；相同 hook 在前一次 build 尚在 `queued`／`initializing` 時再觸發不會重複建置，回 `already_exists: true`） |
| Pages | `https://api.cloudflare.com/client/v4/pages/webhooks/deploy_hooks/<ID>` | https://developers.cloudflare.com/pages/configuration/deploy-hooks/ ——**誠實記錄**：這一頁只在儀表板截圖裡展示 URL（「You will see both the URL as well as the POST request snippet available to copy」），文字沒有印出路徑；`/pages/webhooks/deploy_hooks/` 這個形狀是 Pages 長期使用的形狀（來自先前對儀表板的了解，不是本次文件文字），所以**上線時由操作員貼上儀表板給的 URL，dry run 通過才算數**；若 Cloudflare 改了形狀，預設 allowlist 會拒絕它（失敗是安全的方向），只需更新 `builtinHosts` 的路徑樣式 |

**預設 allowlist 因此恰好是一個主機：`api.cloudflare.com`，且只放行上述兩個路徑樣式**（`/client/v4/(workers/builds|pages/webhooks)/deploy_hooks/<8–128 位 [A-Za-z0-9_-]>`，無 query）——`api.cloudflare.com` 是整個 Cloudflare API 的主機，只放主機就會讓商家的 hook 能指向該 API 的任何路徑。環境變數 `CONTENT_HOOK_ALLOWED_HOSTS`（逗號分隔，精確名稱或 `*.suffix`）**只能新增**操作員核准的主機（對它們不限路徑），不能移除內建項，也不能放寬位址檢查（私有／loopback／link-local／中繼資料位址永遠被擋）。測試與 e2e **從不呼叫真實的 Cloudflare**（`httptest` 與假解析器）；e2e 為了打本機 stub 接收器，額外有 `CONTENT_HOOK_DEV_INSECURE=1`（允許 http、任意埠與 loopback，但**主機仍須在 allowlist**；`APP_ENV=production` 時設定檔拒絕啟動）。

### D7 的修訂（發佈 hook 的實作決定）

1. **去抖動狀態持久化在 `content_publish_hooks` 的 `pending_*` 欄位**，不再只在記憶體（design D7 原文接受「行程重啟丟一次觸發」）。理由有二：①排程發佈在**另一個行程**（`cmd/content-scheduler`，一次性執行後離開）裡發佈，記憶體中的去抖動計時器在它結束時就消失；②多個 API 實例時，記憶體去抖動會各送一次。做法：`Handle` 仍**只入隊**（有界 channel，滿了丟棄並 log，不做 I/O，所以不拖慢發佈請求）；背景 writer 把「待送」記到該店的 hook 列（沒有啟用中的 hook 的店什麼都不寫）；flusher 每 5 秒找「安靜 ≥ 30 秒」（或自第一次起 ≥ 5 分鐘 `CONTENT_HOOK_MAX_WAIT`，防止連續發佈餓死）的列，以**條件式 UPDATE 認領**（`pending_since` 比對 ＋ 影響列數）後才送，所以多實例與一次性行程不會重複呼叫。
2. **最短間隔 60 秒、每日上限 30 次**都存在 DB 欄位（`last_attempt_at`、`cap_day`＋`cap_count`，UTC 日），**不依賴 Redis**（design 原寫 Redis 限流＋DB 最短間隔；每日上限放 DB 讓 Redis 故障時仍然有效、且可用假時鐘測試）。最短間隔未到：觸發**保留為待送**、不丟棄；每日上限用完：**丟棄**該次並記為 `rate_limited`（不算失敗、不累積失敗計數）。手動 `fire` 與 `test` 另有每商家每分鐘 3 次（Redis 限流，fail open，與 DB 的上限疊加）。
3. **重試**：逾時、5xx、429 才重試，最多 2 次（退避 2 秒、4 秒）；4xx（含 3xx 因不跟隨重導向而視為失敗）不重試。**連續 5 次失敗自動停用**（`disabled_reason = consecutive_failures`，狀態 `failed: true`，寫 `hook.disable` 稽核），這就是「最終失敗狀態」；手動重新啟用（`PUT {enabled:true}`）或換 URL 會清除計數。
4. **簽章 secret 的顯示**：design 寫「絕不回 URL 或 secret」。secret 若從不可讀，簽章就沒有任何接收端能驗證。決定：secret 由伺服器產生並加密儲存，**只在 `POST …/publish-hook/signing-secret`（輪替）的回應中顯示一次**（`Cache-Control: no-store`），狀態與 PUT 回應永遠不含；URL 則**永不**可讀回。Cloudflare deploy hook 不驗簽（設定畫面如實說明）。
5. **不在 schema 註冊時觸發**：design D7 的事件只有 `content.published`／`content.unpublished`，所以 `ContentSchemaRegistered` 不觸發 hook（schema 改動伴隨設計層重新部署，那個部署本身會重建）。
6. **`hook.test` 的語意**：單次嘗試、不影響失敗計數、不消耗每日上限、不更新 `last_attempt_at`；`PUT …?dry_run=true` 只驗證 URL 形狀與 allowlist，不儲存也不呼叫。
7. 稽核動作新增 `hook.delete`、`hook.disable`、`hook.secret_rotate`（`content_audit_log.action` 沒有 CHECK）；detail 只有主機、結果類別、HTTP 狀態碼、嘗試次數、合併數。

### D11：排程發佈（新；DECIDED 2026-11-25）

**語意（選「綁定草稿版本」，不選「發佈當時的草稿」）**：排程記錄 `(shop_id, doc_key, draft_rev, publish_at)`。**到點時只在文件的 `draft_rev` 仍等於排程時的值才發佈**，用的就是手動發佈的 `expected_rev` 樂觀鎖（`content.Service.Publish(…, &draft_rev, …)`）。理由：商家排程的是「我審閱過、決定在 X 時發佈的那一版」；若允許排程之後的編輯被自動發佈，一次沒審閱過的修改會在凌晨三點上線，而編輯者也許以為只是在改下一版草稿。因此：
- **之後任何草稿儲存（含 `restore`、相容端點寫入）立即讓排程變為 `superseded`**（同交易，原因 `edited`；顯式可見、不是等到點才發現），到點的檢查是權威的後盾。要發佈新版本就重新排程（`If-Match` 帶現在的 `draft_rev`）。
- 手動發佈同一版草稿 → 排程 `superseded`（`published_manually`）；刪除文件 → `cancelled`（`document_deleted`）。下架不影響排程。
- 每份文件同時至多一筆 `pending` 排程（部分唯一索引 `UNIQUE(shop_id, doc_key) WHERE status = 'pending'`）；改期 = 同一端點再 PUT（更新該筆的 `publish_at`，並以請求的 `If-Match` 重新綁定到**現在的** `draft_rev`——這是明示的重新確認）；取消 = DELETE。
- **到點重新驗證**：發佈路徑本來就對作用中 schema 重新驗證草稿（schema 可能在排程之後被註冊新版本）。驗證失敗 → 排程 `failed`（`failure_code = invalid`，附最多 5 個 JSON pointer）、文件 `needs_attention = true`、寫 `doc.schedule_failed` 稽核、**不發佈**、不重試（內容問題不會自己好；修好草稿後重新排程）。
- 時間：`publish_at` 以 `timestamptz`（UTC）儲存，API 接受帶偏移的 RFC 3339，**無偏移一律 422**（不猜時區）；進位到下一個整分鐘（粒度一分鐘，回應回傳實際儲存值）；最短提前量 2 分鐘（`CONTENT_SCHEDULE_MIN_LEAD`）、最遠 90 天（`CONTENT_SCHEDULE_MAX_AHEAD`）。**時鐘權威是資料庫**：排程器以 DB 的 `now()` 決定「到點」，不用各行程的牆上時鐘，所以多機時鐘偏差不會讓某台提早或延後發佈；建立排程時的最短提前量檢查用 API 行程的時鐘（誤差只影響 2 分鐘邊界）。
- **錯過視窗**（API 或排程器停機）：排程器每次執行處理所有 `publish_at <= now` 的 `pending`；**遲到在寬限內（`CONTENT_SCHEDULE_GRACE`，預設 1 小時）照常發佈**（`late_seconds` 記在稽核）；**超過寬限 → `missed`，不發佈**，寫 `doc.schedule_missed` 稽核。理由：「週五 18:00 開賣」的內容在週一才上線可能是錯的，由人決定是否補排比系統默默補發更安全。
- **冪等與並發**：認領 = 條件式 UPDATE（`status: pending → running`，影響列數 1 才處理），所以兩個排程器同時跑只有一個處理某筆；整輪另用 Postgres advisory lock（`pg_try_advisory_xact_lock`）讓第二個排程器**整輪跳過**（in-process ticker 與 cron 的一次性指令可並存）。`running` 超過 10 分鐘視為崩潰遺留，重置為 `pending`（再處理時 `published_rev == draft_rev` 且已發佈 → 記為 `done`／`already_published`，不重複寫 revision）。
- **權限與到點時的資格**：排程、改期、取消需 `content.publish`（`editor` 沒有）；列出與查看需 `content.view`。排程記錄 `created_by_user_id`，**到點時重新確認該使用者仍是該店成員且仍有 `content.publish`**（被停用、移出或降權 → `failed`／`permission_revoked`，不發佈）。發佈的 actor 記為排程者，稽核 detail 標 `scheduled: true`。
- **走同一條服務路徑**：排程器呼叫的就是 `content.Service.Publish`，所以 revision、稽核（`doc.publish`）、事件（`ContentPublished` → 渲染快取失效、發佈 hook）都與手動發佈相同。一次性指令行程需要 Redis（失效快取）：**Redis 不可達時指令在認領任何排程之前就失敗退出**（靜默留下過期快取比晚發佈更糟，排程維持 `pending` 下一輪再試）。
- **執行方式**：`cmd/content-scheduler`（一次性，適合 cron／Windows 工作排程，預設每分鐘；結束前 `Tick(force)` 送出累積的 hook）＋ API 內的選用 ticker（`CONTENT_SCHEDULER_ENABLED=1`，預設關閉，`CONTENT_SCHEDULER_INTERVAL` 預設 1 分鐘）。兩者呼叫同一個 `content.Scheduler.RunOnce`。
- **後台 API**（供下一個 task 的畫面使用）：`PUT/DELETE/GET /documents/{key}/schedule`、`GET /content/schedules`（本店即將發佈與近期結果）；文件列表與詳情多一個 `scheduled_publish_at`（pending 排程的時間或 `null`）。
- **資料保留**：`content_publish_schedules` 的已結束列保留 90 天（`CONTENT_SCHEDULE_RETENTION`），由 `cmd/purge-content-audit` 一併清除（同樣預設 dry run）。

### D12：稽核清除（`cmd/purge-content-audit`；DECIDED 2026-11-25）

`content_audit_log` 保留 180 天（`CONTENT_AUDIT_RETENTION`）。指令預設 **dry run**，`-apply` 才刪；分批刪除（每批 1000 筆）所以不會長交易；冪等（重跑只刪新過期的）；輸出只有**計數與時間範圍**（不印 id 以外的內容；稽核列本來就不含個資，但仍計數與回報）。同時清除超過保留期的已結束排程列。`-shop N` 可只處理一家。Makefile 目標 `purge-content-audit`（dry run）。repo 沒有排程器，需由操作員每日排程（見 `docs/deploy/content-store-operations.md`）。
