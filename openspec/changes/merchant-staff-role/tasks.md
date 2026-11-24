## 1. 資料模型（design D3／D4／D5）

- [ ] 1.1 `api/internal/ent/schema/account.go`：`ShopUser.status`（Int16 DEFAULT 1，CHECK `IN (0,1)`）
- [ ] 1.2 `api/internal/ent/schema/rbac.go`：`Role.merchant_assignable`（Bool DEFAULT false）
- [ ] 1.3 `api/internal/ent/schema/credentialtoken.go`：`user_id` 改可為 NULL；新增 `email`、`shop_id`、`role_id`（FK）；索引 `(shop_id, email)`
- [ ] 1.4 `go generate ./internal/ent`；`make migrate-gen name=add_shop_team_management`；**手寫** `admin_credential_tokens_kind_check` 的 DROP/ADD 與兩條組合 CHECK（atlas 不偵測 CHECK 運算式變更）；up→down→up
- [ ] 1.5 `api/internal/tenant/enttenancy_internal_test.go`：`notTenantOwned` 新增 `admin_credential_tokens` 並寫明理由（design D5）

## 2. RBAC（design D3）

- [ ] 2.1 `api/internal/rbac/engine.go`：`IsShopMember` 改為 `shop_user.status = 1`
- [ ] 2.2 `api/internal/httpapi/adminauth.go`：`Login`／`RefreshToken` 的 `sids` 只取 `status = 1` 的成員資格
- [ ] 2.3 `api/internal/httpapi/roles.go`：`assignShopRole`／`removeShopRole` 對非平台呼叫者檢查 `merchant_assignable` 與目標成員是否持有不可委派角色（design D4）
- [ ] 2.4 `roles_integration_test.go`：owner 指派 `merchant_owner` 403、平台指派成功、owner 改另一位 owner 的角色 403、既有 editor 指派測試仍綠

## 3. 退貨權限（design D2）

- [ ] 3.1 `api/internal/httpapi/shipping.go`：`advanceShipment` 解析出 `returned` 後以 `Engine.Snapshot(...).Allows("shipment.return")` 二次判定，不足回 403（在呼叫 service 之前）
- [ ] 3.2 `shipping_integration_test.go`：staff 退貨 403 且訂單／點數不變；editor／owner 退貨行為不變；staff 送達成功

## 4. Seed

- [ ] 4.1 `api/internal/seed/seed.go`：`PermissionCatalog` 新增 `shipment.return`、`user.manage_members`；`roleDefs` 新增 `staff`（`order.view`、`payment.view`、`shipment.view`、`shipment.create`、`shipment.update` 五個節點）；`merchant_owner` 加 `shipment.return`、`user.manage_members`；`editor` 加 `shipment.return`；`roleDefs` 新增 `MerchantAssignable` 欄位並在 `seedRoles` upsert（editor、staff 為 true）
- [ ] 4.2 `-demo`：新增 demo 店員 `demo-staff@example.com` / `demo-staff-change-me`（持 `staff`），供 e2e 使用
- [ ] 4.3 `seed_integration_test.go`：staff 節點集合**相等**比對、editor／owner 新節點、`merchant_assignable` 值、冪等

## 5. 邀請服務（design D5–D7）

- [ ] 5.1 `api/internal/account/invite.go`：`Invite`（可委派檢查、本店啟用成員 409、supersede、不建 user）、`ListInvitations`、`RevokeInvitation`、`InspectInvitation`、`AcceptAnonymous`（email 有任何 user 列 → 409；同交易建 user＋shop_user＋role_user＋消費）、`AcceptAuthenticated`（email 相等檢查）
- [ ] 5.2 `api/internal/account/membership.go`：`DisableMembership`／`EnableMembership`（自我 409、不可委派目標 403、`InvalidateUsers`）
- [ ] 5.3 單元／整合測試：並發接受恰好一次成功；「邀請未完成設定的平台帳號」接管嘗試 409 且 `password_hash` 仍 NULL；他人代接受 403

## 6. HTTP

- [ ] 6.1 新增 `api/internal/httpapi/team.go`：`MountShopAdmin`（invitations POST/GET/DELETE、users disable/enable，守衛 `user.manage_members`）、`MountAuth`（invitations inspect/accept 未認證）、`MountAdmin`（`POST /admin/invitations/accept` 已認證）
- [ ] 6.2 `router.go`／`Deps.Team`；限流：邀請建立以 shop＋呼叫者、未認證兩端點以 IP
- [ ] 6.3 `config.go`：`ADMIN_INVITE_TOKEN_TTL`（7d）；`.env.example`
- [ ] 6.4 `wire.go`；`wiring_integration_test.go` ⑤⑥ 通過；更新「已認證但無 RBAC 守衛的路由」清單（加入 `/admin/invitations/accept`）
- [ ] 6.5 `team_integration_test.go`：spec `shop-team-management` 全部情境（含回應同構的鍵集合比對）

## 7. Admin

- [ ] 7.1 新增 `admin/views/team.js`（成員列表、邀請、待接受、停用／啟用、變更角色；不可委派角色的列無操作）與 `admin/views/accept-invite.js`；`index.html` 對應 panel；`app.js` 未登入路由加入 `#/accept-invite/...`
- [ ] 7.2 `admin/views/order-shipments.js`：「標記退貨」按鈕改以 `can("shipment.return")` 決定可見（取消按鈕已由 `order.cancel` 決定，確認之）
- [ ] 7.3 量測 #4′／#5 並記入 CLAUDE.md

## 8. API 契約

- [ ] 8.1 `openapi.yaml`：6 條新路由、`Invitation`／`InvitationInspect` schema；`PUT .../shipments/{shipmentID}` 敘述補「`returned` 另需 `shipment.return`」
- [ ] 8.2 重新產生 `v1-surface.txt`，確認只新增

## 9. E2E

- [ ] 9.1 `e2e/happy-path.mjs`：以 `demo-staff@example.com` 登入 → 導覽只有訂單 → 看得到付款紀錄 → 建立出貨、標記送達 → 無取消與退貨按鈕；直接呼叫 `PUT /products/{id}` 與 `POST /orders/{id}/cancel` 皆 403
- [ ] 9.2 `e2e/happy-path.mjs`：demo owner 邀請每輪唯一 email 為 staff → 新 context 開連結設密碼 → 登入只看到訂單 → owner 停用 → 下一個操作 403
- [ ] 9.3 `e2e/commerce-path.mjs`：退貨段落仍以 owner token 執行且全綠（確認 `shipment.return` 已授予 owner）

## 10. 文件與驗收

- [ ] 10.1 `CLAUDE.md`：staff 節點集合（含 `payment.view` 的理由）與排除理由、`shipment.return` 拆分與「自訂角色可能失去退貨」、成員資格狀態 ≠ 帳號狀態、`merchant_assignable` 與 owner 不能再授予 owner（授權收緊）、邀請永不替既有帳號設密碼、inspect 的已知殘留
- [ ] 10.2 `make test`、`make test-int`、`make lint`、`make openapi`；`node happy-path.mjs`、`node commerce-path.mjs` 全綠
