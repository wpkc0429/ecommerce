# multi-tenancy Specification
> **部分已棄用（ADR 0001，2026-11-24）**：「Host-based tenant resolution」「Path-prefix disambiguation」「Site-shop mapping constraints」「Route resolution cache」描述的是 site／網域對應，隨租戶渲染一起棄用（草稿 change `retire-tenant-rendering`，未核准）；headless 前端以 `X-Publishable-Key` 解析商家。其餘需求不受影響。

## Purpose
TBD - created by archiving change phase1-multitenant-cms-foundation. Update Purpose after archive.
## Requirements
### Requirement: Host-based tenant resolution
系統 SHALL 於每個前台請求取得商家上下文。解析輸入依端點群組而異：

- **前台 API 群組（`/api/v1/shop/*`）**：請求含 `X-Publishable-Key` 時 MUST 以該金鑰解析出的商家為準（詳見 spec storefront-client-auth），金鑰無效時 MUST 回 404 而 MUST NOT 回退；否則取 `Host` 標頭，小寫正規化後查詢 `sites.domain`。此群組 MUST NOT 讀取 `X-Site-Domain` 或 `X-Site-Path`。
- **渲染 bundle 端點（`/api/v1/render/page`）**：請求含 `X-Publishable-Key` 時以其為準；否則含 `X-Site-Domain` 時以其值為準；否則取 `Host`。

`X-Site-Domain` 之所以僅存活於渲染 bundle 端點，是因為該端點的呼叫方為第一方 SSR 伺服器，其唯一職責是中繼訪客實際造訪的網域，而回應內容為已發佈的公開內容——偽造所得等同直接造訪對方網域。此標頭 MUST NOT 於渲染 bundle 端點以外的任何端點生效。

未綁定的網域 MUST 回 404。

#### Scenario: 已綁定網域解析至預設商家
- **WHEN** 請求 `Host: shop1.com`，且 `shop1.com` 存在於 `sites` 並有 `path_prefix IS NULL` 的 `site_shop` 對應
- **THEN** 請求上下文取得該預設商家的 `shop_id`

#### Scenario: 未知網域
- **WHEN** 請求的 Host 不存在於 `sites`
- **THEN** 回應 404

#### Scenario: 自帶前端以金鑰指定站點
- **WHEN** 前台 API 請求含 shop A 的有效 `X-Publishable-Key`（不論 Host 為何值）
- **THEN** 取得 shop A 的商家上下文

#### Scenario: 前台 API 忽略 X-Site-Domain
- **WHEN** `/api/v1/shop/*` 的請求含 `X-Site-Domain: shop1.com`，而 `Host` 為已綁定 shop2 的網域
- **THEN** 解析為 shop2，該標頭 MUST NOT 影響結果

#### Scenario: 渲染 bundle 端點仍接受 X-Site-Domain
- **WHEN** `/api/v1/render/page` 的請求含 `X-Site-Domain: shop1.com` 且該網域已綁定（不論 Host 為何值）
- **THEN** 以 `shop1.com` 進入同一條解析流程，取得對應商家上下文

### Requirement: Path-prefix disambiguation
同一 site 綁定多個商家時，系統 SHALL 以請求路徑對 `site_shop.path_prefix` 做最長前綴匹配決定商家；命中前綴 MUST 自路徑剝離後再進入頁面解析；無任何前綴命中時 SHALL 落到該站 `path_prefix IS NULL` 的預設商家；連預設商家都不存在時 MUST 回 404。

前綴匹配的輸入為**前台路徑**，其來源為渲染 bundle 端點的 `path` 查詢參數。前台 API 群組（`/api/v1/shop/*`）的請求路徑是 API 路徑而非前台路徑，該群組 MUST NOT 以任何標頭接受前台路徑作為消歧輸入；以路徑前綴共用網域的商家若要接自帶前端，MUST 以 publishable key 指定商家。

#### Scenario: 前綴命中
- **WHEN** `saas.com` 綁定 shop A（`path_prefix = '/brand-a'`）與預設 shop B，請求路徑為 `/brand-a/about`
- **THEN** 解析為 shop A，頁面 slug 解析輸入為 `/about`

#### Scenario: 最長前綴優先
- **WHEN** 同站存在 `/brand` 與 `/brand-a` 兩個前綴，請求路徑為 `/brand-a/x`
- **THEN** 解析為 `/brand-a` 對應的商家

#### Scenario: 無前綴命中落到預設商家
- **WHEN** 請求路徑 `/about` 未命中任何前綴，且該站存在預設商家
- **THEN** 解析為預設商家，slug 解析輸入為 `/about`

#### Scenario: 無預設商家
- **WHEN** 請求路徑未命中任何前綴，且該站沒有 `path_prefix IS NULL` 的對應
- **THEN** 回應 404

#### Scenario: 共用網域的商家於前台 API 需以金鑰指定
- **WHEN** `saas.com` 綁定 shop A（`/brand-a`）與預設 shop B，shop A 的自帶前端以 `Host: saas.com` 呼叫前台 API 但未帶金鑰
- **THEN** 解析為預設商家 shop B；shop A MUST 改以 publishable key 指定自身

### Requirement: Shop status gating
解析出的商家 MUST 依 `shops.status` 決定放行：1（啟用）才提供服務；0（停用）回 503 並由前台渲染維護頁；2（審核中）回 404。

#### Scenario: 停用商家
- **WHEN** 解析出的商家 `status = 0`
- **THEN** 渲染 API 回 503，SSR 顯示維護頁

#### Scenario: 審核中商家
- **WHEN** 解析出的商家 `status = 2`
- **THEN** 回應 404

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

### Requirement: Site-shop mapping constraints
`site_shop` MUST 以資料庫約束保證：同站 `path_prefix` 唯一、每站至多一個預設商家（`path_prefix IS NULL`）、每商家至多一個主網域（`is_primary = true`）。

#### Scenario: 重複預設商家被拒
- **WHEN** 對已有預設商家的 site 新增第二筆 `path_prefix IS NULL` 的對應
- **THEN** 寫入失敗（unique violation），API 回 422

#### Scenario: 重複主網域被拒
- **WHEN** 對已有主網域的 shop 將第二個 site 對應設為 `is_primary = true`
- **THEN** 寫入失敗（unique violation），API 回 422

### Requirement: Route resolution cache
Host→(site, 商家對應) 的解析結果 SHALL 快取於 Redis（key `route:{host}`，TTL 5 分鐘）；`sites` 或 `site_shop` 異動時 MUST 主動失效受影響的 host key。

#### Scenario: 對應異動後立即生效
- **WHEN** 管理端修改某網域的 `site_shop` 對應
- **THEN** 該 host 的 `route:{host}` 快取被刪除，下一請求依新對應解析

