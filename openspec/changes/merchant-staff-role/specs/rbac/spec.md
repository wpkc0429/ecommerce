## ADDED Requirements

### Requirement: staff 角色只能處理訂單與出貨

seed MUST 建立商家層角色 `staff`，其權限 MUST 恰好為 `order.view`、`payment.view`、`shipment.view`、`shipment.create`、`shipment.update`。`staff` MUST NOT 持有 `order.cancel`、`shipment.return`、`payment_credential.manage`、任何 `product.*`／`category.*`／`page.*`／`member.*`／`point.*`／`report.*`／`publishable_key.*`／`user.*` 節點。

#### Scenario: staff 可出貨
- **WHEN** 持 `staff` 的成員對本店訂單建立出貨並標記送達
- **THEN** 皆成功

#### Scenario: staff 可查付款紀錄但不能取消
- **WHEN** 持 `staff` 的成員呼叫 `GET /orders/{id}/payments` 與 `POST /orders/{id}/cancel`（未付款訂單）
- **THEN** 前者 200，後者 403

#### Scenario: staff 不能改商品或看名冊
- **WHEN** 持 `staff` 的成員呼叫 `PUT /products/{id}` 或 `GET /members`
- **THEN** 皆回 403

#### Scenario: staff 的有效權限
- **WHEN** 持 `staff` 的成員呼叫 `GET /shops/{shopID}/me`
- **THEN** `permissions` 恰為上述五個節點

### Requirement: 成員資格狀態參與跨店守衛

`shop_user` MUST 具有狀態（啟用／停用，預設啟用）。不具平台角色的使用者，其對商家 S 的跨店守衛 MUST 只在其於 S 的成員資格存在**且啟用**時通過。成員資格狀態變更 MUST 立即清除受影響使用者的授權快取。登入與換發時簽發的 `sids` MUST 只含啟用中的成員資格。

#### Scenario: 停用的成員資格
- **WHEN** 使用者於 shop A 的成員資格被停用
- **THEN** 其呼叫 shop A 任何後台 API 回 403，`/admin/me` 的 `sids` 不含 shop A（於下次換發後）

## MODIFIED Requirements

### Requirement: Role assignment management scope
商家層角色的指派 MUST 限於指派者具權限的同一商家；平台層角色與權限目錄 MUST 僅平台管理員可管理。商家層呼叫者（不持平台角色者）MUST 只能指派、移除或邀請 `merchant_assignable = true` 的角色，且 MUST NOT 對持有任何 `merchant_assignable = false` 角色的成員執行角色變更；`merchant_owner` 的授予與移除 MUST 只能由平台管理員執行。平台管理員不受可委派性限制。

#### Scenario: 商家管理員於自店指派
- **WHEN** shop A 的 `merchant_owner`（具 `user.manage_roles`）將 `editor` 指派給 shop A 的成員
- **THEN** 指派成功，範圍為 shop A

#### Scenario: 商家管理員跨店指派被拒
- **WHEN** shop A 的 `merchant_owner` 嘗試於 shop B 指派角色
- **THEN** 回 403

#### Scenario: 商家管理員不能授予 owner
- **WHEN** shop A 的 `merchant_owner` 嘗試將 `merchant_owner` 指派給 shop A 的成員
- **THEN** 回 403，指派未發生

#### Scenario: 平台管理員可授予 owner
- **WHEN** 平台管理員將 `merchant_owner` 指派給 shop A 的成員
- **THEN** 指派成功
