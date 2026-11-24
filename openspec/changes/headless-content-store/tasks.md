> 前置：design「Decisions record」Q1–Q11 已於 2026-11-25 由使用者決定（採建議答案；Q9 仍待實作時查官方文件）。**群組 1 已由已核准的 `retire-hosted-storefront-and-themes` 第一階段交付**，不要做第二次；其餘群組在**使用者核准本 change 之後**才可實作。每個群組結束時：`cd api && go build ./... && make lint && make test && make test-int && make openapi` 全綠、`node e2e/commerce-path.mjs` 通過、可獨立部署。

## 1. 每店 schema 儲存（**由 `retire-hosted-storefront-and-themes` 的 tasks 群組 1 交付**；此處只列為相依）

- [ ] 1.1 確認以下已存在：`content_schemas` 表與 ent 實體、`api/internal/contentschema/` 的 `Store`、format 1 回填、頁面與全域內容的 schema 讀取已改走 Store（見退役 change 的 1.1–1.8）

## 2. 文件資料表與回填（design D1）

- [ ] 2.1 `api/internal/ent/schema/content.go`：`ContentDocument`、`ContentRevision`、`ContentAuditLog`（欄位、索引、CHECK：`status IN (0,1)`、`rev_no >= 1`、`kind IN (...)`；`key` 格式以 CHECK 正規式約束）；`tenantOwned` 登記三者；`go generate`；`make migrate-gen name=add_content_documents`；手動剪除 atlas 假差異；手寫 CHECK；down 檔 DROP 三表
- [ ] 2.2 回填 migration（同一個 migration 檔的後半，冪等）：`pages` → `content_documents`（`slug→key`、其餘恆等、`draft_rev = 1`、`published_rev = 1 if status=1`、`schema_version =` 該店作用中版本）；`shops.content_json` → 鍵 `global`（`type_key='global'`、`status=1`、`published_json = draft_json`）；每份已發佈文件寫一筆 `publish` revision；不合 `key` 規則的 slug **讓 migration 失敗並列出**（預期 0 筆）；整合測試以「從 migration 檔讀出的 SQL」在含舊資料的資料庫上驗證（同 `add_order_item_options` 的做法）
- [ ] 2.3 `api/internal/seed/seed.go`：demo 商家註冊一份 format 2 的 demo schema（由 `internal/seed/schemas/starter/` 轉出：`home`、`global` 兩個 type 與 documents 宣告），`seed_integration_test.go` 同步
- [ ] 2.4 驗證：同群組標準（尚無行為改變）

## 3. schema 註冊與驗證（design D3、D4）

- [ ] 3.1 新增 `api/internal/contentschema/vocab.go`：詞彙 allowlist（design D3）與常數上限；`Validate(raw json.RawMessage) (*Parsed, []Detail)`：依序做 1–8 步（拒絕重複鍵與預先限制深度的 JSON 解析器、結構上限、詞彙走訪、`$ref` 環與展開節點數、`cms.CompileSchema` 編譯、預設值自洽檢查〔2 秒 context〕、鍵名與 type 引用、文字欄位不含 `<`）
- [ ] 3.2 `api/internal/contentschema/validate_test.go`（單元，不需基礎設施）：每條規則的通過與失敗各一；**惡意案例**：重複鍵、深度 9、`$ref` 環、`$ref` 放大（展開 >5000 節點）、遠端 `$ref`、`patternProperties`、`allOf`、未宣告 `maxLength` 的 string、未宣告 `maxItems` 的 array、`additionalProperties` 缺省、`pattern` 過長、`format: regex`、`x-foo`、128 KiB 以上、含 `<script>` 的 `title`
- [ ] 3.3 `api/internal/contentschema/diff.go`：`Diff(oldDocs, newSchema)`：對每份文件驗證 `draft_json`／`published_json`、計算將被 hydrate 丟棄的鍵路徑（重用 `cms` 的走訪邏輯；必要時把 `hydrator` 的 `resolve`／`sectionBranches` 匯出到新的小套件 `internal/cms/schemawalk` 並讓 `cms` 改用，**行為不變**）；單元測試涵蓋：欄位移除、改名、型別變更、enum 縮小、新增必填（有／無 default）、鍵不再被宣告
- [ ] 3.4 `api/internal/content/schema_service.go`：`Register(ctx, shopID, actor, raw, opts{DryRun, ConfirmRemovals})`——單一交易：驗證 → sha256 比對作用中版本（相同＝`changed:false`）→ 計算報告 → 有移除而無 `ConfirmRemovals` 回 409（帶報告）→ 寫新版本 → 對 `ok` 以外的文件寫 `schema_snapshot` revision（群組 2 的表）→ 建立 `documents` 宣告的缺少文件（空草稿，hydrate 預設）→ 更新各文件的 `needs_attention`／`declared` → commit 後發 `ContentSchemaRegistered`；`audit` 記 `schema.register`／`schema.dry_run`
- [ ] 3.5 `api/internal/httpapi/content_admin.go`（先做 schema 部分）：`GET/PUT /admin/shops/{shopID}/content/schema`（body 上限 128 KiB，覆寫 `decodeJSON` 的 1 MiB；限流每商家每小時 20、dry run 每分鐘 10）；整合測試：dry run 不寫入、重複註冊不增版、移除警告需確認、跨店隔離、權限（群組 5 的節點）
- [ ] 3.6 驗證：同群組標準

