## Context（已對照程式碼與 template 驗證）

**現況（誠實版）**
- merchant template（`C:/Users/ks/Projects/merchant-template`，`template.json` version 0.3.0）：AI 只改設計層（`admin/design/**`、storefront 的 `designPaths`）；`node scripts/check-design.mjs --base <ref>` 是唯一守門員（exit 0／1／2，`--json` 可機讀）；`admin/docs/AI-EDIT-SERVICE-NOTES.md` 描述「每請求一個分支 `ai/<shop>/<request-id>`、預覽、核准後由服務合併、Audit」——**全是需求描述，AI edit service 不存在**，Audit 也沒有儲存處。
- 平台（本 repo）：沒有任何設計請求相關的表、端點或權限節點。`docs/content-schema.md` 已寫明「AI edit service 不得註冊 schema、不得持有 `content.manage`」——同一條精神延伸到本 change：服務不得持有核准權。
- 截圖工具已有雛形：`admin/e2e/parity.mjs`（`capture <dir>`／`compare <a> <b>`）以 Playwright 在 **390×844 與 1280×860**、owner 與 staff 登入、`fullPage: true` 拍每個主要視圖，等待 `networkidle`、無 spinner、`document.fonts.ready`、再 500 ms；`compare` 逐像素比（每通道差總和 > 6 算不同）並回報百分比與列範圍。它的檔頭寫明「**Throwaway stack only**」，登入用 e2e 的 demo 帳號（`demo-owner@example.com` 等，來自 `make seed-demo` 的合成資料）。這正好是 sandbox 資料集規則的現成基礎（D12）。
- 平台後台的認證是**使用者 JWT**（`aud=admin`，`internal/auth/jwt.go`），授權是 `rbac.Engine` 的商家層快照（`AuthzMW.RequireShopPermission(node)`，目標 shop 來自 URL）。**沒有任何「機器帳號」概念**；既有的機器對機器路徑只有 publishable key（識別碼，非憑證）與一次性 token（`admin_credential_tokens`，只存 SHA-256）。所以服務憑證是**新機制**（D8）。
- 媒體：`internal/media` 的 `ObjectStore`（`PresignPut`／`Get`／`Put`／`Delete`／`List`；**沒有 `PresignGet`**），S3 實作與 `FakeStore`／`NullStore`；`docs/media-r2-setup.md` 第 3 步把**整個 bucket 綁成公開自訂網域**——R2 沒有前綴層級的公開／私有，所以「在現有 bucket 放私有前綴」**做不到**（D5）。
- 慣例：新表必須分類（`tenantOwned` 或附理由的 `notTenantOwned`）；稽核表刻意**不設 FK**（`content_audit_log`：歷史要比帳號與文件活得久）；清除指令預設 dry run、`-apply`、`-shop`、分批、冪等、只印計數（`cmd/purge-content-audit`）；`If-Match`／ETag／412／428（`content_documents.go`）；`Idempotency-Key` 標頭 8–255 字元可印 ASCII、錯誤碼 `invalid_idempotency_key`／`idempotency_key_reused`／`idempotency_key_in_progress`（`httpapi/idempotency.go`）；列表為 `page`／`page_size` 加 `total`；錯誤 envelope `{"error":{code,message,details}}`；429 帶 `Retry-After`；路由表不得依設定而異（wiring-integrity ⑥），停用時回 503；v1 表面鎖只能新增行。**migrations 目前沒有任何 trigger**（已查 `api/migrations`），本 change 的 append-only trigger 是第一個。
- 範本的相容性機制：`docs/CORE-CHANGELOG.md` 的 `Design-layer action:` 行標示「這個 core 版本需要設計層跟著改」；`sync-core` 在這種版本會拒絕套用除非人工 `--ack-design-actions`。這是 D13 的「core 相容下限」的天然資料來源。

## Goals / Non-Goals

**Goals**：每一次經服務的重新設計都有一筆不可竄改的、可追溯到「誰說了什麼、改了什麼、長怎樣、誰核准、現在是哪一版」的紀錄；核准在密碼學上或至少在狀態機上**綁定**到被檢查與預覽過的那個 commit；商家能從歷史挑一版回去，且回去也是一筆新紀錄；紀錄由平台持有，與服務分離；隱私有明確規則。

**Non-Goals**：
- **不蓋 AI edit service**，也不規定它的內部（只定契約，D17）。
- **不做程式碼與 migration**（本 change 僅為提案）。
- 不保存完整 diff 或完整程式碼快照（git 已有，紀錄只存 SHA 與摘要）。
- 不做視覺回歸的完整保證（只涵蓋固定視圖，見「不解決的事」）。
- 不做多人會簽、不做 requester≠approver 的強制（單一 owner 的商家做不到）。
- 不改任何既有路由、欄位或權限語意。

## Decisions

### D1：誰是權威——平台持有紀錄，服務只是寫入者
服務可能被攻破、被重寫、被換掉；歷史必須比它活得久。所以紀錄在平台資料庫，服務透過受限憑證寫入（D8）。紀錄的**真實性分三級**，不假裝全部可信：①**平台自己產生的**（請求 id、時間、核准者、核准的 SHA、狀態轉移、tag 名稱）＝權威；②**服務回報的**（prompt、head SHA、diff 摘要、守門員結果、預覽網址、截圖）＝「服務如此聲稱」，平台驗格式與一致性但不能驗真；③**人在畫面上看到並按下的**（核准）＝以②為依據的人類決定。畫面必須標示哪些欄位是服務聲稱的。

### D2：資料模型（全部為提案，欄位與約束）
風格同 `content.go`／`media.go`：snake_case、`shop_id` 直接在每張租戶表、CHECK 約束、無個資欄位。

**`design_requests`**（`tenantOwned`）：
- `id` uuid PK（對外 id；也用於分支名 `ai/<shop>/<id>`）、`shop_id`、`UNIQUE(shop_id, id)`（供複合 FK）。
- `site` varchar(10) CHECK `IN ('storefront','admin')`；`kind` varchar(10) CHECK `IN ('change','revert')`。
- `status` varchar(20) CHECK 於九值（`drafting`、`previewing`、`awaiting_approval`、`approved`、`published`、`rejected`、`failed`、`cancelled`、`reverted`）。
- `requested_by_user_id` int（無 FK，同稽核慣例；伺服器驗證其為該店啟用成員且持 `design.request`，D9）。
- `prompt_text` text NULL（≤ 4000 字 CHECK `char_length`；保存期後遮蔽為 NULL）、`prompt_sha256` char(64)（永久保留，證明「當時說的是這句」）、`prompt_flags` text[]（`email`／`phone`／`card_like` 偵測結果，D12）。
- `base_sha`、`head_sha`、`candidate_sha`、`published_sha` char(40) CHECK `~ '^[0-9a-f]{40}$'`（後三者可為 NULL）；`branch` varchar(120) CHECK `~ '^ai/[a-z0-9][a-z0-9-]*/[0-9a-f-]{36}$'`。
- `core_version` varchar(20)（建立時 template 版本，如 `0.3.0`）、`design_floor_version`（D13）。
- `diff_summary` jsonb（≤ 16 KiB：`files_changed`、`additions`、`deletions`、`files:[{path,status,additions,deletions}]` ≤ 300 筆；**只有路徑與行數，沒有內容**）。
- `guard` jsonb（`passed`、`checked_sha`、`exit_code`、`rule_ids[]` ≤ 50、`violation_count`、`guard_version`、`checked_at`；不含 stderr 全文，因為它含檔案行內容片段）。
- `preview_origin` varchar(255) NULL（**只存 origin，如 `https://p-3f9a.preview.example`，CHECK 不含 `?`、`#`、路徑**）、`preview_registered_at`、`preview_expires_at`。**預覽網址短命，只記存在與到期，不記永久連結**。
- 核准：`approved_by_user_id`、`approved_at`、`approved_head_sha`、`approval_expires_at`、`publish_tag`（平台於核准時配發，`UNIQUE(shop_id, publish_tag)`）；拒絕：`rejected_by_user_id`、`rejected_at`、`reject_reason`（≤ 500）。
- 發佈：`published_at`、`published_tag`（＝`publish_tag`）、`deployment_id` varchar(100) NULL。
- 還原連結：`restores_request_id` uuid NULL、`reverted_by_request_id` uuid NULL；兩者皆為複合 FK `(shop_id, …) → design_requests(shop_id, id)`（**同店強制**）；CHECK `(kind='revert') = (restores_request_id IS NOT NULL)`。
- 失敗：`failure_code` varchar(40)、`failure_detail` varchar(300)（服務給的文字，**純文字顯示**）。
- 並發與冪等：`rev` int（＝最後事件序號，ETag 基礎）、`idempotency_key` varchar(255) `UNIQUE(shop_id, idempotency_key)`、`claimed_at`／`claim_expires_at`（D16 的認領租約）、`created_at`、`updated_at`。
- **部分唯一索引**：每 `(shop_id, site)` 至多一筆非終態請求（`status IN ('drafting','previewing','awaiting_approval','approved')`）。理由：每請求一個分支、發佈進同一個 `main`，並行請求必然互相過期；一次一筆最簡單，也讓核准的「現在是哪一版」無歧義。

