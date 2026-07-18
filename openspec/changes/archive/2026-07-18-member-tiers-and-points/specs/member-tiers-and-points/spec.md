## ADDED Requirements

### Requirement: Merchant-configured member tiers are a shop-scoped CRUD resource

商家後台 API（`/api/v1/admin/shops/{shopID}/member-tiers`）MUST 提供會員等級的完整 CRUD（列表、建立、檢視、更新、刪除），依既有三層 RBAC 判定（spec rbac）：檢視需要 `member_tier.view`，建立需要 `member_tier.create`，更新需要 `member_tier.edit`，刪除需要 `member_tier.delete`。每個會員等級 MUST 包含 `name`（非空）、`min_points`（達到此點數餘額自動升級到此等級的門檻，MUST >= 0）、MAY 包含 `discount_percent`（0–100 之間，本階段僅儲存不套用任何折扣邏輯）。商家 MUST 只能操作 URL 中 `shopID` 所屬的會員等級；跨商家操作 MUST 依既有 cross-shop access guard 回 403。

#### Scenario: 具權限商家建立會員等級
- **WHEN** 持有 shop A `member_tier.create` 權限的使用者以合法 `name`/`min_points` 呼叫建立會員等級 API
- **THEN** 建立成功，回應包含該會員等級的完整欄位

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `member_tier.edit` 權限，嘗試更新 shop A 的會員等級
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的會員等級管理 API
- **THEN** 回 403

#### Scenario: 刪除仍被會員持有的等級會清空該欄位而非阻擋刪除
- **WHEN** 商家刪除一個目前有會員 `level_id` 指向它的會員等級
- **THEN** 刪除成功，該等級底下所有會員的 `level_id` 變為 `NULL`，會員的 `points` 不受影響

### Requirement: Point transactions are an append-only ledger backing a maintained balance cache

`point_transactions` MUST 是 shop-scoped 的附加式（append-only）流水帳，每筆記錄 MUST 包含 `shop_member_id`、`order_id`（nullable，非訂單觸發的異動為 null）、`points_delta`（有號整數）、`reason`（文字說明）、`created_at`，是點數異動的 source of truth。`shop_member.points` MUST 是由這些流水帳維護的快取總額——任何寫入 `point_transactions` 的操作 MUST 在同一資料庫交易內同步更新對應 `shop_member.points`，MUST NOT 依賴另外的重新加總機制回填。

#### Scenario: 每筆點數異動都留下流水帳記錄
- **WHEN** 系統因任何原因（付款核發、退貨扣回、商家手動調整）異動一名會員的點數
- **THEN** `point_transactions` 新增一筆對應記錄，`shop_member.points` 同步反映異動後的餘額

### Requirement: Order payment success automatically and idempotently awards points

當訂單 `payment_status` 透過 `order.Service.UpdatePaymentStatus` 成功轉為已付款時，系統 MUST 自動核發點數給該訂單所屬會員在該商家的 `shop_member` 記錄：核發點數 = `orders.total_amount` 除以 100 取整數（無條件捨去），比例為平台統一常數，不因商家而異。核發 MUST 是冪等的——同一筆訂單 MUST NOT 因付款成功的通知重複送達而被核發超過一次。

#### Scenario: 付款成功正確核發點數
- **WHEN** 一筆 `total_amount = 1290` 的訂單付款成功（`payment_status` 變為已付款）
- **THEN** 該訂單所屬會員在該商家的 `points` 增加 12（`1290 / 100` 取整數），`point_transactions` 新增一筆對應該訂單的核發記錄

#### Scenario: 重複觸發付款成功不重複核發
- **WHEN** 同一筆訂單的付款成功流程因重複投遞（例如 webhook 重送）被觸發第二次
- **THEN** 該會員的 `points` 不因第二次觸發而再次增加，`point_transactions` 內該訂單只存在一筆核發記錄

#### Scenario: 零金額訂單核發零點數但仍留下核發記錄
- **WHEN** 一筆 `total_amount` 小於 100 最小貨幣單位的訂單付款成功
- **THEN** 核發點數為 0，`shop_member.points` 不變，但 `point_transactions` 仍留下一筆該訂單的核發記錄，防止後續重複觸發被誤判為尚未處理

### Requirement: Member level is re-evaluated after every points-changing operation

每次 `shop_member.points` 因核發、退貨扣回或商家手動調整而變動後，系統 MUST 立即重新評估並更新該會員在該商家的 `level_id`：於該商家的 `member_tiers` 中，依 `min_points` 由高到低尋找第一個 `min_points <= 變動後的 points` 的等級並設為 `level_id`；若沒有任何等級符合（含商家尚未設定任何等級的情況），`level_id` MUST 被清為 `NULL`。

#### Scenario: 累積點數達到門檻自動升級
- **WHEN** 會員的 `points` 因核發從低於某會員等級 `min_points` 變為達到或超過該門檻，且沒有更高門檻的等級同時達標
- **THEN** 該會員的 `level_id` 更新為該等級

#### Scenario: 點數減少導致跌出原等級時自動降級或清空
- **WHEN** 會員的 `points` 因退貨扣回或手動調整而降至低於其目前 `level_id` 對應等級的 `min_points`
- **THEN** 該會員的 `level_id` 更新為符合新餘額的最高等級，若無等級符合則清為 `NULL`

### Requirement: Order return claws back previously awarded points, capped at the current balance

