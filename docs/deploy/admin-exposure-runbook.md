# 後台 API 對外開放 Runbook

對應 change `admin-api-public-exposure`。這份文件列出**由你手動套用**的步驟：Cloudflare Access、tunnel
ingress、環境變數、資料庫 migration，以及驗證與回滾。repo 裡的程式與範例檔**不會**去改任何 Cloudflare
設定、DNS、Access policy，也不會動你 repo 外的 tunnel 設定檔。

> **信箱不進 repo。** 下面所有「放行誰」的信箱都只填在 Cloudflare 後台的 Access policy 裡。
> 文件與範例檔一律用 `example.com` 佔位。

## 拓撲（同源 ＋ Access）

```
瀏覽器 ──► admin.shop-a.example.com ──(Cloudflare Access)──► tunnel
                                            ├─ ^/api/v1/admin → API   127.0.0.1:8080
                                            └─ 其餘           → 靜態檔 127.0.0.1:3001
公開 API 網域 api.example.com：^/api/v1/admin 維持 404（現行規則，不動）
```

頁面與 API 同源 → 正式環境不需要 CORS，refresh token 可放 HttpOnly cookie。範例 ingress：
`docs/deploy/cloudflared-admin.example.yml`。

## 0. 前置（在動 Cloudflare 之前）

1. 部署新版 API（含 migration，見第 1 步）。新版在**沒登記 admin origin、沒設 Access** 時，行為等同現狀，
   唯一差別：店面（storefront）origin 在 `/api/v1/admin/*` 上不再有 CORS。
2. 確認 `cms-rich-text-sanitization` 已上線（對外開放前必要條件）。

## 1. 主 DB migration（由你執行，我沒有動主 DB）

本 change 新增 migration `20261124075556_add_allowed_origin_purpose`：

- `shop_allowed_origins.purpose varchar(20) NOT NULL DEFAULT 'storefront'`（CHECK `IN ('storefront','admin')`；既有列＝storefront）
- `shops.admin_branding jsonb NOT NULL DEFAULT '{}'`

```bash
make migrate        # 在主 DB（DATABASE_URL 指向 ecommerce）
```

回滾：`make migrate-down`（只丟這兩個欄位；已登記為 admin 的 origin 會因此消失，記得先記下清單）。
**不需要 re-seed。**

## 2. 登記各商家的 admin origin

平台管理員（`shop.manage_client_origins`）對每家商家登記它的 admin 網域，**用途為 `admin`**：

```
POST /api/v1/admin/shops/{shopID}/allowed-origins
{ "origin": "https://admin.shop-a.example.com", "purpose": "admin" }
```

或在後台「前端接入」頁選「後台」。登記後：

- 該網域的請求在伺服器端被**綁定**在這家商家（對其他商家與平台層路徑一律 403）。
- 該商家的邀請／設定連結的基底改用這個網域（有多筆取 id 最小者）。
- 共用 merchant admin 網域（`ADMIN_PUBLIC_URL`）**不要**登記在任何商家名下。
- 平台自己的 admin 網域也**不要**登記（它放在 `CORS_ALLOWED_ORIGINS` 與 `PLATFORM_ADMIN_HOSTS`）。

## 3. Cloudflare Access（在 Cloudflare Zero Trust 後台手動建立）

建立**兩個** Access 應用（Self-hosted）：

### 3a. 商家 admin 網域（一個應用，涵蓋所有商家 admin 網域與共用 merchant admin 網域）

- Application domains：`admin.shop-a.example.com`、`merchant.example.com`、…（每個商家 admin 網域一條，路徑留空＝整站）
- Login methods：只勾 **One-time PIN**（不要啟用其他 IdP）。
- Policy（Action: Allow）：Include → **Everyone**。因為唯一的登入方式是一次性驗證碼，所以等於
  「任何能以 email 一次性驗證碼證明信箱的人」通過。Access 在這裡只是機器人／撞庫屏障與免費的 email 驗證；
  **授權完全交給應用層 RBAC**。
- 儲存後在應用的 Overview 取得 **Application Audience (AUD) Tag**，記為 `AUD_MERCHANT`。

### 3b. 平台 admin 網域（另一個應用）

- Application domain：`platform-admin.example.com`
- Policy（Action: Allow）：Include → **Emails** → 填**平台擁有者的信箱**（只在這裡填，不要寫進任何檔案或 repo）。
- 取得它的 AUD Tag，記為 `AUD_PLATFORM`。

> 平台擁有者的 Access policy 只放行本人，所以 `ADMIN_PUBLIC_URL` 必須指向**共用 merchant admin 網域**，
> 不能是平台 admin 網域（否則受邀的商家 owner 點連結會被擋在 Access 外）。