**`design_request_events`**（`tenantOwned`，**只能追加**）：`id` bigserial、`shop_id`、`request_id`（FK，`ON DELETE CASCADE`，僅清除指令能刪）、`seq` int（`UNIQUE(request_id, seq)`）、`kind` varchar(30) CHECK 於固定集合（`created`、`claimed`、`head_updated`、`guard_run`、`preview_registered`、`screenshots_complete`、`submitted`、`approved`、`rejected`、`cancelled`、`guard_rechecked`、`published`、`failed`、`revert_requested`、`reverted_mark`、`expired`）、`from_status`／`to_status`、`actor_type` CHECK `IN ('service','user','system')`、`actor_user_id`、`actor_credential_id`、`head_sha`（事件當下）、`detail` jsonb（≤ 8 KiB CHECK `octet_length(detail::text)`；只放 id、計數、規則 id、原因碼，**不放 prompt、不放 stderr、不放 URL**）、`prev_hash`／`hash` char(64)、`created_at`。`hash = sha256(prev_hash ‖ canonical(shop_id, request_id, seq, kind, from, to, actor, head_sha, detail, created_at))`。

**`design_screenshots`**（`tenantOwned`）：`id` uuid、`shop_id`、`request_id`、`phase` CHECK `IN ('before','after')`、`site`、`view_key` varchar(40) CHECK `~ '^[a-z0-9-]+$'`、`viewport` smallint CHECK `IN (390, 1280)`、`role` CHECK `IN ('owner','staff','anon')`、`attempt` smallint、`commit_sha`（拍的是哪個 commit：before＝base、after＝head）、`object_key`、`content_type` CHECK `= 'image/png'`、`byte_size` CHECK `BETWEEN 1 AND 2097152`、`width`（CHECK＝viewport）、`height` CHECK `BETWEEN 1 AND 10000`、`sha256`（伺服器對存入位元組計算）、`diff_ppm` int NULL（after 對 before 的差異像素百萬分比，D14，選用）、`capture_manifest` jsonb（≤ 2 KiB：`tool`、`tool_version`、`dataset`（`sandbox-v1`｜`public-live`）、`api_origin_class`（`sandbox`｜`public`）、`captured_at`、`set_version`）、`purged_at` NULL、`created_at`。`UNIQUE(request_id, phase, role, viewport, view_key, attempt)`。

**`design_service_credentials`**（`notTenantOwned`，理由：認證時先以雜湊查找、之後才知道 shop，與 `admin_credential_tokens` 同；所有管理查詢顯式帶 `shop_id`）：`id`、`shop_id`、`name`、`key_prefix`（顯示用，前 8 碼）、`key_hash`（SHA-256，unique）、`allowed_cidrs` text[] NULL、`created_by_user_id`（平台管理員）、`created_at`、`expires_at`（CHECK `> created_at` 且 ≤ 建立後 366 天；預設 90 天）、`last_used_at`、`revoked_at`。

**`design_site_state`**（`tenantOwned`）：`UNIQUE(shop_id, site)`；`current_request_id`（最後一筆 published）、`current_tag`、`current_sha`、`core_version`、`design_floor_version`、`last_seen_main_sha`、`updated_at`。由服務回報（核准流程之外的 core 同步也會更新）。

**`design_untracked_changes`**（`tenantOwned`）：`commit_sha`（`UNIQUE(shop_id, site, commit_sha)`）、`committed_at`、`subject`（≤ 200，純文字；**不存作者姓名或信箱**，git 作者是個資）、`files_changed`、`additions`、`deletions`、`detected_at`、`acknowledged_by_user_id`／`acknowledged_at`（只能設一次）。

### D3：只能追加，誠實地說它防什麼
三層：①**應用層**：沒有任何更新或刪除事件的函式；請求列的「目前狀態」欄位只能由狀態機函式在**同一交易內**「寫事件＋更新投影」，且每個轉移是條件式 UPDATE（`WHERE status = $from`）加影響列數（與 `idempotency`／`point_transactions` 同一種仲裁）。②**資料庫層**：手寫 migration 加 trigger——`design_request_events`、`design_screenshots`（除 `purged_at`）、`design_untracked_changes`（除 ack 欄位）禁止 `UPDATE`／`DELETE`；請求列的不可變欄位（`id`、`shop_id`、`kind`、`site`、`requested_by_user_id`、`prompt_sha256`、`base_sha`、`created_at`、`restores_request_id`）禁止 UPDATE。**清除指令例外**：在交易內 `SET LOCAL design.retention = 'on'`，trigger 檢查該設定才放行。③**雜湊串**：事件的 `hash` 串接讓「事後插入、刪除、改寫中間一筆」可被偵測（`cmd/design-sweep -verify` 重算）。
**限制**：能直接改資料庫的人（DBA、持有 migration 角色的人）能關 trigger 並重算整條鏈——這防的是**應用程式 bug 與被攻破的應用路徑**，不是惡意的資料庫管理員；要防後者需把鏈頭雜湊定期寫到外部（記為後續，不在本 change）。trigger 是 repo 的第一個，所以 `down` migration 與測試（`INTEGRATION=1` 下嘗試 UPDATE／DELETE 並斷言失敗）必須明確。

### D4：狀態機
```
drafting ──(守門員過＋預覽登記)──► previewing ──(after 截圖齊全＋submit)──► awaiting_approval
   ▲                                   │                                         │  │  │
   └──────(新 head：服務重做)───────────┴──────────────(要求修改，回 drafting)──────┘  │  │
awaiting_approval ──(人：approve，綁 candidate_sha)──► approved ──(服務：guard_rechecked 通過＋published)──► published
awaiting_approval ──(人：reject)──► rejected         approved ──(逾時/重檢失敗)──► failed
任何非終態 ──(服務)──► failed；──(人，design.request)──► cancelled；awaiting_approval 逾 7 天 ──(sweep)──► failed(approval_timeout)
published ──(之後某筆還原發佈)──► reverted（見 D15）
```
- 終態：`rejected`、`failed`、`cancelled`、`reverted`；`published` 可被標為 `reverted`。
- **進入 `previewing`** 需：該 `head_sha` 的 `guard.passed = true` 且 `guard.checked_sha = head_sha`、預覽已登記（origin、到期）。
- **進入 `awaiting_approval`**（submit）需：同一 `head_sha` 下 **before 與 after 的完整截圖集都存在**（固定視圖集，D12）；預覽尚未過期且至少還剩 `DESIGN_MIN_PREVIEW_REMAINING`（預設 30 分鐘）。**截圖上傳失敗就不能送審，所以也不能核准**（D18）。submit 時伺服器把 `candidate_sha := head_sha` 鎖定。
- 進入 `awaiting_approval` 後，服務若要更新 head（商家要求修改）必須先走「回 drafting」事件，使 `candidate_sha` 作廢；任何先前的核准不存在（核准只能從 `awaiting_approval` 發生）。
- `approved` 後 `head_sha` **鎖死**（更新 head 回 409）；核准有效至 `approval_expires_at`（預設 1 小時，`DESIGN_APPROVAL_TTL`）。逾時 → 只能 `failed(approval_expired)`，要重做。

### D5：截圖儲存——獨立的私有 bucket
使用者提的「現有 R2 基礎設施的私有前綴」**在現有 bucket 上做不到**：`docs/media-r2-setup.md` 把整個 bucket 綁成公開自訂網域，R2 的公開存取是 bucket 層級的。所以選擇：**第二個 bucket（例如 `ecommerce-design-history`）、不綁自訂網域、不開 `r2.dev`、專用 API token（Object Read & Write，限該 bucket）**。程式重用 `media.S3Store`／`FakeStore`／`NullStore`，`ObjectStore` 增加 `PresignGet(ctx, key, ttl)`（additive；所有實作補上）。設定同「全有或全無」：`DESIGN_R2_BUCKET`（其餘沿用 `R2_ACCOUNT_ID`、`R2_ENDPOINT`；**憑證另設** `DESIGN_R2_ACCESS_KEY_ID`／`DESIGN_R2_SECRET_ACCESS_KEY`，因為 token 範圍限該 bucket，不能與公開圖片 token 共用）。未設定：路由照掛、截圖相關操作回 `503 design_storage_not_configured`（授權檢查在 503 之前）。
- **鍵**：`shops/{shop_id}/design/{request_id}/{screenshot_id}.png`。bucket 本身私有，鍵可猜也無妨，但仍含 uuid。
- **讀取**：只經 API：`GET …/screenshots` 為每張回 `url`（`PresignGet`，**5 分鐘**，`DESIGN_SCREENSHOT_URL_TTL`）與 `url_expires_at`；回應 `Cache-Control: no-store`。簽名網址不寫進日誌、不寫進任何資料表。
- **上傳**：服務把 PNG 位元組直接 `PUT` 給 API（不經 presigned PUT）：API 驗證後存入。理由：圖小（≤ 2 MiB）、服務是伺服器端、API 必須**自己解碼驗證**（防偽、防炸彈）、省掉 `incoming/` 與意圖表。實作要點：`http.MaxBytesReader`（2 MiB）、`image.DecodeConfig` 先驗尺寸（寬必須等於視口、高 ≤ 10,000、像素 ≤ 1280×10,000）、完整解碼並**重新編碼 PNG**（Go 的編碼器不寫入任何輔助區塊，自然去除 tEXt／eXIf 等中繼資料）、對**存入位元組**算 SHA-256；若服務附 `X-Content-SHA256`，必須等於**上傳位元組**的雜湊（傳輸完整性），不符 422。沿用 media 的處理併發信號量。
- **上限**：每請求 ≤ 80 個槽位（phase×角色×視圖×視口；重拍同一槽位不佔額度，超過 409 `slot_cap_exceeded`）、嘗試總數 ≤ 240（`DESIGN_MAX_SCREENSHOT_ATTEMPTS`，超過 409 `attempt_cap_exceeded`）、≤ 40 MiB（被取代但仍存放的 attempt 照算）；每店 ≤ 2 GiB（`DESIGN_SHOP_QUOTA_BYTES`），超過 409 `quota_exceeded`。
- 格式 PNG（無損，供像素比較與文字清晰度）；不轉 WebP。

