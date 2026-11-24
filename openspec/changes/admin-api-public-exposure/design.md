## Context

- Tunnel（`~/.cloudflared/ecommerce-test.yml`，repo 外）：`api-test.ksdevworks.online` 的 `path: ^/api/v1/admin` → `http_status:404`，其餘 → `127.0.0.1:8080`。admin SPA 不在 tunnel 上。
- `NewCORSMiddleware(allowed, registry)`：全域、只問「這個 origin 有沒有被任何商家登記」，不分路徑、不分用途；不發 `Allow-Credentials`。`tenant.Resolver.IsRegisteredOrigin` 以 `corsorigin:{origin}` 快取 `"1"/"0"`，由 `StorefrontClientChanged`／`SiteMappingChanged` 失效。
- 客戶端 IP：只信任 `TRUSTED_PROXIES` 內對端送來的 `CF-Connecting-IP`。
- 登入限流（auth-rate-limiting）：滑動視窗，`broad`（IP）與 `tight`（IP＋email），計所有嘗試、fail-open。
- admin token：`localStorage` 存 access 與 refresh；access 15 分鐘、refresh 30 天、輪替 ＋ 重放偵測（鏈撤銷）。
- 後續：搬 VPS（可能不再有 Cloudflare Tunnel，但多半仍在 Cloudflare 後面）；平台網站提供開店申請。

## Goals / Non-Goals

**Goals:** 後台 API 可從 per-shop admin 網域使用；未經 Access 不可達（正式拓撲）；per-shop admin 前端在伺服器端只能操作自己的店；帳號層級暴力破解有上限；refresh token 不被 JS 讀到。

**Non-Goals:**
- MFA／WebAuthn（Access 的 IdP 可以提供，應用層不做）。
- 會員（前台）登入的帳號鎖定。
- 後台讀取審計軌跡（ONDO 阻塞項，獨立 change）。
- 自動化 Cloudflare 設定（Terraform 等）。

## Decisions

### D1：正式拓撲是「同源 ＋ Access」，CORS 是給其他拓撲的
候選：(a) admin 網域同時代理 `/api/v1/admin`（同源）；(b) admin 靜態站跨源呼叫一個獨立的 `admin-api.` 網域，兩者都掛 Access。

選 (a)：
- Access 以 `CF_Authorization` cookie 認證；(b) 的跨源 XHR 要帶這個 cookie 必須 `credentials: "include"`、API 回 `Allow-Credentials: true`、Access 應用設定 CORS——三處設定任何一處錯就是全站壞，且開啟 credentials 的 CORS 正是最容易配錯的那種。
- 同源讓 refresh token 能放進 `HttpOnly` cookie 而不需要跨源 credentials（D5）。
- tunnel 原生支援同一 hostname 依 path 分流到不同 service。

拓撲（範例檔放 `docs/deploy/cloudflared-admin.example.yml`）：
```
- hostname: admin.bouquet.ksdevworks.online
  path: ^/api/v1/admin
  service: http://127.0.0.1:8080
- hostname: admin.bouquet.ksdevworks.online
  service: http://127.0.0.1:3001
- hostname: api-test.ksdevworks.online       # 既有，不動
  path: ^/api/v1/admin
  service: http_status:404
```
CORS 分流（D3）仍然實作，因為：本機開發（:3001 → :8080）是跨源的；搬 VPS 後可能有不走 Access 的部署；商家也可能把 admin 前端放在別的託管上。**誠實的定位**：CORS 不是授權機制（curl 不看 CORS），它防的是「別的網站上的 JS 讀到後台回應」；在 bearer token 存於記憶體的設計下它是衛生措施，在 cookie 模式下它是必要條件。真正的邊界是 Access ＋ RBAC ＋ D4 的主機綁定。

