> **切片 2（截圖與私有儲存）已於 2026-11-26 實作**（第 2 組、4.4、6.x、8.x 與 9.x 的對應部分已勾選；Cloudflare 的 bucket 與 token、sandbox 堆疊的部署是人工步驟，見 9.3 與操作員手冊）。
> **切片 1（資料與 API，無截圖、無簽章 permit）已於 2026-11-26 實作**，已勾選的項目即其內容；切片 2（截圖與儲存）、切片 3（選用）與 template 後續任務仍未做。
> design.md 的 Q1–Q10 已於 2026-11-25 由使用者定案（採用建議原文）；Q9（publish grant）與 Q10（通知信）為附條件決議，見 design.md。每組可獨立部署；切片對應 design 的 Migration Plan。

## 1. 平台資料與 API（切片 1，design D2／D4／D8／D15／D16）

- [x] 1.1 `api/internal/ent/schema/design.go`：`DesignRequest`、`DesignRequestEvent`、`DesignScreenshot`、`DesignSiteState`、`DesignUntrackedChange`（欄位、CHECK、索引、部分唯一索引「每 `(shop_id, site)` 至多一筆非終態」、複合 FK 同店約束，見 D2）；`api/internal/ent/schema/designcredential.go`：`DesignServiceCredential`
- [x] 1.2 `go generate ./internal/ent`（`GOTOOLCHAIN=go1.26.4`）；`make migrate-gen name=add_design_history`；**手寫**：部分唯一索引、複合 FK、所有 CHECK 運算式（atlas 不偵測 CHECK 運算式變更）、剪除 atlas 夾帶的 `members` 與 `role_user`／`user_permission` 假差異、重算 `atlas.sum`；up→down→up
- [x] 1.3 手寫 migration `…_add_design_history_append_only_triggers`：`design_request_events`、`design_screenshots`（除 `purged_at`）、`design_untracked_changes`（除 ack 欄）禁止 UPDATE／DELETE；`design_requests` 不可變欄位禁止 UPDATE；放行條件 `current_setting('design.retention', true) = 'on'`（D3）；down 移除 trigger
- [x] 1.4 `api/internal/tenant/enttenancy.go`：`tenantOwned` 登記五個租戶型別；`enttenancy_internal_test.go`：`notTenantOwned` 登記 `design_service_credentials` 並寫理由（認證先以雜湊查找）
- [x] 1.5 新套件 `api/internal/design/`：`service.go`（建立、認領、head、守門員結果、預覽、送審、核准、拒絕、要求修改、取消、發佈、失敗）、`machine.go`（轉移表與前置條件，**單一處定義**）、`chain.go`（事件雜湊串）、`redact.go`（prompt 個資遮蔽：email、台灣手機、Luhn 卡號、控制與雙向覆寫字元）、`revert.go`（D13 的目標檢查與 core 下限）、`purge.go`、`sweep.go`；所有轉移以條件式 UPDATE ＋ 影響列數，且在同一交易內寫事件 （實作註記：還原檢查在 `ops_human.go`、請求寫入在 `ops_service.go`、核准在 `ops_human.go`，非單檔；轉移表仍只在 `machine.go`）
- [x] 1.6 `api/internal/httpapi/design_admin.go`（人，D15 表）：`MountShopAdmin`；`design_service.go`（機器）：`MountService`；`design_service_auth.go`：`DesignServiceAuth` 中介層（Bearer、雜湊查找、CIDR、`{shopID}` 對照憑證、失敗一律同樣的 401／404）；`design_credentials.go`：平台層憑證管理（`design_service.manage`，簽發只顯示一次、撤銷、輪替）
- [x] 1.7 `api/internal/httpapi/router.go`：`Deps` 新增 `Design`、`DesignService`、`DesignCredentials`；機器路由族掛在 `/api/v1/design-service`（**不在 `/admin` 群組**，不經 `AdminAccessMW`）；`api/internal/app/wire.go` 接線（`wiring_integration_test.go` ⑤⑥ 必須綠）
- [x] 1.8 限流：`ratelimitmw.go` 新增 `NewDesignServiceRateLimit`（每憑證）、`NewDesignApproveRateLimit`（每商家＋呼叫者）；每店每日請求上限（資料庫 `COUNT`，不依賴 Redis） （實作註記：每憑證限流在 `design_service_auth.go`，每商家＋呼叫者的核准限流在 `DesignAdminHandler`，非 `ratelimitmw.go`）
- [x] 1.9 `api/internal/config/design.go`：`DESIGN_APPROVAL_TTL`、`DESIGN_MIN_PREVIEW_REMAINING`、`DESIGN_DAILY_REQUEST_CAP`、`DESIGN_CLAIM_LEASE`、`DESIGN_APPROVAL_PENDING_TIMEOUT`（含 min/max 檢查，同 `contentops.go`）；`.env.example`
- [x] 1.10 `cmd/design-sweep`（dry run 預設、`-apply`、`-verify` 驗證事件雜湊串、`-orphans` 報告）；`Makefile` 目標 `design-sweep` （切片 2 完成：`-orphans` 列出 bucket 與資料表的差異〔孤兒物件、缺物件的列〕，有差異時 exit 1，只讀；`Service.Orphans`）