### D6：標準化截圖
- **視口**：`390×844`（手機）與 `1280×860`（桌面），與 `parity.mjs` 一致；`fullPage: true`，高度上限 10,000 px（超過則截斷並在 `capture_manifest.truncated=true` 標示）。
- **固定視圖集（`set_version: 1`）**：
  - admin（沿用 `parity.mjs` 的視圖名稱）：owner——`login`、`home`、`orders`、`order-detail`、`products`、`product-edit`、`members`、`reports`；staff——`home`、`orders`；＝10 個（角色×視圖）× 2 視口 ＝ **20 張／階段、40 張／請求**。
  - storefront：以 anon 身分、公開資料——首頁、商品列表、商品詳情（固定一個 sandbox 商品）、購物車（以合成 session 加入一項）、內容頁（FAQ／故事若存在）；`checkout` 只截到「填表前」；**不截訂單確認頁、不登入**。具體頁面清單由 template 的擷取腳本宣告（後續任務），版本號隨清單變更遞增。
  - 視圖集版本記入 `capture_manifest.set_version`；before 與 after 必須同版本，否則 submit 被拒。
- **命名**：`{phase}-{role}-{viewport}-{view_key}`（如 `after-owner-390-orders`）；API 路徑由這些欄位決定，不接受自由檔名。
- **決定性**：固定視口與 `deviceScaleFactor: 1`、等 `networkidle`、無 `.spinner`／`.state--loading`、`document.fonts.ready`、再 500 ms（同 `parity.mjs` 的 `settle`）、關閉動畫（`prefers-reduced-motion` ＋ 注入 `*{animation:none!important;transition:none!important}`，**只在擷取環境**）、固定時區與語系、sandbox 資料集固定（D12）、遊標與焦點移除、時間類顯示以固定時鐘（Playwright `page.clock`）。同一 SHA 重拍應逐像素相同——這本身是擷取環境健康的測試（before 重拍與上一筆 after 的差異超過門檻＝環境漂移，記事件但不阻擋）。
- **像素差異分數（選用）**：服務可附 `diff_ppm`（after 對同槽 before 的差異像素數 / 總像素 × 10⁶；演算法同 `parity.mjs compare`：每通道差總和 > 6）。平台只存、不信、不用它做閘門；畫面顯示「差異 3.2%」輔助商家判斷「這次改動到底動了哪些畫面」。
- **在哪裡跑**：服務的預覽環境內（Playwright，`parity.mjs capture` 泛化而來，是 template 的後續任務）。**擷取用的堆疊與商家看的預覽分開**（D12）。

### D7：與 `parity.mjs` 的關係
`parity.mjs` 是「舊站 vs 新站」的人工對照工具（`Throwaway stack only`）。本 change 把它升級為**服務可呼叫、輸出規格固定**的擷取腳本（template 任務 T6），沿用其 `settle`、視圖清單與像素比較函式；**不改它現有的 CLI 行為**（它在鎖定的 `admin/e2e/`，改動是人工審查的核心變更）。人工 workaround（「不在範圍內的臨時辦法」）見「不解決的事」。

### D8：服務憑證與路由族
- 憑證：`dsk_` ＋ 32 bytes `crypto/rand` base32；**只在建立回應顯示一次**，DB 存 SHA-256（與 `admin_credential_tokens` 同；高熵隨機值不需要 argon2）。**每店一把**（爆炸半徑＝一家店；服務自己保管 N 把，這是它的秘密管理問題）。90 天到期（輪替＝建新的、舊的保留 7 天重疊後撤銷）、可立即撤銷（驗證路徑不快取，或快取 ≤ 30 秒並寫明）、`last_used_at` 記錄、選用 `allowed_cidrs`（服務出口 IP）。管理端點：`/api/v1/admin/design-service/credentials`（**平台層**，節點 `design_service.manage`，只有 `super_admin`；商家不能自己簽發，因為它是整合動作，同 `content.manage` 的立場）。
- 路由：機器端點在 **`/api/v1/design-service/shops/{shopID}/…`**，**不在 `/admin` 群組下**：那裡的中介層是 user JWT＋`rbac`＋Cloudflare Access＋admin 主機綁定，服務沒有 user 身分。新中介層 `DesignServiceAuth`：`Authorization: Bearer dsk_…` → 雜湊查找 → 憑證有效（未過期未撤銷）、CIDR 允許、**URL 的 `{shopID}` 必須等於憑證的 `shop_id`**（否則 404，不洩漏）、所有查詢顯式帶 `shop_id`。這條路徑不受 `AdminAccessMW`（Cloudflare Access）保護——runbook 建議營運者另外加一條只允許服務出口 IP／Access service token 的規則（部署層，非程式）。
- 限流：每憑證每分鐘 120 次一般寫入、截圖上傳每憑證每分鐘 60 次；每店每日建立請求上限 `DESIGN_DAILY_REQUEST_CAP`（預設 20，也是成本護欄）。

### D9：誰能做什麼（權限）
新節點（`PermissionCatalog`＋`roleDefs`；seed 測試以**相等比對**釘住集合，同 `merchant-staff-role`）：
| 節點 | 層 | 意義 | super_admin | merchant_owner | editor | staff |
| --- | --- | --- | --- | --- | --- | --- |
| `design.view` | 商家 | 看歷史、詳情、事件、截圖 | `*` | 有 | **有（只有這個）** | 無 |
| `design.request` | 商家 | 發起重新設計（服務在商家對話中代為建立）、取消請求 | `*` | 有 | 無 | 無 |
| `design.approve` | 商家 | 核准／拒絕 | `*` | 有 | 無 | 無 |
| `design.revert` | 商家 | 請求還原 | `*` | 有 | 無 | 無 |
| `design_service.manage` | 平台 | 簽發／撤銷服務憑證 | `*` | 無 | 無 | 無 |
理由：editor 只看——歷史是營運透明度（editor 已能讀訂單與顧客，不構成新的資訊面），但改外觀是品牌決定；`design.approve` 不給 editor，因為核准即「讓 AI 的改動上線」。服務建立請求時帶 `requested_by_user_id`，平台**驗證**該 user 是此店啟用中成員且 `Snapshot.Allows("design.request")`，否則 403——所以被攻破的服務**不能把請求掛在任意人頭上**（頂多掛在有權發起的 owner 名下）。`merchant_assignable` 不變（自訂角色預設 false）。

### D10：核准綁定 `head_sha`（TOCTOU）
- 核准端點本文必帶 `head_sha`、必帶 `If-Match`（ETag＝`rev`）。伺服器條件：狀態＝`awaiting_approval`、`body.head_sha == candidate_sha == head_sha`、`If-Match` 吻合；條件式 UPDATE `WHERE status='awaiting_approval' AND candidate_sha=$sha AND rev=$rev`＋影響列數；失敗回 409 `head_moved`／412。成功時寫 `approved_head_sha`、`approval_expires_at`、配發 `publish_tag`。
- **重放**：核准是單次消耗——只有 `awaiting_approval → approved` 一條路；重送同一請求（同使用者、同 SHA）在已核准時回 200（同一結果，便於網路重試），其他情況 409；`published` 的條件式轉移（`WHERE status='approved' AND approval_expires_at > now()`）使一次核准只能換一次發佈。
- **發佈前重檢**：`published` 事件被接受的條件是：已核准未逾期、存在 `guard_rechecked` 事件滿足 `head_sha == approved_head_sha` 且 `passed` 且發生於核准**之後**、本文的 `published_sha == approved_head_sha`、`tag == publish_tag`。服務有義務在發佈前 `check-design --base` 於**該確切 SHA**重跑（AI-EDIT-SERVICE-NOTES 第 5 步已有此要求）。
- **誠實的邊界**：平台**無法**阻止一個握有 git 寫入權的服務直接推 `main`——API 的「不能發佈」只意味著它**無法讓平台承認一次未經核准的發佈**，而不是 git 層的物理阻擋。縱深：①服務的 git token 限該商家 repo、`main` 設分支保護（template notes 第 1 步已要求）；②**第三切片（選用；Q9 已定案：服務只推分支、由人合併，故**依決定省略、不建置**；若服務日後取得 `main` 寫入權須重新檢討）**：核准時平台簽發 **publish grant**（Ed25519 簽章，內含 shop、request、`approved_head_sha`、tag、到期），商家 repo 的部署 CI 在 `main` 推送時驗證 grant（公鑰由平台公開端點提供、固定在 repo 的鎖定檔）。這才讓「無核准就無法上線」成為物理事實。③`reconciliation`（D19）會把繞過者列為「未追蹤的變更」。

