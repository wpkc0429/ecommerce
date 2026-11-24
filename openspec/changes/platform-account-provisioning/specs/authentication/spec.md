## MODIFIED Requirements

### Requirement: Disabled account rejection
`users.status = 0` 或 `members.status = 0` 的帳號 MUST 無法登入，且其既有 refresh token MUST 無法換發新 token。後台帳號（`users`）被停用時，其**已簽發且尚未過期的 access token** 亦 MUST 於下一個請求起被所有 RBAC 守衛的後台端點拒絕（403），且 `GET /api/v1/admin/me` MUST 回 401；停用 MUST 同時撤銷該帳號全部 refresh token。`users.password_hash` 為 NULL（尚未設定密碼）的帳號 MUST 無法登入，其失敗回應與時序 MUST 與「帳號不存在」無法區分。

#### Scenario: 停用後 refresh 失效
- **WHEN** 帳號被停用後以先前取得的 refresh token 換發
- **THEN** 回 401

#### Scenario: 停用後 access token 立即失效
- **WHEN** 後台帳號被停用後，以停用前取得且未過期的 access token 呼叫需要 `order.view` 的端點
- **THEN** 回 403

#### Scenario: 未設定密碼的帳號無法登入
- **WHEN** 以 `password_hash` 為 NULL 的帳號 email 與任意密碼登入
- **THEN** 回與「帳號不存在」相同格式與訊息的 401