### D2：API 端驗證 Access JWT，防 tunnel 規則漂移
`~/.cloudflared/*.yml` 不在 repo、不受 review、不受測試——那條 404 規則今天是整個後台的唯一屏障。設定 `CF_ACCESS_TEAM_DOMAIN`（`<team>.cloudflareaccess.com`）與 `CF_ACCESS_AUD` 後，中介層對 `/api/v1/admin/*` 要求 `Cf-Access-Jwt-Assertion`：RS256、以 `https://<team>/cdn-cgi/access/certs` 的 JWKS 驗簽（記憶體快取 1 小時，遇到未知 `kid` 重抓一次，抓不到則 fail **closed**——與限流器的 fail-open 相反，因為這是認證不是配額）、`aud` 包含設定值、`iss` 相符、`exp` 未過。未設定時中介層不掛載行為（但路由表不變：中介層恆掛，未設定時 passthrough）。`APP_ENV=production` 且未設定時啟動記一行 WARN（不拒絕啟動：本機 tunnel 測試環境合法地沒有 Access）。

選填加強：`CF_ACCESS_BIND_EMAIL=true` 時，已認證的後台請求還要求 Access JWT 的 `email` 等於後台帳號 email——偷到的後台 token 沒有同一個人的 Access session 就用不了。預設關閉（見 Open Questions：它要求每個後台帳號都能通過 Access policy）。

Access policy（**已定案**）：
- **商家 admin 網域**（共用 SPA 的各網域與 Bouquet 自訂前端）：「任何能以 email 一次性驗證碼證明信箱的人」通過——Access 當作機器人／撞庫屏障 ＋ 免費的 email 驗證，授權完全交給應用層 RBAC。`CF_ACCESS_BIND_EMAIL` **預設關閉**。
- **平台 admin 網域**：Access policy 只放行平台擁有者的信箱。信箱**只存在於 Cloudflare Access 的 policy 設定**（runbook 步驟），程式與 repo 中不出現任何信箱。伺服器端的對應防線是 D10 的 `PLATFORM_ADMIN_HOSTS`。這也讓 `platform-account-provisioning` 的設定連結與 `merchant-staff-role` 的邀請連結自然得到「受邀者確實擁有該信箱」的驗證——只要 `ADMIN_PUBLIC_URL` 指向 Access 保護的網域。

### D3：origin 用途分流
`shop_allowed_origins.purpose`（`storefront`／`admin`），origin 仍全域唯一（同一 origin 不能同時是兩種用途）。`IsRegisteredOrigin` 改為 `LookupOrigin(origin) → (shopID, purpose, ok)`，快取值改為 `"{shop_id}:{purpose}"`／`"0"`（新鍵前綴 `corsorigin2:` 避免與舊快取值混淆；舊鍵自然過期）。

CORS 判定依**請求路徑**（preflight 的 URL 就是實際請求的 URL，所以 preflight 也答得出來）：

| 路徑 | 允許的 origin |
|---|---|
| `/api/v1/admin/shops/{id}/…` | 平台清單 ∪ `purpose=admin` 且 `shop_id = id` |
| `/api/v1/admin/auth/…`、`/api/v1/admin/me`、`/api/v1/admin/branding`、`/api/v1/admin/invitations/accept`、`/api/v1/admin/media/config` | 平台清單 ∪ 任一 `purpose=admin` |
| 其他 `/api/v1/admin/…`（平台層） | 只有平台清單 |
| 其他所有路徑 | 平台清單 ∪ `purpose=storefront`（**不變**） |

`/api/v1/admin/shops/{id}` 的 `{id}` 由路徑字串解析（CORS 中介層在 chi 路由之前執行，拿不到 URL 參數）；解析失敗視同平台層路徑。

### D4：admin 主機綁定（伺服器端，不只是 CORS）
CORS 只約束瀏覽器讀回應。綁定在授權之前、AdminMW 之後執行：取請求的 `Origin`（同源的 GET 通常不帶）否則 `Host`（cloudflared 預設轉送原始 hostname），若是已登記的 `purpose=admin` 主機且屬於 shop X：
- `/api/v1/admin/shops/{Y}/…` 且 Y≠X → 403；
- 平台層路徑 → 403；
- `auth`／`me`／邀請接受 → 放行。

