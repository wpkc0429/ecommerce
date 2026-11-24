## ADDED Requirements

### Requirement: owner 以 email 邀請成員

系統 SHALL 提供 `POST /api/v1/admin/shops/{shopID}/invitations`（`user.manage_members`），請求含 `email` 與 `role_id`。`role_id` MUST 指向 `merchant_assignable = true` 的商家層角色，否則回 422（平台層呼叫者不受此限）。email 已是本店啟用中成員 MUST 回 409。成功時 MUST 建立一張 7 天有效的邀請並回傳 `invitation_id`、`delivery`、（manual 時）`invite_url` 與 `expires_at`；對「尚無帳號的 email」與「已有帳號的 email」，回應 MUST 同構且不含任何可區分兩者的欄位。邀請建立 MUST NOT 建立任何 `users` 列。同一商家對同一 email 的新邀請 MUST 使舊的未接受邀請失效。此端點 MUST 以「商家＋呼叫者」為界限流。

#### Scenario: 邀請店員
- **WHEN** owner 邀請 `clerk@bouquet.test` 為 `staff`
- **THEN** 回 201，含 `invite_url`；`users` 中尚無 `clerk@bouquet.test`

#### Scenario: 不能邀請 owner
- **WHEN** owner 以 `merchant_owner` 的 `role_id` 邀請
- **THEN** 回 422

#### Scenario: 回應不透露帳號是否存在
- **WHEN** owner 分別邀請一個不存在的 email 與一個在他店已有帳號的 email
- **THEN** 兩個回應的鍵集合與狀態碼相同

#### Scenario: editor 無權邀請
- **WHEN** 只持 `editor` 的成員呼叫邀請端點
- **THEN** 回 403

### Requirement: 邀請的接受永遠不替既有帳號設定密碼

系統 SHALL 提供未認證的 `POST /api/v1/admin/auth/invitations/inspect`（`{token}` → `{shop_name, role_name, email, requires_login, expires_at}`）、未認證的 `POST /api/v1/admin/auth/invitations/accept`（`{token, password}`）與已認證的 `POST /api/v1/admin/invitations/accept`（`{token}`）。

未認證接受 MUST 只在該 email 沒有任何後台帳號時成功：在同一交易內建立帳號（密碼至少 12 字元）、成員資格與角色指派，並消費邀請。該 email 已有帳號（不論是否已設定密碼）時 MUST 回 409 且不做任何變更。已認證接受 MUST 要求呼叫者 email 等於邀請 email，否則 403。無效、過期、已使用、已撤回的 token MUST 一律回相同的 404。消費 MUST 是原子的。未認證端點 MUST 以來源 IP 為界限流。

#### Scenario: 新人接受邀請
- **WHEN** 尚無帳號的受邀者以 token 與密碼接受
- **THEN** 回 204；以該 email 登入後為本店成員並持有受邀角色

#### Scenario: 邀請不能接管未完成設定的帳號
- **WHEN** 平台剛建立、尚未設定密碼的帳號 `x@test` 被某 owner 邀請，持有邀請連結者以未認證接受端點送出密碼
- **THEN** 回 409，該帳號的 `password_hash` 仍為 NULL，且未建立成員資格

#### Scenario: 既有帳號登入後接受
- **WHEN** 已是他店成員的帳號登入後以已認證端點接受邀請
- **THEN** 回 204，該帳號成為本店成員並持有受邀角色，密碼不變

#### Scenario: 他人無法代為接受
- **WHEN** 帳號 B 登入後以發給 A 的邀請 token 呼叫已認證接受
- **THEN** 回 403

### Requirement: owner 可列出與撤回邀請

系統 SHALL 提供 `GET /api/v1/admin/shops/{shopID}/invitations`（`user.manage_members`；只列本店未接受、未過期、未撤回者，依建立時間遞減）與 `DELETE .../invitations/{invitationID}`（撤回；之後該 token 視同無效）。列表 MUST NOT 含 token 或連結。

#### Scenario: 撤回後連結失效
- **WHEN** owner 撤回邀請後，受邀者開啟該連結
- **THEN** inspect 回 404

### Requirement: 成員資格可由 owner 停用與啟用

系統 SHALL 提供 `POST /api/v1/admin/shops/{shopID}/users/{userID}/disable` 與 `/enable`（`user.manage_members`），變更的是**該店成員資格**的狀態，MUST NOT 變更帳號的全域狀態。停用的成員在該店的跨店守衛 MUST 視同非成員（下一個請求即 403），在其他商家的權限 MUST 不受影響。商家層呼叫者 MUST NOT 停用自己（409），MUST NOT 對持有任何不可委派角色的成員執行停用、啟用或角色變更（403）。

#### Scenario: 停用店員
- **WHEN** owner 停用店員 C 後，C 以既有 access token 呼叫本店 `GET /orders`
- **THEN** 回 403

#### Scenario: 不影響他店
- **WHEN** C 同時是 shop B 的成員，被 shop A 的 owner 停用
- **THEN** C 呼叫 shop B 的後台 API 仍依其 shop B 角色判定

#### Scenario: 不能停用另一位 owner
- **WHEN** shop A 有兩位 `merchant_owner`，其中一位嘗試停用另一位
- **THEN** 回 403
