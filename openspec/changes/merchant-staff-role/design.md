## Context

- seed 角色：`super_admin`（platform，`*`）、`merchant_owner`、`editor`（merchant）。`editor` 已含 `order.view`、`shipment.*`、`product.create/edit`、`page.*`（不含 publish/delete）、`point.adjust`、`member.view`、`report.view`。
- `RolesHandler.assignShopRole`（`user.manage_roles`）可把任何 merchant scope 角色——包括 `merchant_owner`——指派給本店成員。
- `ShippingHandler.advanceShipment`：單一端點、body `status` 為 `delivered` 或 `returned`，守衛 `shipment.update`。退貨會把整張訂單標為 `fulfillment_status=3` 並扣回整筆點數（partial-shipment）。
- `rbac.Engine.load` 的跨店守衛輸入是 `shop_user` 是否存在。
- `platform-account-provisioning` 提供：`admin_credential_tokens`、`LinkDelivery`／`ManualDelivery`、`password_hash` 可為 NULL、停用做在快照。

## Goals / Non-Goals

**Goals:** 一個只能處理訂單與出貨的角色；owner 自己邀請、停用店員；退貨與送達分開授權；不打破既有 seed 角色的能力。

**Non-Goals:**
- 自訂角色的商家端編輯（今天只有平台能建角色）。
- 店員層級的操作審計軌跡（記為後續；ONDO 上線阻塞項已列「後台讀取無審計軌跡」）。
- owner 轉移（只有平台能授予／移除 `merchant_owner`）。
- 邀請信件（manual 交付，同 `platform-account-provisioning` D4）。

## Decisions

### D1：`staff` 的節點集合
`order.view`、`payment.view`、`shipment.view`、`shipment.create`、`shipment.update`。

納入 `payment.view`（已定案）：店員要能回答「顧客說已付款」——訂單的 `payment_status` 只是一個數字，付款紀錄（provider、`provider_transaction_id`、`paid_at`）才答得出「什麼時候、哪一筆」。它是唯讀的對帳資訊，不含任何密鑰（金流憑證是另一個節點 `payment_credential.manage`）。`GET /admin/shops/{shopID}/payments`（以識別碼反查）一併可用。

逐項排除的理由：
- `order.cancel`：取消（包括未付款訂單）會回補庫存、與付款狀態互動（已付款不可取消），屬於營運判斷——**只有 owner（與既有 editor）**（已定案）。
- `member.view`：訂單已帶收件人資訊，出貨不需要名冊；與 `member-directory` D4 給 editor 的理由（「擋不住的界線比沒有界線更糟」）相反——staff **沒有**其他路徑讀到名冊的 email 與點數，所以這條界線是真的。
- `shipping_method.view`：訂單已帶運送方式快照。
- 其他（商品、頁面、點數、報表、金流、成員）與出貨無關。

### D2：退貨拆成 `shipment.return`，做法是「額外要求」而不是新端點
`PUT .../shipments/{shipmentID}` 的守衛仍是 `shipment.update`；handler 解析出 `status = "returned"` 後再以 `Snapshot.Allows("shipment.return")` 判定，不足回 403。不新增端點（v1 表面不變），也不把「依 body 決定權限」塞進中介層（中介層讀 body 是既有 `peekEmail` 的特例，不該擴散）。

seed 把 `shipment.return` 加進 `merchant_owner` 與 `editor`，所以**既有 seed 角色的能力逐字不變**。代價：平台透過 `POST /admin/roles` 建立、只持有 `shipment.update` 的自訂角色會失去退貨能力——今天沒有這種角色（只有 seed 角色），記為 Risk 並在 CLAUDE.md 明示。

**staff 不能退貨**（已定案）：退貨會扣回顧客整筆點數且無法撤銷（`points` 的 `UNIQUE(order_id, kind)` 只防重複不防誤操作），而 partial-shipment 已明說「任一筆退貨標記整單」是過寬的。

### D3：成員資格狀態，不是帳號狀態
同一個 `users` 帳號可以屬於多家店。owner 停用店員 MUST 只影響本店——若改 `users.status`，A 店 owner 就能把 B 店的人鎖在外面。新增 `shop_user.status`（0/1，DEFAULT 1）；`rbac.Engine.load` 的 `IsShopMember` 改為 `status = 1`；登入時的 `sids` 只列啟用中的成員資格。停用／啟用都 `InvalidateUsers`。

### D4：可委派角色是資料，不是名稱判斷
`roles.merchant_assignable`（DEFAULT false）。seed：`editor`、`staff` 為 true，`merchant_owner` 為 false。商家層呼叫者（`assignShopRole`、`removeShopRole`、邀請）只能操作 `merchant_assignable = true` 的角色；目標成員若持有任何**不可委派**角色（例如另一位 owner），商家層呼叫者也不得停用他、改他的角色。平台層呼叫者（持平台角色者，含經由 `/shops/{id}` 路徑操作時）不受此限。

