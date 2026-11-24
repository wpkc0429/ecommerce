## ADDED Requirements

### Requirement: 公開申請端點不洩漏任何既有資料

系統 SHALL 提供未認證的 `POST /api/v1/public/shop-applications`。請求 MUST 含 `email`、`contact_name`、`shop_name`、`business_summary`、`consent_version`、`turnstile_token`，MAY 含 `phone`；`website` 欄位（honeypot）MUST 為空。通過驗證的請求 MUST 一律回 `202` 與相同的回應主體，無論 email 是新的、已有未完成申請、位於再申請冷卻期、或已屬後台帳號；回應內容與耗時 MUST NOT 使這些情況可被區分。欄位格式錯誤、同意版本不符、Turnstile 失敗回 4xx，且只取決於請求本身。

#### Scenario: 新 email 與重複 email 回應相同
- **WHEN** 同一 email 先後兩次送出有效申請
- **THEN** 兩次皆回 202，回應位元組相同；資料庫中該 email 只有一筆未完成申請

#### Scenario: 已是後台帳號的 email
- **WHEN** 以某既有後台帳號的 email 送出有效申請
- **THEN** 回 202 且與新 email 的回應位元組相同；審核端的該申請帶風險旗標 `email_has_account`

#### Scenario: honeypot 被填
- **WHEN** `website` 欄位非空
- **THEN** 回 422，不建立任何資料

#### Scenario: 同意版本不符
- **WHEN** `consent_version` 不等於目前條款版本
- **THEN** 回 422

### Requirement: 濫用防護

公開申請端點 MUST 依序要求：有效的 Cloudflare Turnstile token（或等價機制；驗證服務不可用時回 503，MUST NOT 放行）、per-IP 限流（broad 與 tight 兩條）、per-email 限流（不含 IP）、全站每日上限；超限回 429 並帶 `Retry-After`。`APP_ENV` 為 production 時未設定 Turnstile 密鑰 MUST 拒絕啟動。請求 body MUST 不超過 16 KiB。限流器因 Redis 故障放行時，同一 email 至多一筆未完成申請的唯一性 MUST 仍由資料庫保證。

#### Scenario: per-email 限流跨 IP
- **WHEN** 同一 email 在 24 小時內從不同 IP 送出超過上限次數
- **THEN** 超限的請求回 429

#### Scenario: Turnstile 服務故障
- **WHEN** siteverify 逾時或回 5xx
- **THEN** 回 503，不建立申請

#### Scenario: Redis 故障時的資料庫後盾
- **WHEN** 限流器 fail open 時同一 email 連續送出兩次
- **THEN** 資料庫中仍只有一筆未完成申請，且 10 分鐘內只簽發一枚驗證 token

### Requirement: Email 驗證

申請建立時 SHALL 簽發 32 bytes 的一次性驗證 token，資料庫 MUST 只存其 SHA-256，有效 24 小時，單次使用且消費為原子操作；對同一申請簽發新 token MUST 使舊的失效。系統 SHALL 提供未認證的 `POST /api/v1/public/shop-applications/verify`（`{token}`）；無效、過期、已使用的 token MUST 一律回相同的 404。驗證成功 MUST 把狀態由 `pending_verification` 轉為 `pending_review`。尚未驗證的申請 MUST NOT 能被核准，除非操作員執行有稽核紀錄的人工確認。

#### Scenario: token 只能用一次
- **WHEN** 同一 token 第二次呼叫 verify
- **THEN** 回 404，狀態不變

#### Scenario: 並發 verify
- **WHEN** 兩個請求同時以同一 token 呼叫 verify
- **THEN** 恰好一個成功

#### Scenario: 未驗證不可核准
- **WHEN** 操作員對 `pending_verification` 的申請呼叫 approve
- **THEN** 回 409，不建立商家

### Requirement: 通知抽象與人工後備

通知 MUST 經由 `Notifier` 介面送出，回傳的 `Delivery.Mode` 為 `manual` 或 `email`，呼叫端 MUST 依 `Mode` 判斷。未設定 SMTP 時 MUST 使用 outbox 實作：訊息寫入 `shop_application_messages`（`pending_manual`），由操作員在後台複製並標記為已寄出。owner 的密碼設定連結 MUST NOT 寫入任何訊息列，MUST 僅在核准回應中一次性回傳（`delivery: "manual"`）。

