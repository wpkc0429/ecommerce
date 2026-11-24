## Why

後續每家商家會有自己的 admin 前端（例如 `admin.bouquet.ksdevworks.online`），而今天的後台 API 只在本機可用：Cloudflare Tunnel 的 ingress 對 `api-test.ksdevworks.online` 的 `^/api/v1/admin` 一律回 404。要把後台 API 開出去，現狀有四個缺口：

1. **CORS 不分用途**：`NewCORSMiddleware` 是全域的，任何已登記的 **storefront** origin 都會在 `/api/v1/admin/*` 上拿到 `Access-Control-Allow-Origin`——商家的店面網域與後台共用同一張允許表。也沒有「這個 admin 前端只屬於這家店」的概念。
2. **登入只有速率限制，沒有帳號層級的鎖定**：既有規則是「每 IP」與「每 IP＋email」，所以從多個 IP 對單一帳號的猜測不受限制。
3. **token 儲存**：`admin/lib/api.js` 把 access 與 refresh token 都放在 `localStorage`，任何 XSS 都能取走 30 天有效的 refresh token。
4. **沒有與 Cloudflare Access 的整合方案**：計畫是在 admin 網頁前放 Cloudflare Access，但 API 本身仍需要一條經過 Access 的路徑，且不能在 tunnel 規則被誤改時裸奔。

## What Changes

- **建議拓撲（同源）**：每個 admin 網域是一個 Cloudflare Access 應用（所有 admin 網域共用一個 Access 應用、一個 AUD）。tunnel ingress 在該網域上把 `^/api/v1/admin` 轉到 API、其餘轉到 admin 靜態檔；**API 的公開網域繼續封鎖 `^/api/v1/admin`**（現行規則不動）。結果：後台 API 只能經由 Access 到達，而且與 admin 頁面同源——正式環境不需要 CORS，也讓 cookie 型 refresh token 成為可能。附範例 `docs/deploy/cloudflared-admin.example.yml` 與 runbook。
- **API 端驗證 Cloudflare Access JWT（選填、建議正式開啟）**：設定 `CF_ACCESS_TEAM_DOMAIN` 與 `CF_ACCESS_AUD` 後，所有 `/api/v1/admin/*` MUST 帶有效的 `Cf-Access-Jwt-Assertion`（RS256、JWKS 快取、驗 `aud`／`iss`／`exp`），否則 403。防的是 tunnel 規則被誤改而讓後台 API 繞過 Access。
- **origin 用途分流**：`shop_allowed_origins` 新增 `purpose`（`storefront`／`admin`，預設 `storefront`，既有列不變）。CORS 規則改為：
  - `/api/v1/admin/*`：只允許平台清單（`CORS_ALLOWED_ORIGINS`）與 `purpose=admin` 的 origin；**storefront origin 不再在後台路徑上獲得 CORS**。
  - `/api/v1/admin/shops/{shopID}/*`：admin origin 必須登記於**該** shop；平台層路徑（`/admin/shops`、`/admin/accounts`、`/admin/sites`…）只允許平台清單。
  - 其他路徑：行為不變（storefront origin ∪ 平台清單）。
