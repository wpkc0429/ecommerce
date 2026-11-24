## ADDED Requirements

### Requirement: 平台管理員可建立後台帳號，且帳號密碼只能由本人設定

系統 SHALL 提供 `POST /api/v1/admin/accounts`，僅持有平台角色且具 `account.manage` 者可呼叫。請求含 `email`，MAY 含 `shop_id` 與 `role_id`（MUST 為 merchant scope 角色）以同時建立成員資格並指派角色。新帳號 MUST 沒有密碼（無法登入），系統 MUST 同時產生一次性的密碼設定 token。email 已存在 MUST 回 409。API MUST NOT 接受由管理員提供的密碼。

#### Scenario: 建立帳號並取得設定連結
- **WHEN** 具 `account.manage` 的平台管理員建立 `owner@bouquet.test`，並指定 shop 5 與 `merchant_owner`
- **THEN** 回 201，回應含 `user_id`、`delivery: "manual"`、`setup_url`（形如 `{ADMIN_PUBLIC_URL}/#/set-password/<token>`）與 `expires_at`；該帳號為 shop 5 成員並持有 `merchant_owner`；以任何密碼登入皆回 401

#### Scenario: email 已存在
- **WHEN** 建立的 email 已屬於既有帳號
- **THEN** 回 409，不建立任何資料

#### Scenario: 商家角色呼叫被拒
- **WHEN** 只持有 `merchant_owner`（商家層）者呼叫
- **THEN** 回 403

#### Scenario: 不接受管理員代填密碼
- **WHEN** 請求 body 含 `password` 欄位
- **THEN** 該欄位被忽略或回 422，帳號仍為未設定密碼狀態

### Requirement: 密碼設定與重設 token

密碼設定／重設 token MUST 以密碼學安全亂數產生，資料庫 MUST 只儲存其雜湊；MUST 為單次使用，消費 MUST 是原子的；設定 token 預設 72 小時、重設 token 預設 24 小時過期。對同一帳號發出新的同類 token 時，舊的未使用 token MUST 失效。

系統 SHALL 提供未認證的 `POST /api/v1/admin/auth/password-setup/inspect`（`{token}` → `{email, kind, expires_at}`）與 `POST /api/v1/admin/auth/password-setup`（`{token, password}`）。無效、過期、已使用的 token MUST 一律回相同的 404（不區分原因）。密碼 MUST 至少 12 字元。設定成功後 MUST 撤銷該帳號全部 refresh token。兩個端點 MUST 以來源 IP 為界施加速率限制，超限回 429 並帶 `Retry-After`。

#### Scenario: 設定密碼後可登入
- **WHEN** 以有效 setup token 設定密碼 `correct-horse-battery`
- **THEN** 回 204；之後以該 email 與密碼登入成功

#### Scenario: token 只能用一次
- **WHEN** 同一 token 第二次用於設定密碼
- **THEN** 回 404，密碼不變

#### Scenario: 重發後舊連結失效
- **WHEN** 管理員對同一帳號產生第二個重設連結後，使用者以第一個連結設定密碼
- **THEN** 回 404

#### Scenario: 重設密碼踢掉既有 session
- **WHEN** 使用者經重設連結設定新密碼後，以重設前取得的 refresh token 換發
- **THEN** 回 401

### Requirement: 連結的交付方式由回應明示

建立帳號、產生重設連結、開店等會產生連結的回應 MUST 包含 `delivery`（`manual` 或 `email`）。`delivery = "manual"` 時回應 MUST 包含連結與到期時間；`delivery = "email"` 時回應 MUST NOT 包含連結。未設定帳號信件寄送時 MUST 為 `manual`。

#### Scenario: 未設定寄信
- **WHEN** 系統未設定帳號信件寄送時產生重設連結
- **THEN** 回應 `delivery` 為 `manual` 且含 `reset_url`

### Requirement: 平台管理員可管理帳號的成員資格、停用與重設