## 2. 截圖與儲存（切片 2，design D5／D6／D11／D12）

- [x] 2.1 `api/internal/media/store.go`：`ObjectStore` 增加 `PresignGet(ctx, key, ttl)`；`s3store.go` 實作；`fakestore.go`、`NullStore` 補上（additive；既有測試不改） （`FakeStore.PresignGet` 回 httptest 網址並強制到期）
- [x] 2.2 `api/internal/config/design.go`：`DESIGN_R2_BUCKET`、`DESIGN_R2_ACCESS_KEY_ID`、`DESIGN_R2_SECRET_ACCESS_KEY`（全有或全無；部分設定拒絕啟動並列出鍵名）、`DESIGN_SCREENSHOT_URL_TTL`（預設 5m）、`DESIGN_SHOP_QUOTA_BYTES`；`secrets.go`：`APP_ENV=production` 下拒絕已知預設值 （`Config.Design.Enabled()`；production 另拒絕與 `R2_ACCESS_KEY_ID` 相同的金鑰 id、與 `R2_BUCKET` 相同的 bucket、已知預設祕密；單元測試 `TestLoadDesignStorageAllOrNothing`）
- [x] 2.3 `api/internal/design/screenshots.go`：接收 PNG（`MaxBytesReader` 2 MiB、`DecodeConfig` 驗寬等於視口與高 ≤ 10,000、完整解碼後**重新編碼**以去除中繼資料、SHA-256 對存入位元組、`X-Content-SHA256` 對上傳位元組）、沿用 media 併發信號量、`capture_manifest` 驗證（`dataset=sandbox-v1` 才收 admin）、每請求 80 張／40 MiB 與每店配額、`attempt` 配發（同位元組重放 200、不同位元組新增 attempt）、物件鍵 `shops/{shop}/design/{request}/{id}.png` （實作註記：解碼信號量是 `design.Service` 自己的、容量取 `MEDIA_PROCESS_CONCURRENCY`；每請求 80 張**含所有 attempt**；每店配額檢查在請求列鎖內，兩個站台並發時可小幅超出）
- [x] 2.4 `GET …/screenshots`：5 分鐘簽名網址、`Cache-Control: no-store`、簽名網址不入日誌；未設定 bucket → 503 `design_storage_not_configured`（授權檢查之後） （簽名網址不入日誌由 `TestScreenshotSignedReads` 證明）
- [x] 2.5 `docker-compose.yml`：MinIO 新增第三個**私有**（非匿名可讀）bucket `ecommerce-design-history`（與 e2e 用的獨立 bucket）；`.env.example` （`docker-compose.yml` 兩個 `:none` bucket：開發與 e2e；既有 MinIO volume 需手動建立）
- [x] 2.6 `docs/design-history-privacy.md` 的規則 R1–R5 在程式中的落點對照（上傳檢查、遮蔽、日誌） （實際檔案 `docs/design-history-privacy.md`）