### 3c. 團隊網域

Zero Trust → Settings → Custom Pages 附近可看到你的 team domain：`<team>.cloudflareaccess.com`。

## 4. tunnel ingress（你的 tunnel 設定檔，repo 外）

把 `docs/deploy/cloudflared-admin.example.yml` 的規則**手動合併**進你的 `~/.cloudflared/<tunnel>.yml`
（我沒有動它）：

- 每個 admin 網域兩條規則：`path: ^/api/v1/admin` → API；其餘 → 靜態檔。**path 規則要排在同 hostname 的萬用規則之前**。
- 公開 API 網域那條 `^/api/v1/admin → http_status:404` **保持不變**。
- 在 Cloudflare DNS 為每個 admin 網域建立指向 tunnel 的 CNAME（`cloudflared tunnel route dns <tunnel> <hostname>` 或後台操作）。
- 重啟 cloudflared。

admin 靜態站要由某個程序提供在 `127.0.0.1:3001`（`make admin-dev` 或用 `admin/Dockerfile` 的 nginx，後者帶 CSP）。

## 5. API 環境變數

| 變數 | 值 | 說明 |
|---|---|---|
| `CF_ACCESS_TEAM_DOMAIN` | `<team>.cloudflareaccess.com` | 與下一項**必須一起設定**，否則拒絕啟動 |
| `CF_ACCESS_AUD` | `AUD_MERCHANT,AUD_PLATFORM` | 逗號分隔；JWT 的 `aud` 含其中任一即通過 |
| `CF_ACCESS_BIND_EMAIL` | 預設關閉 | `true` 時 Access JWT 的 email 必須等於後台帳號 email。商家網域是「任何人都能過 Access」，所以預設不開 |
| `PLATFORM_ADMIN_HOSTS` | `platform-admin.example.com` | 逗號分隔 `host[:port]`；平台層後台 API 只能從這些主機呼叫 |
| `ADMIN_PUBLIC_URL` | `https://merchant.example.com` | 共用 merchant admin 網域（沒有登記 admin origin 的商家用它當連結基底） |
| `TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | 既有；cloudflared 在本機時用於取得客戶端 IP（鎖定與限流依賴它） |
| `RATE_LIMIT_LOGIN_ACCOUNT_FAILURES` / `_WINDOW` | 預設 `10` / `15m` | 帳號層級登入鎖定（選用調整） |

設定後重啟 API。`APP_ENV=production` 且沒設 `CF_ACCESS_*` 或 `PLATFORM_ADMIN_HOSTS` 時，啟動日誌會各有一行 WARN。

**Access JWT 驗證是 fail closed**：抓不到 Cloudflare 的 JWKS（`https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`）
時，所有後台請求回 403（金鑰快取 1 小時）。這是刻意的。

## 6. 驗證（五項，缺一不可）

1. **公開 API 網域擋後台**：`curl -i https://api.example.com/api/v1/admin/me` → **404**。
2. **未過 Access 被導向登入**：在無痕視窗開 `https://admin.shop-a.example.com/` → 被導向 Cloudflare Access 的登入頁。
   `curl -i https://admin.shop-a.example.com/api/v1/admin/me`（沒有 Access cookie）→ 被 Access 擋下（302／403），不會到 API。
3. **過 Access 後可登入**：通過一次性驗證碼 → 出現後台登入頁 → 以商家帳號登入成功；開 DevTools 確認
   `__Secure-admin_refresh` cookie 是 HttpOnly，且 localStorage 沒有任何 token。
4. **從商家 admin 網域打其他商家 → 403**：以同時是兩家商家成員的帳號登入 `admin.shop-a…`，
   在 console 對 `/api/v1/admin/shops/{另一家 id}/me` 發 `fetch` → **403**。
5. **從商家 admin 網域打平台層 → 403**：同一個 session 對 `/api/v1/admin/accounts` 發 `fetch` → **403**；
   而平台擁有者從 `platform-admin.example.com` 呼叫同一條 → 依 RBAC（200）。

另外確認：`GET https://admin.shop-a.example.com/api/v1/admin/branding` → 200 與該商家名稱；
在未登記的網域（例如共用 merchant 網域）→ 404（前端用預設外觀）。

## 7. 回滾

- 最快：**移除 tunnel 裡 admin 網域的 `^/api/v1/admin` 規則**（或整個 admin 網域）→ 回到現狀（公開網域仍 404、後台只在本機）。
- 關掉 API 端驗證：清空 `CF_ACCESS_TEAM_DOMAIN`／`CF_ACCESS_AUD` 後重啟。
- migration 回滾見第 1 步。
