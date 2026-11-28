> **本 change 只有設計。** 下列任務全部**未開始**，且在 design.md 第 14 節的擁有者專屬行動（O1–O10）有答覆之前不得開工。標 `[O]` 者只有擁有者能做；標 `[$]` 者會花錢。任何任務都不得在未經擁有者明確啟用下呼叫付費 API、建立帳號、放入憑證或部署。總開關預設**關**。

## 1. 文件修訂（不含程式碼；可最先做）

- [ ] 1.1 `docs/design-service-contract.md` 第 3 節第 11–14 步、第 8 節、`openspec/changes/design-history/design.md` D17 第 11–12 步：改寫為「人合併、對帳器回報」（AD7）；分支名稱改 `design/<site>-<request-id>`（AD6）；補第 4 節補丁交出模型的說明（服務端不等於 agent）
- [ ] 1.2 `docs/deploy/design-history-operations.md`：新增營運者章節（合併 PR 的步驟、只允許 merge commit 的 repo 設定、`DESIGN_APPROVAL_TTL=24h`、警報處理）
- [ ] 1.3 `docs/deploy/sandbox-stack.md`、`docs/design-history-privacy.md`：修正「template T6 尚未實作」的過時敘述（`scripts/design-capture.mjs` 已存在於 template）
- [ ] 1.4 `openspec/changes/design-history/design.md` Q9 附註：指向本 change 的 AD4／AD7，說明「服務只推分支」由協調器（不是 agent）執行
- [ ] 1.5 `docs/PLATFORM-ROADMAP.md` 與 `CLAUDE.md` 加指標

## 2. 平台側 additive 小改（ecommerce；每項獨立、皆須維持 `make test`／`make test-int`／`make openapi` 全綠）

- [ ] 2.1 確認 `DESIGN_APPROVAL_TTL` 設 24 小時不需程式改動（上限本來就是 24 小時）；補測試斷言「24 小時邊界」
- [ ] 2.2 評估是否需要新增 `published` 的樹雜湊比對（AD7 選項 B）——**預設不做**；只在 P3 營運者合併成為瓶頸時另開 change
- [ ] 2.3 **G1**：確認或新增「預覽 origin 的公開讀取」機制（storefront 預覽的 CORS／allowed-origins、publishable key 在預覽 origin 的唯讀行為）；沒有此機制 storefront 預覽不可上線
- [ ] 2.4 服務路由新增一個唯讀、只回計數的「用量查詢」端點（協調器對帳 `DESIGN_DAILY_REQUEST_CAP` 用）——**僅在 `GET /requests` 不足時才做**
- [ ] 2.5 為 AI 建立的請求提供可辨識欄位（`created_via=ai_service`）——**僅在營運需要區分時才做**；additive

## 3. 探測（spike；每個只做最小呼叫，先取得擁有者對應的 O 項）

- [ ] 3.1 `[O1][$]` S1：沙盒是否有 git／Node／（若需要）Chromium；`limited` 網路下 git proxy 是否可用
- [ ] 3.2 `[O1][O5][$]` S2：`github_repository.authorization_token` 能否用 GitHub App 安裝權杖；唯讀 clone 私有 repo
- [ ] 3.3 `[O1][$]` S3：`/mnt/session/outputs/` 補丁取回
- [ ] 3.4 `[O1][$]` S4：成本與預算實測（3 個固定提示詞 × Opus／Sonnet），回填 design.md 第 8 節
- [ ] 3.5 `[O4]` S5：template 預覽映像一請求一實例
- [ ] 3.6 S6：假 agent 端對端空轉（不呼叫模型；用 sandbox 平台與測試 repo）
- [ ] 3.7 S7：storefront 預覽 CORS 缺口（對應 2.3）

## 4. 協調器（`ecommerce/cmd/ai-edit-orchestrator`，獨立進程，不連主資料庫）