不是已登記 admin 主機（本機、平台自己的 admin 網域、直接打 API）→ 不綁定，行為不變。效果：一位同時是兩家店成員、甚至是平台管理員的人，在 `admin.bouquet…` 上也只能操作 bouquet——per-shop 前端的「單店」性質由伺服器保證，而不是靠前端自律。平台管理員要做平台操作時使用平台自己的 admin 網域（`CORS_ALLOWED_ORIGINS` 內、不登記於任何商家）。

### D5：token 儲存——access 在記憶體、refresh 在 HttpOnly cookie（同源時）
- **access token**：只存在 JS 記憶體。重新整理頁面後以 refresh 換一枚新的。
- **refresh token**：登入帶 `"refresh_transport": "cookie"` 時，以 `Set-Cookie: __Secure-admin_refresh=<token>; HttpOnly; Secure; SameSite=Strict; Path=/api/v1/admin/auth; Max-Age=<refresh TTL>` 交付，回應 body 不含 `refresh_token`。refresh 輪替時更新 cookie；logout 清除。
- **CSRF**：`SameSite=Strict` 擋跨站，但**同站不同源**（例如商家日後的靜態店面 `bouquet.ksdevworks.online`，與 `admin.bouquet.ksdevworks.online` 同屬一個可註冊網域）仍會帶上 cookie。因此 cookie 模式的 refresh／logout MUST 帶 `X-Admin-Refresh: 1`：自訂標頭迫使跨源請求先 preflight，而 D3 不會放行店面 origin，請求根本不會送出。另外 refresh 回應本身只有同源 JS 讀得到。
- 跨源（本機開發）時 `SameSite=Strict` 的 cookie 不會被帶上，所以 admin SPA 只在 `API_BASE` 與 `location.origin` 同源時使用 cookie 模式，否則沿用 body 模式（現行行為）。**不帶 `refresh_transport` 時 API 行為逐位元組不變**——既有消費者（e2e、ONDO 不使用後台）不受影響。
- 為什麼不全面換 cookie session：access token 是無狀態 JWT 且已被 RBAC、`sids`、e2e 全面使用；只把**壽命長**的那一枚移出 JS 可及範圍，風險降低最多而改動最少。access token 被 XSS 讀到的窗口是 15 分鐘且不可續期。
- CSP（`admin/nginx.conf`）：`default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; connect-src 'self' <R2 presign origin>; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`——XSS 的主要防線是不讓它發生。

### D6：帳號層級的登入鎖定
新增第三條規則：鍵為 `HMAC(server_secret, normalized_email)`（不讓 Redis 鍵本身成為 email 清單），**只計失敗**，15 分鐘內 10 次失敗（`RATE_LIMIT_LOGIN_ACCOUNT_FAILURES`／`_WINDOW`）→ 之後該 email 的登入一律 429 直到視窗滑出。不存在的 email 一樣計數、一樣鎖——行為不因帳號是否存在而不同。成功登入刪除計數。fail-open。

需要新的限流原語：既有 `Limiter.Allow` 是「每次都計」，鎖定需要「先查、失敗才記」：新增 `Limiter.Peek(key, limit, window)` 與 `Limiter.Record(key, window)`、`Limiter.Reset(key)`；在 `AdminAuthHandler.Login` 內呼叫（中介層看不到結果）。

**DoS 取捨**：任何人都能以錯誤密碼把某位 owner 鎖 15 分鐘。在 D1 拓撲下，攻擊者必須先通過 Access（證明擁有某個信箱）才碰得到登入端點，所以這個 DoS 的成本是「每個嘗試者一個真實信箱」；而不鎖定的代價是分散式猜測無上限。日誌記 `admin_login_lockout` 與 email 雜湊，不記 email（PII 規則）。

