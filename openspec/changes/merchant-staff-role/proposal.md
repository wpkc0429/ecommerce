## Why

商家 owner 需要讓店員處理訂單與出貨，但不該讓店員改價、看報表、改金流憑證或調點數。今天的商家層角色只有 `merchant_owner` 與 `editor`，而 `editor` 能改商品、改頁面、調點數、看顧客名冊——遠超過「處理訂單」。owner 也沒有任何方法自己加人或停用人：成員資格只能由平台（change `platform-account-provisioning`）建立。

另外，出貨的「送達」與「退貨」今天由同一個節點 `shipment.update` 守著，而退貨會把整張訂單標成已退貨並**扣回整筆點數**（partial-shipment 的既有行為）——這是比「標記送達」嚴重得多的操作，不應該和它綁在一起授權。

## What Changes

- 新增商家層角色 **`staff`**：`order.view`、`payment.view`（看付款狀態與付款紀錄，處理「顧客說已付款」的詢問）、`shipment.view`、`shipment.create`、`shipment.update`（送達）。**不含**取消訂單（含未付款訂單）、退貨、商品、頁面、顧客名冊、點數、報表、金流憑證、成員管理——取消與退貨只有 owner（與既有 editor）能做。
- 新增權限節點 **`shipment.return`**：把出貨推進到「已退貨」除了 `shipment.update` 之外還需要它。seed 授予 `merchant_owner` 與 `editor`（既有 seed 角色的行為不變），**不授予 `staff`**（已定案）。
- 新增權限節點 **`user.manage_members`**（只給 `merchant_owner`）與 owner 端成員管理 API：
  - `POST /api/v1/admin/shops/{shopID}/invitations`：以 email 邀請並指定角色；回應不論該 email 是否已有帳號都相同，連結以 `delivery: "manual"` 交付（沿用 `platform-account-provisioning` 的機制）。
  - `GET .../invitations`（待接受）、`DELETE .../invitations/{invitationID}`（撤回）。
  - `POST .../users/{userID}/disable`、`/enable`：停用的是**這家店的成員資格**，不是全域帳號。
- 新增 `shop_user.status`：成員資格可被停用；停用的成員在該店的跨店守衛視同非成員，其他店不受影響。
- 新增 `roles.merchant_assignable`：商家層呼叫者（持 `user.manage_roles`／`user.manage_members`）**只能指派或邀請 `merchant_assignable = true` 的角色**（seed：`editor`、`staff`）。`merchant_owner` 的授予與移除改為只有平台能做。
- 邀請的接受：未有帳號的 email → 在接受時才建立帳號並設定密碼；已有帳號者 → 必須先登入、以該帳號接受（**邀請連結永遠不能替既有帳號設定密碼**）。
- 後台新增「成員」視圖（owner 可見）：成員列表、邀請、待接受邀請、停用／啟用、變更角色；`staff` 登入後只看得到訂單相關畫面，取消與退貨按鈕依權限隱藏。

## Capabilities

### New Capabilities
- `shop-team-management`：owner 端的成員邀請、接受、撤回、成員資格停用／啟用與可委派角色規則。

### Modified Capabilities
- `rbac`：新增 `staff` 角色與 `shipment.return`、`user.manage_members` 節點；成員資格狀態參與跨店守衛；商家層角色指派限於可委派角色。
- `shipping-logistics`：推進至已退貨需要 `shipment.return`。
- `admin-operations-ui`：成員視圖；以 staff 身分登入時的畫面範圍。

## Impact

- **資料**：`shop_user.status`（DEFAULT 1，零回填）、`roles.merchant_assignable`（DEFAULT false，seed 設定）；`admin_credential_tokens` 擴充邀請欄位（`email`、`shop_id`、`role_id`、`user_id` 改可為 NULL）＋ **手寫** CHECK 變更。
- **程式碼**：`api/internal/ent/schema/account.go`／`rbac.go`／`credentialtoken.go`、`api/internal/rbac/engine.go`、`api/internal/account/`（邀請）、新 `api/internal/httpapi/team.go`、`roles.go`（可委派檢查）、`shipping.go`（退貨二次檢查）、`adminauth.go`（`sids` 只含啟用中的成員資格）、`api/internal/tenant/enttenancy_internal_test.go`（`notTenantOwned` 新增 `admin_credential_tokens`）、`api/internal/seed/seed.go`、新 `admin/views/team.js`、`admin/views/order-shipments.js`。
- **API 契約**：新增 6 條路由；`v1-surface.txt` 只新增行。`POST /shops/{shopID}/users/{userID}/roles` 對 `merchant_owner` 的商家層呼叫由 200 變為 403——**這是授權收緊，不是介面形狀變更**，鎖檔不記錄授權，但必須在 CLAUDE.md 與 design 中明示。
- **相依**：**需要 `platform-account-provisioning`**（token 表、`LinkDelivery`、`password_hash` 可為 NULL、`account.Service`）。