- **admin 主機綁定**：請求的 `Origin`（若有）或 `Host`（若是已登記的 admin 主機）屬於 shop X 時，對 `/api/v1/admin/shops/{Y}` 且 Y≠X 的請求與所有平台層路徑 MUST 回 403——即使呼叫者本身是多家店的成員或平台管理員。這讓 per-shop admin 前端在伺服器端只碰得到自己那家店。
- **帳號層級的登入鎖定**：新增「每 email、跨所有 IP、只計失敗」的規則（預設 15 分鐘內 10 次失敗 → 該 email 鎖定至視窗滑出），對存在與不存在的 email 行為相同；成功登入清除計數；限流器故障時 fail-open（同既有規則）。日誌只記 email 的雜湊。
- **token 儲存指引與 cookie 模式 refresh**：登入可選 `"refresh_transport": "cookie"`（additive）：refresh token 改以 `__Secure-admin_refresh` cookie（`HttpOnly; Secure; SameSite=Strict; Path=/api/v1/admin/auth`）交付，回應不含 `refresh_token`；`/auth/refresh`、`/auth/logout` 在 body 未帶時讀 cookie，且 cookie 模式 MUST 帶 `X-Admin-Refresh: 1` 標頭（迫使跨源請求走 preflight 而失敗，防同站子網域的 CSRF）。access token 只存在記憶體。不帶 `refresh_transport` 時行為逐位元組不變。admin SPA 在與 API 同源時改用 cookie 模式。
- admin 的 `nginx.conf` 加上 CSP、`frame-ancestors 'none'`、`Referrer-Policy: no-referrer`。
- **多個 admin 前端並存（已定案）**：共用的 merchant admin SPA（一般商家，依商家換 logo／顏色）與 Bouquet 的自訂 admin 前端同時存在，API 不假設只有一個前端：
  - 新增 admin 品牌設定：`PUT /api/v1/admin/shops/{shopID}/settings` additive 接受 `admin_branding`（`logo_url`、`primary_color`、`accent_color`；`shop.update`），讀取用 `GET /api/v1/admin/shops/{shopID}/branding`（成員可讀），以及**未登入**的 `GET /api/v1/admin/branding`（依請求的已登記 admin 主機解析商家，讓共用 SPA 在登入頁就換上該店品牌；未登記主機回 404，前端用平台預設）。
  - 設定／邀請連結的基底：商家有登記 `purpose=admin` 的 origin 時用它（id 最小者），否則用 `ADMIN_PUBLIC_URL`——Bouquet 的人收到的連結指向 Bouquet 自己的前端。
  - 新增 `docs/admin-frontend-integration.md`：任何 admin 前端都必須實作的契約（連結 fragment 路由、token 儲存、cookie／body 模式的選擇、上傳流程、Access 重導處理）。
- **平台層路徑只能經由平台 admin 主機（已定案）**：新設定 `PLATFORM_ADMIN_HOSTS`（逗號分隔）；設定時，平台層後台路徑的請求主機不在清單內一律 403。平台 admin 網域的 Cloudflare Access policy 只放行平台擁有者的信箱——**信箱寫在 Access policy 裡（runbook 步驟），不寫進程式或 repo**。

## Capabilities

### New Capabilities
- `admin-api-exposure`：後台 API 的 CORS 用途分流、admin origin／主機與商家的綁定、Cloudflare Access JWT 驗證。

### Modified Capabilities
- `storefront-client-auth`：origin 登記新增 `purpose`。
- `authentication`：帳號層級登入鎖定；cookie 模式的 refresh token 交付。

## Impact

- **資料**：`shop_allowed_origins.purpose`（DEFAULT `'storefront'`，CHECK）；`shops.admin_branding`（jsonb DEFAULT `'{}'`）。
- **程式碼**：`api/internal/httpapi/middleware.go`（CORS 分流）、新 `api/internal/httpapi/adminorigin.go`（主機綁定）、新 `api/internal/httpapi/cfaccess.go`（JWT 驗證）、`api/internal/tenant/clientauth.go`（origin 查詢回傳 shop 與用途、快取鍵）、`api/internal/httpapi/clientauth.go`（`purpose` 欄位）、`api/internal/httpapi/adminauth.go`（鎖定、cookie 模式）、`api/internal/ratelimit/limiter.go`（只計失敗的計數原語）、`api/internal/config/config.go`、`admin/lib/api.js`、`admin/nginx.conf`、`admin/views/storefront-clients.js`（origin 用途）、`api/internal/httpapi/settings.go`（`admin_branding`）、新 `api/internal/httpapi/branding.go`、`api/internal/account/delivery.go`（連結基底）、`admin/app.js`（套用品牌）。
- **API 契約**：`AllowedOrigin.purpose`、登入的 `refresh_transport`、refresh／logout 的 `X-Admin-Refresh` 標頭；`Access-Control-Allow-Headers` 加入 `X-Admin-Refresh`；全部 additive。
- **維運**：Cloudflare Access 應用與 policy、tunnel ingress 新規則（**在 repo 外的 `~/.cloudflared/*.yml`，由使用者套用**）。
- **相依**：建議在 `platform-account-provisioning` 與 `merchant-staff-role` 之後上線（兩者新增的未認證端點與成員模型都要一起被暴露）；`cms-rich-text-sanitization` MUST 在對外開放前上線。