### D7：API 不假設只有一個 admin 前端
已定案同時存在兩種前端：共用的 merchant admin SPA（一般商家，依商家換品牌）與 Bouquet 的自訂前端。原則：
- 伺服器上**沒有任何**以前端身分（User-Agent、特定 origin 名稱）分支的程式；差異只來自資料（該 origin 登記給哪家店、哪種用途）與請求本身（`refresh_transport`）。
- 自訂前端可以同源（tunnel 依 path 分流，與共用 SPA 相同拓撲）或跨源（另行託管，登記為 admin origin 後走 D3 的 CORS，refresh 用 body 模式）——兩者都受支援，前者建議。
- 前端必須實作的行為寫成一份契約文件 `docs/admin-frontend-integration.md`（活文件，比照 `docs/storefront-integration.md`），而不是散落在共用 SPA 的程式碼裡：連結 fragment 路由（`#/set-password/{token}`、`#/accept-invite/{token}`）、token 儲存（D5）、上傳流程與 HEIC 轉檔（`product-images-r2` D10）、Access session 過期的重導處理、`GET /admin/shops/{id}/me` 作為可見性來源。

### D8：per-shop 品牌是資料，由共用 SPA 套用
`shops.admin_branding`（jsonb，DEFAULT `'{}'`）：`logo_url`（媒體已設定時必須是影像網域，與 CMS 圖片欄位同一個驗證函式）、`primary_color`／`accent_color`（`#RRGGBB`）。寫入沿用既有 `PUT /admin/shops/{shopID}/settings`（`shop.update`，additive 欄位；未帶 = 不變）——不新增節點，因為品牌與時區同屬「商家設定」。讀取：
- `GET /api/v1/admin/shops/{shopID}/branding`（`RequireShopAccess`）：登入後切換商家時用。
- `GET /api/v1/admin/branding`（**未認證**）：以 D4 的主機解析找出商家，回傳品牌與商家名稱；主機不是已登記的 admin 主機時 404。共用 SPA 在登入頁就套用。洩漏面：任何人知道某 admin 網域就能看到它的商家名稱與 logo——那本來就是該網域對外展示的東西。
共用 SPA 以 CSS 變數套用顏色；Bouquet 的自訂前端可以完全忽略此端點。

### D9：連結基底依商家的 admin origin
`account.LinkDelivery` 的連結基底：連結帶有商家上下文（邀請、開店時的 owner 設定）且該商家有 `purpose=admin` 的登記 → 用 id 最小的那一筆；否則用 `ADMIN_PUBLIC_URL`（共用 merchant admin 網域）。平台 admin 網域永遠不作為商家連結的基底（它的 Access 只放行平台擁有者）。

### D10：`PLATFORM_ADMIN_HOSTS`
共用 merchant admin 網域可能不屬於任何一家店（多店共用一個網域時），D4 的主機綁定對它不生效，平台層路徑在那裡只剩 RBAC 一道防線。設定 `PLATFORM_ADMIN_HOSTS` 後，平台層後台路徑（D3 表格第三列）的請求主機（`Origin` 優先、否則 `Host`）不在清單內 → 403。未設定時不生效（本機開發）；`APP_ENV=production` 未設定時記 WARN。與 Access policy 合起來：平台層 API 只能從「只有平台擁有者過得了 Access」的網域呼叫。

## Risks / Trade-offs

- [tunnel 規則在 repo 外] → D2 的 JWT 驗證；runbook 列出驗證步驟（從公開 API 網域打 `/api/v1/admin/me` 必須 404）。
- [Access session 過期時 XHR 收到 Access 的 302/登入頁] → admin `api.js` 偵測非 JSON 回應或跨網域重導 → 整頁重新載入以走 Access 登入。
- [storefront origin 失去後台 CORS] → 行為收緊；沒有合法消費者從店面呼叫後台 API（已查 docs 與 e2e）。
- [JWKS 抓不到 → 後台全停] → fail closed 是刻意的；快取 1 小時降低對 Cloudflare 端點的依賴。
- [主機綁定讓平台管理員在商家 admin 網域上無法做平台操作] → 刻意；平台操作走平台 admin 網域。
- [cookie 模式下 refresh 端點速率限制仍以 IP 計] → 不變；cookie 不改變攻擊面。

## Migration Plan

