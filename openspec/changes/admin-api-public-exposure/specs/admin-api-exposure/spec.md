## ADDED Requirements

### Requirement: 後台路徑的 CORS 依用途與商家分流

CORS 中介層 MUST 依請求路徑決定允許集合：
- `/api/v1/admin/shops/{shopID}/…`：平台清單 ∪ 登記為 `purpose=admin` 且屬於該 `shopID` 的 origin；
- `/api/v1/admin/auth/…`、`/api/v1/admin/me`、`/api/v1/admin/branding`、`/api/v1/admin/invitations/accept`、`/api/v1/admin/media/config`：平台清單 ∪ 任一 `purpose=admin` 的 origin；
- 其他 `/api/v1/admin/…`：只有平台清單；
- 所有非後台路徑：平台清單 ∪ `purpose=storefront` 的 origin（與本變更前相同）。

`purpose=storefront` 的 origin MUST NOT 在任何 `/api/v1/admin/…` 路徑上獲得 `Access-Control-Allow-Origin`。preflight 的判定 MUST 只依 `Origin` 與請求路徑，MUST NOT 需要任何認證或自訂標頭的值。`Access-Control-Allow-Headers` MUST 包含 `X-Admin-Refresh`。

#### Scenario: storefront origin 無法跨源呼叫後台
- **WHEN** `https://bouquet.example` 登記為 shop A 的 storefront origin，瀏覽器自該 origin 對 `/api/v1/admin/auth/login` 送出 preflight
- **THEN** 回應不帶任何 `Access-Control-Allow-*` 標頭

#### Scenario: admin origin 只對自己的店放行
- **WHEN** `https://admin.bouquet.example` 登記為 shop A 的 admin origin，自該 origin 對 `/api/v1/admin/shops/{B}/orders` 送出 preflight
- **THEN** 回應不帶 `Access-Control-Allow-Origin`；對 `/api/v1/admin/shops/{A}/orders` 則帶

#### Scenario: admin origin 不能呼叫平台層路徑
- **WHEN** 自 shop A 的 admin origin 對 `/api/v1/admin/accounts` 送出 preflight
- **THEN** 回應不帶 `Access-Control-Allow-Origin`

#### Scenario: 店面路徑不受影響
- **WHEN** 自 shop A 的 storefront origin 對 `/api/v1/shop/products` 送出請求
- **THEN** 回應帶 `Access-Control-Allow-Origin`，與本變更前相同

### Requirement: per-shop admin 主機在伺服器端只能操作自己的商家

當後台請求的 `Origin`（若存在）或 `Host`（若為已登記的 admin 主機）對應到 `purpose=admin` 且屬於商家 X 的登記時，系統 MUST 對 `/api/v1/admin/shops/{Y}/…`（Y≠X）與所有平台層後台路徑回 403，即使呼叫者持有平台角色或是 Y 的成員；`auth`、`me`、邀請接受路徑 MUST 放行。`Origin` 與 `Host` 皆不對應任何 admin 登記時，MUST NOT 施加此限制。

#### Scenario: 多店成員在單店 admin 上
- **WHEN** 同時是 shop A 與 shop B 成員的使用者，經 shop A 的 admin 主機呼叫 shop B 的 `GET /orders`
- **THEN** 回 403

#### Scenario: 平台管理員在商家 admin 上
- **WHEN** 平台管理員經 shop A 的 admin 主機呼叫 `POST /api/v1/admin/shop-provisioning`
- **THEN** 回 403

#### Scenario: 未登記主機不受限
- **WHEN** 請求的 `Host` 為 `localhost:8080` 且無 `Origin`
- **THEN** 不套用主機綁定，依 RBAC 判定

### Requirement: 可選的 Cloudflare Access JWT 驗證

設定 `CF_ACCESS_TEAM_DOMAIN` 與 `CF_ACCESS_AUD` 時，系統 MUST 對所有 `/api/v1/admin/…` 請求要求 `Cf-Access-Jwt-Assertion` 標頭，並驗證其以團隊 JWKS 的 RS256 簽章、`aud` 包含設定值、`iss` 相符、未過期；任一不符 MUST 回 403。JWKS 無法取得時 MUST 拒絕請求（fail closed）。未設定時此檢查 MUST NOT 生效，且路由表 MUST NOT 因此改變。

#### Scenario: 繞過 Access 的請求被拒
- **WHEN** 已設定 Access 驗證，請求未帶 `Cf-Access-Jwt-Assertion` 直接打到後台登入端點
- **THEN** 回 403

#### Scenario: 錯誤 audience
- **WHEN** JWT 簽章有效但 `aud` 不含設定值
- **THEN** 回 403

#### Scenario: 未設定時不影響
- **WHEN** 未設定 `CF_ACCESS_*`
- **THEN** 後台請求不需要該標頭，行為與本變更前相同

### Requirement: 平台層後台路徑只能經由平台 admin 主機

設定 `PLATFORM_ADMIN_HOSTS` 時，平台層後台路徑（非 `/api/v1/admin/shops/{shopID}/…`、非 `auth`／`me`／`branding`／邀請接受）的請求，其主機（`Origin` 優先、否則 `Host`）不在清單內 MUST 回 403，不論呼叫者權限。未設定時 MUST NOT 生效。系統 MUST NOT 在程式或設定預設值中內建任何信箱。

#### Scenario: 從共用商家網域呼叫平台 API
- **WHEN** 已設定 `PLATFORM_ADMIN_HOSTS=admin.ksdevworks.online`，super_admin 經 `merchant.ksdevworks.online` 呼叫 `GET /api/v1/admin/accounts`
- **THEN** 回 403

#### Scenario: 從平台網域呼叫
- **WHEN** 同一請求經 `admin.ksdevworks.online`
- **THEN** 依 RBAC 判定

### Requirement: 多個 admin 前端並存與 per-shop 品牌

API MUST NOT 依前端身分做任何分支。商家設定 MUST 接受選填 `admin_branding`（`logo_url`、`primary_color`、`accent_color`；顏色為 `#RRGGBB`，`logo_url` 在媒體已設定時 MUST 為影像網域），未帶時 MUST 不變。系統 SHALL 提供 `GET /api/v1/admin/shops/{shopID}/branding`（跨店守衛，無需權限節點）與未認證的 `GET /api/v1/admin/branding`（依請求的已登記 admin 主機解析商家，回傳商家名稱與品牌；主機未登記時 404）。

產生設定或邀請連結時，若連結屬於某商家且該商家有 `purpose=admin` 的 origin 登記，連結基底 MUST 為 id 最小的該 origin；否則 MUST 為 `ADMIN_PUBLIC_URL`。

#### Scenario: 登入前顯示商家品牌
- **WHEN** shop A 設定了 logo 與主色，瀏覽器在 shop A 的 admin 主機上請求 `GET /api/v1/admin/branding`
- **THEN** 回 200，含 shop A 的名稱、`logo_url` 與顏色

#### Scenario: 未登記主機
- **WHEN** 在未登記為任何商家 admin origin 的主機上請求 `GET /api/v1/admin/branding`
- **THEN** 回 404

#### Scenario: 非法顏色
- **WHEN** 以 `"primary_color": "red"` 更新設定
- **THEN** 回 422

#### Scenario: Bouquet 的邀請連結指向自己的前端
- **WHEN** Bouquet 登記了 admin origin `https://admin.bouquet.ksdevworks.online`，其 owner 邀請一位店員
- **THEN** `invite_url` 以 `https://admin.bouquet.ksdevworks.online/#/accept-invite/` 開頭