### D11：隱私——擷取什麼、從哪裡擷取
**規則（建議，需使用者確認）**：
- **R1** admin 截圖**只從 sandbox 堆疊擷取**，該堆疊以固定合成資料集（`sandbox-v1`，例如 `make seed-demo` 同等的虛構顧客、地址、訂單）運行；**絕不對真實商家的 live 後台截圖**。擷取腳本拒絕在 API origin 不在 sandbox 允許清單時啟動；平台於上傳時檢查 `capture_manifest.dataset == 'sandbox-v1'`（admin）否則 422 `live_data_capture_refused`。（平台無法驗真，這是契約＋縱深。）
- **R2** storefront 只從 **live 公開資料**擷取（商品與內容本來就公開），**不登入、不顯示任何顧客資料**；視圖集排除訂單確認頁與任何帶表單輸入值的頁面；購物車使用合成 session。
- **R3**（縱深）核心對會顯示個資的元素加上 `data-pii` 屬性（顧客姓名、電話、email、地址；template 後續任務），擷取工具在拍照前把它們以純色遮罩——若 R1 被誤違反，仍不外洩可讀個資。
- **R4** prompt 在入庫時伺服器端掃描：email、台灣手機（`09\d{8}`／`+8869\d{8}`）、疑似卡號（13–19 碼通過 Luhn）→ 以 `[redacted]` 取代並設 `prompt_flags`；聊天 UI 應提示「不要貼顧客資料」。**服務自己的聊天紀錄不在本平台範圍**（需在服務設計中同樣處理）。
- **R5** 預覽與簽名網址不入紀錄（D2）；日誌只含 id、計數、規則 id（沿用 content store 的規則，以 slog buffer 的整合測試證明）。
**需要使用者決定**：sandbox 堆疊要不要蓋（D12 選項）、prompt 是否全文保存、storefront 是否允許從 live 擷取。

### D12：sandbox 堆疊
選項：**A** 獨立的 sandbox 部署（另一個 API＋DB，`make seed-demo` 同等資料，固定帳號 owner／staff），預覽的靜態檔以同源代理指向它；**B** 在正式 DB 內建一個 sandbox 商家＋demo 帳號。**建議 A**：B 會在正式環境放一組已知密碼的帳號，擴大攻擊面。成本：一個小型 API＋Postgres（可按需起、拍完即毀，`parity.mjs` 本來就假設「throwaway stack」）。商家看的預覽（連真實 API，以商家自己登入）與擷取預覽（連 sandbox）是**同一份靜態檔、兩個部署**。此決定影響 storefront 以外的所有 admin 截圖，列為最高優先的待決事項。

### D13：還原與 core 相容下限
- **還原的語意**：「把 `design/`（及 storefront designPaths）的內容恢復成目標已發佈 tag 當時的狀態」＝服務產生一個**新 commit**，其設計路徑的樹等於目標 tag 的樹（不用 `git revert` 逐一反轉，避免對較新提交的衝突）；因此不會改寫任何歷史，且「還原」可以再被還原。
- **請求**：`POST /design/requests/{id}/revert`（`design.revert`）→ 建立新請求 `kind='revert'`、`restores_request_id={id}`、`drafting`，**事件記 `revert_requested`**；目標必須 `status IN ('published','reverted')`（曾發佈過）、同 `site`、有 `published_tag`；目標已是目前設計（`design_site_state.current_request_id`）→ 409 `already_current`；該站台已有非終態請求 → 409 `request_in_progress`。
- **core 相容檢查**：每筆請求記 `core_version`（建立時 template.json 版本）與 `design_floor_version`（當時該店「設計層必須至少是這個 core 世代」的下限＝服務依 `docs/CORE-CHANGELOG.md` 計算：目前版本以下、最新一個帶非 `none` 的 `Design-layer action:` 行的版本）。**規則**：`target.core_version >= current.design_floor_version`；否則 409 `revert_below_core_floor`（訊息說明：該設計早於一次需要設計層跟著改的 core 同步，還原它會讓畫面退回內建版本）。通過檢查後，守門員（以**目前** core）與預覽仍是最終裁判——檢查只是提前、可解釋的拒絕。
  這個解讀比「target.core_version 不得低於店鋪目前 core 版本」寬：後者等於禁止任何跨越 core 同步的還原，而多數 core 同步並不要求設計層修改。**需使用者確認採哪一種**（建議：下限制）。
- **內容儲存**：還原只動設計層檔案；`content_documents`（草稿、已發佈內容、版本歷史）、`content_schemas` 不受影響（它們是平台資料，不在 git）。但**設計與內容的相容性**（還原回的設計可能讀不到現在內容的新文件鍵）不由平台檢查：template 的內容讀取本來就「缺鍵回退到預設」（`ShopCommerce.content` 失敗回退），所以最壞是回到預設文案，不是壞掉；歷史詳情頁要明說這一點。若設計層需要 `public/content.schema.json` 變動，仍是人工註冊（`content.manage`），還原**不會**重新註冊 schema。
- **歷史連結**：還原請求 `published` 後，系統把目標與目標之後、還原之前的所有 `published` 請求標為 `reverted` 並寫 `reverted_mark` 事件，`reverted_by_request_id` 指向還原請求；歷史畫面以連結呈現。

### D14：事件與可讀性
每個狀態轉移與重要動作一個事件；畫面以事件時間軸呈現。事件 `detail` 只放 id、計數、規則 id、原因碼。`GET …/events` 分頁。

### D15：API 表面（草案；皆 additive）
**後台（人）** `/api/v1/admin/shops/{shopID}/design/requests`（RBAC＋Access＋主機綁定）：
| 方法 路徑 | 權限 | 備註 |
| --- | --- | --- |
| `GET /design/requests` | `design.view` | `site`、`status`、`kind`、`from`、`to`（日期，店鋪時區換算同報表）、`page`、`page_size`（≤ 100）；排序 `created_at DESC, id`；回 `{requests, page, page_size, total}`；不含 prompt（列表只回 `prompt_excerpt` ≤ 120 字） |
| `GET /design/requests/{id}` | `design.view` | 全欄位（見下 schema）；`ETag: "<rev>"`；`If-None-Match`→304 |
| `GET /design/requests/{id}/events` | `design.view` | 分頁，`seq` 升冪 |
| `GET /design/requests/{id}/screenshots` | `design.view` | 每張含 5 分鐘簽名 `url`；`?phase=`、`?all=true`（含舊 attempt）；`no-store` |
| `POST /design/requests/{id}/approve` | `design.approve` | `{head_sha, note?}`，`If-Match` 必帶（缺 428、不符 412）；回請求＋`publish_tag`＋`approval_expires_at` |
| `POST /design/requests/{id}/reject` | `design.approve` | `{reason?}` |
| `POST /design/requests/{id}/request-changes` | `design.approve` | `{note}`（回 drafting，使 candidate 作廢；note 以純文字交給服務） |
| `POST /design/requests/{id}/cancel` | `design.request` | 非終態才行 |
| `POST /design/requests/{id}/revert` | `design.revert` | `{note?}`；建立 revert 請求，201 回新請求 |
| `GET /design/state` | `design.view` | 各站台目前設計（tag、sha、發佈時間、core 版本、下限）＋未追蹤變更數 |
| `GET /design/untracked-changes`、`POST …/{sha}/acknowledge` | `design.view`／`design.approve` | |
**服務（機器）** `/api/v1/design-service/shops/{shopID}`（`Bearer dsk_…`）：
| 方法 路徑 | 備註 |
| --- | --- |
| `POST /requests` | `Idempotency-Key` 必帶（同 key 同內容回 200 既有；同 key 不同內容 422 `idempotency_key_reused`）；本文 `site`、`requested_by_user_id`、`prompt`、`base_sha`、`branch`、`core_version`、`design_floor_version`；超過每日上限 429；已有非終態請求 409 |
| `GET /requests?status=drafting&claimed=false` | 工作佇列（revert 請求由此被服務發現） |
| `GET /requests/{id}` | 唯讀狀態；**不回 prompt 全文**，只回 `prompt_sha256`、狀態、`candidate_sha`、核准欄位 |
| `POST /requests/{id}/claim` | 15 分鐘租約（`claim_expires_at`）；可續約 |
| `PUT /requests/{id}/head` | `{head_sha, diff_summary}`；只在 `drafting`（或 `previewing`／`awaiting_approval` 且帶 `reset:true` 回 `drafting`） |
| `POST /requests/{id}/guard-runs` | `{checked_sha, passed, exit_code, rule_ids, violation_count, guard_version}`；`phase: initial｜recheck` |
| `PUT /requests/{id}/preview` | `{origin, expires_at}` → `previewing`（條件見 D4） |
| `PUT /requests/{id}/screenshots/{phase}/{role}/{viewport}/{view_key}` | `Content-Type: image/png`、`X-Content-SHA256`、`X-Capture-Manifest`（base64 JSON）；同位元組重放 200；不同位元組新增 `attempt`（舊的保留） |
| `POST /requests/{id}/submit` | → `awaiting_approval`（條件見 D4） |
| `POST /requests/{id}/published` | `{published_sha, tag, deployment_id?}`（條件見 D10） |
| `POST /requests/{id}/failed` | `{code, detail}` |
| `PUT /sites/{site}/state` | 回報 main sha、core 版本、下限 |
| `POST /untracked-changes` | 批次（≤ 100）；對帳工作的輸出（D19） |
**共通**：分頁 `page`／`page_size`；錯誤碼（snake_case，掛在既有 envelope）：`design_storage_not_configured`(503)、`invalid_state`(409)、`head_moved`(409)、`request_in_progress`(409)、`already_current`(409)、`revert_below_core_floor`(409)、`quota_exceeded`(409)、`slot_cap_exceeded`(409)、`attempt_cap_exceeded`(409)、`screenshots_incomplete`(409)、`guard_not_passed`(409)、`approval_expired`(409)、`approval_required`(409)、`preview_expired`(409)、`live_data_capture_refused`(422)、`invalid_screenshot`(422)、`requester_not_permitted`(403)、`precondition_failed`(412)、`precondition_required`(428)、`daily_cap_reached`(429)。
**OpenAPI**：約 24 條新路由與 schema（`DesignRequest`、`DesignRequestSummary`、`DesignEvent`、`DesignScreenshot`、`DesignState`、`DesignUntrackedChange`、`DesignServiceCredential` 等）；`v1-surface.txt` 以 `-openapi.update-surface` 重新產生，**只新增行**；`Idempotency-Key`、`If-Match` 與 `ETag` 回應標頭依既有形式宣告（它們會成為新行，不是改既有行）。

