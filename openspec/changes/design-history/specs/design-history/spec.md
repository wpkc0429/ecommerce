## ADDED Requirements

### Requirement: 每一次經 AI edit service 的重新設計 MUST 是一筆設計請求紀錄

系統 SHALL 為每一次重新設計保存一筆 `design_requests` 紀錄，屬於單一商家與單一站台（`storefront` 或 `admin`），並記錄：請求人（商家使用者）、商家的原話（prompt，經個資遮蔽）與其 SHA-256、`base_sha`、`head_sha`、分支、template 的 `core_version` 與 `design_floor_version`、diff 摘要（檔案路徑與行數，MUST NOT 含檔案內容）、守門員報告摘要（通過與否、檢查的 SHA、規則 id）、預覽的 origin 與到期時間（MUST NOT 記錄含路徑或查詢字串的永久連結）、狀態、核准者與時間、發佈 tag 與部署識別碼。狀態 MUST 為 `drafting`、`previewing`、`awaiting_approval`、`approved`、`published`、`rejected`、`failed`、`cancelled`、`reverted` 之一。同一商家同一站台同時 MUST 至多一筆非終態請求（`drafting`、`previewing`、`awaiting_approval`、`approved`）。

#### Scenario: 建立請求
- **WHEN** 持有該店服務憑證的服務以 `Idempotency-Key` 建立請求，`requested_by_user_id` 是該店啟用中且持 `design.request` 的成員
- **THEN** 回 201，請求為 `drafting`，並有一筆 `created` 事件

#### Scenario: 冪等重送
- **WHEN** 以相同 `Idempotency-Key` 與相同本文重送建立請求
- **THEN** 回 200 與既有請求，不新增第二筆

#### Scenario: 同鍵不同內容
- **WHEN** 以相同 `Idempotency-Key` 但不同本文建立
- **THEN** 回 422 `idempotency_key_reused`

#### Scenario: 請求人不具資格
- **WHEN** `requested_by_user_id` 不是該店啟用成員，或不持 `design.request`（例如 staff）
- **THEN** 回 403 `requester_not_permitted`，不建立請求

#### Scenario: 同站台已有進行中的請求
- **WHEN** 同商家同站台已有非終態請求時再建立
- **THEN** 回 409 `request_in_progress`

#### Scenario: 每日上限
- **WHEN** 該店當日建立的請求數已達 `DESIGN_DAILY_REQUEST_CAP`
- **THEN** 回 429 `daily_cap_reached`

### Requirement: 歷史 MUST 只能追加

每個狀態轉移與重要動作 MUST 在**同一交易**內寫入一筆 `design_request_events`，事件含序號、種類、前後狀態、行為者類型（`service`、`user`、`system`）、當下的 `head_sha`、不含 prompt 與 URL 的 `detail`，以及串接前一筆的 SHA-256 雜湊。系統 MUST NOT 提供更新或刪除事件、截圖列與未追蹤變更列的任何應用層路徑；資料庫 MUST 以 trigger 拒絕對這些表（與請求列的不可變欄位）的 UPDATE 與 DELETE，唯一的例外是保存期限清除指令在其交易內明示放行。請求列的目前狀態 MUST 只能由狀態機以條件式 UPDATE 並檢查受影響列數的方式改變。

#### Scenario: 事件與狀態同交易
- **WHEN** 狀態機把請求從 `drafting` 轉為 `previewing`
- **THEN** 請求列的狀態與一筆 `status_changed` 事件同時存在，或同時不存在

#### Scenario: 直接改寫事件被拒
- **WHEN** 以一般資料庫連線對 `design_request_events` 執行 UPDATE 或 DELETE
- **THEN** 失敗且資料不變

#### Scenario: 雜湊串偵測竄改
- **WHEN** 有人以特權繞過 trigger 修改某請求中間一筆事件，之後執行 `design-sweep -verify`
- **THEN** 報告該請求的雜湊串不連續

### Requirement: 狀態機與送審條件