## 4. content 服務、現有端點切換與雙寫（design D1 遷移路徑、D6）

- [ ] 4.1 新增 `api/internal/content/service.go`：`SaveDraft`（樂觀鎖 `draft_rev`、`validateAndSanitize`、大小上限 128 KiB、每店 100 份）、`Publish`（重新驗證、每店 2 MiB 總量、寫 revision 並同交易裁剪到最近 20 筆 `publish`、更新 `published_*`、事件 `ContentPublished`＋相容事件 `PagePublished`）、`Unpublish`、`Delete`（僅 collections）、`Restore`、`List`、`Get`、`Revisions`；全部以條件式 UPDATE ＋影響列數仲裁（同 `partial-shipment` 的做法）
- [ ] 4.2 `api/internal/content/sanitize.go`：由 `cms/sanitize.go` 搬移並泛化 `validateAndSanitize`（輸入 schema 節點與 payload）；新增 `CheckLinkFields`（`api/internal/cms/linkcheck.go`，與 `CheckImageFields` 同構；scheme allowlist、控制字元、長度）及其單元測試；`cms` 的舊函式改為轉呼叫新實作（行為不變）
- [ ] 4.3 `cms.Service` 的頁面與全域內容方法改為委派給 `content.Service`；**雙寫**：同交易把結果寫回 `pages`／`shops.content_json`（新表為主）；`createHomePage` 改為建立 `home` 文件（仍寫 `pages` 列）；`httpapi/pages.go` 的回應由新資料對應回原形狀（`slug←key`、`id←document id`、`content←draft_json`）；`render/bundle.go` 改讀 `content_documents`
- [ ] 4.4 回歸：既有 `cms`／`richtext`／`pages`／render／`media`／`wiring` 整合測試**不改斷言**全綠；新增雙寫一致性測試（任一路徑寫入後 `pages` 與 `content_documents` 內容相同）；`cmd/sanitize-content` 改走文件表並測試
- [ ] 4.5 驗證：同群組標準；`node e2e/happy-path.mjs` 的頁面編輯與發佈段落通過

## 5. 權限與管理端 API（design D5）

- [ ] 5.1 `internal/seed/seed.go`：`PermissionCatalog` 新增 `content.view`／`content.edit`／`content.publish`／`content.manage`；授予 `merchant_owner` 全部、`editor` 只有 `view`＋`edit`；`staff` 無；`page.*` 的現有授予複製成對應 `content.*`；`seed_integration_test.go` 以**相等**比對斷言 staff 與 editor 的節點集合（新增節點不會悄悄流進 staff）；自訂角色的複製寫成 migration 並測試
- [ ] 5.2 `api/internal/httpapi/content_admin.go`（文件部分）：`GET /documents`、`GET|PUT /documents/{key}`（`If-Match`／`If-None-Match: *`，412 語意）、`publish`、`unpublish`、`DELETE`、`revisions`（列表／讀取）、`restore`、`preview-token`；各自限流（儲存 60／分、發佈 20／分，每商家＋呼叫者）；`Deps` 欄位與 `wire.go` 接線（稽核 ⑤ 要求非 nil）
- [ ] 5.3 `httpapi/media.go`：`content_image` 上傳的守衛改為 `content.edit`（相容期同時接受 `page.edit`）
- [ ] 5.4 稽核：所有寫入記 `content_audit_log`；PII 整合測試（slog 導入 buffer，內容與 hook URL 的特徵值一個都找不到）；`cmd/purge-content-audit`（預設 dry run、`-apply`、冪等、只印 id）與 `Makefile` 目標
- [ ] 5.5 測試：樂觀鎖（並發兩個 PUT 恰一個成功）、跨店隔離（A 店 owner 讀寫 B 店 key 回 403／404）、`editor` 不能 `publish` 與 `schema`、`staff` 全部 403、`restore` 只動草稿、revision 裁剪恰為 20、schema 變更後 `needs_attention` 的文件不能發佈
- [ ] 5.6 `internal/app/authguard_integration_test.go`：新路由皆有 RBAC 守衛；`expectedUnguardedAdminRoutes` 不變

## 6. 公開讀取 API、快取與預覽（design D2）