### D16：認領租約與工作發現
revert 請求由人建立，服務需要知道。選擇**拉取**而非平台呼叫服務的 webhook（避免平台新增另一條出站路徑與 SSRF 面，content hook 的出站防護已經很重）。服務輪詢 `GET /requests?status=drafting&claimed=false`，`claim` 以條件式 UPDATE（`claim_expires_at IS NULL OR < now()`）取得 15 分鐘租約；過期未續約的請求由 `design-sweep` 釋放或標 `failed(claim_lost)`。

### D17：給 AI edit service 的契約（有序清單；服務必須遵守）
1. 持有且只持有**該店的** `dsk_` 憑證；憑證與 git token 分開保管；憑證即將到期（<14 天）時請平台輪替。
2. 商家在對話中表達請求後：先確認該商家使用者持 `design.request`（呼叫 `POST /requests` 時平台也會驗），以 `Idempotency-Key` 建立請求，傳**原文 prompt**（入庫時平台會遮蔽個資）。若回 409 `request_in_progress`，告知商家先完成或取消目前的請求。
3. 取得 `base_sha`＝`main` 當下 SHA，建立分支 `ai/<shop>/<request-id>`（與平台記錄的 `branch` 一致）。
4. 每一輪 agent 動作後：`check-design --base <base_sha>`；失敗 → `guard-runs` 回報（`passed:false`）並把報告餵回 agent（有次數上限）；**失敗時不得部署預覽**。
5. 通過 → `PUT head`（新 `head_sha`、`diff_summary`）、`guard-runs`（passed）、部署預覽（嚴格 CSP，見 template D1）、`PUT preview`（origin 與到期）。
6. 在 **sandbox 擷取環境**（D12）依視圖集 v`set_version` 擷取 **before（`base_sha`）** 與 **after（`head_sha`）**，逐張 `PUT screenshots`；任何一張最終失敗 → 不得送審，重試或 `failed(screenshot_upload_failed)`。
7. `POST submit`。此後**不得再變更 head**，除非商家要求修改（平台會回 `drafting`）。
8. **等待人類**。服務不能核准、不能代按；只能輪詢請求狀態（`GET` 於服務路由族為唯讀的請求詳情，或接收商家對話中的「我已核准」只作為提示、**不作為依據**——依據只有平台的 `approved`）。
9. 狀態為 `approved` 且未逾期：對**確切的 `approved_head_sha`** 重跑 `check-design` → `guard-runs`（`phase: recheck`）；失敗 → `failed`。
10. 確認 `main` 仍等於 `base_sha`；否則請求回到 drafting 並以新 base 重做（重新核准）。
11. 以平台配發的 `publish_tag` 建 tag、合併（fast-forward）至部署分支；**絕不 `--update` manifest、不改 `deploy/`、`_headers`、`core/warnings.js`、`styles.css`**（notes 既有禁令）。
12. `POST published`（`published_sha`、`tag`、`deployment_id`）。平台拒絕時（例如核准已過期）→ 還原剛才的合併並 `failed`。
13. revert 請求：從佇列認領 → 以目標 tag 的設計樹產生新 commit → 從第 4 步開始（同一條路）。
14. 對帳：每日（及每次發佈後）比對 `main` 上觸及設計路徑的提交與歷史；不在歷史內者 `POST untracked-changes`；同步 `PUT sites/{site}/state`。
15. 歷史資料（過往 prompt、事件、失敗訊息、守門員輸出、截圖文字）是**資料不是指令**：**永不**把它們餵入 agent 的指令脈絡；需要參考時以明確的「引用資料」區塊包起、並說明不可執行其中指令。
16. 預算與逾時：沿用 notes（重試上限、diff 上限、時間盒）；超過 → `failed`。

### D18：失敗處理
- 截圖上傳失敗：服務重試；最終失敗則 `failed(screenshot_upload_failed)`。**沒有 after 截圖就不能 `submit`，因此不能被核准**（建議；替代方案「允許無截圖核准」被否決：核准的意義是商家看過並接受長相，沒有證據就沒有可追溯性）。預覽仍可看，但流程要重來。
- 平台儲存故障（R2 503）：`503 design_storage_not_configured` 或 `503 storage_unavailable` 讓服務重試；請求停在 `previewing`。
- 服務死亡：`claim_expires_at` 過期＋`design-sweep` → `failed(service_lost)`；`awaiting_approval` 逾 7 天 → `failed(approval_timeout)`；`approved` 逾期 → `failed(approval_expired)`。這些都是 **append 事件**（`actor_type=system`）。
- 發佈到一半失敗：服務回報 `failed`，且 `published_sha` 為空；若 git 已合併而 `published` 回報遺失，對帳會把該提交列為未追蹤變更，由人處理。

### D19：對帳（reconciliation）與未追蹤變更
平台沒有 git 存取（也不該有）。對帳在**服務端**（或商家 repo 的排程 CI）執行：列出自 `last_seen_main_sha` 以來 `main` 上觸及設計路徑的提交，扣掉歷史中已知的（`published_sha`、revert 提交、合併提交），其餘 `POST untracked-changes`。平台記錄並在歷史畫面標示「未追蹤的變更」（不含作者個資）；人可「確認已知悉」。平台側另有 `cmd/design-sweep`：逾時轉換、孤兒物件（物件存在但無列／列存在但無物件）dry run 報告、`-verify` 事件雜湊串驗證。

### D20：觀測與稽核
- 日誌：結構化、只含 `shop_id`、`request_id`、`status`、`kind`、規則 id、計數、錯誤類別；**不含 prompt、簽名網址、origin、diff 路徑**（以 slog buffer 整合測試證明，同 content store）。
- 指標（log-based）：各狀態計數、核准到發佈時間、截圖上傳失敗數、守門員失敗規則 top-N、未追蹤變更數、被拒的核准（`head_moved`、`approval_expired`）數。
- 歷史系統自身的稽核＝事件表本身；憑證的簽發、撤銷、使用錯誤（未知 key 的嘗試，限流後記 WARN）也寫 `design_request_events` 之外的 `design_service_credentials` 欄位（`last_used_at`、`revoked_at`）；憑證管理動作寫結構化日誌 `design credential audit`（只含 id）。

### D21：保存期限（建議值；需使用者確認）
| 資料 | 建議 | 理由 |
| --- | --- | --- |
| `prompt_text` | 365 天後遮蔽為 NULL（保留 `prompt_sha256`） | 最可能含個資的欄位；一年足以追溯客訴 |
| 已發佈請求的截圖（before＋after） | 365 天 | 還原選擇與客訴；成本低（D22） |
| 其他請求（rejected／failed／cancelled）的截圖 | 30 天 | 無長期價值 |
| 請求列與事件（無個資，只有 user id、SHA、規則 id） | 730 天 | 核准稽核；比 content 稽核（180 天）長，因為這是上線決定 |
| 未追蹤變更列 | 730 天 | 同上 |
| 服務憑證列 | 撤銷或過期後 90 天 | 稽核；雜湊本身無價值 |
| 版本預覽使用紀錄（D25） | 90 天（`DESIGN_VERSION_PREVIEW_RETENTION`） | 只用於限流與用量；非核准依據、無個資 |
清除：`cmd/purge-design-history`（dry run 預設、`-apply`、`-shop N`、`-all-for-shop N`〔店家下架：匯出後刪光〕、分批 1000、冪等、只印計數與時間範圍；清除時刪 R2 物件，**物件刪除在 DB commit 後 best-effort**，同 media D8，孤兒由 `design-sweep` 報告）。設定：`DESIGN_PROMPT_RETENTION`、`DESIGN_SCREENSHOT_RETENTION`、`DESIGN_SCREENSHOT_RETENTION_UNPUBLISHED`、`DESIGN_AUDIT_RETENTION`、`DESIGN_UNTRACKED_RETENTION`、`DESIGN_CREDENTIAL_RETENTION`（`internal/config/design.go`，同 `contentops.go` 的 min/max 檢查）。repo 沒有排程器，需營運者排程（runbook）。**匯出與刪除（個資請求）**：`GET /design/requests/{id}` 即可匯出單筆；整店匯出為 `cmd/purge-design-history -export-shop N`（輸出 JSON 與截圖清單，不含簽名網址）；刪除為 `-all-for-shop N -apply`。

