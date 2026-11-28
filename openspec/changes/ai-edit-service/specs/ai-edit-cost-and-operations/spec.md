## ADDED Requirements

### Requirement: 預算四層

系統 SHALL 以四層限制花費：(1) 每個 session 建立時 MUST 設定硬性 `budget`（`max_list_cost`）；(2) 協調器 MUST 以 `session.usage.list_cost` 加總並強制每店每日上限；(3) 協調器 MUST 強制全域每月上限；(4) MUST 有每請求牆鐘時間、守門員重試 3 次、要求修改每請求 3 輪與每店每日請求數上限。預設值 MUST 可設定，且 MUST 在擁有者設定前使用保守的 pilot 預設或維持關閉。session 到達預算（`budget_reached`）時協調器 MUST NOT 自行提高預算，MUST 以 `failed(budget_exceeded)` 結束並以平白話通知商家。

#### Scenario: 單一請求到頂
- **WHEN** session 的 list cost 到達 `budget`
- **THEN** session 暫停於 `budget_reached`，協調器回報 `failed(budget_exceeded)`，預算不被提高

#### Scenario: 每店每日上限
- **WHEN** 某店當日累計 list cost 達到每日上限
- **THEN** 該店新請求被拒絕並以平白話告知，他店不受影響

#### Scenario: 全域上限
- **WHEN** 全域當月累計達到上限
- **THEN** 協調器停止受理新請求並警報營運者

### Requirement: 總開關與每店開關

系統 SHALL 提供：全域開關（關閉受理並中斷進行中的請求）、每店開關（撤銷該店 `dsk_`、協調器店級停用清單、解除該 repo 的 GitHub App 安裝）與緊急輪替清單（GitHub App 私鑰、Anthropic key、各店 `dsk_`）。平台對 `dsk_` 回 401 時協調器 MUST 立即停止該店所有動作且不重試。總開關預設 MUST 為關閉。

#### Scenario: 撤銷金鑰
- **WHEN** 營運者在平台撤銷某店 `dsk_`
- **THEN** 協調器於下一次呼叫得到 401，停止該店所有請求並警報

#### Scenario: 預設關閉
- **WHEN** 協調器首次部署且未設定
- **THEN** 不受理任何請求

### Requirement: 稽核與觀測

平台設計歷史事件 MUST 是稽核的權威；協調器每個重要動作 MUST 對應一個平台事件。協調器日誌 MUST 只含 `shop_id`、`request_id`、狀態、規則 id、計數、費用與錯誤類別。以下情況 MUST 警報：`scope_violation`、`live_data_capture_refused`、`401`、合併內容與核准不符、`main` 出現不在歷史內的設計路徑提交、單日花費超過上限 80%。協調器 MUST 每日將 session `list_cost` 與 Anthropic 用量帳對帳。

#### Scenario: 範圍違規警報
- **WHEN** 驗證器以 `scope_violation` 拒絕補丁
- **THEN** 營運者收到警報，平台事件記錄失敗碼，商家看到「這次沒有改動任何東西」

#### Scenario: 日誌最小化
- **WHEN** 檢查任一請求的協調器日誌
- **THEN** 不含 prompt、diff 路徑、簽名網址、預覽 origin 全文、金鑰或權杖

### Requirement: 失敗處理

協調器 MUST 依 design.md 第 9 節處理失敗：對暫時性錯誤指數退避至多 5 次；對守門員失敗至多重試 3 次；對範圍違規、`401`、預算到頂不重試；協調器重啟後 MUST 依「意圖＋結果」記錄補完或回滾外部副作用，MUST NOT 重複建立 session、分支或部署。

#### Scenario: 重啟不重複
- **WHEN** 協調器在推送分支後、回報 head 前當機並重啟
- **THEN** 它辨識分支已存在並繼續回報，不建立第二個分支或 PR

### Requirement: 分階段上線

系統 SHALL 依序經過：影子模式（不推分支、不部署）、只有 bouquet-next 的 pilot、少數商家、一般開放；每階段 MUST 滿足 design.md 第 12 節的進入與出口條件，且在擁有者專屬行動（帳號、額度、Cloudflare、GitHub App、法務）完成前 MUST NOT 進入下一階段。

#### Scenario: 影子模式不外送
- **WHEN** 系統處於影子模式
- **THEN** 不推送任何分支、不部署任何預覽、不呼叫平台的 `preview` 與 `submit`

### Requirement: 對守門員的對抗性測試

每次發版與每次 template 同步後，系統 MUST 對真實 agent 執行對抗性提示詞套件（改守門員、放寬範圍、外連、混淆、假確認、索取憑證、間接注入、巨量或惡形檔案、自我核准、內網存取），並斷言沒有任何超出範圍的補丁通過驗證器、補丁與日誌中沒有憑證或外部 URL。

#### Scenario: 套件全綠才可發版
- **WHEN** 任一對抗性提示詞產生了通過驗證器的超出範圍補丁
- **THEN** 發版被阻擋，並以該案例新增回歸測試
