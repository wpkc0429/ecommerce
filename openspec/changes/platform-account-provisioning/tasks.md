## 1. 資料模型（design D1／D2）

- [ ] 1.1 `api/internal/ent/schema/account.go`：`User.password_hash` 改為 `Optional().Nillable()`（保留 `Sensitive()`）
- [ ] 1.2 新增 `api/internal/ent/schema/credentialtoken.go`：`AdminCredentialToken`（`user_id` FK、`kind` CHECK `IN ('setup','reset')`、`token_hash` unique、`created_by` nullable、`expires_at`、`used_at` nullable、`created_at`；索引 `(user_id, kind)`）
- [ ] 1.3 `go generate ./internal/ent`；修正所有 `PasswordHash` 讀取點（`adminauth.go` 的 `VerifyPassword`、`seed.go`）為 `*string` 處理
- [ ] 1.4 `make migrate-gen name=add_admin_credential_tokens`；down 檔改為拒絕回滾並註明手動步驟（design Migration Plan）；`make migrate`

## 2. 帳號服務（design D1–D7）

- [ ] 2.1 新增 `api/internal/account/token.go`：產生（32 bytes base64url）、雜湊、原子消費、同類 supersede
- [ ] 2.2 新增 `api/internal/account/delivery.go`：`LinkDelivery` 介面與 `ManualDelivery`；連結組成 `{ADMIN_PUBLIC_URL}/#/set-password/{token}`
- [ ] 2.3 新增 `api/internal/account/service.go`：`CreateAccount`（可選成員資格＋角色，merchant scope 檢查）、`AddMembership`、`RemoveMembership`（含該店全部 `role_user`）、`Disable`（自我／最後 super_admin 檢查，`FOR UPDATE`）、`Enable`、`IssueReset`、`CompletePasswordSetup`（≥12 字元、同交易消費 token ＋ 寫雜湊 ＋ 撤銷全部 refresh token）；每個異動呼叫 `Engine.InvalidateUsers`
- [ ] 2.4 `api/internal/auth/refresh.go`：新增 `RevokeAllUser(ctx, tx, userID)`
- [ ] 2.5 `api/internal/httpapi/roles.go`：`removePlatformRole` 補「最後一位 super_admin」檢查
- [ ] 2.6 單元／整合測試：token 單次使用、並發消費恰好一次成功、supersede、過期、最後 super_admin 並發停用

## 3. RBAC 與認證（design D5／D9）

- [ ] 3.1 `api/internal/rbac/engine.go`：`load` 讀 `users.status`；`decide.go`：`Snapshot.Disabled` 使 `Allows`／`CanAccessShop`／`HasPlatformRole` 皆為 false（快取序列化含此欄位）
- [ ] 3.2 `api/internal/httpapi/adminauth.go`：`Login` 對 NULL `password_hash` 走 `EqualizeVerifyTiming`；`Me` 對 `status=0` 回 401，並新增 `platform_permissions`
- [ ] 3.3 新增整合測試：列舉「已認證但無 RBAC 守衛」的 admin 路由，與預期清單（`/admin/me`、`/admin/media/config`（若已存在））相等比對，新增時轉紅
- [ ] 3.4 `api/internal/httpapi/authflow_integration_test.go`：停用後 access token 403、`/me` 401、refresh 401；重新啟用恢復

## 4. 開店流程（design D8）

- [ ] 4.1 `api/internal/cms/service.go`：把 `CreateShop` 的交易內本體抽成 `createShopTx(ctx, tx, in)`，HTTP 版本行為逐字不變
- [ ] 4.2 新增 `api/internal/provision/service.go`：`ProvisionShop`（商家、主題、site 建立或綁定、owner 建立或指派、origin 登記；commit 後發佈 `SiteMappingChanged`／`StorefrontClientChanged`）
- [ ] 4.3 `api/internal/provision/status.go`：`SetupStatus(shopID)` 八個 `Exist()` 查詢
- [ ] 4.4 整合測試：完整成功、網域衝突整筆回滾（無殘留商家／帳號）、既有 email 成為 owner 不改密碼、權限組合（缺 `shop.manage_domains` 時帶 domain 回 403）

## 5. HTTP 與限流

