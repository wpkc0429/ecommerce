## ADDED Requirements

### Requirement: Storefront can read the shop's active shipping methods

系統 MUST 提供公開唯讀端點 `GET /api/v1/shop/shipping-methods`，供商家自帶前端在結帳前呈現可選的運送方式。商家上下文 MUST 依既有前台租戶解析規則決定（spec multi-tenancy：publishable key 優先、否則 `Host`），該端點 MUST NOT 要求任何認證，MUST NOT 套用 RBAC 角色權限節點系統。

回應 MUST 僅包含 `is_active = true` 的運送方式；已停用者 MUST NOT 出現。每筆 MUST 包含識別碼、名稱、物流商名稱與固定運費，MUST NOT 包含 `is_active`（回傳的全部皆為啟用）與其他後台專用欄位。運費 MUST 以整數最小貨幣單位表達，其幣別 MUST 與該筆訂單的幣別相同（本階段每張購物車與每筆訂單皆為單一幣別）。

#### Scenario: 前台取得可選的運送方式

- **WHEN** 未認證的前台以已解析的商家上下文呼叫 `GET /api/v1/shop/shipping-methods`
- **THEN** 回 200，回傳該商家所有 `is_active = true` 的運送方式（識別碼、名稱、物流商、固定運費）

#### Scenario: 已停用的運送方式不出現在公開清單

- **WHEN** 商家將某運送方式設為 `is_active = false` 後，前台再次呼叫該端點
- **THEN** 回應中 MUST NOT 包含該運送方式

#### Scenario: 跨店查詢看不到彼此的運送方式

- **WHEN** 以 shop A 的商家上下文呼叫該端點
- **THEN** 回應 MUST NOT 包含任何屬於 shop B 的運送方式

### Requirement: A shipping method's flat rate becomes the order's shipping fee when selected at checkout

`shipping_methods.flat_rate` MUST 為結帳時運費的唯一來源。結帳選定某運送方式時，系統 MUST 將該筆的 `flat_rate` 與 `name` 於當下複製為訂單自身的欄位（spec order-management：`shipping_fee` 與運送方式名稱），此後 MUST NOT 因該運送方式改價、停用或被刪除而重新計算既有訂單的任何金額。

商家後台對 `shipping_methods` 的更新或刪除 MUST NOT 因為某些訂單曾經選用該筆而被阻擋，MUST NOT 連帶修改任何既有訂單。

#### Scenario: 選定的運送方式決定訂單運費

- **WHEN** 某運送方式 `flat_rate = 200`，結帳時選定該方式
- **THEN** 訂單 `shipping_fee = 200`，訂單應付總額為品項小計加 `200`

#### Scenario: 改價不影響既有訂單

- **WHEN** 訂單建立後商家將該運送方式的 `flat_rate` 由 `200` 改為 `300`
- **THEN** 既有訂單的 `shipping_fee` 仍為 `200`，其應付總額不變

#### Scenario: 刪除曾被選用的運送方式仍然成功

- **WHEN** 商家刪除一個已被既有訂單選用過的運送方式
- **THEN** 刪除成功，既有訂單的運送方式名稱與 `shipping_fee` 完全不變
