## ADDED Requirements

### Requirement: 設計服務憑證

系統 SHALL 提供只有平台層 `design_service.manage` 持有者能簽發與撤銷的設計服務憑證。憑證 MUST 綁定單一商家、以 `crypto/rand` 產生、格式 `dsk_` 加隨機字串、只在簽發回應顯示一次、資料庫只存 SHA-256；MUST 有到期（預設 90 天，上限 366 天）、可立即撤銷、記錄 `last_used_at`，並可選擇性限制來源 CIDR。憑證 MUST NOT 被授予任何使用者角色或權限節點，且 `design_service.manage` MUST NOT 授予任何商家層角色。

#### Scenario: 簽發只顯示一次
- **WHEN** super_admin 為 shop A 簽發憑證
- **THEN** 回應含完整 `dsk_` 字串一次；之後的任何讀取只含 `key_prefix`

#### Scenario: 商家不能簽發
- **WHEN** shop A 的 `merchant_owner` 呼叫簽發端點
- **THEN** 403

#### Scenario: 撤銷立即生效
- **WHEN** 憑證被撤銷後立即以它呼叫服務端點
- **THEN** 401

#### Scenario: 過期
- **WHEN** 憑證已過 `expires_at`
- **THEN** 401

### Requirement: 服務憑證 MUST NOT 能核准或發佈，且 MUST 限於單一商家

服務路由族 MUST 要求有效憑證，URL 的 `{shopID}` MUST 等於憑證的商家，否則回 404 且不洩漏他店資訊。憑證 MUST NOT 能呼叫 `/api/v1/admin/**` 的任何端點。核准、拒絕、要求修改、取消與請求還原 MUST 只存在於後台人類路由族，並以 `design.*` 權限判定；狀態機 MUST 只接受 `actor_type = user` 的核准事件。`published` 的記錄 MUST 以已核准、未逾期、重檢通過、SHA 與 tag 相等為前提，因此服務無法讓平台承認一次未經核准的發佈。

#### Scenario: 跨店
- **WHEN** shop A 的憑證呼叫 shop B 的 `/design-service/shops/{B}/requests`
- **THEN** 404

#### Scenario: 憑證打後台
- **WHEN** 以服務憑證作為 Bearer 呼叫 `/api/v1/admin/shops/{A}/design/requests`
- **THEN** 401

#### Scenario: 被攻破的服務嘗試把發佈記為已完成
- **WHEN** 服務對 `awaiting_approval` 的請求回報 `published`
- **THEN** 409 `invalid_state`

#### Scenario: 服務無法掛名他人
- **WHEN** 服務以 `requested_by_user_id` 為一位只有 `editor` 權限的成員建立請求
- **THEN** 403 `requester_not_permitted`

### Requirement: 服務路由族的濫用防護

服務路由族 MUST 對每個憑證限流（一般寫入每分鐘 120 次、截圖上傳每分鐘 60 次，超過回 429 與 `Retry-After`），MUST 不經 Cloudflare Access 中介層但 MUST 以憑證與可選 CIDR 驗證；日誌 MUST 只含 shop id、request id、狀態、規則 id、計數與錯誤類別，MUST NOT 含 prompt、簽名網址、預覽 origin、diff 路徑或憑證字串。

#### Scenario: 日誌不洩漏
- **WHEN** 一次完整的請求流程跑完（含失敗路徑），日誌被導入緩衝區
- **THEN** 緩衝區中找不到 prompt、簽名網址、預覽 origin 與憑證字串的特徵值

#### Scenario: 限流
- **WHEN** 同一憑證在一分鐘內送出超過上限的截圖上傳
- **THEN** 超出者回 429 並帶 `Retry-After`

### Requirement: 歷史內容 MUST 被視為資料

後台與服務 API MUST 把 prompt、失敗訊息、拒絕理由、提交主旨與檔案路徑視為純文字資料：API MUST NOT 對它們做任何 HTML 解讀；服務路由族的唯讀端點 MUST NOT 回傳 prompt 全文。服務契約 MUST 規定這些內容永不作為指令餵給 agent。

#### Scenario: 服務唯讀端點不回 prompt
- **WHEN** 服務以憑證讀取請求詳情
- **THEN** 回應含 `prompt_sha256` 與狀態，不含 `prompt_text`
