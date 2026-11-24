## Context

- `users`：`email` 全域唯一、`password_hash` NOT NULL、`status`（0/1）。`shop_user`＝成員資格、`role_user`（`shop_id` NULL＝平台角色）。
- `rbac.Engine.load` 由 `user_permission`／`role_user`／`shop_user` 推導快照，**不讀 `users`**；快取 `authz:user:{id}:shop:{shop_id}` 10 分鐘，`InvalidateUsers` 清除。
- `AdminAuthHandler.Login` 只在登入時檢查 `status`；`RefreshService.RotateUser` 要求 `status=1`；access token 15 分鐘、無狀態。
- `cms.Service.CreateShop` 建立商家並在同一交易建首頁；網域綁定在 `SitesHandler`（平台 `shop.manage_domains`）；storefront origin 在 `ClientAuthHandler`（平台 `shop.manage_client_origins`）。
- 寄信：`internal/notify` 只服務訂單事件，`SMTP_*` 未設定＝`DiscardSender`，而且正式環境目前就是未設定。
- 後續 phase：商家經平台網站申請開店（需要可被程式呼叫的開店動作）。

## Goals / Non-Goals

**Goals:** 平台管理員能在後台完成開店與指派 owner，不碰 seed／SQL；帳號可停用、可重設密碼；停用立即生效；密碼永遠由本人設定。

**Non-Goals:**
- 寄信（只定義交付介面，見 D4；信件服務於搬 VPS 時接上）。
- 商家自行管理成員（change `merchant-staff-role`）。
- MFA、密碼強度評分、密碼到期。
- 刪除帳號（停用即可；刪除牽涉審計與外鍵，另議）。
- 開店申請表單與審核（後續 phase）。

## Decisions

### D1：密碼設定連結，而不是管理員代填密碼
代填密碼的問題：管理員知道密碼（之後任何該帳號的操作都無法歸責）、密碼經由聊天軟體傳遞、使用者通常不會改。連結的性質：單次使用、短期、本人設定、管理員從未看到密碼。沒有寄信能力時，管理員仍然要以帶外方式交付**連結**——差別是連結會過期、用過即失效，而密碼不會。

`users.password_hash` 改為可為 NULL，NULL＝「尚未設定」＝無法登入。這與 `members.password_hash IS NULL`（訪客身分）是同一種表達，不另造 `status=2` 或隨機雜湊的「魔術值」——後者會讓「這個帳號有沒有密碼」變成需要知道慣例才答得出來的問題。登入時遇到 NULL 走 `EqualizeVerifyTiming()`，與「帳號不存在」同時序、同回應。

### D2：Token 表
`admin_credential_tokens`：`id`、`user_id`（FK）、`kind`（`setup`／`reset`，CHECK）、`token_hash`（SHA-256，unique）、`created_by`（user id，nullable：系統產生時為 NULL）、`expires_at`、`used_at`、`created_at`。
- 明文 token 是 32 bytes `crypto/rand`、base64url，只出現在回應的連結中一次；DB 只存雜湊（同 refresh token 的既有做法）。
- 單次使用：`UPDATE ... SET used_at = now() WHERE token_hash = ? AND used_at IS NULL AND expires_at > now()` ＋ 影響列數；設定密碼與消費 token 同一交易。
- 對同一使用者發出新的同類 token 時，舊的未使用 token 一併失效（`used_at` 設為 now，標註 superseded），所以「重發連結」不會留下多把有效鑰匙。
- 設定密碼成功後撤銷該使用者全部 refresh token（重設密碼的意義之一就是踢掉既有 session）。
- 此表不含 `shop_id`，不涉 `tenantOwned` 分類。（`merchant-staff-role` 會為邀請加上 `shop_id`／`role_id`，屆時依 `notTenantOwned` 的規則分類，並**手寫** CHECK 變更——atlas 以名稱比對 CHECK，改運算式它不會發現。）