訂單 `fulfillment_status` 透過 `order.Service.UpdateFulfillmentStatus` 轉為已退貨（`3`）時，系統 MUST 嘗試扣回該訂單先前核發的點數：扣回量 = `min(該訂單原本核發的點數, 會員目前的 points)`，MUST NOT 造成 `shop_member.points` 變為負數。扣回 MUST 是冪等的——同一筆訂單的退貨轉換 MUST NOT 造成扣回被重複執行。訂單若從未核發過點數（例如訂單從未付款即被出貨後退貨），扣回量為 0，`shop_member.points` 不變。

#### Scenario: 已核發點數的訂單退貨後扣回等量點數
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，且會員目前餘額 >= 12
- **THEN** 該會員 `points` 減少 12，`point_transactions` 新增一筆該訂單的扣回記錄

#### Scenario: 扣回量不超過會員目前餘額
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，但會員目前餘額因其他異動已降為 5
- **THEN** 該會員 `points` 最多扣回至 0（扣回 5 點，而非 12 點），MUST NOT 變為負數

#### Scenario: 重複觸發退貨轉換不重複扣回
- **WHEN** 同一筆訂單的已退貨轉換因重複觸發被處理第二次
- **THEN** 該會員的 `points` 不因第二次觸發而重複扣回

### Requirement: Merchant can manually adjust a member's points

商家後台 API（`POST /api/v1/admin/shops/{shopID}/members/{shopMemberID}/points/adjust`）MUST 依既有三層 RBAC 判定，需要 `point.adjust` 權限。請求 MUST 附帶 `points_delta`（非零有號整數）與 `reason`（非空文字）。若調整後餘額（`目前 points + points_delta`）會小於 0，MUST 拒絕（422），不建立任何流水帳記錄，`shop_member.points` 不變。調整成功時 MUST 建立一筆 `point_transactions` 記錄（`order_id` 為 null），並依既有規則重新評估 `level_id`。目標會員不存在於該商家 MUST 回 404。

#### Scenario: 具權限商家增加會員點數
- **WHEN** 持有 `point.adjust` 權限的使用者對 shop A 一名會員呼叫調整 API，`points_delta = 50`、`reason = "客服補償"`
- **THEN** 該會員 `points` 增加 50，`point_transactions` 新增一筆 `order_id` 為 null 的記錄，`level_id` 依新餘額重新評估

#### Scenario: 調整後會變為負數的請求被拒
- **WHEN** 會員目前 `points = 10`，商家呼叫調整 API 帶 `points_delta = -20`
- **THEN** 回 422，該會員 `points` 不變，不建立流水帳記錄

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `point.adjust` 權限，嘗試調整 shop A 某會員的點數
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫調整 shop B 某會員點數的 API
- **THEN** 回 403

### Requirement: Merchant back-office can view a member's points balance and transaction ledger

商家後台 API（`GET /api/v1/admin/shops/{shopID}/members/{shopMemberID}/points`、`GET /api/v1/admin/shops/{shopID}/members/{shopMemberID}/points/transactions`）MUST 依既有三層 RBAC 判定，需要 `point.view` 權限。前者 MUST 回傳該會員目前的點數餘額與目前等級；後者 MUST 回傳該會員的流水帳，分頁。商家 MUST 只能查詢 URL 中 `shopID` 所屬的會員；跨商家操作 MUST 依既有 cross-shop access guard 回 403。目標會員不存在於該商家 MUST 回 404。

#### Scenario: 具權限商家檢視會員點數餘額與等級
- **WHEN** 持有 shop A `point.view` 權限的使用者查詢 shop A 一名會員的點數
- **THEN** 回應包含該會員目前的 `points` 與 `level_id`（含等級名稱）

#### Scenario: 具權限商家檢視會員流水帳
- **WHEN** 持有 shop A `point.view` 權限的使用者查詢 shop A 一名會員的點數流水帳
- **THEN** 回傳該會員的所有 `point_transactions` 記錄，分頁，時間由新到舊

#### Scenario: 無權限或跨店查詢被拒
- **WHEN** 使用者不具 `point.view` 權限，或僅屬 shop A 卻查詢 shop B 某會員的點數
- **THEN** 回 403

### Requirement: Member self-service points access is scoped by member identity

會員自助 API（`GET /api/v1/shop/points`、`GET /api/v1/shop/points/transactions`）MUST 僅依賴已驗證會員 JWT 中的 `member_id` 判斷存取範圍，MUST NOT 套用 RBAC 角色權限節點系統，MUST NOT 接受請求參數指定的會員或 shop_member 識別碼。前者 MUST 回傳呼叫者自己目前的點數餘額與目前等級；後者 MUST 回傳呼叫者自己的流水帳，分頁。

#### Scenario: 會員查詢自己的點數餘額與等級
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/points`
- **THEN** 回應包含呼叫者自己目前的 `points` 與目前等級

#### Scenario: 會員查詢自己的點數流水帳
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/points/transactions`
- **THEN** 回傳僅屬於呼叫者自己的流水帳記錄，分頁

#### Scenario: 跨店重用會員 token 存取點數被拒
- **WHEN** 以 shop A 簽發的會員 token 呼叫 shop B 網域的點數 API
- **THEN** audience 不符，回 401

### Requirement: Tenant data isolation for member_tiers and point_transactions tables

`member_tiers`、`point_transactions` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop。

#### Scenario: 跨店查詢看不到彼此的會員等級或點數資料
- **WHEN** shop A 的商家或會員查詢自己的會員等級或點數資料
- **THEN** 回應不含任何 shop B 的會員等級或點數流水帳資料

#### Scenario: 跨會員查詢看不到彼此的點數資料
- **WHEN** 會員 A 嘗試查詢屬於會員 B 的點數資料
- **THEN** 依各自 API 的既有存取規則被拒（會員自助 API 僅回自己的資料、商家後台 API 依 RBAC 與 shop 範圍判定）