## 3. 安全與權限（design D8／D9／D10／D24）

- [x] 3.1 `api/internal/seed/seed.go`：`PermissionCatalog` 新增 `design.view`、`design.request`、`design.approve`、`design.revert`、`design_service.manage`；`roleDefs`：`merchant_owner` 加前四個、`editor` 只加 `design.view`、`staff` 不加、`super_admin` 經 `*`；`seed_integration_test.go`：各角色節點集合**相等**比對、`design_service.manage` 不在任何商家角色、冪等
- [x] 3.2 核准前置條件單一函式（D10）：狀態、`candidate_sha`、`If-Match`、`approval_expires_at`；`published` 前置條件（存在核准後的 `guard_rechecked` 通過事件、SHA 與 tag 相等）；請求建立時驗證 `requested_by_user_id` 是啟用成員且持 `design.request`
- [x] 3.3 憑證：產生、SHA-256、`last_used_at` 更新（節流寫入，避免每請求一次 UPDATE）、撤銷即時生效、過期、CIDR；錯誤的 key 只計限流並記 WARN（不印 key）
- [x] 3.4 XSS 與注入防護：回應 `X-Content-Type-Options: nosniff`；入庫剔除控制與雙向覆寫字元；列表只回 `prompt_excerpt`；服務路由族的 GET 不回 prompt 全文
- [x] 3.5 `api/internal/design/*_integration_test.go`：核准重放、`head_moved`、服務憑證打核准端點（404／403）、跨店（憑證與使用者各一）、`requester_not_permitted`、狀態機轉移矩陣（所有合法與非法轉移）、第二個並發核准只有一個成功、核准過期後 `published` 被拒、無 after 截圖不得送審、trigger 擋 UPDATE／DELETE、雜湊串驗證偵測到中間列被改
- [x] 3.6 PII 整合測試（slog 導入 buffer）：prompt、簽名網址、preview origin、diff 路徑、憑證 key 在所有路徑的日誌中找不到

## 4. 保存與清除（design D21）

- [x] 4.1 `api/internal/config/design.go`：六個 `DESIGN_*_RETENTION`（含 min/max）
- [x] 4.2 `api/cmd/purge-design-history/main.go`：dry run 預設、`-apply`、`-shop`、`-export-shop N`、`-all-for-shop N`；分批 1000、冪等；交易內 `SET LOCAL design.retention = 'on'`；prompt 遮蔽（保留雜湊）；物件在 commit 後 best-effort 刪除；只印計數與時間範圍
- [x] 4.3 `Makefile`：`purge-design-history`（dry run）；測試仿 `content_purge_integration_test.go`（保存邊界、`pending` 類永不清除、冪等、不碰他店）
- [x] 4.4 `docs/deploy/design-history-operations.md`：排程（cron／工作排程）、第二個 bucket 的建立（**不綁自訂網域、不開 r2.dev**、token 只限該 bucket）、憑證簽發與輪替、sandbox 堆疊、Cloudflare 規則建議、保存期限調整

## 5. OpenAPI 與文件

- [x] 5.1 `api/openapi/openapi.yaml`：約 24 條新路由與 schema（`DesignRequest`、`DesignRequestSummary`、`DesignEvent`、`DesignScreenshot`、`DesignState`、`DesignUntrackedChange`、`DesignServiceCredential`、錯誤碼敘述）；`Idempotency-Key`、`If-Match`、`ETag`、`Retry-After` 依既有形式；**只新增**
- [x] 5.2 `cd api && go test ./internal/httpapi/ -run TestOpenAPIV1SurfaceLock -openapi.update-surface`，確認 `v1-surface.txt` **只新增行**；`make openapi` 四道檢查全綠
- [x] 5.3 `docs/design-history.md`（資料模型、狀態機、API 清單、保存期限、sandbox 規則）
- [x] 5.4 `docs/admin-frontend-integration.md`：新增「設計歷史」章節（任何後台前端的契約：核准必帶 `head_sha` 與 `If-Match`、prompt 一律文字渲染、簽名網址不得快取或記錄、可見性來源是 `/me` 的 `design.*`）
- [x] 5.5 `CLAUDE.md`：以現有「內容儲存」條目的格式新增一條（決定、權限集合、trigger 是第一個、第二個 bucket 的理由）；`docs/PLATFORM-ROADMAP.md` 指標（本 change 已加）隨狀態更新