### D3：連結形狀與 `ADMIN_PUBLIC_URL`
`{ADMIN_PUBLIC_URL}/#/set-password/{token}`。token 放在 fragment：瀏覽器不會把 fragment 送到 nginx、Cloudflare 或任何 Referer，所以它不會出現在任何存取日誌。admin 的 hash router 已是 `#/<view>/<param>` 形狀。`ADMIN_PUBLIC_URL` 是平台層設定（預設 `http://localhost:3001`），代表**共用的 merchant admin SPA** 的網域，**不是平台管理員自己的 admin 網域**——後者的 Cloudflare Access policy 只放行平台擁有者的信箱（`admin-api-public-exposure` D2），商家 owner 打開連結會被 Access 擋下。`admin-api-public-exposure` 會再細化為「該商家有登記 admin origin 時，連結以該 origin 為基底」，讓 Bouquet 的自訂前端收到指向自己網域的連結。

**連結形狀是一份給所有 admin 前端的契約**，不是共用 SPA 的內部路由：任何 admin 前端（共用 SPA、Bouquet 自訂前端）MUST 在其根路徑處理 `#/set-password/{token}`（以及 `merchant-staff-role` 的 `#/accept-invite/{token}`），寫進 `docs/admin-frontend-integration.md`。API 端不知道、也不假設是哪一個前端在處理它。

### D4：交付介面，今天只有 manual
`account.LinkDelivery` 介面：`Deliver(ctx, LinkMessage) (Delivery, error)`。今天唯一的實作是 `ManualDelivery`：回傳 `delivery: "manual"`，API 回應帶 `setup_url`／`reset_url` 與 `expires_at`。日後的信件實作回傳 `delivery: "email"`，回應**不帶連結**（管理員不需要也不該看到別人的重設連結）。契約上 `setup_url` 是選填、`delivery` 是必填 enum——消費端 MUST 先看 `delivery`，這與 ECPay 的 `redirect_form` 先於 `redirect_url` 是同一種規則。

選擇不在本 change 接上 `SMTP`：`internal/notify` 是訂單事件的下游，寄件人是平台 Gmail，且 order-notification-email D3 已量化 Gmail 的額度天花板；帳號信件的寄件身分、模板與退信處理是獨立的設計問題。

### D5：停用立即生效，做在 RBAC 快照而不是 AdminMW
`rbac.Engine.load` 多讀一次 `users.status`（主鍵查詢），停用者的快照 `Disabled=true`：`Allows` 一律 false、`CanAccessShop` false、`HasPlatformRole` false。停用時：`status=0` → 撤銷全部 refresh token → `InvalidateUsers`。因此所有 `RequireShopPermission`／`RequireShopAccess`／`RequirePlatformPermission` 守衛的路由在下一個請求即回 403。

為什麼不做在 `AdminMW`（每個請求查一次 DB）：快照已有 Redis 快取與失效機制，放在這裡等於免費得到快取；AdminMW 加查詢會讓每個後台請求多一次 DB round-trip 或者需要第二套快取。殘留面：沒有 RBAC 守衛的已認證路由只有 `GET /admin/me`（改為讀到 `status=0` 回 401）與 `GET /admin/media/config`（無敏感資訊）——列入 Risk，並在 `wiring-integrity` 之外加一條整合測試列舉「已認證但無 RBAC 守衛」的路由清單，新增時轉紅要求人審視。

### D6：保護條款
- 不可停用自己（409）——避免平台管理員把自己鎖在外面。
- 不可停用最後一位啟用中且持有 `super_admin` 的帳號（409），以 `SELECT ... FOR UPDATE` 鎖住所有 super_admin 的 `users` 列後計數，避免兩人互相停用的競態。
- 不可移除最後一位 `super_admin` 的平台角色（既有的 `removePlatformRole` 一併補上同一檢查）。

### D7：建立帳號時 email 已存在 → 409
平台管理員是完全受信任的角色，告訴他「這個 email 已經有帳號」沒有洩漏問題，反而是必要資訊（應改用「加入成員資格」）。這與 `merchant-staff-role` 中商家 owner 邀請的處理（不得透露帳號是否存在於其他商家）刻意不同。

