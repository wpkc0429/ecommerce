## ADDED Requirements

### Requirement: Platform-only shop creation

系統 SHALL 提供 `POST /api/v1/admin/shops`，僅持有平台角色（`role_user.shop_id IS NULL`）且具 `shop.create` 權限者可呼叫；請求 MUST 提供 `name`（非空字串），MAY 提供 `theme_id`（須為既存 theme，否則回 422）與初始 `status`（須 ∈ {0,1,2}，否則回 422；未提供時預設 1 啟用）。建立成功後系統 MUST 在同一交易內依 page-management capability 的「Auto-created home page」需求自動建立該商家的 `type_key=home, slug=home` 草稿頁——不論是否提供 `theme_id`。非平台角色（含商家自身持有 shop-scoped 角色者，如 `merchant_owner`）呼叫 MUST 回 403。

#### Scenario: 平台角色建店成功並自動建首頁
- **WHEN** 持有 `shop.create` 的平台角色呼叫 `POST /admin/shops`，body 為 `{"name": "New Shop"}`
- **THEN** 回應 201 並回傳新商家 `id`；該商家存在 `slug = 'home'`、`status = 0`（草稿）的頁面

#### Scenario: 商家自身角色呼叫被拒
- **WHEN** 僅持有商家 A 的 `merchant_owner`（shop-scoped）角色者呼叫 `POST /admin/shops`
- **THEN** 回應 403

#### Scenario: 名稱缺漏被拒
- **WHEN** 平台角色呼叫 `POST /admin/shops`，body 未提供 `name` 或為空字串
- **THEN** 回應 422

#### Scenario: 指定不存在的主題被拒
- **WHEN** 平台角色呼叫 `POST /admin/shops`，`theme_id` 指向不存在的主題
- **THEN** 回應 422

### Requirement: Platform shop query and listing

系統 SHALL 提供 `GET /api/v1/admin/shops/{shopID}`（單筆）與 `GET /api/v1/admin/shops`（分頁列表），僅持有平台角色且分別具 `shop.view`／`shop.list` 權限者可呼叫。列表端點 MUST 接受 `page`（預設 1，小於 1 正規化為 1）與 `page_size`（預設 20，上限 100）查詢參數，回應 MUST 含 `page`、`page_size`、`total`（符合條件的商家總數）與 `shops` 陣列，且陣列筆數 MUST NOT 超過 `page_size`。單筆查詢對不存在的 `shopID` MUST 回 404。

#### Scenario: 分頁列表正確性
- **WHEN** 系統存在 25 筆商家，平台角色呼叫 `GET /admin/shops?page=2&page_size=10`
- **THEN** 回應 `shops` 陣列含 10 筆、`total = 25`、`page = 2`、`page_size = 10`，且與第一頁（`page=1`）回傳的商家不重複

#### Scenario: 查詢不存在的商家
- **WHEN** 平台角色呼叫 `GET /admin/shops/{shopID}`，`shopID` 不存在
- **THEN** 回應 404

### Requirement: Platform shop update and status transition

系統 SHALL 提供 `PUT /api/v1/admin/shops/{shopID}`，僅持有平台角色且具 `shop.update` 權限者可呼叫，支援部分更新 `name`、`status`、`meta`。`status` 若提供 MUST ∈ {0, 1, 2}（對應既有 Shop status gating 需求的語意：0 停用/1 啟用/2 審核中），否則回 422。`status` 實際發生變更時，系統 MUST 使該商家所有已綁定網域的 route resolution cache（`route:{host}`）立即失效，使後續請求依最新 status 判定放行（不得依賴既有 TTL 兜底）。

#### Scenario: 合法 status 轉換
- **WHEN** 平台角色對 `status = 1` 的商家呼叫 `PUT /admin/shops/{shopID}`，body 為 `{"status": 0}`
- **THEN** 回應 200，商家 `status` 更新為 0，該商家綁定網域的路由快取被清除

#### Scenario: 非法 status 值被拒
- **WHEN** 平台角色呼叫 `PUT /admin/shops/{shopID}`，body 為 `{"status": 9}`
- **THEN** 回應 422，商家 status 不變

#### Scenario: 更新後立即影響前台放行
- **WHEN** 已啟用商家的網域先前已有快取的路由解析結果，平台角色將其 status 更新為 0（停用）
- **THEN** 下一個對該網域的前台請求立即回 503（而非等待路由快取 TTL 過期）
