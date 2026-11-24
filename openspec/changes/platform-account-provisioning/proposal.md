## Why

今天後台帳號**只能由 seed 建立**：`users` 沒有任何建立端點，`RolesHandler.assignShopRole` 要求目標使用者已經有 `shop_user` 成員資格，而 `shop_user` 也沒有任何寫入端點。開一家新店的實際步驟是：`POST /admin/shops` → `POST /admin/sites` → 綁網域 → 登記 origin → **改 seed 或手寫 SQL** 建立 owner 帳號。平台管理員無法停用帳號、無法重設密碼。

這擋住了上線第二家商家，也擋住後續 phase 的「商家經平台網站申請開店」——審核通過之後需要一個可以被程式呼叫的「開店」動作。

另外查證發現一個既有缺陷：**停用帳號（`users.status = 0`）對已簽發的 access token 沒有作用**——`rbac.Engine.load` 不讀 `users.status`，被停用的人在 access token 到期前（預設 15 分鐘）仍可操作所有後台 API；refresh 才會被擋。

## What Changes

- **平台帳號管理 API**（新權限節點 `account.view`／`account.manage`，平台層，只有 `super_admin` 持有）：
  - `GET /api/v1/admin/accounts`（搜尋 email、分頁）、`GET /api/v1/admin/accounts/{userID}`（含成員資格與角色）
  - `POST /api/v1/admin/accounts`：建立帳號（**不設密碼**），可選擇同時指派某商家的商家層角色；回傳一次性的**密碼設定連結**
  - `POST /api/v1/admin/accounts/{userID}/memberships`、`DELETE .../memberships/{shopID}`：加入／移出商家並指派角色（補上 `shop_user` 的第一個寫入端點）
  - `POST /api/v1/admin/accounts/{userID}/disable`、`/enable`
  - `POST /api/v1/admin/accounts/{userID}/password-reset`：產生一次性重設連結
- **密碼設定連結取代「管理員代填密碼」**：`users.password_hash` 改為可為 NULL（NULL＝尚未設定、無法登入，與 `members` 的既有慣例同一種表達）。新增 `admin_credential_tokens`（只存 SHA-256 雜湊、單次使用、設定連結 72h／重設連結 24h）。未認證端點 `POST /api/v1/admin/auth/password-setup/inspect` 與 `POST /api/v1/admin/auth/password-setup`（皆限流）。連結形如 `{ADMIN_PUBLIC_URL}/#/set-password/{token}`（token 在 fragment，不進伺服器日誌與 Referer）。
- **沒有寄信能力時的交付方式**：回應帶 `delivery: "manual"` 與 `setup_url`，後台顯示一次並提供複製；API 端定義 `AccountLinkDelivery` 介面，日後接上信件時改為 `delivery: "email"` 且**不再回傳連結**——呼叫端以 `delivery` 判斷，不以欄位是否存在判斷。
- **停用立即生效**：停用時撤銷該使用者全部 refresh token、清除 RBAC 快取；`rbac.Engine` 讀取 `users.status`，停用者的快照拒絕一切（含平台角色），所以**既有 access token 在下一個請求就被 403**；`GET /admin/me` 對停用者回 401。不可停用自己；不可停用最後一位啟用中的 `super_admin`。
- **開店流程**：`POST /api/v1/admin/shop-provisioning`——單一交易內建立商家（含既有的自動首頁）、套用主題、建立或綁定網域、建立或指派 owner（`merchant_owner`）、可選登記 storefront origin；回傳商家、綁定結果與 owner 的設定連結。核心在新套件 `internal/provision`，供日後「申請開店審核通過」直接呼叫。另新增 `GET /api/v1/admin/shops/{shopID}/setup-status`（平台 `shop.view`）：由伺服器推導的開店檢查清單。
- **後台 UI**：新增平台視圖「商家」（列表、開店精靈、檢查清單）與「帳號」（列表、建立、停用／啟用、重設連結、成員資格）；新增未登入可進入的 `#/set-password/{token}` 頁。`GET /api/v1/admin/me` additive 新增 `platform_permissions`，作為平台視圖的可見性來源。

## Capabilities

### New Capabilities
- `account-provisioning`：後台帳號的建立、成員資格與角色指派、停用／啟用、密碼設定／重設連結與其交付方式、開店流程與檢查清單。
- `admin-platform-ui`：平台管理員的後台視圖與密碼設定頁。

### Modified Capabilities
- `authentication`：停用帳號的拒絕範圍擴及既有 access token；未設定密碼的帳號無法登入；新增密碼設定端點與其限流。
- `rbac`：授權快照納入帳號啟用狀態。

## Impact

- **資料**：`users.password_hash` DROP NOT NULL；新表 `admin_credential_tokens`。
- **程式碼**：`api/internal/ent/schema/account.go`、新 `api/internal/ent/schema/credentialtoken.go`、新 `api/internal/account/`（service）、新 `api/internal/provision/`、`api/internal/httpapi/accounts.go`／`provisioning.go`／`adminauth.go`／`router.go`／`ratelimitmw.go`、`api/internal/rbac/engine.go`／`decide.go`、`api/internal/auth/refresh.go`（撤銷全部）、`api/internal/config/config.go`（`ADMIN_PUBLIC_URL`、token TTL）、`api/internal/seed/seed.go`（新節點）、`admin/app.js`、新 `admin/views/platform-shops.js`／`platform-accounts.js`／`set-password.js`、`admin/index.html`。
- **API 契約**：新增約 13 條路由；`GET /admin/me` 新增 `platform_permissions`；`v1-surface.txt` 只新增行。
- **相依**：無硬性相依；`merchant-staff-role` 依賴本 change 的 token 與帳號服務。