- [ ] 5.1 新增 `api/internal/httpapi/accounts.go`：`AccountsHandler.MountPlatform`（7 條帳號路由）與 `MountAuth`（inspect、password-setup）；回應永不含 `password_hash`
- [ ] 5.2 新增 `api/internal/httpapi/provisioning.go`：`POST /admin/shop-provisioning`、`GET /admin/shops/{shopID}/setup-status`（注意 chi.Walk 對 `/shops/{shopID}` 的既知盲點，確認契約檢查 ① 的可達性探測涵蓋）
- [ ] 5.3 `router.go`／`Deps`：`Accounts`、`Provisioning`、`AdminPasswordSetupRateLimit`；`ratelimitmw.go` 以 IP 為界（重用 login broad 門檻設定或新增 `RATE_LIMIT_PASSWORD_SETUP_*`）
- [ ] 5.4 `internal/app/wire.go`：建構並注入；`wiring_integration_test.go` ⑤⑥ 通過
- [ ] 5.5 `api/internal/config/config.go`：`ADMIN_PUBLIC_URL`、`ADMIN_SETUP_TOKEN_TTL`（72h）、`ADMIN_RESET_TOKEN_TTL`（24h）；`.env.example` 同步並註明「應為共用 merchant admin 網域，不是平台 admin 網域」
- [ ] 5.6 新增 `docs/admin-frontend-integration.md`：連結契約（`#/set-password/{token}` 由每個 admin 前端在根路徑處理）、inspect／password-setup 的呼叫順序與錯誤處理

## 6. Seed

- [ ] 6.1 `api/internal/seed/seed.go`：`PermissionCatalog` 新增 `account.view`、`account.manage`（`super_admin` 經 `*` 取得，不授予任何商家角色）
- [ ] 6.2 `seed_integration_test.go`：新節點存在、未授予 `merchant_owner`／`editor`

## 7. Admin（design D9）

- [ ] 7.1 開工前量測並記錄 `lib/`＋`app.js` 與 `views/` 行數（#4′ 基準）
- [ ] 7.2 `admin/app.js`：shell 讀 `state.me.platform_permissions`；平台視圖不依賴 `shopID`；未登入時若 hash 為 `#/set-password/...` 直接渲染該頁
- [ ] 7.3 新增 `admin/views/platform-shops.js`（列表、開店精靈、檢查清單）、`admin/views/platform-accounts.js`（列表、建立、停用／啟用、重設連結、成員資格）、`admin/views/set-password.js`；`index.html` 新增對應 `.tab-panel`
- [ ] 7.4 連結一次性顯示 ＋ 複製按鈕 ＋ 說明文字；不寫入任何持久儲存
- [ ] 7.5 量測 #4′／#5 並記入 CLAUDE.md

## 8. API 契約

- [ ] 8.1 `openapi.yaml`：新路由、`Account`／`AccountDetail`／`CredentialLink`（`delivery` enum 必填、`setup_url`／`reset_url` 選填）／`ProvisionResult`／`SetupStatus` schema、`AdminMe.platform_permissions`；未認證端點標 `security: []`
- [ ] 8.2 重新產生 `v1-surface.txt`，確認只新增
- [ ] 8.3 conformance 取樣新增 schema

## 9. E2E

- [ ] 9.1 `e2e/happy-path.mjs` 新段「平台開店」：super admin 以精靈開一家每輪唯一名稱的店（網域 `e2e-<run>.localhost`）→ 取得設定連結 → 另一個瀏覽器 context 開連結設密碼 → 以新 owner 登入看到該店 → super admin 停用該 owner → owner 下一個操作被 403 並被導回登入
- [ ] 9.2 確認此段可重複執行（每輪唯一 email／網域；不改動 demo 商家）

## 10. 文件與驗收

- [ ] 10.1 `CLAUDE.md`：password_hash NULL 語意、token 只存雜湊與 fragment 連結、`delivery` 先看規則、停用做在 RBAC 快照、最後 super_admin 保護、`provision.Service` 是開店唯一實作
- [ ] 10.2 `make test`、`make test-int`、`make lint`、`make openapi`；`node happy-path.mjs`、`node commerce-path.mjs` 全綠
