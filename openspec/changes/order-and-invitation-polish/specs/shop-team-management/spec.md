## ADDED Requirements

### Requirement: 邀請清單包含已過期的邀請

`GET /api/v1/admin/shops/{shopID}/invitations` MUST 回傳本店所有未接受、未撤回的邀請，包含已過期者，每筆帶 `status`（`pending` 或 `expired`，由 `expires_at` 推導）。已接受、已撤回、被取代的邀請 MUST NOT 出現。`DELETE …/invitations/{id}` MUST 對 `expired` 的邀請同樣有效，成功後該邀請 MUST 自清單消失。回應不得含 token 或連結。

#### Scenario: 看見並撤回過期邀請
- **WHEN** owner 列出邀請，其中一筆已過期，並撤回它
- **THEN** 該筆 `status = "expired"`，撤回回 204，再次列出時不存在