系統 SHALL 以單一轉移表定義合法轉移：`drafting → previewing`（該 `head_sha` 的守門員通過且檢查的 SHA 相等、已登記預覽）、`previewing → awaiting_approval`（`submit`）、`awaiting_approval → approved`（人類核准）、`approved → published`、任何非終態 `→ failed`、非終態 `→ cancelled`、`awaiting_approval → rejected`、`awaiting_approval → drafting`（要求修改或服務重做）、`published → reverted`。`submit` MUST 要求：同一 `head_sha` 的 before 與 after 完整截圖集都已存在且視圖集版本相同、預覽尚未過期且剩餘時間不低於 `DESIGN_MIN_PREVIEW_REMAINING`；送審時 MUST 鎖定 `candidate_sha := head_sha`。核准之後 `head_sha` MUST NOT 再被修改。`awaiting_approval` 超過逾時期限 MUST 由清掃工作轉為 `failed`（`approval_timeout`）。

#### Scenario: 守門員未通過不得預覽
- **WHEN** 服務在 `guard.passed = false` 時登記預覽
- **THEN** 回 409 `guard_not_passed`

#### Scenario: 缺少 after 截圖不得送審
- **WHEN** after 截圖集不完整時呼叫 `submit`
- **THEN** 回 409 `screenshots_incomplete`，請求維持 `previewing`

#### Scenario: 非法轉移
- **WHEN** 對 `rejected` 的請求呼叫核准
- **THEN** 回 409 `invalid_state`

### Requirement: 核准 MUST 綁定被檢查與預覽的 head_sha

核准 MUST 只能由持有 `design.approve` 的人類透過後台端點執行，本文 MUST 帶 `head_sha`、標頭 MUST 帶 `If-Match`（請求 `rev`，缺少回 428）。系統 MUST 在 `status = awaiting_approval`、`head_sha = candidate_sha` 且 `rev` 吻合時，以條件式 UPDATE 並檢查受影響列數完成核准；核准同時 MUST 記錄 `approved_head_sha`、`approval_expires_at`（預設 1 小時）並配發 `publish_tag`。核准 MUST 為單次消耗：只有 `approved` 且未逾期的請求能被記錄為 `published`，且一次核准最多換得一次發佈。

#### Scenario: 分支在送審後移動
- **WHEN** 人類核准時本文的 `head_sha` 不等於 `candidate_sha`
- **THEN** 回 409 `head_moved`，請求維持 `awaiting_approval`

#### Scenario: 過期的 ETag
- **WHEN** 核准的 `If-Match` 與目前 `rev` 不符
- **THEN** 回 412 `precondition_failed`

#### Scenario: 重放核准
- **WHEN** 同一使用者以相同 `head_sha` 對已核准的請求重送核准
- **THEN** 回 200 與相同結果，不產生新的事件與新的 tag

#### Scenario: 他人對已核准的請求再次核准
- **WHEN** 另一個使用者對已 `approved` 的請求送核准
- **THEN** 回 409 `invalid_state`

### Requirement: 發佈 MUST 先重檢確切的 SHA

系統 MUST 只在以下條件全部成立時接受 `published` 回報：請求為 `approved` 且 `approval_expires_at` 尚未到；核准之後存在一筆 `guard_rechecked` 事件，其 `head_sha` 等於 `approved_head_sha` 且守門員通過；本文的 `published_sha` 等於 `approved_head_sha`、`tag` 等於 `publish_tag`。成功時 MUST 記錄 `published_at`、`published_tag`、可選的 `deployment_id`，並更新該站台的目前設計。

#### Scenario: 未重檢
- **WHEN** 服務在核准後未回報 `guard_rechecked` 就回報 `published`
- **THEN** 回 409 `guard_not_passed`，請求維持 `approved`

#### Scenario: 核准已過期
- **WHEN** `approval_expires_at` 已過後回報 `published`
- **THEN** 回 409 `approval_expired`

#### Scenario: 發佈的 SHA 不同
- **WHEN** 回報的 `published_sha` 不等於 `approved_head_sha`
- **THEN** 回 422，請求維持 `approved`

### Requirement: 還原 MUST 建立新請求而不改寫歷史

持有 `design.revert` 的人 SHALL 能對一筆曾發佈的請求（`published` 或 `reverted`）請求還原；系統 MUST 建立一筆新的 `kind = revert` 請求，`restores_request_id` 指向目標（複合外鍵強制同商家），初始為 `drafting`，並寫入 `revert_requested` 事件。目標已是該站台目前設計時 MUST 回 409 `already_current`。還原請求 MUST 經過與一般請求相同的守門員、預覽、前後截圖、送審與人類核准。還原請求 `published` 後，系統 MUST 把目標之後、還原之前所有 `published` 的請求標為 `reverted` 並以 `reverted_by_request_id` 指向還原請求；先前的紀錄與事件 MUST NOT 被修改或刪除。還原只涉及設計層檔案，MUST NOT 影響內容儲存的任何資料，也 MUST NOT 註冊或變更內容 schema。

