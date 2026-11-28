## ADDED Requirements

### Requirement: 協調器與 agent 的分工

系統 SHALL 以獨立的協調器（orchestrator）驅動每一件設計請求；agent 工作階段 MUST 只做「讀取設計層、修改設計層檔案、執行本機守門員、輸出補丁」。推分支、開 PR、部署預覽、截圖與上傳、對平台的所有呼叫、預算與開關 MUST 只由協調器執行。協調器 MUST NOT 執行 agent 產生的程式碼或腳本，且 MUST NOT 連接平台主資料庫。

#### Scenario: agent 無法呼叫平台
- **WHEN** agent 在沙盒內嘗試以任何方式呼叫 `/api/v1/design-service/**` 或 `/api/v1/admin/**`
- **THEN** 沙盒內沒有可用的金鑰或網路，呼叫失敗，且請求狀態不變

#### Scenario: 補丁由協調器套用
- **WHEN** agent 完成一輪修改
- **THEN** 只有 `/mnt/session/outputs/` 內的補丁被取回，並在可信驗證器內套用；沙盒內的 git 狀態不被採信

### Requirement: 狀態對應與平台為權威

協調器 MUST 以平台既有九態機為唯一狀態來源，並依 design.md 第 4 節對應每一步到既有的服務端點。協調器 MUST NOT 自行推進平台狀態到 `approved`、`rejected`、`cancelled`，且 MUST NOT 把聊天中的「已核准」當依據；唯一依據是平台回報的 `status = approved` 與 `approved_head_sha`。對平台的寫入 MUST 遵守契約的冪等與重試規則。

#### Scenario: 全流程狀態
- **WHEN** 一件 storefront 或 admin 請求經過守門員、預覽、截圖、送審、人類核准、人工合併
- **THEN** 平台狀態依序為 `drafting`、`previewing`、`awaiting_approval`、`approved`、`published`，且每個轉移都有平台事件

#### Scenario: 聊天中的核准不算
- **WHEN** 商家在對話中寫「我核准了」而平台狀態仍為 `awaiting_approval`
- **THEN** 協調器不做任何發佈相關動作

### Requirement: 可信驗證器為守門員的權威結果

回報給平台的 `guard-runs` 結果 MUST 來自可信驗證器，而非 agent 的自述或分支上的腳本。驗證器 MUST 先以自備程式檢查補丁形狀與路徑（所有變更檔案必須落在 base 版 `template.json` 該站台的 `designPaths`、不在 `NEVER` 清單、無符號連結、無 `..`、無子模組、無檔案模式變更、不超出守門員大小上限），再以 base 版的守門員腳本（`check-layers`、`check-design`、`check-locked`）在套用補丁後的樹上執行。超出範圍的補丁 MUST NOT 重試，MUST 以 `scope_violation` 失敗並警報。

#### Scenario: 補丁改了守門員自己
- **WHEN** 補丁修改 `scripts/check-scope.mjs` 使其永遠通過
- **THEN** 驗證器的自備路徑檢查拒絕該補丁，請求以 `scope_violation` 失敗，分支不被推送

#### Scenario: 補丁在設計層內但違規
- **WHEN** 補丁在 `admin/design/` 內使用被禁的 `fetch`
- **THEN** base 版 `check-design` 失敗，規則 id 與位置以資料區塊回饋 agent，最多重試 3 次

#### Scenario: 失敗時不得部署預覽
- **WHEN** 最近一次驗證器結果為不通過
- **THEN** 不推分支、不部署預覽、不登記預覽

### Requirement: 預覽與截圖由可信程式產生

預覽 MUST 使用 template 的預覽映像、唯讀預覽模式、嚴格 CSP、獨立且由營運者登記的預覽 origin，內容 MUST 來自驗證器產出的補丁樹並與 `head_sha` 對應。截圖 MUST 由協調器控制的擷取環境產生（admin 只對 sandbox 堆疊的合成資料集 `sandbox-v1`），agent MUST NOT 參與截圖或上傳。擷取（有瀏覽器）與上傳（有金鑰）MUST 在不同進程執行，擷取進程的環境 MUST NOT 含服務金鑰。

#### Scenario: 擷取進程沒有金鑰
- **WHEN** 擷取進程啟動
- **THEN** 其環境變數中不存在 `DESIGN_SERVICE_KEY`

#### Scenario: 對 live 後台拒絕
- **WHEN** 擷取目標不在 sandbox 允許清單
- **THEN** 擷取程式拒絕啟動，協調器以 `live_data_capture_refused` 處理並警報

### Requirement: 發佈由人合併、由對帳器回報

協調器 MUST NOT 合併或推送 `main`。核准後，協調器 MUST 先對確切的 `approved_head_sha` 於驗證器重跑守門員並回報 `phase: recheck`；人 MUST 以 merge commit 合併 PR；對帳器 MUST 確認合併提交的第二親等於核准 SHA、設計路徑上合併結果與核准 head 無差異，之後才建立 `publish_tag` 的 tag 並回報 `published`（`published_sha` 等於核准 SHA）。不符時對帳器 MUST NOT 回報並 MUST 警報。

#### Scenario: 正確的合併
- **WHEN** 營運者以 merge commit 合併已核准的 PR
- **THEN** 對帳器回報 `published` 並建立 tag

#### Scenario: squash 合併
- **WHEN** PR 以 squash 或 rebase 合併，使 `main` 上沒有核准的 head
- **THEN** 對帳器不回報 `published` 並警報；平台的未追蹤變更對帳最終標示該提交

#### Scenario: 合併前多加料
- **WHEN** 合併前分支被推入核准 SHA 之後的新提交
- **THEN** 合併提交的第二親不等於核准 SHA，對帳器拒絕回報

#### Scenario: 逾期才合併
- **WHEN** 營運者在 `approval_expires_at` 之後才合併
- **THEN** 平台已將請求標為 `failed(approval_expired)`，對帳器不回報，並以未追蹤變更處理

### Requirement: 還原不使用模型

`kind=revert` 的請求 MUST 由協調器以決定性方式（目標 tag 的設計層路徑檢出為新 commit）產生，MUST NOT 啟動 agent 工作階段，且 MUST 走與一般請求相同的守門員、預覽、截圖、核准與人合併流程。

#### Scenario: 還原請求無模型費用
- **WHEN** 協調器處理一件還原請求
- **THEN** 不建立任何 Managed Agents session，費用為零模型 token

### Requirement: 要求修改使用同一工作階段

商家「要求修改」使請求回到 `drafting` 時，協調器 MUST 在**同一請求**的同一 session 送入修改備註（以資料區塊包裝），且每請求最多 3 輪。不同請求之間 MUST NOT 共用 session。

#### Scenario: 超過修改輪數
- **WHEN** 同一請求第 4 次被要求修改
- **THEN** 協調器以 `failed(revision_limit)` 結束並請商家重新發起
