## MODIFIED Requirements

### Requirement: Tenant data isolation enforcement
所有租戶擁有資料（`pages`、`members` 等）的查詢與寫入 MUST 在資料存取層強制附加當前上下文的 `shop_id` 條件（ent privacy/interceptor），且後台 API 的目標 shop MUST 由授權上下文決定、不得信任 client 傳入的 shop 識別。

租戶擁有資料 MUST NOT 存在由多家商家共享的資料列——任一列 MUST 恰好隸屬於一個 `shop_id`。此不變式使一家商家的資料集可僅以 `shop_id` 條件完整界定（詳見 spec member-identity）。

#### Scenario: 前台跨租戶讀取被隔離
- **WHEN** shop A 的網域請求 shop B 已發佈頁面的 slug
- **THEN** 回應 404（查詢範圍僅及 shop A）

#### Scenario: 資料層強制範圍
- **WHEN** 應用程式碼在租戶上下文中查詢 `pages` 但未顯式指定 shop 條件
- **THEN** 資料層自動附加 `shop_id = 當前租戶` 條件（以自動化測試驗證）

#### Scenario: 顧客資料同受資料層強制範圍
- **WHEN** 應用程式碼在 shop A 的租戶上下文中查詢 `members` 但未顯式指定 shop 條件
- **THEN** 資料層自動附加 `shop_id = shop A` 條件，結果不含任何 shop B 的顧客

#### Scenario: 無跨商家共享資料列
- **WHEN** 檢視任一租戶擁有資料表的任一列
- **THEN** 該列恰好隸屬於一個 `shop_id`，MUST NOT 存在同時屬於多家商家的列
