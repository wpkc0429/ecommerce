## 1. 資料模型與 origin 查詢（design D3）

- [ ] 1.1 `api/internal/ent/schema/clientauth.go`：`ShopAllowedOrigin.purpose`（varchar(20) DEFAULT `'storefront'`，CHECK `IN ('storefront','admin')`）
- [ ] 1.2 `go generate ./internal/ent`；`make migrate-gen name=add_allowed_origin_purpose`；確認 CHECK 有產生（否則手寫）；up→down→up
- [ ] 1.3 `api/internal/tenant/clientauth.go`：`LookupOrigin(ctx, origin) (shopID int, purpose string, ok bool)`，快取鍵 `corsorigin2:{origin}` 值 `"{shop}:{purpose}"`／`"0"`；既有 `IsRegisteredOrigin` 改以它實作並只回 storefront；`StorefrontClientChanged`／`SiteMappingChanged` 失效新鍵
- [ ] 1.4 `api/internal/httpapi/clientauth.go`：`POST /allowed-origins` 接受選填 `purpose`；列表回應含 `purpose`
- [ ] 1.5 `clientauth_integration_test.go`：預設 storefront、`purpose` 非法 422、跨用途重複 422

## 2. CORS 分流（design D3）

- [ ] 2.1 `api/internal/httpapi/middleware.go`：`NewCORSMiddleware` 依路徑分類（純函式 `classifyAdminPath(path) (kind, shopID)`，單元測試涵蓋表格中每一列與畸形路徑）；`Allow-Headers` 加入 `X-Admin-Refresh`
- [ ] 2.2 `OriginRegistry` 介面改為 `LookupOrigin`（`router.go` 註解同步）
- [ ] 2.3 `clientauth_integration_test.go`／新 `cors_integration_test.go`：spec `admin-api-exposure` 第一條需求的四個情境＋既有 CORS 測試全綠

## 3. admin 主機綁定（design D4）

- [ ] 3.1 新增 `api/internal/httpapi/adminorigin.go`：`NewAdminHostBindingMiddleware(registry)`，掛在 `/api/v1/admin` 群組（AdminMW 之前即可判定，不需身分）；以 `Origin` 優先、否則 `Host`（去 port 前的正規化需與 `tenant.NormalizeOrigin` 一致；`Host` 需組成 `https://host` 再查）
- [ ] 3.2 單元＋整合測試：多店成員跨店 403、平台管理員平台路徑 403、auth/me 放行、未登記主機不受限

## 4. Cloudflare Access JWT（design D2）

- [ ] 4.1 `api/internal/config/config.go`：`CF_ACCESS_TEAM_DOMAIN`、`CF_ACCESS_AUD`、`CF_ACCESS_BIND_EMAIL`（兩者需同時設定，否則拒絕啟動）；production 未設定時 WARN
- [ ] 4.2 新增 `api/internal/httpapi/cfaccess.go`：JWKS 抓取與 1 小時快取、未知 `kid` 重抓一次、RS256 驗簽、`aud`／`iss`／`exp`；fail closed；未設定時 passthrough（中介層恆掛，路由表不變）
- [ ] 4.3 `CF_ACCESS_BIND_EMAIL`：於 AdminMW 之後比對 JWT `email` 與後台帳號 email（auth 路徑與 password-setup／邀請接受路徑豁免）
- [ ] 4.4 單元測試：以測試金鑰簽發的 JWT（有效、錯 aud、過期、錯 iss、未知 kid、JWKS 500）；整合測試：設定後未帶標頭 403、未設定時不影響

## 5. 帳號層級鎖定（design D6）

- [ ] 5.1 `api/internal/ratelimit/limiter.go`：新增 `Peek`、`Record`、`Reset`（滑動視窗、fail-open），`limiter_integration_test.go` 覆蓋
- [ ] 5.2 `api/internal/config/config.go`：`RATE_LIMIT_LOGIN_ACCOUNT_FAILURES`（10）、`RATE_LIMIT_LOGIN_ACCOUNT_WINDOW`（15m）；鍵 HMAC 用的 secret 取自既有 admin JWT secret 衍生（不新增祕密）或新增 `LOGIN_LOCKOUT_KEY_SECRET`（擇一，於實作時記入 design）
- [ ] 5.3 `api/internal/httpapi/adminauth.go`：`Login` 開頭 `Peek` → 429；失敗 `Record`；成功 `Reset`；日誌只記雜湊
- [ ] 5.4 `ratelimit_integration_test.go`：分散 IP 鎖定、不存在 email 相同行為、成功清除、Redis 故障 fail-open

## 6. Cookie 模式 refresh（design D5）