- [ ] 4.1 受理 API：使用者 token 驗證、`design.request` 檢查、`RedactPII`、速率限制、總開關與店級停用
- [ ] 4.2 小型獨立資料庫（請求↔session↔分支↔安裝的對應、外部副作用的意圖與結果、預算帳）；所有查詢必須帶 `shop_id`
- [ ] 4.3 狀態機驅動器：實作 design.md 第 4 節每一步與冪等、重試表（第 9 節）、認領續約
- [ ] 4.4 Managed Agents 客戶端：agent／environment 以版本控管的定義檔（`ant apply`）建立；session 建立（唯讀 repo、`budget`、`initial_events`）；事件串流與重新連線（先串流再補列事件、去重）；自動拒絕待確認；`session.usage` 記帳；補丁取回
- [ ] 4.5 GitHub App 客戶端：鑄造唯讀與可寫安裝權杖（限定單一 repo 與權限）、推分支、開 draft PR、設 commit status、查 PR 合併狀態
- [ ] 4.6 預算：四層（AD16）、到頂處理、每日費用對帳
- [ ] 4.7 開關：全域、每店；`401` 即停；演練腳本（AD17）
- [ ] 4.8 對帳器：合併核對（第二親＝核准 SHA、設計路徑無差異）、打 `publish_tag`、回報 `published`、每日未追蹤變更、補回報掃描
- [ ] 4.9 日誌與指標、警報（第 10 節）；slog buffer 測試證明無 prompt／金鑰／權杖／origin 全文

## 5. 可信驗證器、預覽、擷取

- [ ] 5.1 驗證器：補丁形狀檢查、自備路徑檢查（base `template.json`）、base 版腳本覆蓋後執行三支守門員；無網路、無憑證、用完即丟
- [ ] 5.2 預覽部署腳本：template 映像、`ADMIN_PREVIEW_MODE=change`、預覽目錄記錄 `head_sha`、TTL 拆除、`PUT /preview`
- [ ] 5.3 擷取：`design-capture capture` 與 `upload` 分兩個進程（擷取時環境無金鑰）；exit code 對應（3／4／5）
- [ ] 5.4 還原請求：決定性還原（AD21）

## 6. template 側（`merchant-template`；**人工審查的核心變更**，依 `core-change` 流程與版本發佈）

- [ ] 6.1 核心擁有的「描述你想要的改版」入口（`ctx` 新函式屬人工審查變更；入口呼叫協調器受理 API，帶使用者 token）；`lib/public-api.json`、`DESIGN-LAYER.md`、守門員 fixtures、e2e、`CORE-CHANGELOG`
- [ ] 6.2 受理狀態顯示（請求進度、失敗的平白話原因）
- [ ] 6.3 `guards.yml` 對 `design/*` 的補充說明（第三道檢查的定位）；不需改觸發條件（AD6 已對齊）
- [ ] 6.4 更新 `admin/docs/AI-EDIT-SERVICE-NOTES.md`：補丁交出、人合併、merge commit、可信驗證器

## 7. Managed Agents 定義

- [ ] 7.1 兩個 agent 定義（admin 設計、storefront 設計）：system＝`DESIGN-LAYER.md` 原文＋協調器的資料區塊規則；工具僅 bash／讀／寫／編輯／搜尋；停用 web 工具；model＝`claude-opus-5-5`、effort `high`（AD14）
- [ ] 7.2 environment 定義：`limited`、`allowed_hosts` 空、`allow_package_managers: false`（S1 決定是否例外）
- [ ] 7.3 固定提示詞集（成本回歸與對抗性測試共用）

## 8. 測試與上線

- [ ] 8.1 對抗性提示詞套件（design.md 第 12 節第 3 點每一項）；每次發版與每次 template 同步後跑
- [ ] 8.2 隔離、洩漏掃描、失敗注入、人合併情境、成本回歸
- [ ] 8.3 P1 影子模式（`[O1][O2][$]`）：bouquet-next 20 件
- [ ] 8.4 P2 pilot：admin 先行；storefront 待 2.3
- [ ] 8.5 P3：每店 GitHub App 安裝與 `dsk_` 簽發（人工）

## 9. 營運

- [ ] 9.1 營運手冊：合併 PR、警報處理、開關與輪替演練（協調器私鑰、Anthropic key、各店 `dsk_`）
- [ ] 9.2 session 保留與刪除（AD24）的設定與演練（刪除為不可逆，須明確啟用）
- [ ] 9.3 `docs/launch-checklist.md`（私有）與 `merchant-onboarding` skill：新商家開通 AI 改版的步驟（簽發 `dsk_`、安裝 GitHub App、repo 只允許 merge commit、寫入協調器對應表）

## 10. 驗收

- [ ] 10.1 所有 `make test`、`make test-int`、`make openapi`、`make lint` 全綠；協調器自己的單元與整合測試全綠
- [ ] 10.2 S6 空轉與 P1 影子模式的結果寫回 design.md（成本表、通過率）
- [ ] 10.3 擁有者對 P2 出口條件簽收
