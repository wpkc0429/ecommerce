# multi-tenancy Specification

## Purpose
TBD - created by archiving change phase1-multitenant-cms-foundation. Update Purpose after archive.
## Requirements
### Requirement: Host-based tenant resolution
系統 SHALL 於每個前台請求取得站點識別——請求含 `X-Site-Domain` 標頭時以其值為準（原生 APP 等非瀏覽器用戶端通道），否則取 `Host` 標頭——小寫正規化後查詢 `sites.domain`，解析出對應的 site；未綁定的網域 MUST 回 404。

#### Scenario: 已綁定網域解析至預設商家
- **WHEN** 請求 `Host: shop1.com`，且 `shop1.com` 存在於 `sites` 並有 `path_prefix IS NULL` 的 `site_shop` 對應
- **THEN** 請求上下文取得該預設商家的 `shop_id`

#### Scenario: 未知網域
- **WHEN** 請求的 Host 不存在於 `sites`
- **THEN** 回應 404

#### Scenario: App 用戶端以顯式標頭指定站點
- **WHEN** 請求含 `X-Site-Domain: shop1.com` 且該網域已綁定（不論 Host 為何值）
- **THEN** 以 `shop1.com` 進入同一條解析流程，取得對應商家上下文

### Requirement: Path-prefix disambiguation
同一 site 綁定多個商家時，系統 SHALL 以請求路徑對 `site_shop.path_prefix` 做最長前綴匹配決定商家；命中前綴 MUST 自路徑剝離後再進入頁面解析；無任何前綴命中時 SHALL 落到該站 `path_prefix IS NULL` 的預設商家；連預設商家都不存在時 MUST 回 404。

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

### Requirement: Shop status gating
解析出的商家 MUST 依 `shops.status` 決定放行：1（啟用）才提供服務；0（停用）回 503 並由前台渲染維護頁；2（審核中）回 404。

#### Scenario: 停用商家
- **WHEN** 解析出的商家 `status = 0`
- **THEN** 渲染 API 回 503，SSR 顯示維護頁

#### Scenario: 審核中商家
- **WHEN** 解析出的商家 `status = 2`
- **THEN** 回應 404

### Requirement: Tenant data isolation enforcement
所有租戶擁有資料（`pages`、`shop_member` 等）的查詢與寫入 MUST 在資料存取層強制附加當前上下文的 `shop_id` 條件（ent privacy/interceptor），且後台 API 的目標 shop MUST 由授權上下文決定、不得信任 client 傳入的 shop 識別。

#### Scenario: 前台跨租戶讀取被隔離
- **WHEN** shop A 的網域請求 shop B 已發佈頁面的 slug
- **THEN** 回應 404（查詢範圍僅及 shop A）

#### Scenario: 資料層強制範圍
- **WHEN** 應用程式碼在租戶上下文中查詢 `pages` 但未顯式指定 shop 條件
- **THEN** 資料層自動附加 `shop_id = 當前租戶` 條件（以自動化測試驗證）

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

