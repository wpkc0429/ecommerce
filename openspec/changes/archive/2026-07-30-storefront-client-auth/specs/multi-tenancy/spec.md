## MODIFIED Requirements

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
