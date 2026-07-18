# authentication — 後台/前台雙軌 JWT 認證與會員跨店關聯

## ADDED Requirements

### Requirement: Admin login and token issuance
後台使用者 SHALL 以 email + 密碼登入；成功時系統 MUST 簽發 access token（15 分鐘，`aud=admin`，claims 含 `sub` 與所屬 `sids` 列表）與 refresh token（30 天、不透明隨機值、雜湊後存 `user_refresh_tokens`）；失敗回應 MUST 為不區分「帳號不存在」與「密碼錯誤」的 401。

#### Scenario: 登入成功
- **WHEN** 提交正確的 email 與密碼且 `users.status = 1`
- **THEN** 回傳 access + refresh token，access token 可通過後台 API 驗證

#### Scenario: 登入失敗不洩漏原因
- **WHEN** 提交不存在的 email 或錯誤密碼
- **THEN** 回相同格式與訊息的 401

### Requirement: Member registration and login in shop context
前台會員的註冊與登入 MUST 於商家上下文（Host 解析）進行。註冊時 email/phone 命中既有 `members` 者 SHALL 沿用該身分並建立該店 `shop_member`；全新者建立 `members` + `shop_member`；兩種情形的回應 MUST 同構，不得洩漏該帳號已存在於其他商家。登入成功簽發 `aud=shop:{shop_id}` 的 access token 與綁定該店的 refresh token。

#### Scenario: 全新會員註冊
- **WHEN** 於 shop A 網域以未使用過的 email 註冊
- **THEN** 建立 `members` 與（shop A, member）的 `shop_member`，回傳該店 token

#### Scenario: 既有平台身分於第二家店註冊
- **WHEN** 已於 shop A 註冊的 email 於 shop B 網域註冊（密碼驗證通過）
- **THEN** 不建立新 `members`，僅新增 shop B 的 `shop_member`，回應與全新註冊同構

### Requirement: Token isolation between admin and member
後台與前台 JWT MUST 使用完全不同的簽章金鑰與 audience；member token 呼叫後台 API、admin token 呼叫會員 API、以及 shop A 的 member token 呼叫 shop B 上下文的 API，皆 MUST 回 401。

#### Scenario: 會員 token 打後台 API
- **WHEN** 以 `aud=shop:1` 的 token 呼叫後台管理 API
- **THEN** 回 401

#### Scenario: 跨店重用會員 token
- **WHEN** 以 shop A 簽發的會員 token 呼叫 shop B 網域的會員 API
- **THEN** audience 不符，回 401

### Requirement: Refresh token rotation and revocation
Refresh token MUST 於每次使用時輪替（舊 token 標記 `revoked_at`、新 token 記錄 `rotated_from`）；已輪替 token 的重放 MUST 使該輪替鏈全部撤銷；登出 MUST 撤銷當前 refresh token。

#### Scenario: 正常輪替
- **WHEN** 以有效 refresh token 換發
- **THEN** 取得新 access + refresh token，舊 refresh token 隨即失效

#### Scenario: 重放偵測
- **WHEN** 已被輪替的 refresh token 再次被使用
- **THEN** 回 401 且該鏈上所有未過期 token 被撤銷

### Requirement: Password hashing
`users` 與 `members` 的密碼 MUST 以 Argon2id 雜湊儲存（OWASP 建議參數起步）；系統 MUST NOT 以任何形式儲存或記錄明文密碼；`members.password_hash` 允許 NULL（預留社群登入）。

#### Scenario: 儲存格式
- **WHEN** 建立或變更密碼
- **THEN** 資料庫僅存 `$argon2id$` 開頭的雜湊字串

### Requirement: Disabled account rejection
`users.status = 0` 或 `members.status = 0` 的帳號 MUST 無法登入，且其既有 refresh token MUST 無法換發新 token。

#### Scenario: 停用後 refresh 失效
- **WHEN** 帳號被停用後以先前取得的 refresh token 換發
- **THEN** 回 401
