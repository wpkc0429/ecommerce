## Why

即將接入的第一個外部前端（ONDO 溫度花室）是商家自行撰寫、零建置流程、部署於自家網域的靜態站，**由瀏覽器直接跨源呼叫本 API**。它不使用 CMS／主題渲染管線，只當 API 客戶。這種「自帶前端」交付模式已確認是產品能力（第一個，但不是唯一一個），而現行架構的兩個假設在它身上同時失效：

1. **租戶宣告靠一個未登記、不可撤銷的 header。** `X-Site-Domain` 優先於 `Host`。自家 SSR 下這沒問題——header 由平台伺服器注入、瀏覽器碰不到。靜態站則只能把它硬編碼在前端 JS 裡，任何人用 DevTools 就能改成別家商家的網域。目前後果有限（讀到別家的公開商品），但 `guest-checkout` 一旦上線，同一條路徑就能替別家商家建立訂單。更根本的問題是：平台無法辨識、限流或切斷任何一個具體的前端部署——所有客戶端都以同一種、無法驗證也無法撤銷的方式宣告身分。
2. **CORS 是全域扁平清單。** `CORS_ALLOWED_ORIGINS` 環境變數 → 靜態 map，掛在 router 最外層。每接一家自帶前端的商家就要改環境變數並重啟 API；SaaS 規模下不可行。

破壞性變更的窗口正在關閉：v1 目前的消費端僅有本 monorepo 內同步部署的 `admin`／`web`／`e2e`，ONDO 尚未開始接入（已再次確認 `bouquet/public/app.js` 仍為硬編碼資料、全樹零 API 呼叫）。ONDO 接入後此條件即消失。

## What Changes

- 新增 **publishable key**（`pk_` 前綴的公開識別碼）：宣告「這個前端代表哪家商家」。它是**識別碼，不是憑證**——公開於前端 JS 原始碼、明文儲存並建索引、洩漏本身不構成資安事件。存取控制仍全數由既有機制承擔（會員 JWT、RBAC、CORS、速率限制、端點自身的授權規則）。
- 新增每商家 **allowed origins 登記**：CORS 的允許集合由「環境變數平台清單」擴充為「環境變數平台清單 ∪ 全部已登記的商家 origin」，登記變更即時生效，不需改環境變數或重啟。
- **BREAKING** `/api/v1/shop/*`（會員認證、公開商品、購物車、訂單、金物流、點數）的租戶解析改為：`X-Publishable-Key` 優先，否則 `Host`。**`X-Site-Domain` 與 `X-Site-Path` 不再被讀取**——留著它們等於留下一條未登記、不可撤銷的等效通道，會使金鑰撤銷失去意義（撤銷後改用 header 即可繼續）。原生 APP 通道改用 publishable key，能力嚴格更強（可登記、可撤銷、可歸因）。
- `X-Site-Domain` **僅保留於 `/api/v1/render/page`**：該端點的呼叫方是第一方 SSR 伺服器中繼訪客的 Host，回傳內容為已發佈的公開內容，偽造所得等同直接造訪對方網站，無任何額外取得。
- 新增後台金鑰管理 API（產生／列出／撤銷，商家層 RBAC）與平台層 allowed origin 管理 API，並新增對應權限節點。
- 資料層新增 `publishable_keys` 與 `shop_allowed_origins` 兩張租戶擁有表；金鑰解析與 origin 查詢皆走 Redis 快取 + 領域事件失效（比照既有 `route:{host}` 模式）。

不在本次範圍內（明確記錄，非遺漏）：

- **不做 test／live 環境分離**（`pk_test_` / `pk_live_`）。平台目前不存在可分離的 sandbox 模式（單一資料庫、mock 金流是正式佈線的參考實作），標示 `pk_test_` 的金鑰行為與 `pk_live_` 完全相同——一個承諾了自己做不到的隔離的標籤，比沒有標籤更糟。金鑰格式保留該命名空間（見 design D8）。
- **不做 wildcard origin**（`*.example.com`）。
- **不新增速率限制維度**：per-key 配額等於讓商家自助擴額（多產生幾把金鑰即可），現行 shop-scoped 規則已正確涵蓋。
- **不把 publishable key 當成認證機制**：不簽章、不雜湊儲存、不設有效期、不參與任何授權判定。

## Capabilities

### New Capabilities
- `storefront-client-auth`: 自帶前端的商家客戶端如何向 API 宣告其代表的商家、平台如何登記與撤銷這些客戶端、以及瀏覽器跨源存取（CORS）如何以每商家粒度授予。此能力橫跨租戶解析與傳輸層授權，現行 specs 中無任何一份擁有它。

### Modified Capabilities
- `multi-tenancy`: 「Host-based tenant resolution」的解析輸入改變——前台 API 群組改以 publishable key 優先、`Host` 回退，`X-Site-Domain`／`X-Site-Path` 由該群組移除並限縮至渲染 bundle 端點。
- `authentication`: 「Member registration and login in shop context」中描述商家上下文取得方式的括號（`Host` 解析）不再準確，改為指向 multi-tenancy 的解析規則；註冊／登入的行為、回應形狀與速率限制要求皆不變。

## Impact

**Schema / migration**
- `api/internal/ent/schema/clientauth.go`（新檔）：`PublishableKey`（`shop_id`、`key` 唯一、`label`、`created_at`、`revoked_at`）與 `ShopAllowedOrigin`（`shop_id`、`origin` 全域唯一、`created_at`）。
- `api/migrations/`：新增 versioned migration（純新增兩張表，無資料搬移）。

**Go packages**
- `api/internal/tenant`：`Resolver` 新增金鑰解析與 origin 查詢（含 Redis 快取與失效），新增 `NormalizeOrigin`。
- `api/internal/httpapi/tenantmw.go`：解析順序改寫（key → Host），`X-Site-Domain`／`X-Site-Path` 移除。
- `api/internal/httpapi/middleware.go`：CORS 中介層接受 origin registry，`Access-Control-Allow-Headers` 以 `X-Publishable-Key` 取代 `X-Site-Domain, X-Site-Path`。
- `api/internal/httpapi/clientauth.go`（新檔）：金鑰與 origin 的後台 API。
- `api/internal/httpapi/router.go`：CORS 中介層改為無條件掛載（登記的 origin 不再需要環境變數存在）；新路由掛載。
- `api/internal/events`、`api/internal/app/wire.go`：新增 `StorefrontClientChanged` 事件與其失效訂閱。
- `api/internal/seed`：新增權限節點並授予對應角色。

**API 契約**
- 前台：新增 `X-Publishable-Key` 請求 header；`X-Site-Domain`／`X-Site-Path` 於 `/api/v1/shop/*` 失效（破壞性，見 design D6/D10）。
- 後台：新增 `/api/v1/admin/shops/{shopID}/publishable-keys`（GET/POST/DELETE）與 `/api/v1/admin/shops/{shopID}/allowed-origins`（GET/POST/DELETE）。皆為新增，不動既有端點。

**下游相依**
- `guest-checkout` 的前置：訪客結帳是第一個「未認證卻會寫入」的端點，其租戶宣告必須是可撤銷、可歸因的。
- ONDO 接入面：前端需帶 `X-Publishable-Key`，其 origin 需由平台登記。本變更完成後 ONDO 即可開始接入，屆時 v1 的破壞性變更窗口關閉。
- 倉庫內消費端：`web` 僅呼叫 `/api/v1/render/{page,preview}`、`admin` 僅呼叫 `/api/v1/admin/*`、`e2e` 僅使用 `X-Site-Domain` 於渲染端點——三者皆不受破壞性面向影響。