### D22：成本估算
- 每請求：事件約 20–30 列、請求 1 列、截圖列約 40 列（admin）；DB 約 **50 KB**（含 diff_summary 與 jsonb）。
- 儲存：admin 截圖以文字為主，約 100–300 KB／張 → **約 6–10 MB／請求**；storefront 含相片可到 0.5–1 MB／張，設視圖 ≤ 12 張、約 **10–20 MB／請求**。
- 每店每年（假設每月 2 筆已發佈請求＋50% 被拒／失敗）：約 36 筆請求 → 截圖 **約 250–450 MB**（365 天保存下即穩態）；DB < 3 MB。R2 儲存約 US$0.015／GB·月 → 每店約 **US$0.005／月**；操作次數可忽略。整體成本不是決策因素；上限（D5）是防濫用不是省錢。

### D23：後續（template repo；不在本 change 實作，列為任務 T1–T8）
- 新 `ctx.data`：`listDesignRequests({site,status,page})`、`getDesignRequest(id)`（含簽名截圖網址與事件）、`getDesignState()`、`listUntrackedChanges()`；`ctx.actions`：`approveDesignRequest(id, {headSha})`（**核心顯示確認**，新的**不可逆動作**：固定警告說明「核准後 AI 的改動會在數分鐘內對所有顧客上線」，須加進 `warnings.js` 與 `WARNED_CONFIRM_IDS`）、`rejectDesignRequest`、`requestDesignChanges`、`cancelDesignRequest`、`revertDesign(id)`（固定警告：「將以新的版本還原設計，期間需再次核准；內容文件不受影響」）。權限由核心在動作內以 `design.*` 檢查。
- **核心擁有的前後對照元件** `ctx.ui.beforeAfter({before, after, labels})`（並排與滑桿兩模式，鍵盤可操作，`alt` 文字來自 `copy.js`；圖片以 `<img src>` 載入簽名網址，CSP `img-src` 需放行截圖簽名網址的來源——**這是 CSP 變更，是人工審查的核心變更**，因為嚴格 CSP 目前是 `img-src 'self' data: <image-public-domain>`；替代方案：由核心以 `fetch` 取回後轉 `blob:`，但 `blob:` 同樣需要 `img-src`，故仍要改 CSP，建議 `img-src` 增列私有 bucket 的簽名網址主機）。
- 預設設計層畫面（`design-defaults/views/`）：歷史列表、詳情（並排截圖、請求原文——**一律 `textContent`**、事件時間軸、守門員摘要、diff 摘要、核准／拒絕）、還原按鈕與確認。新增 copy 鍵須同步 `design/copy.js` 與 `design-defaults/copy.js`；`CORE-CHANGELOG` 的 `Design-layer action:` 行；`lib/public-api.json`、`DESIGN-LAYER.md`、守門員 fixtures、e2e。
- 「歷史／還原 UI 放在哪裡」見決議 Q8。
- **新增 `ctx` 函式是人工審查的核心變更**（`.claude/skills/core-change`，template `docs/DECISIONS.md` D4）；AI 設計工作階段不得自行新增。

### D24：濫用情境與對策
| 情境 | 對策 |
| --- | --- |
| **服務憑證外洩** | 每店一把、爆炸半徑一家店；只能建立／更新請求、上傳截圖；**不能核准**（路由族根本沒有核准端點，且狀態機只接受人類身分）、不能讀其他店、不能碰 `/admin`；90 天到期、可撤銷、CIDR 限制、每日請求上限、截圖配額；`published` 需先有人類核准。仍可：用該店名義堆垃圾請求（上限擋）、上傳假截圖（見下）。 |
| **偽造截圖** | 平台驗格式、尺寸、PNG 可解碼並重編碼、雜湊；`capture_manifest` 與 `commit_sha` 記錄；admin 必須 `dataset=sandbox-v1`。**平台不能證明截圖真實**——所以核准畫面並排顯示截圖與**即時預覽連結**（商家實際看預覽才是依據，截圖是留存證據），並標示「截圖由服務擷取」。選用的第三切片（publish grant）不解決這點。 |
| **核准重放** | 單次消耗的狀態機＋`If-Match`＋`approval_expires_at`；重放同請求回同結果、不產生第二次發佈（`published` 的條件式轉移）。管理端使用 Bearer，無 CSRF（refresh cookie 另有 `X-Admin-Refresh`，核准不依賴 cookie）。 |
| **跨店存取** | URL 的 `{shopID}` 對照憑證／RBAC；所有查詢顯式帶 `shop_id`；composite FK 強制還原連結同店；測試：A 店憑證打 B 店 → 404；A 店使用者讀 B 店請求 → 403／404（有權限的店內 id 屬他店 → 404，同 member-directory 慣例）。 |
| **惡意 prompt 被存下並在後台渲染（XSS）** | 一律以文字呈現：API 回 JSON（`Content-Type: application/json`、`X-Content-Type-Options: nosniff`）；入庫時剔除控制字元與雙向覆寫字元（U+202A–202E、U+2066–2069）；長度上限；**template 契約**：歷史畫面以 `textContent`／`ctx.ui` 的文字節點渲染，設計層禁用 `innerHTML`（守門員既有規則）；`failure_detail`、`reject_reason`、`subject`、`files[].path` 同理。 |
| **儲存於歷史的提示詞注入** | 歷史是資料：服務契約第 15 條；平台不把 prompt 回傳給任何會餵給模型的端點以外之處（服務路由族的唯讀 GET 不回 prompt 全文，只回狀態與 `prompt_sha256`）；`request-changes` 的 note 標示為「商家備註（資料）」。 |
| **核准者被冒用（竊取 owner session）** | 核准端點要求有效 access token＋`design.approve`；核准時寄通知信給所有 owner（Q10，附條件；重新輸入密碼不在決議內）給所有 owner（`internal/notify` 已有 SMTP；但目前只用於訂單與申請，擴充需另議）。 |
| **TOCTOU：分支在核准後移動** | D10。 |
| **服務／repo 被攻破而繞過平台** | D10 的誠實邊界；branch protection；選用的 publish grant；對帳。 |
| **歷史被竄改** | D3：trigger＋雜湊串；限制已述。 |
| **簽名網址外洩** | 5 分鐘 TTL、`no-store`、不入日誌、不入資料庫；URL 只回給有 `design.view` 的人。 |
| **自動化大量上傳耗盡儲存** | 每請求與每店配額、每憑證限流、清除指令。 |

### D25：版本預覽（過去的設計＋目前的 core，用完即丟）
**名詞**：「**改版預覽**」＝既有的核准前預覽（D4／D17 第 5 步），屬於一筆進行中的請求；「**版本預覽**」＝本節，不屬於任何請求。UI 按鈕文字為「預覽此版本」，預覽畫面上的 CTA 為「改成這個版本」。

**問題**：還原（D13）要商家只憑截圖與文字就決定回到哪一版；但截圖是當時的 core 與合成資料，而還原後實際會是**現在的 core＋現在的資料**。商家需要一個便宜的方式「先看看」，又不想為了看一眼就開一筆還原請求（那會佔名額、走完整個流程）。