- [ ] 6.1 `api/internal/httpapi/adminauth.go`：`refresh_transport`；`Set-Cookie` 屬性；refresh／logout 讀 cookie 並要求 `X-Admin-Refresh: 1`；logout 清除 cookie
- [ ] 6.2 `authflow_integration_test.go`：cookie 登入、缺標頭 401 且未輪替、輪替後 cookie 更新、重放偵測在 cookie 模式下同樣撤銷鏈、預設模式逐位元組不變（比對回應鍵集合與無 `Set-Cookie`）
- [ ] 6.3 `admin/lib/api.js`：access token 改存記憶體；`API_BASE` 預設改為「若 `location.origin` 非 localhost 則用 `location.origin`」；同源時登入用 cookie 模式且 refresh 帶 `X-Admin-Refresh`，跨源時沿用 body 模式；偵測 Access 重導（非 JSON 回應）時整頁重新載入
- [ ] 6.4 `admin/nginx.conf`：CSP、`frame-ancestors 'none'`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff`；`connect-src` 納入 R2 presign 網域（若 `product-images-r2` 已上線）

## 6a. 多前端與品牌（design D7–D10）

- [ ] 6a.1 `api/internal/ent/schema/tenancy.go`：`Shop.admin_branding`（JSON DEFAULT `{}`）；migration 併入 1.2
- [ ] 6a.2 `api/internal/httpapi/settings.go`：`admin_branding` additive（顏色 `#RRGGBB`、`logo_url` 走與 CMS 圖片欄位相同的網域驗證函式；未帶不變）
- [ ] 6a.3 新增 `api/internal/httpapi/branding.go`：`GET /admin/shops/{shopID}/branding`（`RequireShopAccess`）與未認證 `GET /admin/branding`（以主機解析，未登記 404）；加入「已認證但無 RBAC 守衛」清單與 CORS 第二列
- [ ] 6a.4 `api/internal/account/delivery.go`：連結基底依商家 `purpose=admin` origin（id 最小）否則 `ADMIN_PUBLIC_URL`
- [ ] 6a.5 `api/internal/config/config.go`：`PLATFORM_ADMIN_HOSTS`；中介層（可與 3.1 合併）對平台層路徑檢查主機；production 未設定 WARN
- [ ] 6a.6 整合測試：品牌讀寫、未登記主機 404、非法顏色 422、Bouquet 邀請連結基底、`PLATFORM_ADMIN_HOSTS` 403／放行、未設定不生效
- [ ] 6a.7 新增或擴充 `docs/admin-frontend-integration.md`（活文件）：連結 fragment 契約、token 儲存、cookie／body 模式選擇、CORS 與 origin 登記、上傳與 HEIC、Access 重導、品牌端點；註明 API 不依前端身分分支

## 7. Admin UI

- [ ] 7.1 `admin/views/storefront-clients.js`：origin 登記可選用途（前台／後台），列表顯示用途並說明差異
- [ ] 7.2 `admin/app.js`：開機時呼叫 `GET /admin/branding`（404 用預設），切換商家時呼叫 `/shops/{id}/branding`；以 CSS 變數套用 logo 與顏色（`styles.css` 的顏色改為變數）
- [ ] 7.3 報表視圖旁的商家設定表單（既有時區設定處）新增品牌欄位；量測 #4′／#5

## 8. API 契約

- [ ] 8.1 `openapi.yaml`：`AllowedOrigin.purpose`、登入 `refresh_transport`、refresh／logout 的 `X-Admin-Refresh` 標頭參數（`optional`）與 `Set-Cookie` 回應標頭；`Cf-Access-Jwt-Assertion` 以敘述記載（不列為參數，因未設定時不需要）
- [ ] 8.2 重新產生 `v1-surface.txt`，確認只新增

## 9. 部署文件

- [ ] 9.1 新增 `docs/deploy/cloudflared-admin.example.yml`（design D1 的 ingress 範例，含既有 404 規則）
- [ ] 9.2 新增 `docs/deploy/admin-exposure-runbook.md`：兩個 Access 應用（商家 admin 網域：email OTP 任何人；平台 admin 網域：只放行平台擁有者信箱——信箱在 Cloudflare 後台填，不進 repo）、取得 AUD、tunnel 規則套用、`CF_ACCESS_*` 與 `PLATFORM_ADMIN_HOSTS` 設定、驗證步驟（design Migration Plan 第 6 點）、回滾
- [ ] 9.3 `docs/storefront-integration.md`：說明 storefront 與 admin origin 的差異

## 10. E2E

- [ ] 10.1 `e2e/happy-path.mjs`：登記一個 admin origin 給 demo 商家 → 以 `fetch` 模擬該 Origin 的 preflight 對他店路徑不放行、對本店放行；storefront origin 對 `/admin/auth/login` 不放行
- [ ] 10.2 `e2e/commerce-path.mjs`：既有流程以 body 模式登入，全綠（證明預設行為不變）

## 11. 文件與驗收

- [ ] 11.1 `CLAUDE.md`：多前端原則（不依前端身分分支、契約文件）、品牌端點、連結基底、`PLATFORM_ADMIN_HOSTS`、同源＋Access 是正式拓撲、CORS 不是授權、origin 用途表、主機綁定、Access JWT fail closed、鎖定的 DoS 取捨、cookie 模式與 `X-Admin-Refresh` 的理由
- [ ] 11.2 `make test`、`make test-int`、`make lint`、`make openapi`；`node happy-path.mjs`、`node commerce-path.mjs` 全綠
- [ ] 11.3 依 runbook 在 `api-test` 環境實測五項驗證（由使用者套用 tunnel 設定後執行）