這收緊了 `assignShopRole` 的既有行為（owner 今天能指派 `merchant_owner`）——**已定案：owner 不能任命 owner，只有平台能**。理由：owner 能產生 owner，就能產生 owner 移除不了、平台也不知道的永久後門帳號；而且「owner 是誰」是平台與商家的契約關係（後續 phase 的開店申請審核就是在決定這件事）。平台角色不受影響。以 `rbac` spec 的 MODIFIED 需求明示。

用欄位而不是硬寫角色名：平台日後建立的自訂商家角色可以自己決定是否可委派，預設不可（fail closed）。

### D5：邀請 token 與「永遠不能替既有帳號設定密碼」
擴充 `admin_credential_tokens`：`kind` 加 `invite`；新增 `email`（invite 必填）、`shop_id`、`role_id`；`user_id` 改可為 NULL（invite 對尚無帳號的 email）。CHECK 手寫：`(kind = 'invite') = (shop_id IS NOT NULL AND role_id IS NOT NULL AND email IS NOT NULL)`、`(kind <> 'invite') = (user_id IS NOT NULL)`。**atlas 以名稱比對 CHECK，改運算式不會被偵測**——DROP/ADD 手寫在 migration。此表從此含 `shop_id`，登記進 `notTenantOwned`（理由：授權邀請，於未認證端點以 token 雜湊查找，不在租戶上下文）。

邀請時**不建立帳號**。接受時：
- email 沒有任何 `users` 列 → `POST /api/v1/admin/auth/invitations/accept`（未認證，`{token, password}`）在同一交易建立帳號、成員資格、角色指派。
- email 已有 `users` 列（不論有無密碼）→ 必須以該帳號登入後呼叫 `POST /api/v1/admin/invitations/accept`（已認證，`{token}`），且 token 的 email 必須等於呼叫者 email。未認證的接受端點對這種 token 回 409「請先登入」。

為什麼這麼嚴格：manual 交付代表**邀請者自己握有連結**。若邀請連結能替「已存在但尚未設定密碼」的帳號設密碼，owner 只要邀請一個平台剛建立、尚未完成設定的帳號（甚至是平台管理員的），就能接管它。邀請在接受前不建立帳號，也避免 owner 以邀請大量「佔用」email。

### D6：邀請回應不透露帳號是否存在，但 inspect 會
建立邀請的回應對「新 email」與「既有帳號」逐位元組同構（`delivery`、`invite_url`、`expires_at`、`invitation_id`）。但持有連結者呼叫 inspect 會得到 `requires_login: true/false`——頁面必須知道要顯示「設定密碼」還是「請登入」。manual 交付下持有者就是邀請者，所以 owner **可以**得知某 email 在平台上是否有後台帳號（但不知道屬於哪家店）。這是可接受的殘留：接受頁面本身必須分流，沒有既能分流又不洩漏的設計；接上信件交付後持有者變成受邀者本人，殘留消失。

### D7：邀請的限制
- 同一 shop、同一 email 同時只有一張有效邀請（新邀請 supersede 舊的）。
- 已是本店啟用中成員的 email → 409（這不洩漏跨店資訊：本店成員本來就看得到）。
- 有效期 7 天（`ADMIN_INVITE_TOKEN_TTL`）。
- 邀請建立端點以 `shop + 呼叫者` 限流（防止被盜的 owner token 被用來大量列舉 email 是否存在，D6）。

### D8：自我保護
owner 不可停用自己、不可移除自己的角色（409）；不可對持有不可委派角色的成員做任何成員管理操作（403）。

## Risks / Trade-offs

- [平台自建、只持 `shipment.update` 的自訂角色失去退貨] → 今天不存在；CLAUDE.md 明示；平台可為其加上 `shipment.return`。
- [owner 不再能指派 owner] → 行為收緊；需要第二位 owner 時由平台在帳號視圖指派。
- [inspect 洩漏帳號存在] → D6；限流；接上信件後消失。
- [`sids` 改為只含啟用成員資格] → `sids` 只用於 `/admin/me` 與 admin 的商家選單；授權永遠以 DB 為準，不受影響。

## Migration Plan

1. migration：`shop_user.status`、`roles.merchant_assignable`、`admin_credential_tokens` 欄位與手寫 CHECK。
2. seed：`staff` 角色、`shipment.return`、`user.manage_members`、`merchant_assignable` 設定（`seedRoles` 改為同時 upsert 此欄位）。
3. 回滾：down migration 刪除新欄位；invite 列在 down 前刪除（只是未接受的邀請）。

## Decided（原 Open Questions，已由使用者定案）

- staff **可以**看付款狀態與付款紀錄（`payment.view`）。
- staff **不能**退貨（`shipment.return`）、**不能**取消訂單（含未付款）；兩者只有 owner（與既有 editor）。
- owner **不能**任命其他 owner；只有平台能授予／移除 `merchant_owner`。
- 邀請連結有效期 7 天。