**決定**（建議；需使用者確認）：
1. **入口**：歷史列表與詳情中，每個過去已發佈的設計（目標 `status IN ('published','reverted')` 且有 `published_tag`，與還原相同）有「預覽此版本」，呼叫 `POST /design/requests/{id}/version-preview`（`design.view`）。
2. **內容**：部署＝目標 tag 的設計層＋**目前**的 core，只此一份、用完即丟。由**服務**部署（平台沒有 git，同 D19），沿用 D16 的拉取模型：平台建立 `design_version_previews` 列（`queued`），服務輪詢、認領、部署、回報 `ready`（只存 origin 與到期，不存路徑與查詢字串，規則同 D2 的 `preview_origin`）或 `failed`。
3. **資料與身分**：預覽連真實 API，由商家成員**以自己的身分登入**觀看（同改版預覽，D12）；admin 的版本預覽因此會把**他自己本來就看得到的真實資料**顯示給他自己。**不拍、不存任何截圖**，所以 D11 的 R1（admin 截圖只從 sandbox 擷取）不受影響——R1 規範的是「被保存的影像」，版本預覽沒有被保存的影像。origin 只回給發起者本人，不寫入日誌與事件。
4. **相容檢查（Q7）先於部署**：平台在建立時同步檢查 `target.core_version >= design_site_state.design_floor_version`（與 D13 同一函式）、同 `site`、有 `published_tag`；不通過 → `409 revert_below_core_floor`（沿用，訊息說明原因：該設計早於一次需要設計層跟著改的 core 同步）或 `409 already_current`（目標已是目前設計，沒有東西可比），**不建立列、不部署**。通過後，服務部署前仍以目前 core 對該組合跑守門員（`check-design`，D13 的「最終裁判」）；失敗 → 回報 `failed(guard_failed)`、**不部署**，並把原因（規則 id）顯示給發起者。
5. **不佔名額、不寫歷史事件**：不碰 `design_requests` 的部分唯一索引（D2），所以進行中的請求不阻擋版本預覽，版本預覽也不阻擋請求。**不寫** `design_request_events`（事件串是「誰核准了什麼」的決定紀錄；版本預覽不是決定，混進去會稀釋它）。取而代之只有兩樣：①**使用紀錄表** `design_version_previews`（一般 `tenantOwned` 租戶表：`id`、`shop_id`、`site`、`target_request_id`、`target_tag`、`requested_by_user_id`〔無 FK，同稽核慣例〕、`status` 於 `queued｜claimed｜ready｜failed｜expired｜superseded`、`failure_code`、`core_version`、`design_floor_version`、`preview_origin`、`created_at`、`ready_at`、`expires_at`；無 trigger、無雜湊串）；②**限流與上限**。理由：保留「誰在何時預覽過哪一版」足以回答濫用與用量的問題（admin 預覽會顯示真實資料，值得知道誰開過），但它**不是核准依據**，不需要防竄改的代價；日誌只含 id 與計數（D20 規則）。
6. **權限**：`design.view`（即能看歷史的人：`merchant_owner`、`editor`，`super_admin` 經 `*`）。理由：預覽只呈現他本來就有權看的資料與畫面、不改任何狀態、不能核准或發佈；要求 `design.request` 反而會把 editor 擋在看歷史之外（Q3 已定 editor 可看歷史）。**替代方案**：要求 `design.revert`——較保守，且「預覽是為了還原」；但商家也可能只想看看過去長怎樣。**需使用者確認**。「改成這個版本」只對持 `design.revert` 者顯示。
7. **TTL**：版本預覽預設 **30 分鐘**（`DESIGN_VERSION_PREVIEW_TTL`，上限 2 小時；比改版預覽短，因為不需要等人核准）。到期服務必須拆除部署，`design-sweep` 把逾時列標 `expired`、把 `claimed` 逾租約者標 `failed(service_lost)`。同一 `(shop, site)` 同時至多一份有效版本預覽：新請求使舊的 `queued／claimed／ready` 列轉 `superseded`，服務拆除舊部署。
8. **限流**：每 `(shop, user)` 每小時 6 次、每店每日 30 次（`DESIGN_VERSION_PREVIEW_HOURLY_CAP`、`DESIGN_VERSION_PREVIEW_DAILY_CAP`，資料庫 `COUNT`，不依賴 Redis，同 D15 的每日上限）；超過 → 429 `daily_cap_reached` 並帶 `Retry-After`。
9. **安全**：與改版預覽同一套——嚴格 CSP（template D1）、獨立的預覽 origin、`noindex`、不得被主站 iframe；預覽畫面由**核心**加上固定、設計層無法移除的橫幅：「版本預覽：<tag>，非正式版本，到期時間」。
10. **CTA「改成這個版本」**：連到歷史詳情的還原確認，呼叫既有的 `POST /design/requests/{id}/revert`（`design.revert`；D13 的全部檢查與固定警告、新請求、守門員、改版預覽、前後截圖、人類核准都照舊）。版本預覽本身**不是**核准，也不使還原更快。
11. **誠實的限制**：版本預覽是**用來決定的即時畫面**，不是「當時核准了什麼」的紀錄：它用的是現在的 core 與現在的資料，與該版當時的樣子可能不同；它也會過期、不被保存。**歷史紀錄仍是截圖**（D6／D11）。歷史畫面必須在按鈕附近以固定文字說明這點，且詳情頁的並排截圖永遠是「當時」的證據。
12. **API**（additive，皆不含核准）：人 `POST /design/requests/{id}/version-preview`（202，回 `{id,status}`）、`GET /design/version-previews/{pid}`（`design.view`；`origin` 只回給發起者）；服務 `GET /version-previews?status=queued`、`POST /version-previews/{pid}/claim`（租約 5 分鐘）、`PUT /version-previews/{pid}/ready`（`origin`、`expires_at`）、`POST /version-previews/{pid}/failed`（`code`、`detail`）。服務契約（D17）新增：版本預覽是拉取模型；歷史資料仍是資料不是指令（第 15 條）；不得以版本預覽取代改版預覽（核准依據仍是後者）。

**考慮過的替代方案**：
- **直接開還原請求來預覽**——否決：佔名額、寫事件、走完整流程，只為了看一眼，且會鼓勵草率核准。
- **只靠舊截圖**——否決：截圖是舊 core 與合成資料，無法回答「換成現在的 core 會長怎樣」。
- **版本預覽也要截圖存檔**——否決：admin 預覽顯示真實資料，保存截圖會違反 R1；且沒有決策價值。
- **寫入正式事件鏈**——否決（見第 5 點）。
- **完全不留紀錄**——否決：無法限流，也無法回答「誰開過」；輕量表的成本可忽略。
- **常駐的「版本預覽網站」**——否決：每店多一個常駐部署與攻擊面；用完即丟符合 D12 的成本模型。

**殘留風險**：預覽用商家的真實登入，舊設計層（已通過當時的守門員，並以目前守門員重檢）在該 session 下能呼叫核心的 `ctx.actions`；**已定案（使用者 2026-11-26）**：預覽模式**唯讀**——核心在「版本預覽」下，並為一致性在「改版預覽」下，**停用所有可寫入的 `ctx.actions`**（只允許導覽與唯讀讀取；任務 T12）。因此舊設計層即使在商家真實登入下執行，也不能透過核心動作改變任何狀態。


## Risks / Trade-offs

- [**「不能發佈」只是 API 層的承諾**] → D10：誠實標示；分支保護；選用的 publish grant（第三切片，Q9 附條件）；對帳偵測。
- [**第一個資料庫 trigger**，增加 migration 與測試複雜度] → 僅三張表，`down` 明確，整合測試斷言 UPDATE／DELETE 被擋；替代方案（只靠應用層）被否決，因為「只能追加」是本 change 的主張，值得一道資料庫層的防線。
- [**sandbox 堆疊是新的營運負擔**] → 可按需拋棄式；若使用者不願，退回「只拍 storefront＋admin 以遮罩 live 資料」並接受更高隱私風險（不建議）。
- [**截圖不是證明**] → 並排即時預覽；標示來源；紀錄本身誠實分級（D1）。
- [**視圖集小於整個 UI**] → 明說（「不解決的事」）；`set_version` 允許日後擴充。
- [**版本預覽在真實登入下執行舊設計層**] → 嚴格 CSP、核心橫幅、守門員以目前 core 重檢、預覽模式唯讀（T12，已定案：版本預覽與改版預覽皆停用寫入型 `ctx.actions`）。
- [**每店一次一個請求限制了吞吐**] → 商家改版是低頻操作；以明確 409 取代難以推理的並行。
- [**新增 ctx 函式與 CSP 變更是人工審查的核心變更**] → 排在 template 後續，不阻擋平台側；平台側可先用「假服務」e2e 驗證。
- [**本 change 引入大量新介面，影響 API 契約檢查**] → 全部 additive；`FillNonNil` 會強迫 `Deps` 新欄位被接線與入契約。
- [**R2 第二個 bucket 的設定錯誤（誤綁公開網域）會洩漏所有截圖**] → runbook 明列「不得綁自訂網域、不得開 r2.dev」；啟動時以自檢（嘗試匿名 GET 一個已知不存在的鍵並預期 403/404 非 200 無法證明私有，故不做）——改由部署檢查清單與 e2e 的 MinIO 私有 bucket 驗證；此為殘留風險。

## Migration Plan

三個可獨立部署的切片（皆在實作階段，不是本提案）：
1. **切片 1：資料與讀寫 API（無截圖）**：新表與 trigger migration（純新增、可逆）；`design.*` 節點與 seed（**主 DB 需 `make migrate` 再 `make seed`**，否則 owner 看不到歷史）；憑證表與管理端點；服務與後台路由；狀態機、事件雜湊串；`cmd/design-sweep`；OpenAPI；以「假服務」e2e 驗證契約。
2. **切片 2：截圖**：第二個 bucket、`PresignGet`、上傳端點與簽名讀取、配額與 sandbox 檢查、`cmd/purge-design-history`。未設定 bucket 時 `submit` 回 503（因此切片 1 單獨上線時無法送審——這是刻意的，符合 D18）。
3. **切片 3（選用）**：publish grant、整店匯出／刪除的完整流程、核准通知信。
**切片 4（選用，D25）**：版本預覽——依賴切片 1（狀態與 `design_site_state`）；需要服務實作拉取模型才有意義，故可在服務存在後再做；純新增一張表與約 6 條路由，回滾＝服務不再輪詢（或移除對應路由），資料表保留無害。
Template 後續任務與平台切片互相獨立（平台 API 不依賴 template 版本）。**回滾**：移除服務憑證即停用寫入；路由因 wiring-integrity 保持掛載；資料表保留無害；trigger 的 `down` 可移除。

## 不解決的事（誠實）
- **git 已經有歷史，但不是商家看得懂的**；本 change 提供的是商家可讀的層，不取代 git。
- **視圖集以外的視覺回歸**不被偵測；`diff_ppm` 只涵蓋被拍的畫面。
- **在服務之外做的手動變更**不在歷史中；D19 的對帳只能事後偵測並標示，不能補回當時的 prompt 與截圖。
- **服務可能說謊**（D1 的第二級資料）：平台能驗一致性不能驗真。
- **惡意 DBA**（D3）。
- **服務自己的聊天紀錄**（可能含個資）不在平台範圍。
- **版本預覽不是紀錄**（D25）：即時畫面用目前的 core 與資料，與該版當時的樣子可能不同，且會過期；歷史紀錄仍是截圖。
- **還原不保證設計與目前內容或目前 core 完全相容**（只有下限檢查＋守門員＋預覽）。
- **人工 workaround（在服務蓋好之前）**：每次人工改設計前，在 sandbox 堆疊執行 `node e2e/parity.mjs capture <dir>`（改前、改後各一次，`compare` 看差異），把兩個資料夾與請求原文放進專案資料夾；提交後打 tag（`design-<site>-YYYYMMDD-N`），並在 commit message 記請求者與核准者。這是紀律不是系統。

## 決議（Q1–Q10，2026-11-25 已定案）