### D8：開店是一個交易、一個服務
`provision.Service.ProvisionShop(ctx, ProvisionInput)`：
1. `cms` 建商家 ＋ 首頁（重用 `CreateShop` 的交易內版本；需把它重構為接受 `*ent.Tx` 的內部函式，HTTP 版本呼叫它）；
2. 選填 `theme_id`；
3. 選填 `domain`／`path_prefix`：網域不存在則建立 site，存在則綁定（沿用 site-shop 約束；`is_primary` 預設 true）；
4. 必填 `owner_email`：不存在 → 建立無密碼帳號 ＋ setup token；存在 → 直接加入成員資格（平台受信任，D7）；兩者都指派 `merchant_owner`；
5. 選填 `storefront_origins`：登記 `shop_allowed_origins`；
6. commit 後發佈既有的 `SiteMappingChanged`／`StorefrontClientChanged` 事件（快取失效走事件，不直呼 Redis）。

任何一步失敗整筆回滾——不會留下沒有 owner 的商家。權限：`shop.create`，另外依輸入要求 `shop.manage_domains`／`shop.manage_client_origins`／`account.manage`（handler 內以平台快照判定；`super_admin` 全部持有）。既有的 `POST /admin/shops`、`/admin/sites` 等**保留不動**，精靈只是另一個入口。日後的「申請開店審核」直接呼叫 `ProvisionShop`。

`setup-status` 是**推導**不是儲存：`theme_applied`、`domain_bound`、`owner_assigned`、`owner_password_set`、`home_published`、`shipping_method_active`、`payment_credentials_set`、`publishable_key_active`——每一項是一個 `Exist()` 查詢。不存「開店進度」欄位，因為那會與事實漂移。

### D9：平台可見性來源是 `GET /admin/me` 的 `platform_permissions`
平台視圖不屬於任何商家，`/shops/{id}/me` 不適合回答「我能不能開店」。`/admin/me` additive 新增 `platform_permissions`（`PlatformCtx` 快照的決定集合，無平台角色時為 `[]`）。與 D5 同樣是體驗層，伺服器的 `RequirePlatformPermission` 仍是唯一權威。

### D10：新權限節點只有兩個
`account.view`（列表與詳情）與 `account.manage`（建立、成員資格、停用／啟用、重設）。不拆 `account.disable`／`account.reset`：今天只有 `super_admin` 會持有，拆開沒有消費者。

## Risks / Trade-offs

- [manual 交付時，管理員握有對方的重設連結＝可以設定對方密碼] → 平台管理員本來就能停用、指派任何角色，這不擴大其能力；連結 24h 過期、單次使用；`admin_credential_tokens.created_by` 留下是誰產生的紀錄。接上信件後此風險消失（D4）。
- [停用的殘留面：無 RBAC 守衛的已認證路由] → D5 的列舉測試。
- [`password_hash` 改 NULL 影響既有程式] → 全樹搜尋 `PasswordHash` 的讀取點（login、seed），ent 會把型別改成 `*string`，編譯器會列出全部使用處。
- [開店交易較大] → 全部是少量列的 INSERT，無外部呼叫；事件在 commit 後發佈。

## Migration Plan

1. migration：`ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL`；建立 `admin_credential_tokens`。
2. seed：新增 `account.view`／`account.manage`（`super_admin` 是 `*`，自動取得）。
3. 回滾：down migration 在存在 NULL `password_hash` 時會失敗——down 檔先刪除 `password_hash IS NULL` 的帳號是破壞性的，改為**拒絕回滾**並在檔頭註明手動處理步驟。

## Decided（原 Open Questions，已由使用者定案）

- 有效期：設定連結 72h、重設連結 24h（`merchant-staff-role` 的邀請 7 天）。
- 後台密碼最低 12 字元。
- 帳號信件服務於**搬到 VPS 時**接上；在那之前一律 `delivery: "manual"`。
- 設定密碼成功後**不**直接登入，導回登入頁並預填 email（少一條簽發 token 的路徑；依建議）。
