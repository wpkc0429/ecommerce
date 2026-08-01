## MODIFIED Requirements

### Requirement: Merchant-configured member tiers are a shop-scoped CRUD resource

商家後台 API（`/api/v1/admin/shops/{shopID}/member-tiers`）MUST 提供會員等級的完整 CRUD（列表、建立、檢視、更新、刪除），依既有三層 RBAC 判定（spec rbac）：檢視需要 `member_tier.view`，建立需要 `member_tier.create`，更新需要 `member_tier.edit`，刪除需要 `member_tier.delete`。每個會員等級 MUST 包含 `name`（非空）、`min_points`（達到此點數餘額自動升級到此等級的門檻，MUST >= 0）、MAY 包含 `discount_percent`（0–100 之間）。商家 MUST 只能操作 URL 中 `shopID` 所屬的會員等級；跨商家操作 MUST 依既有 cross-shop access guard 回 403。

`discount_percent` MUST 在結帳時套用於屬於該等級的顧客所建立的訂單（spec tier-discount-application）：它 MUST NOT 再被視為僅儲存而不影響計價的保留欄位。設定值的變更 MUST 立即對其後建立的訂單生效，且 MUST NOT 影響任何既有訂單。

#### Scenario: 具權限商家建立會員等級
- **WHEN** 持有 shop A `member_tier.create` 權限的使用者以合法 `name`/`min_points` 呼叫建立會員等級 API
- **THEN** 建立成功，回應包含該會員等級的完整欄位

#### Scenario: 折扣率設定後對新訂單生效
- **WHEN** 商家把某等級的 `discount_percent` 由 `0` 改為 `10`，其後一位屬於該等級的顧客結帳
- **THEN** 該筆新訂單套用 10% 折扣；商家變更之前既已成立的訂單金額完全不變

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `member_tier.edit` 權限，嘗試更新 shop A 的會員等級
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的會員等級管理 API
- **THEN** 回 403

#### Scenario: 刪除仍被會員持有的等級會清空該欄位而非阻擋刪除
- **WHEN** 商家刪除一個目前有會員 `level_id` 指向它的會員等級
- **THEN** 刪除成功，該等級底下所有會員的 `level_id` 變為 `NULL`，會員的 `points` 不受影響，**且依該等級成立的既有訂單其折扣快照與金額完全不變**

### Requirement: Order payment success automatically and idempotently awards points

當訂單 `payment_status` 透過 `order.Service.UpdatePaymentStatus` 成功轉為已付款時，系統 MUST 自動核發點數給該訂單所屬的 `members` 記錄：核發點數 = **核發基準**除以 100 取整數（無條件捨去），比例為平台統一常數，不因商家而異。

**核發基準 MUST 為 `orders.subtotal_amount − orders.discount_amount`（折扣後的品項小計），即顧客實際為商品支付的金額。** 兩項排除各有其理由，且 MUST 同時成立：

- 運費（`orders.shipping_fee`）MUST NOT 產生點數——點數獎勵的是商品購買，MUST NOT 因顧客選擇較貴的配送方式而增加。
- 等級折抵（`orders.discount_amount`）MUST NOT 產生點數——折扣與點數是同一份回饋的兩種形式，以折扣前的金額核發會使回饋成本在商家背後相乘放大，且會讓一筆百分之百折抵的訂單核發點數。

核發 MUST 是冪等的——同一筆訂單 MUST NOT 因付款成功的通知重複送達而被核發超過一次。

本規則 MUST 對本規則生效之前既已核發的點數回溯成立：折扣機制導入前的每一筆訂單其 `discount_amount` 為 `0`，因此其核發基準與舊規則完全相同。

#### Scenario: 付款成功正確核發點數
- **WHEN** 一筆 `subtotal_amount = 1290`、`discount_amount = 0`、`shipping_fee = 0` 的訂單付款成功（`payment_status` 變為已付款）
- **THEN** 該訂單所屬會員的 `points` 增加 12（`1290 / 100` 取整數），`point_transactions` 新增一筆對應該訂單的核發記錄

#### Scenario: 運費不計入核發點數
- **WHEN** 一筆 `subtotal_amount = 3600`、`discount_amount = 0`、`shipping_fee = 200`（`total_amount = 3800`）的訂單付款成功
- **THEN** 核發點數為 36（依 `3600` 計算），MUST NOT 為 38

#### Scenario: 等級折抵計入核發基準
- **WHEN** 一筆 `subtotal_amount = 3800`、`discount_amount = 190`（5% 等級折扣）、`shipping_fee = 200` 的訂單付款成功
- **THEN** 核發點數為 36（依 `3800 - 190 = 3610` 計算），MUST NOT 為 38（折扣前）、MUST NOT 為 40（含運費）