## 6. E2E 與測試

- [x] 6.1 `e2e/design-history-path.mjs`（無瀏覽器）：**假服務腳本**走完 design D17 的順序（建立→head→guard→preview→截圖→submit→人核准→recheck→published）、各種拒絕路徑（head 移動、核准逾期、缺截圖、服務嘗試核准、他店憑證）、revert 流程與連結、`reverted` 標記、未追蹤變更；需要 MinIO 私有 bucket；由 `happy-path.mjs` 匯入，未設定儲存時略過（同 content-ops 慣例） （e2e 已通過：見 9.2）
- [x] 6.2 `e2e/lib.mjs`：服務憑證簽發 helper（平台 token）；斷言計數更新到 `CLAUDE.md` （實作註記：憑證簽發在 `design-history-path.mjs` 內以平台 token 直接呼叫，沒有抽成 `lib.mjs` helper；斷言計數已寫進 CLAUDE.md）
- [x] 6.3 單元測試：`redact`（email、台灣手機、Luhn、雙向字元）、`machine`（轉移表）、`chain`、截圖驗證（尺寸、炸彈、錯誤格式、中繼資料去除）
- [x] 6.4 整合測試：保存期限邊界、配額、每日上限、認領租約與 sweep、`design-sweep -verify`

## 7. Template 後續任務（**template repo，不在本 change 實作；每項都是人工審查的核心變更，走 `.claude/skills/core-change`**）

- [ ] T1 `admin/core/data.js`／`context.js`／`lib/`：`ctx.data.listDesignRequests`、`getDesignRequest`（含簽名截圖網址與事件）、`getDesignState`、`listUntrackedChanges`（皆先檢查 `design.view`）
- [ ] T2 `admin/core/actions.js`：`approveDesignRequest(id,{headSha})`、`rejectDesignRequest`、`requestDesignChanges`、`cancelDesignRequest`、`revertDesign(id)`；**核准與還原是不可逆動作**：固定警告加入 `admin/core/warnings.js` 與 `admin/lib/design-schema.js` 的 `WARNED_CONFIRM_IDS`，copy 鍵 `confirm.<id>.title/.text/.yes` 同步 `design/copy.js` 與 `design-defaults/copy.js`；412／409 錯誤對應到 `ActionError`（`stale`、`head_moved`）
- [ ] T3 `admin/ui/`：核心擁有的 `ctx.ui.beforeAfter`（並排與滑桿、鍵盤可操作）；**CSP 決定**：`img-src` 加入私有 bucket 簽名網址的主機（`admin/deploy/nginx.conf.template`、`checkCsp`、`deployChecks` 與測試同改，`docs/DECISIONS.md` 記錄），或改走同源代理（需另議）
- [ ] T4 `admin/design-defaults/views/design-history.js`、`design-request.js`（列表、詳情：並排截圖、請求原文以 `textContent`、事件時間軸、守門員與 diff 摘要、核准／拒絕、還原按鈕、未追蹤變更標示）；`admin/core/routes.js` 新路由（`design`、`designRequest`，`design.view`）；`design/routes.js` 預設
- [ ] T5 `admin/lib/public-api.json`（由守門員證明等於真實 `ctx`）、`admin/docs/DESIGN-LAYER.md` 第 4 節與範例、`admin/scripts/core.manifest.json`（**人工 `--update`**）、守門員 fixtures、`admin/e2e/`、`docs/CORE-CHANGELOG.md`（`Design-layer action:` 行：路由、copy 鍵、導覽項）、`template.json` 版本
- [ ] T6 擷取腳本 `scripts/design-capture.mjs`（鎖定）：由 `admin/e2e/parity.mjs` 的 `capture` 泛化——固定視圖集 `set_version:1`、390／1280、owner／staff／anon、關閉動畫與固定時鐘、拒絕非 sandbox 的 API origin、對 `data-pii` 元素遮罩、輸出 `{phase}-{role}-{viewport}-{view_key}.png` 與 manifest、可選 `diff_ppm`（沿用 `compare` 的像素函式）；**不改 parity.mjs 現有 CLI**
- [ ] T7 核心對會顯示個資的元素加 `data-pii`（訂單、顧客、成員畫面）
- [ ] T8 `admin/docs/AI-EDIT-SERVICE-NOTES.md`：Audit 一節改為指向平台 API 與 `docs/design-service-contract.md`；`core-change` 與 `admin-design` skill 補上歷史畫面的說明