- [ ] 6.1 `api/internal/content/preview.go` 與 `auth/jwt.go`：`IssueContentPreview`／`VerifyContentPreview`（`aud=content-preview`，claims＝user、shop、scope、key；TTL＝`CONTENT_PREVIEW_TTL`，預設沿用 `PREVIEW_TOKEN_TTL`）；單元測試：跨店、過期、錯 aud、錯 scope 皆失敗
- [ ] 6.2 `api/internal/httpapi/content_public.go`：`GET /shop/content`（`?include=content`）、`GET /shop/content/{key}`、`GET /shop/content/{key}/preview`（`X-Preview-Token`；使用時檢查持有者仍有 `content.view`、商家未停用；`no-store`、`X-Robots-Tag: noindex`）；hydrate；`ETag`／`If-None-Match`／`Cache-Control`／`Vary`；404 對「不存在」與「未發佈」位元組相同（測試）；CORS 的 `Allow-Headers` 加 `X-Preview-Token`、`If-None-Match`，`Expose-Headers` 加 `ETag`（**漏掉任一個瀏覽器端就壞**，測試斷言）
- [ ] 6.3 快取：`render.Cache` 擴充 content 命名空間的 get／delete／bump；`render.Invalidator` 新增 `ContentPublished`／`ContentUnpublished`／`ContentSchemaRegistered` 的 case；`cache_integration_test.go` 擴充（發佈後立即可見、Redis 故障退回 DB、schema 註冊使整店失效、已發佈內容在 schema 改變後仍照常提供）
- [ ] 6.4 限流：公開端點掛 per-IP 規則（每分鐘 120，可設定）；測試 429
- [ ] 6.5 `wiring_integration_test.go`：⑤ 涵蓋新 `Deps`；⑥ 路由表一致；⑦ `expectedSubscribers` 有意識更新（hook 訂閱者，群組 7）
- [ ] 6.6 驗證：同群組標準

## 7. 發佈 hook（design D7）

- [ ] 7.1 ent：`ContentPublishHook`（`shop_id` 唯一、`url_ciphertext`、`host`、`signing_secret_ciphertext`、`enabled`、`last_status`、`last_http_status`、`last_attempt_at`、`consecutive_failures`）；`tenantOwned`；migration；`internal/cryptbox` 用於加密
- [ ] 7.2 `api/internal/content/hook/`：`Validate(url)`（https、埠、無 userinfo、非 IP 字面值、≤512、allowlist）、`safeClient`（自訂 `DialContext`：解析→逐一檢查位址類別→只連線到驗證過的 IP；`CheckRedirect` 不跟隨；5 秒逾時；只讀 4 KiB）、`Sign`、`Scheduler`（去抖動 30 秒、最短間隔 60 秒、每日 30 次、有界 channel ＋ worker、失敗重試 2 次、連續 5 次失敗自動停用）；config：`CONTENT_HOOK_ALLOWED_HOSTS`、`CONTENT_HOOK_DEBOUNCE`、`CONTENT_HOOK_DAILY_CAP`；production 沒有 allowlist 時 hook 功能整體不可啟用（不是拒絕啟動）
- [ ] 7.3 單元測試（`httptest` ＋ 假解析器，不連網）：私有／loopback／link-local／`169.254.169.254`／IPv6／IPv4-mapped／CGNAT 皆被擋；DNS rebinding（第二次解析回私有位址）不被採用；3xx 不跟隨；逾時；回應主體只讀 4 KiB；簽章可被獨立驗證；URL 含 userinfo／非 443／IP 字面值被拒；allowlist 外主機被拒；**URL 與 secret 不出現在任何日誌與 API 回應**
- [ ] 7.4 `httpapi/content_admin.go`：`GET/PUT /publish-hook`（只寫；回應只含 `host`、遮罩 URL、狀態）、`POST /publish-hook/test`、`POST /publish-hook/fire`；限流每商家每分鐘 3 次；稽核；整合測試（回應位元組不含完整 URL 與 secret、去抖動合併、每日上限、Redis 故障 fail open 仍受最短間隔約束）
- [ ] 7.5 訂閱者：`dispatcher.Subscribe(hookScheduler.Handle)`（只入隊，不做 I/O）；`wiring_integration_test.go` 的 `expectedSubscribers` 更新；測試：發佈請求不被 hook 拖慢（hook 端點睡 10 秒，發佈仍在 200ms 內回應）
- [ ] 7.6 驗證：同群組標準

## 8. 平台後台（`admin/`）