系統 SHALL 提供（皆為平台層）：`GET /api/v1/admin/accounts`（`account.view`；`q` 對 email 子字串比對、分頁、依 id 遞增）、`GET /api/v1/admin/accounts/{userID}`（`account.view`；含 `status`、`has_password`、各商家成員資格與角色、平台角色；MUST NOT 含 `password_hash`）、`POST .../{userID}/memberships`（`{shop_id, role_id}`，建立成員資格並指派商家層角色，已存在則只補角色）、`DELETE .../{userID}/memberships/{shopID}`（移除成員資格與該店全部角色指派）、`POST .../{userID}/disable`、`POST .../{userID}/enable`、`POST .../{userID}/password-reset`（`account.manage`）。所有異動 MUST 立即清除受影響使用者的授權快取。

停用 MUST 將 `users.status` 設為 0 並撤銷該帳號全部 refresh token。呼叫者 MUST NOT 停用自己（409）；MUST NOT 停用最後一位啟用中的 `super_admin`（409），此檢查 MUST 在並發下成立。

#### Scenario: 停用後既有 access token 立即失效
- **WHEN** 管理員停用某商家 owner，該 owner 以停用前取得、尚未過期的 access token 呼叫 `GET /admin/shops/{shopID}/orders`
- **THEN** 回 403

#### Scenario: 停用自己被拒
- **WHEN** 平台管理員停用自己的帳號
- **THEN** 回 409

#### Scenario: 移除成員資格
- **WHEN** 管理員移除使用者於 shop 5 的成員資格後，該使用者呼叫 shop 5 的後台 API
- **THEN** 回 403

#### Scenario: 詳情不含密碼雜湊
- **WHEN** 讀取帳號詳情
- **THEN** 回應位元組不含 `password_hash` 鍵，也不含 bcrypt 前綴 `$2`

### Requirement: 一次完成的開店流程

系統 SHALL 提供 `POST /api/v1/admin/shop-provisioning`（平台層，要求 `shop.create`；提供 `domain` 時另要求 `shop.manage_domains`、提供 `storefront_origins` 時另要求 `shop.manage_client_origins`、並要求 `account.manage`）。請求含 `name`、`owner_email`，MAY 含 `theme_id`、`status`、`domain`、`path_prefix`、`storefront_origins`。系統 MUST 在單一交易內：建立商家（含自動首頁）、套用主題、建立或綁定網域、建立或指派 owner（新 email 建立無密碼帳號並產生設定 token；既有 email 直接加入成員資格）並授予 `merchant_owner`、登記 origin。任一步驟失敗 MUST 整筆回滾。回應 MUST 含商家、綁定結果、owner 的 `user_id`、`created`（是否新建）與（新建時）`delivery`／`setup_url`。

#### Scenario: 一次開好一家店
- **WHEN** 平台管理員以 `{"name":"Bouquet","theme_id":1,"domain":"bouquet.ksdevworks.online","owner_email":"owner@bouquet.test"}` 呼叫
- **THEN** 回 201；商家存在且有首頁草稿；網域解析到該商家；`owner@bouquet.test` 是該店成員並持有 `merchant_owner`；回應含 `setup_url`

#### Scenario: 網域衝突時整筆回滾
- **WHEN** 指定的網域與路徑前綴已綁定其他商家
- **THEN** 回 409 或 422，且不存在新商家、新帳號或新成員資格

#### Scenario: 既有帳號成為 owner
- **WHEN** `owner_email` 已是既有帳號
- **THEN** 該帳號被加入新商家並授予 `merchant_owner`，回應 `created` 為 false 且不含 `setup_url`，既有密碼不變

### Requirement: 開店檢查清單由伺服器推導

系統 SHALL 提供 `GET /api/v1/admin/shops/{shopID}/setup-status`（平台 `shop.view`），回傳由當下資料推導的布林檢查項：`theme_applied`、`domain_bound`、`owner_assigned`、`owner_password_set`、`home_published`、`shipping_method_active`、`payment_credentials_set`、`publishable_key_active`。系統 MUST NOT 儲存開店進度。

#### Scenario: owner 設定密碼後檢查項改變
- **WHEN** 新商家的 owner 透過設定連結設好密碼
- **THEN** 該商家的 `owner_password_set` 由 false 變為 true
