## ADDED Requirements

### Requirement: 版本預覽 MUST 是不屬於任何請求的用完即丟預覽

系統 SHALL 允許持有 `design.view` 的商家成員，對該店一筆曾發佈的設計請求（`published` 或 `reverted`，且有 `published_tag`）請求「版本預覽」：部署一份只含該 tag 的設計層與**目前** core 的用完即丟預覽，由該成員以自己的身分登入、以目前資料即時觀看。版本預覽 MUST NOT 屬於任何設計請求，MUST NOT 佔用每 `(shop_id, site)` 至多一筆非終態請求的名額，也 MUST NOT 寫入 `design_request_events` 或改變任何 `design_requests` 列。版本預覽 MUST NOT 拍攝或保存任何截圖。進行中的請求與版本預覽 MUST 可同時存在。

#### Scenario: 建立版本預覽
- **WHEN** 持 `design.view` 的成員對一筆 `published` 請求呼叫 `POST /design/requests/{id}/version-preview`，且相容檢查通過
- **THEN** 回 202 與 `{id, status: "queued"}`，建立一筆 `design_version_previews` 列，`design_requests` 與 `design_request_events` 不變

#### Scenario: 不阻擋也不被阻擋
- **WHEN** 該站台已有一筆 `awaiting_approval` 的請求，成員請求版本預覽
- **THEN** 版本預覽被接受，該請求的狀態與 `rev` 不變

#### Scenario: 目標不是已發佈的設計
- **WHEN** 目標請求狀態為 `rejected`
- **THEN** 回 409 `invalid_state`，不建立列

#### Scenario: 不保存截圖
- **WHEN** 服務為版本預覽回報 `ready`
- **THEN** `design_screenshots` 沒有任何新列，私有 bucket 沒有新物件

### Requirement: 部署前 MUST 先通過 core 相容檢查，不相容 MUST 拒絕並說明原因

建立版本預覽時，系統 MUST 以還原使用的同一個檢查（`target.core_version >= design_site_state.design_floor_version`）同步判斷。不通過 MUST 回 409 `revert_below_core_floor` 並附說明原因的訊息，目標已是該站台目前設計 MUST 回 409 `already_current`；兩者皆 MUST NOT 建立列、MUST NOT 讓服務部署任何東西。通過檢查後，服務在部署前 MUST 以目前 core 對該組合執行守門員；守門員失敗時服務 MUST 回報 `failed`（`code = guard_failed`、規則 id）且不得部署，發起者 MUST 能看到該原因。

#### Scenario: 早於 core 下限
- **WHEN** 目標的 `core_version` 低於目前的 `design_floor_version`
- **THEN** 回 409 `revert_below_core_floor`，訊息說明原因，沒有 `design_version_previews` 列

#### Scenario: 目標已是目前設計
- **WHEN** 目標就是 `design_site_state.current_request_id`
- **THEN** 回 409 `already_current`

#### Scenario: 守門員以目前 core 失敗
- **WHEN** 服務認領後以目前 core 對目標設計層執行守門員並失敗
- **THEN** 服務回報 `failed`（`guard_failed`），列轉為 `failed`，沒有部署，發起者讀取該列時看到原因與規則 id

### Requirement: 版本預覽的權限、可見性與跨店隔離

建立與讀取版本預覽 MUST 要求 `design.view`（`merchant_owner`、`editor`；`staff` 不可；`super_admin` 經 `*`），不新增權限節點。預覽的 `origin` MUST 只回給發起者本人；他人讀同一列時 MUST 不含 `origin`。目標請求屬於他店 MUST 回 404。服務路由族 MUST 只提供版本預覽的列出、認領、`ready`、`failed`，MUST NOT 提供建立、核准或任何改變請求狀態的端點。「改成這個版本」入口 MUST 只對持 `design.revert` 者顯示，且 MUST 呼叫既有的請求還原端點。

#### Scenario: staff 被拒
- **WHEN** `staff` 呼叫 `POST /design/requests/{id}/version-preview`
- **THEN** 回 403

#### Scenario: 他人看不到 origin
- **WHEN** 另一位 `editor` 讀取 owner 發起的版本預覽
- **THEN** 回應不含 `origin`

#### Scenario: 跨店
- **WHEN** A 店成員以 B 店的請求 id 要求版本預覽
- **THEN** 回 404

#### Scenario: 服務不能建立
- **WHEN** 持有服務憑證的服務嘗試建立版本預覽
- **THEN** 回 404 或 403（路由不存在於服務路由族）

### Requirement: 版本預覽 MUST 短命、受限流，且嚴格隔離

