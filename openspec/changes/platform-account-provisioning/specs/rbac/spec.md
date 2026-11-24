## ADDED Requirements

### Requirement: 停用帳號不持有任何權限

授權快照 MUST 納入帳號啟用狀態：`users.status = 0` 的使用者，其任一 shop_ctx 與平台 ctx 的判定 MUST 一律拒絕（含平台角色與個人允許覆蓋），跨店守衛 MUST 視其為非成員。帳號狀態變更 MUST 立即清除該使用者的授權快取。

#### Scenario: 停用的超級管理員失去平台權限
- **WHEN** 持有 `super_admin` 的帳號 A 被另一位 super_admin 停用後，A 以既有 access token 呼叫 `POST /admin/shops`
- **THEN** 回 403

#### Scenario: 重新啟用後恢復
- **WHEN** 被停用的帳號重新啟用並重新登入
- **THEN** 其權限判定依原有角色恢復

### Requirement: 呼叫者的平台層有效權限可自我查詢

`GET /api/v1/admin/me` MUST 包含 `platform_permissions`：呼叫者於平台 ctx 經三層判定後的有效權限節點（依名稱排序）；不持有平台角色者 MUST 為空陣列。此欄位僅供介面可見性使用，MUST NOT 被任何伺服器端授權判定讀取。

#### Scenario: 商家 owner 沒有平台權限
- **WHEN** 只持有商家層 `merchant_owner` 的使用者呼叫 `GET /admin/me`
- **THEN** `platform_permissions` 為 `[]`

#### Scenario: 超級管理員的平台權限
- **WHEN** `super_admin` 呼叫 `GET /admin/me`
- **THEN** `platform_permissions` 含 `account.manage` 與 `shop.create`
