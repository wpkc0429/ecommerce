## ADDED Requirements

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