版本預覽 MUST 有 `expires_at`，預設 30 分鐘（`DESIGN_VERSION_PREVIEW_TTL`，上限 2 小時）；服務 MUST 於到期拆除部署，清掃工作 MUST 把逾時的列標為 `expired`、把逾租約的 `claimed` 列標為 `failed`（`service_lost`）。同一 `(shop_id, site)` 同時 MUST 至多一份有效版本預覽，新請求 MUST 使舊的 `queued`、`claimed`、`ready` 列轉為 `superseded`。每 `(shop, user)` 每小時與每店每日 MUST 受上限約束（超過回 429 `daily_cap_reached` 並帶 `Retry-After`），計數 MUST 取自資料庫。版本預覽 MUST 套用與改版預覽相同的嚴格 CSP、獨立 origin 與 `noindex`，且 MUST 由核心顯示設計層無法移除的橫幅（標示版本預覽、tag、到期時間）。登記的 `origin` MUST 只含 scheme 與主機，MUST NOT 含路徑、查詢或片段。

#### Scenario: 到期
- **WHEN** 一份 `ready` 的版本預覽超過 `expires_at`
- **THEN** 清掃工作把列標為 `expired`，且服務已拆除該部署

#### Scenario: 取代舊預覽
- **WHEN** 同一站台已有一份 `ready` 的版本預覽，成員要求另一版
- **THEN** 舊列轉為 `superseded`，新列為 `queued`

#### Scenario: 超過限流
- **WHEN** 同一成員一小時內第 7 次要求版本預覽
- **THEN** 回 429 並帶 `Retry-After`，不建立列

#### Scenario: origin 含路徑
- **WHEN** 服務以含路徑的 `origin` 回報 `ready`
- **THEN** 回 422，列維持 `claimed`

#### Scenario: 嚴格 CSP
- **WHEN** 瀏覽器載入版本預覽的任何頁面
- **THEN** 回應帶與改版預覽相同的嚴格 CSP，且頁面頂端有核心橫幅

### Requirement: 使用紀錄 MUST 輕量，且 MUST NOT 成為歷史的一部分

系統 SHALL 以 `design_version_previews`（`tenantOwned`，欄位：`shop_id`、`site`、`target_request_id`、`target_tag`、`requested_by_user_id`〔無外鍵〕、`status`、`failure_code`、`core_version`、`design_floor_version`、`preview_origin`、`created_at`、`ready_at`、`expires_at`）記錄用量與限流。此表 MUST NOT 有 append-only trigger 或雜湊串，MUST NOT 寫入 `design_request_events`。日誌 MUST 只含 id、狀態與計數，MUST NOT 含 origin 或簽名網址。此表 MUST 依 `DESIGN_VERSION_PREVIEW_RETENTION`（預設 90 天）由清除指令移除，且 MUST NOT 被視為核准或決定的依據。

#### Scenario: 日誌不含 origin
- **WHEN** 版本預覽走完 `queued` 到 `ready` 並到期
- **THEN** 所有日誌輸出中找不到 `preview_origin` 的值

#### Scenario: 清除
- **WHEN** 以 `-apply` 執行清除指令且有列早於保存期限
- **THEN** 僅這些列被刪除，`design_requests`、事件與截圖不受影響，且重跑為冪等

### Requirement: CTA 只進入既有還原流程，且 MUST 誠實標示限制

版本預覽畫面與歷史畫面的「改成這個版本」MUST 只導向既有還原流程（`POST /design/requests/{id}/revert`），因此仍 MUST 經守門員、改版預覽、前後截圖與人類核准；版本預覽本身 MUST NOT 被視為核准，也 MUST NOT 縮短或略過還原流程的任何步驟。歷史畫面 MUST 在「預覽此版本」附近以固定文字說明：版本預覽是用目前的 core 與資料即時渲染、用來決定，不是當時核准內容的紀錄，歷史紀錄以截圖為準；詳情頁的並排截圖 MUST 仍為「當時」的證據。

#### Scenario: 先預覽再還原
- **WHEN** owner 在版本預覽按「改成這個版本」並確認
- **THEN** 建立一筆新的 `kind = revert` 請求（`drafting`），仍需人類核准才會發佈

#### Scenario: 沒有 design.revert
- **WHEN** `editor` 開啟版本預覽
- **THEN** 看不到「改成這個版本」按鈕，直接呼叫還原端點回 403

#### Scenario: 限制說明
- **WHEN** 歷史畫面顯示「預覽此版本」
- **THEN** 附近有固定文字說明即時畫面不是核准當時的紀錄