Q1–Q10 已於 2026-11-25 由使用者定案（採用 design.md 建議原文）。另有使用者在任務中指定的前提（服務永不核准、核准綁 head_sha、歷史只能追加、核准需要截圖）。以下保留各項理由。

- **Q1 保存期限（已定案）**：見 D21 的表。prompt 全文 365 天（遮蔽）、已發佈截圖 365 天、其餘截圖 30 天、請求與事件 730 天。
- **Q2 誰能核准（已定案）**：只有 `merchant_owner`（`design.approve`）與平台 `super_admin`；`editor` 不行。若店只有一位 owner，核准者即請求者，不強制不同人。
- **Q3 staff／editor 能看歷史嗎（已定案）**：`editor` 可看（`design.view`）、`staff` 不可看。
- **Q4 prompt 是否全文保存（已定案）**：全文保存（追溯需要），入庫即遮蔽 email／電話／卡號、365 天後遮蔽整段、聊天 UI 警語。（理由：若只存摘要與雜湊，客訴時看不到原話。）
- **Q5 admin 截圖（已定案）**：admin **只用 sandbox（選項 A：拋棄式堆疊，合成資料）**；storefront 允許 live 公開資料。
- **Q6 tag 命名（已定案）**：`design-{site}-{YYYYMMDD}-{N}`（如 `design-admin-20261125-1`；日期為 UTC、N 由平台於核准時配發並以 `UNIQUE(shop_id, publish_tag)` 保證不重複），因為 admin 與 storefront 在同一個 repo。原提議 `design-YYYYMMDD-N` 在兩站同日發佈時需另外區分，故加上 site。還原發佈同樣配發新 tag。
- **Q7 core 相容檢查的解讀（已定案）**：「不低於設計下限」（D13）；替代「target.core_version ≥ 目前 core」過於嚴格。
- **Q8 對照 UI 放哪裡（已定案）**：由**核心擁有元件**、預設畫面在 `design-defaults`（設計層的預設備份；設計層可重新排版，但不能移除核准／還原確認與固定警告）；並同時在平台的共用 merchant admin 提供相同畫面（商家的後台可能是 template 也可能是 Bouquet 這類自訂前端，契約寫入 `docs/admin-frontend-integration.md`）。
- **Q9 publish grant（切片 3）（已定案，附條件；條件已於 2026-11-26 判定）**：先做切片 1–2；僅當服務將持有 `main` 寫入權才建置 grant。**使用者 2026-11-26 決定：AI 改版服務只推分支，由人（或經擁有者核准的流程）合併；簽章的 publish grant 不建置（依決定省略）。** 註記：若服務日後取得 `main` 寫入權，必須重新檢討此決定。商家 repo 的分支保護在免費方案不可用，因此服務的 git token 權限必須最小化（只限該商家 repo、只允許推分支）。
- **Q10 核准通知信（已定案，附條件）**：核准、發佈、還原各寄一封通知信給店主（所有 owner）；以 email 額度／SMTP 處理完成為前提（見 CLAUDE.md 通知信一節），未完成前此項不開工。

## 實作期間的預設決定（2026-11-26，使用者授權照建議）

決定者：使用者將「可逆的技術細節」授權給助理，照助理建議採用；以下每項都可隨時改。

- **A1 次數上限**：每個請求的上限計算「保留下來的截圖槽位」，另設獨立的「嘗試次數」上限（已於 539a137 完成；預設 240，環境變數 `DESIGN_MAX_SCREENSHOT_ATTEMPTS`）。
- **儲存配額**：允許小幅超出（已接受）。
- **jsonb**：`prompt_flags`、`allowed_cidrs` 用 jsonb。
- **分支名稱**：預設 `ai/shop-{id}/{request id}`；允許服務提供 `request_id`。
- **請求者**：必須是該店的啟用中成員且具 `design.request`；因此不是成員的 super_admin 不能當請求者。
- **未追蹤變更確認**：以 commit sha 為鍵。
- **核准頻率限制**：放在 handler。
- **核准通知信**：延後到 SMTP 處理完成後（Q10 的條件）。
- **版本預覽（D25）**：權限 `design.view`；TTL 30 分鐘；每位使用者每小時 6 次、每店每日 30 次；每個 site 同時 1 個；用量紀錄保留 90 天。

### 擁有者決定（2026-11-26，照建議；原待決項目）

- **版本預覽唯讀模式（已定案，使用者 2026-11-26）**：版本預覽與改版預覽皆停用所有寫入型 `ctx.actions`（唯讀）。
- **Q9（已定案，使用者 2026-11-26）**：服務只推分支、由人工合併；切片 3 的 publish grant 依決定省略（日後服務取得 `main` 寫入權時須重新檢討）。

本節未改動 `docs/` 下任何檔案。


## T6 擷取腳本的契約選擇（2026-11-28，實作時的最小合理決定）

T6 的任務文字（一行）沒有寫死下列細節；以下是為了不改任何 API、且與 D6／D7／D11／D12 一致而做的選擇。腳本在 template repo：`scripts/design-capture.mjs`（鎖定、列入 `lockedPaths`；`admin/e2e/parity.mjs` 一個字未改）。

- **介面**：`capture --site admin|storefront --phase before|after --out DIR --sandbox-env FILE`、`upload --dir DIR --api URL --shop N --request UUID`、`views`、`selftest`。`FILE` 是 `make sandbox-env` 的輸出（含 origin 清單、公開的合成帳號、publishable key）。輸出 `{phase}-{role}-{viewport}-{view_key}.png` ＋ `manifest.json`（不含任何憑證）。`--diff-against <before 目錄>` 為每張 after 算 `diff_ppm`（演算法同 parity compare，每通道差總和 > 6）。
- **允許清單**：`design_sandbox_origins` ∪ `allowed_api_origins`（來自 FILE）∪ 環境變數 `DESIGN_CAPTURE_ALLOWED_ORIGINS`；與平台的 `DESIGN_SANDBOX_ORIGINS` 是同一份來源。**admin、API、storefront 三種 origin 都必須在清單內**（storefront 不開放 `public-live`：第一版只拍 sandbox 資料；要拍公開來源需另議並改契約）。空清單＝拒絕；loopback 以外不得用明文 http；`:8080`、`:3001` 等主堆疊埠一律拒絕。拒絕＝結束碼 2，瀏覽器與任何網路請求都還沒開始。**瀏覽器本身也被圍起來**：對清單（加 sandbox 媒體 origin）以外的任何請求一律 abort。
- **憑證**：登入只接受 `@example.com` 的合成帳號（FILE 的 `logins`，或環境變數覆寫但同樣檢查網域）；沒有任何旗標能傳入真實帳密。服務金鑰 `dsk_` **只**從環境變數 `DESIGN_SERVICE_KEY` 讀，argv 出現任何祕密類旗標即拒絕；所有輸出經過清洗器（已知祕密值、`dsk_…`、Bearer、簽名網址）。
- **manifest 標頭**：`tool=design-capture`、`dataset=sandbox-v1`、`api_origin_class=sandbox`、`set_version=1`、`captured_at`、**`origin`＝實際擷取的 admin（或 storefront）origin**。
- **視圖集 v1**：admin owner 八個（`login`〔登出狀態，role 記 owner，依 D6〕、`home`、`orders`、`order-detail`、`products`、`product-edit`、`members`、`reports`）＋ staff 兩個（`home`、`orders`）；storefront anon：`home`、`shop`、`product`、`cart`、`story`（網站沒有故事頁就略過並記入 manifest `skipped`）、`checkout`（只到第一步，不輸入、不送出）。購物車用核心商務層加一件合成 session，不建立帳號或訂單。視口 390×844 與 1280×860，`deviceScaleFactor 1`、`zh-TW`、`Asia/Taipei`、動畫關閉；高於 10,000 px 截斷並在 manifest 標 `truncated`。`data-pii` 與密碼欄以 Playwright `mask` 純黑遮罩。
- **固定時鐘只用在 storefront**：後台核心在開機時凍結 JS 內建物件（D18），Playwright 的 clock 會讓它無法啟動；admin 的決定性來自固定的 sandbox 資料集。這是 D6「決定性」一節的已知例外。每個角色只登入一次（平台對登入限流），視口在同一個工作階段內切換。
- **上傳與保留槽位規則**：路徑與標頭同 §4；上傳前在本地規劃——不同槽位 > 80 或（帳本累計＋待傳）> 240 次嘗試即拒絕（結束碼 3，不送任何請求）；帳本檔 `.upload-ledger.json` 記住已存槽位與嘗試數，重跑時位元組相同的槽位不再送出（所以重跑不會吃嘗試額度）；重拍既有槽位不佔槽位額度（平台規則）。回應處理依契約：201／200 成功；429／502／5xx／網路錯誤指數退避（1、2、4、8、16 秒＋抖動，遵守 `Retry-After`）最多 5 次後放棄（結束碼 4）；422 雜湊不符重傳一次；`slot_cap_exceeded`／`attempt_cap_exceeded`／`quota_exceeded`／`live_data_capture_refused`／`design_storage_not_configured`／`invalid_state`／413 一律放棄（結束碼 3，不重試）；409 `head_moved` 結束碼 5（請以新 head 重拍 after）。
- **Playwright 是借來的**，不是依賴：`DESIGN_CAPTURE_PLAYWRIGHT_DIR`，否則本層的 `playwright`，否則並列的 ecommerce checkout 的 `e2e/node_modules`。