#### Scenario: 百分之百折抵的訂單不核發點數
- **WHEN** 一筆 `subtotal_amount = 3800`、`discount_amount = 3800` 的訂單付款成功
- **THEN** 核發點數為 0，`point_transactions` 仍留下一筆該訂單的核發記錄

#### Scenario: 配送方式不影響核發點數
- **WHEN** 兩筆品項完全相同（`subtotal_amount` 相同）、折抵相同的訂單分別選定運費 `200` 與運費 `0` 的運送方式，兩者皆付款成功
- **THEN** 兩筆訂單核發的點數完全相同

#### Scenario: 重複觸發付款成功不重複核發
- **WHEN** 同一筆訂單的付款成功流程因重複投遞（例如 webhook 重送）被觸發第二次
- **THEN** 該會員的 `points` 不因第二次觸發而再次增加，`point_transactions` 內該訂單只存在一筆核發記錄

#### Scenario: 零金額訂單核發零點數但仍留下核發記錄
- **WHEN** 一筆核發基準小於 100 最小貨幣單位的訂單付款成功
- **THEN** 核發點數為 0，`members.points` 不變，但 `point_transactions` 仍留下一筆該訂單的核發記錄，防止後續重複觸發被誤判為尚未處理

### Requirement: Order return claws back previously awarded points, capped at the current balance

訂單 `fulfillment_status` 透過 `order.Service.UpdateFulfillmentStatus` 轉為已退貨（`3`）時，系統 MUST 嘗試扣回該訂單先前核發的點數：扣回量 = `min(該訂單原本核發的點數, 會員目前的 points)`，MUST NOT 造成 `members.points` 變為負數。

扣回量 MUST 取自該訂單**原本那一列核發記錄的 `points_delta`**，MUST NOT 以任何當下的金額或折扣率重新計算——因此帶折扣的訂單退貨時扣回的恰好是它當初核發的數字。

扣回 MUST 是冪等的——同一筆訂單的退貨轉換 MUST NOT 造成扣回被重複執行。訂單若從未核發過點數（例如訂單從未付款即被出貨後退貨），扣回量為 0，`members.points` 不變。

#### Scenario: 已核發點數的訂單退貨後扣回等量點數
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，且會員目前餘額 >= 12
- **THEN** 該會員 `points` 減少 12，`point_transactions` 新增一筆該訂單的扣回記錄

#### Scenario: 帶折扣的訂單退貨扣回的是當初核發的數字
- **WHEN** 一筆折後基準核發 36 點的訂單轉為已退貨，且期間商家已把該等級的折扣率改成別的值
- **THEN** 扣回量為 36，MUST NOT 依新的折扣率重新計算

#### Scenario: 扣回量不超過會員目前餘額
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，但會員目前餘額因其他異動已降為 5
- **THEN** 該會員 `points` 最多扣回至 0（扣回 5 點，而非 12 點），MUST NOT 變為負數

#### Scenario: 重複觸發退貨轉換不重複扣回
- **WHEN** 同一筆訂單的已退貨轉換因重複觸發被處理第二次
- **THEN** 該會員的 `points` 不因第二次觸發而重複扣回

### Requirement: Member self-service points access is scoped by member identity

會員自助 API（`GET /api/v1/shop/points`、`GET /api/v1/shop/points/transactions`）MUST 僅依賴已驗證會員 JWT 中的 `member_id` 判斷存取範圍，MUST NOT 套用 RBAC 角色權限節點系統，MUST NOT 接受請求參數指定的會員或 shop_member 識別碼。前者 MUST 回傳呼叫者自己目前的點數餘額與目前等級；後者 MUST 回傳呼叫者自己的流水帳，分頁。

回傳的目前等級 MUST 包含該等級的 `discount_percent`——這是會員前台在結帳前預估折抵金額的唯一資料來源，系統 MUST NOT 為此另設端點。呼叫端據此預估時 MUST 使用與伺服器完全相同的整數規則（spec tier-discount-application）。

#### Scenario: 會員查詢自己的點數餘額與等級
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/points`
- **THEN** 回應包含呼叫者自己目前的 `points` 與目前等級，且該等級物件包含其 `discount_percent`

#### Scenario: 會員查詢自己的點數流水帳
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/points/transactions`
- **THEN** 回傳僅屬於呼叫者自己的流水帳記錄，分頁

#### Scenario: 跨店重用會員 token 存取點數被拒
- **WHEN** 以 shop A 簽發的會員 token 呼叫 shop B 網域的點數 API
- **THEN** audience 不符，回 401