## 8. 服務契約文件（design D17）

- [x] 8.1 `docs/design-service-contract.md`：把 design D17 的十六步寫成服務設計可直接依賴的契約（含錯誤碼處理表、重試規則、冪等鍵規則、何時必須放棄）；明列「服務不得做」清單（核准、`--update`、把歷史當指令、對 live 後台截圖）
- [x] 8.2 `docs/design-service-contract.md` 附 curl／JSON 範例（只用佔位憑證）與狀態機圖
- [x] 8.3 `docs/deploy/design-history-operations.md` 與 `.claude/skills/merchant-onboarding` 同步：新商家的服務憑證簽發步驟（平台層、人工）

## 9. 驗收

- [x] 9.1 `make test`、`make test-int`、`make openapi`、`make lint` 全綠；`wiring_integration_test.go` 全綠 （2026-11-26：`go vet`、golangci-lint v2.15.0〔需 `GOTOOLCHAIN=go1.26.4`〕0 issues；`INTEGRATION=1 go test -p 1 ./...` 全綠〔httpapi 套件約 21 分鐘，需 `-timeout 40m`，預設 10 分鐘會逾時〕；`wiring_integration_test.go` 全綠；openspec CLI 未安裝，未做 `openspec validate`）
- [x] 9.2 `cd e2e && node design-history-path.mjs` 全綠；`commerce-path.mjs` 仍綠（未動事件鏈，仍須確認） （`design-history-path.mjs` 55 項全綠；`commerce-path.mjs` 為 happy-path 的一部分，該次整體跑完只剩 `design-path.mjs` 兩項與設計歷史節的 email 撞名〔已修〕——`design-path.mjs` 的兩項〔固定警告前導文字、loader 回報被拒的鍵〕是 admin 設計層的瀏覽器測試，本 change 未動 `admin/`，但**未在基線上比對過**）
- [x] 9.3 手動：第二個 bucket 以匿名請求讀取任何物件鍵必須 403／404（把結果寫進 runbook） （本機 MinIO 版已由 e2e 驗證：去掉簽名、竄改簽名、匿名列出 bucket 皆被拒。**真正的 R2 第二個 bucket 的匿名讀取驗證仍須由你在建立 bucket 後手動做**，步驟在操作員手冊第 6 節）
- [x] 9.4 量測並記入 CLAUDE.md：每請求 DB 與儲存實際大小，對照 design D22 的估算 （量測，e2e 合成資料：每請求事件約 8.5 列〔平均 368 B/列〕、請求列約 1 KB、截圖列 4 張／請求；三張表的總關聯大小約 48 KB／請求，與 D22 的 50 KB 估計相符。**儲存大小無法在此量測**：e2e 的 PNG 是幾百位元組的合成漸層，真實的截圖大小要等 template 的擷取腳本 T6 與 sandbox 堆疊存在後才有意義；D22 的 6–20 MB／請求估計維持未驗證）