#### Scenario: 請求還原
- **WHEN** owner 對一筆已發佈的舊請求呼叫 `POST …/revert`
- **THEN** 回 201 一筆新的 `revert` 請求，舊請求紀錄不變

#### Scenario: 還原到目前設計
- **WHEN** 目標就是該站台目前設計
- **THEN** 回 409 `already_current`

#### Scenario: 低於 core 相容下限
- **WHEN** 目標的 `core_version` 低於該站台目前的 `design_floor_version`
- **THEN** 回 409 `revert_below_core_floor`，不建立請求

#### Scenario: 還原發佈後的連結
- **WHEN** 還原請求被記錄為 `published`
- **THEN** 目標之後的已發佈請求狀態為 `reverted`，且歷史畫面可由還原請求連到目標

### Requirement: 未追蹤的變更 MUST 被列出而不是被忽略

服務或其對帳工作 SHALL 能批次回報 `main` 上觸及設計路徑、但不屬於任何請求的提交；系統 MUST 以 `UNIQUE(shop_id, site, commit_sha)` 去重、保存 SHA、時間、主旨（≤ 200 字、純文字）與檔案與行數摘要，MUST NOT 保存作者姓名或信箱。歷史畫面 MUST 把它們標示為「未追蹤的變更」，持有 `design.approve` 的人可確認已知悉（只能設定一次）。

#### Scenario: 回報未追蹤提交
- **WHEN** 對帳工作回報兩個未知提交，其中一個之前已回報過
- **THEN** 只新增一列，重複者被忽略，回應列出新增數

### Requirement: 後台讀取 API

系統 SHALL 提供 `GET /api/v1/admin/shops/{shopID}/design/requests`（`design.view`，支援 `site`、`status`、`kind`、`from`、`to`、`page`、`page_size ≤ 100`，排序 `created_at` 降冪後 `id`，回 `requests`、`page`、`page_size`、`total`，列表只含 `prompt_excerpt`）、`GET …/{requestID}`（全欄位、`ETag`、`If-None-Match` 回 304）、`GET …/{requestID}/events`。他店的請求 id MUST 回 404。無權限者 MUST 回 403。

#### Scenario: editor 只能看
- **WHEN** 持 `editor` 的成員呼叫列表與詳情，並呼叫核准
- **THEN** 前兩者 200，核准 403

#### Scenario: staff 不可看
- **WHEN** 持 `staff` 的成員呼叫列表
- **THEN** 403

#### Scenario: 他店請求
- **WHEN** shop A 的 owner 以 shop A 的路徑讀取 shop B 的請求 id
- **THEN** 404

### Requirement: 服務可更新的範圍

服務路由族（`/api/v1/design-service/shops/{shopID}`）SHALL 只提供：建立請求、列出與認領待處理請求、更新 head 與 diff 摘要、回報守門員結果、登記預覽、上傳截圖、送審、回報已發佈、回報失敗、回報站台狀態、批次回報未追蹤變更，以及唯讀的請求詳情（MUST NOT 回 prompt 全文）。此路由族 MUST NOT 提供核准、拒絕、要求修改、取消或請求還原的任何端點。認領 MUST 為 15 分鐘的租約，過期未續約的請求由清掃工作釋放。

#### Scenario: 服務嘗試核准
- **WHEN** 持有效服務憑證者對核准端點發請求（任何路徑）
- **THEN** 回 404 或 403，請求狀態不變

#### Scenario: 租約競爭
- **WHEN** 兩個服務實例同時認領同一請求
- **THEN** 恰好一個成功，另一個回 409

### Requirement: 內容與還原的邊界 MUST 明示

歷史詳情與還原確認 MUST 向商家明示：還原不會改變內容儲存的任何內容，若舊設計讀取不存在的內容鍵會回退到設計層的預設文案。系統 MUST NOT 在還原時註冊內容 schema。

#### Scenario: 還原不碰內容
- **WHEN** 還原請求發佈後
- **THEN** `content_documents`、`content_revisions` 與 `content_schemas` 的列與版本與還原前相同