#### Scenario: 無 SMTP 時的驗證信
- **WHEN** 未設定 SMTP 並送出有效申請
- **THEN** 產生一筆 `verify_email` 訊息列（`manual`/`pending_manual`），後台可見其內容

#### Scenario: 連結不落地
- **WHEN** 核准成功
- **THEN** `shop_application_messages` 與 `shop_application_events` 中找不到設定連結的 token

### Requirement: 核准即呼叫既有開店服務

`POST /api/v1/admin/shop-applications/{id}/approve` SHALL 要求 `shop_application.review`，並要求與 `POST /api/v1/admin/shop-provisioning` 相同的平台權限（`shop.create`、`account.manage`）。核准 MUST 在單一資料庫交易內完成：條件式把申請由 `pending_review` 轉為 `approved`、呼叫開店服務建立商家與 owner（不帶主題與網域）、寫入 `provisioned_shop_id` 與稽核事件。任何一步失敗 MUST 整筆回滾，申請維持 `pending_review`。owner email 已有後台帳號時，請求 MUST 帶 `confirm_existing_account: true`，否則回 409。重複核准回 409。

#### Scenario: 核准成功
- **WHEN** 具權限的操作員核准一筆已驗證的申請
- **THEN** 回 201，含 `shop`、`owner`（新帳號時含一次性 `setup_url` 與 `delivery:"manual"`）；申請為 `approved` 且 `provisioned_shop_id` 指向新商家

#### Scenario: 開店失敗整筆回滾
- **WHEN** 開店服務在交易中失敗
- **THEN** 不存在新商家或帳號，申請仍為 `pending_review`，並記錄 `approve_failed` 事件

#### Scenario: 權限不足
- **WHEN** 只有 `shop_application.review` 而缺 `account.manage` 的操作員核准
- **THEN** 回 403

### Requirement: 拒絕需附理由

`POST …/{id}/reject` SHALL 要求 `shop_application.review` 與非空 `decision_reason`；`decision_reason` 對申請者可見，`internal_note` 僅內部可見。已 `approved` 或已 `rejected` 的申請 MUST NOT 再被拒絕（409）。

#### Scenario: 缺理由
- **WHEN** reject 的 body 沒有 `decision_reason`
- **THEN** 回 422

### Requirement: 稽核紀錄

每次狀態轉移與操作員動作 MUST 寫入 `shop_application_events`（含 `actor_user_id`），`detail` MUST NOT 含 email、姓名或電話。申請的 PII 被清除後事件列 MUST 保留。

#### Scenario: 事件不含 PII
- **WHEN** 檢視某申請的全部事件列
- **THEN** 其 `detail` 中找不到該申請的 email、姓名、電話

### Requirement: 個資保存與清除

系統 SHALL 提供 `purge-applications` 指令，預設為 dry run，`-apply` 才寫入且冪等：未驗證申請於 7 天後刪除；已拒絕、已過期、已撤回的申請於保存期限後清除 PII 欄位（且不早於再申請冷卻結束）；已核准的申請於保存期限後清除 PII 欄位。系統 MUST NOT 儲存原始來源 IP，只儲存其 HMAC。日誌 MUST NOT 含 email、姓名或電話。

#### Scenario: dry run 不寫入
- **WHEN** 不帶 `-apply` 執行
- **THEN** 只列出將被處理的申請 id，資料不變

#### Scenario: 日誌不含 PII
- **WHEN** 申請走完提交、驗證、核准路徑並把 slog 導入緩衝區
- **THEN** 緩衝區中找不到特徵 email、姓名、電話

### Requirement: 新權限節點

系統 SHALL 新增平台層權限節點 `shop_application.view` 與 `shop_application.review`，僅 `super_admin` 持有；MUST NOT 授予任何商家層角色。

#### Scenario: 商家角色沒有申請權限
- **WHEN** 列舉所有商家層 seed 角色的權限
- **THEN** 其中沒有 `shop_application.*`
