## ADDED Requirements

### Requirement: 設計歷史的權限節點

seed MUST 建立商家層節點 `design.view`、`design.request`、`design.approve`、`design.revert` 與平台層節點 `design_service.manage`。`merchant_owner` MUST 持有前四個；`editor` MUST 只持有 `design.view`；`staff` MUST NOT 持有任何 `design.*`；`super_admin` 經 `*` 持有全部。`design_service.manage` MUST NOT 授予任何商家層角色。上述集合 MUST 以相等比對的測試釘住。

#### Scenario: owner 的節點
- **WHEN** `merchant_owner` 呼叫 `GET /shops/{shopID}/me`
- **THEN** `permissions` 含四個 `design.*` 節點，不含 `design_service.manage`

#### Scenario: editor 只能看
- **WHEN** `editor` 呼叫 `GET …/me`
- **THEN** `permissions` 中的 `design.*` 恰為 `design.view`

#### Scenario: staff 沒有
- **WHEN** `staff` 呼叫 `GET …/me`
- **THEN** `permissions` 不含任何 `design.*`

#### Scenario: 平台層節點
- **WHEN** 檢查所有商家層角色的節點
- **THEN** 沒有任何一個含 `design_service.manage`