1. 部署 API（`purpose` migration 預設 `storefront`；所有新行為在未登記 admin origin、未設定 Access 時等同現狀，除了「storefront origin 在後台路徑不再有 CORS」）。
2. 平台在後台把 `https://admin.bouquet.ksdevworks.online` 登記為 bouquet 的 `admin` origin。
3. 建 Cloudflare Access 應用（涵蓋所有 admin 網域）與 policy；取得 AUD。
4. 依範例更新 tunnel ingress（使用者手動套用）；設定 `CF_ACCESS_TEAM_DOMAIN`／`CF_ACCESS_AUD` 並重啟 API。
5. 平台 admin 網域建立**另一個** Access 應用，policy 只放行平台擁有者的信箱（在 Cloudflare 後台填入，不進 repo）；設定 `PLATFORM_ADMIN_HOSTS`。
6. 驗證：公開 API 網域的 `/api/v1/admin/me` 404；admin 網域未過 Access 被導向登入；過 Access 後可登入；從 admin.bouquet 呼叫其他店 403；從商家 admin 網域呼叫平台層路徑 403。
7. 回滾：移除 tunnel 規則即回到現狀。

## Decided（原 Open Questions，已由使用者定案）

- 商家 admin 網域的 Access policy：任何能以 email 一次性驗證碼證明信箱的人；授權交給 RBAC。
- `CF_ACCESS_BIND_EMAIL` 預設關閉。
- 平台 admin 網域：Access 只放行平台擁有者信箱（runbook，不寫進程式）；伺服器端以 `PLATFORM_ADMIN_HOSTS` 對應（D10）。
- admin 前端：共用 merchant admin SPA（per-shop 品牌）與 Bouquet 自訂前端並存；API 不假設單一前端（D7–D9）。
- 鎖定門檻：15 分鐘內 10 次失敗（依建議）。

## Implementation notes（實作時定案，與 proposal 的差異）

- **鎖定 HMAC 的祕密（tasks 5.2 二擇一）**：不新增 `LOGIN_LOCKOUT_KEY_SECRET`；以 `HMAC(AdminJWTSecret, "login-lockout-key-v1")` 衍生鎖定金鑰，再 `HMAC(key, "admin_login_lockout:"+email)` 成為計數鍵。輪替 admin JWT secret 的副作用只是重置計數。
- **`CF_ACCESS_AUD` 可逗號分隔多個值**（proposal 寫單一值）：決議的拓撲有**兩個** Access 應用（商家網域、平台網域），各有自己的 AUD；單一值會讓其中一個應用的所有後台請求被 API 以 403 拒絕。`aud` 含其中任一即通過。
- **`PUT /settings` 的欄位語意**：原本「`timezone` 省略＝清除」在長出第二個欄位後不再安全（只更新品牌會洗掉時區）。規則：`timezone` 鍵缺席**且** `admin_branding` 存在 → 時區不動；兩個鍵都沒有（空 body）→ 維持歷史語意（清時區），所以既有呼叫端行為不變。`admin_branding` 是**整個物件取代**（`null`／`{}` 清除全部，空字串清除單鍵）。
- **主機綁定的 `Host` 只組成 `https://{host}` 查詢**（依 tasks 3.1）：登記為 `http://` 的 admin origin 只能經 `Origin` 標頭被辨識（本機跨源開發的情形），經 `Host` 的同源判定只適用 https（正式拓撲）。
- **JWKS 未知 `kid` 的重抓**：最多每 10 秒一次（proposal：「重抓一次」），避免不受信任的標頭變成對 Cloudflare 的外送請求放大器。
- **`GET /admin/branding`（未認證）沒有限流**：每次請求只在 Origin／Host 已登記時才多一次主鍵查詢，未登記主機只花一次 Redis 查詢（含負快取）；回應內容本來就是該網域登入頁公開的東西。若日後觀察到濫用，補一條 IP 規則即可。
- **e2e 沒有測帳號鎖定**：e2e 的所有請求來自同一個 IP，既有的「IP＋email」規則（5 次／5 分鐘）會先於帳號規則回 429，無法區分兩者；鎖定由整合測試（分散 IP）覆蓋。