- [ ] 8.1 `admin/views/pages.js`、`content.js`、`lib/schemaform.js`：改打新端點（或保留舊端點，擇一並在 PR 說明）；新增 `link`、`select`、`textarea`、`list` 的控件；顯示 `needs_attention`、`declared`、版本歷史與還原、`draft_rev` 的 412 處理（「他人已更新，重新載入」）
- [ ] 8.2 新視圖：內容 schema 檢視與註冊（dry run 報告、移除警告確認）、發佈 hook 設定（遮罩顯示、測試、手動重建）；`admin/app.js` 註冊表與 `index.html` 面板；權限由 `/shops/{id}/me` 的節點決定可見性
- [ ] 8.3 e2e：`e2e/happy-path.mjs` 新增內容流程（註冊 schema → 儲存草稿 → 預覽 token → 發佈 → 公開端點讀到 → 改 schema 移除欄位的警告 → 還原）；`e2e/commerce-path.mjs` 新增公開讀取與 ETag／304 的無瀏覽器斷言；`CLAUDE.md` 同步斷言計數；守住 tripwire #4′／#5 並記錄

## 9. 契約與文件

- [ ] 9.1 `api/openapi/openapi.yaml`：新增全部新路由、`ContentDocument`／`ContentSchemaReport`／`ContentRevision`／`PublishHookStatus` 等 schema、`ETag`／`If-Match`／`If-None-Match`／`X-Preview-Token` 參數與標頭、422／409／412／429 回應；`-openapi.update-surface`（**只新增行**，檢視 diff）；`make openapi` 全綠
- [ ] 9.2 `docs/content-store-integration.md`（新）：給商家站台的消費契約（端點、ETag、預覽、hydrate 行為、`link`／`richtext` 的呈現規則、CSP 建議、build-time 與 runtime 的取捨與建議）；`docs/storefront-integration.md` 加指向；`docs/admin-frontend-integration.md` 補內容端點與 412／422 的處理；`docs/media-r2-setup.md` 的內容圖片段落更新權限名
- [ ] 9.3 `CLAUDE.md`（內容儲存條目：模型、樂觀鎖、消毒閘門、hook 的 SSRF 規則、PII 規則）、`README.md`、`openspec/project.md`；`.claude/skills/merchant-onboarding/`（標示已實作並更新步驟與 known-gaps）
- [ ] 9.4 spec 增量：實作完成、封存時把本 change 的 delta 併入 `openspec/specs/`，並對 `page-management`／`content-rendering` 以 MODIFIED 對齊

## 10. 清理（Q2 已決定 2026-11-25：保留相容端點、不做第二次 v1 例外；與其他群組獨立）

- [ ] 10.1 依 Q2 的決定：移除 `pages` 表（保留 `Shop.content_json` 為由 `global` 文件投影的唯讀值）、移除雙寫、`createHomePage`；保留相容端點與其轉接層
- [ ] 10.2 依 Q1（已決定）：`page.*` 別名保留；它與相容端點的退役是日後另一次決定（需要自己的 v1 例外），本 change 不做
- [ ] 10.3 migration（破壞性，down 拒絕回滾並說明從備份還原）、備份與演練

## 11. merchant template 後續工作（**在 merchant-template repo，守門員流程之內；本 change 只列出，不實作**；design D10）

- [ ] 11.1 T1：`template.json` 的 `designPaths` 加 `public/content.schema.json`；範例 schema；`CLAUDE.md`／`DESIGN-LAYER.md` 補規則
- [ ] 11.2 T2：`admin/core/data.js`、`actions.js`、`context.js` 新增 `ctx.data`／`ctx.actions` 內容函式；`admin/lib/content.js`（API 呼叫、412／422 對應）；核心的 schema 驅動表單建構器 `ctx.ui.contentForm`／`contentField`（驗證鎖在核心、設計層控制版面、必填欄位不可省略）；`core/routes.js` 新增 `content`、`contentDocument`、`contentSchema`、`contentHook`；`design-defaults/` 的預設畫面與 `copy.js` 的 `content.*` 鍵；`lib/public-api.json` 與 `check-design` 的 `ctx` 清單；`core.manifest.json` 重建；`admin/e2e` 內容流程；`DESIGN-LAYER.md` 補表格與範例
- [ ] 11.3 T3：`public/commerce/content.js`（`ShopCommerce.content.all/get/onChange`，ETag，失敗退回 `SHOP_CONTENT`）；`public-api.json`、`commerce.manifest.json`；`check-layers` 允許其 `fetch`
- [ ] 11.4 T4：`ShopCommerce.content.richText`／`link`；allowlist 同步腳本與雜湊檢查（兩邊 `allowlist.json` 的 sha256 在 CI 比對）
- [ ] 11.5 T5：`scripts/fetch-content.mjs`（選用）、預覽模式（`?content_preview=`）、`docs/CORE-CHANGELOG.md`（標示 `Design-layer action`）、`new-merchant.mjs` 與 `sync-core` 更新
- [ ] 11.6 `merchant-onboarding` skill 與 `docs/TEMPLATE-NOTES.md` 同步
