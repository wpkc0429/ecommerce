# rbac Specification

## Purpose
TBD - created by archiving change phase1-multitenant-cms-foundation. Update Purpose after archive.
## Requirements
### Requirement: Three-tier permission decision
對（user, permission, shop_ctx）的授權判定 MUST 依序：(1) `user_permission` 存在精確匹配（含 `shop_id = shop_ctx` 或 NULL 平台層）時依 `is_granted` 直接允許或拒絕；(2) 無覆蓋時取使用者於該 shop_ctx 及平台層的所有角色，經 `role_permission` 聯集後含該權限即允許；(3) 皆無 MUST 回 403。

#### Scenario: 個人覆蓋授予
- **WHEN** 使用者角色不含 `page.publish`，但 `user_permission` 存在（user, page.publish, shop A, is_granted = true）
- **THEN** 於 shop A 執行發佈被允許

#### Scenario: 個人覆蓋剝奪優先於角色
- **WHEN** 使用者於 shop A 的角色含 `page.publish`，但 `user_permission` 存在（user, page.publish, shop A, is_granted = false）
- **THEN** 於 shop A 執行發佈回 403

#### Scenario: 角色授權
- **WHEN** 無個人覆蓋，使用者於 shop A 具有含 `page.edit` 的角色
- **THEN** 於 shop A 編輯頁面被允許

#### Scenario: 預設拒絕
- **WHEN** 使用者於 shop A 無覆蓋亦無任何含該權限的角色
- **THEN** 回 403

### Requirement: Shop-scoped role assignment
`role_user` 與 `user_permission` MUST 以 `shop_id` 區分作用範圍；於 shop A 指派的角色與覆蓋 MUST NOT 於 shop B 生效。

#### Scenario: 角色不跨店外溢
- **WHEN** 使用者於 shop A 具 `merchant_owner` 角色、於 shop B 僅具 `editor` 角色
- **THEN** 其於 shop B 的判定僅依 `editor`（及平台層角色），不含 shop A 的角色

### Requirement: Platform-scope roles
`scope = 'platform'` 的角色 MUST 以 `role_user.shop_id IS NULL` 指派；持有平台角色者其角色權限 SHALL 對所有 shop_ctx 生效（平台超級管理員即持有平台管理角色者）。

#### Scenario: 超級管理員跨店操作
- **WHEN** 使用者持有含 `shop.update` 的平台角色，對任一商家執行更新
- **THEN** 判定允許，無須該商家的 `shop_user` 成員資格

### Requirement: Cross-shop access guard
不具平台角色的使用者，其後台操作的目標 shop MUST 屬於其 `shop_user` 成員集合，否則 MUST 回 403（在進入權限判定前先行檢查）。

#### Scenario: 操作非所屬商家
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的後台 API
- **THEN** 回 403

### Requirement: Role assignment management scope
商家層角色的指派 MUST 限於指派者具權限的同一商家；平台層角色與權限目錄 MUST 僅平台管理員可管理。

#### Scenario: 商家管理員於自店指派
- **WHEN** shop A 的 `merchant_owner`（具 `user.manage_roles`）將 `editor` 指派給 shop A 的成員
- **THEN** 指派成功，範圍為 shop A

#### Scenario: 商家管理員跨店指派被拒
- **WHEN** shop A 的 `merchant_owner` 嘗試於 shop B 指派角色
- **THEN** 回 403

### Requirement: Authorization cache invalidation
權限判定結果 SHALL 快取於 Redis（`authz:user:{id}:shop:{shop_id}`，TTL 10 分鐘）；任何影響該使用者權限的寫入（角色指派/移除、`role_permission` 異動、個人覆蓋異動）MUST 立即刪除受影響的快取 key，使變更於下一請求生效。

#### Scenario: 撤權立即生效
- **WHEN** 管理員移除使用者於 shop A 的 `editor` 角色後，該使用者再次呼叫需 `page.edit` 的 API
- **THEN** 快取已失效，判定依最新資料回 403

### Requirement: Caller's effective permission set in a shop is self-queryable

系統 SHALL 提供一個商家範圍端點，讓已認證的後台使用者取得**自己**在該商家的有效權限節點集合。該集合 MUST 為三層判定（spec rbac 的 Three-tier permission decision）逐節點求值後的結果，亦即個人覆蓋（含強制拒絕）優先於角色聯集，MUST NOT 只是角色權限的原始聯集。

該端點的授權 MUST 僅要求「已認證」與跨店存取守衛（spec rbac 的 Cross-shop access guard），MUST NOT 要求任何特定權限節點——「我在這家店能做什麼」是每個進得了該商家的使用者都必須能取得的自我資訊；以任一既有節點作為守衛，會使僅被授予其他節點的使用者無法取得自己的權限集合。

回應 MUST 只包含呼叫者自己的權限，MUST NOT 包含其他使用者的權限、角色定義或權限目錄的完整內容。權限節點集合 MUST 由系統的權限目錄推導，MUST NOT 硬編碼，使新增權限節點時該端點自動涵蓋。

#### Scenario: 商家管理者取得自己的權限集合
- **WHEN** 具 `merchant_owner` 角色的使用者查詢自己在該商家的權限
- **THEN** 回傳該角色被授予的全部權限節點，且不含未被授予的節點

#### Scenario: 個人覆蓋剝奪反映在結果中
- **WHEN** 使用者的角色含 `product.delete`，但存在（user, product.delete, 該 shop, is_granted = false）的個人覆蓋
- **THEN** 回傳的集合 MUST NOT 含 `product.delete`

#### Scenario: 個人覆蓋授予反映在結果中
- **WHEN** 使用者的角色不含 `page.publish`，但存在（user, page.publish, 該 shop, is_granted = true）的個人覆蓋
- **THEN** 回傳的集合 MUST 含 `page.publish`

#### Scenario: 不具任何權限節點但屬於該商家者仍可查詢
- **WHEN** 屬於該商家但其角色未授予任何權限節點的使用者查詢
- **THEN** 回 200 與一個空集合，MUST NOT 回 403

#### Scenario: 非該商家成員被拒
- **WHEN** 不具平台層角色、且非該商家成員的使用者查詢該商家的權限
- **THEN** 回 403

#### Scenario: 未認證被拒
- **WHEN** 未帶有效後台 access token 查詢
- **THEN** 回 401

#### Scenario: 平台層管理者跨商家可查詢
- **WHEN** 具平台層角色的使用者查詢任一商家的權限集合
- **THEN** 回 200，且集合反映其於該商家上下文的三層判定結果

