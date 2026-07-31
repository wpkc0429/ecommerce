# authentication Specification

## Purpose
TBD - created by archiving change phase1-multitenant-cms-foundation. Update Purpose after archive.
## Requirements
### Requirement: Admin login and token issuance

後台使用者 SHALL 以 email + 密碼登入；成功時系統 MUST 簽發 access token（15 分鐘，`aud=admin`，claims 含 `sub` 與所屬 `sids` 列表）與 refresh token（30 天、不透明隨機值、雜湊後存 `user_refresh_tokens`）；失敗回應 MUST 為不區分「帳號不存在」與「密碼錯誤」的 401。

`/api/v1/admin/auth/login` MUST 施加速率限制：以來源 IP 為界的寬鬆規則（防單一來源對大量帳號的地毯式嘗試）與以「IP + 提交的 email」組合為界的嚴格規則（防針對單一帳號的暴力破解）並行套用，任一規則超限即 MUST 回 429 並帶 `Retry-After` header（單位：秒）。速率限制計數 MUST 涵蓋所有嘗試（不論登入成功或失敗）。速率限制器故障（如 Redis 不可用）時 MUST fail-open（放行請求），不得因限流器本身故障而阻斷所有登入。

#### Scenario: 登入成功
- **WHEN** 提交正確的 email 與密碼且 `users.status = 1`
- **THEN** 回傳 access + refresh token，access token 可通過後台 API 驗證

#### Scenario: 登入失敗不洩漏原因
- **WHEN** 提交不存在的 email 或錯誤密碼
- **THEN** 回相同格式與訊息的 401

#### Scenario: 單一帳號超限
- **WHEN** 同一來源 IP 對同一 email 在門檻視窗內的登入嘗試次數超過嚴格規則上限
- **THEN** 回 429，body 為 `{"error":{"code":"rate_limited",...}}`，並帶 `Retry-After` header

#### Scenario: 視窗過期後恢復
- **WHEN** 已超限的來源 IP/email 組合等待超過速率限制視窗長度後再次嘗試
- **THEN** 該次嘗試正常進入登入邏輯（不再被 429 擋下）

### Requirement: Member registration and login in shop context

前台會員的註冊與登入 MUST 於商家上下文（前台租戶解析，見 spec multi-tenancy 的 Host-based tenant resolution：publishable key 優先、否則 `Host`）進行，且 MUST 完全在該商家範圍內判定。同一 email/phone 於其他商家是否已存在 MUST NOT 影響本商家的註冊結果，系統 MUST NOT 提供任何沿用其他商家身分的路徑。

註冊時系統 MUST 依該商家內既有 `members` 的狀態決定行為：

- email/phone 未命中本商家任何身分者，建立一筆隸屬該商家的 `members` 並設定密碼；
- 命中一筆 `password_hash IS NULL` 的**無密碼身分**（由訪客結帳建立，見 spec guest-checkout）者，MUST 認領該身分——為其設定密碼並簽發 token，`member_id` MUST NOT 改變，該身分名下既有的訂單與點數 MUST 於認領後直接可由該會員自助查詢，MUST NOT 需要任何資料搬移；
- 命中一筆已設定密碼的身分者 MUST 被拒。

認領 MUST 對「密碼尚未設定」這個前提做原子判定（例如以 `WHERE password_hash IS NULL` 為條件寫入並檢查受影響列數），並發的兩次認領 MUST 僅有一次成功，另一次 MUST 走統一的拒絕回應。

各種**失敗**情形的回應 MUST 同構，不得洩漏失敗原因。認領成功的回應 MUST 與全新註冊成功的回應同構。由回應可區分的邊界因此為「該 email 於本商家**是否已設定密碼**」，而非「是否為本商家顧客」——無密碼身分與從未存在的 email，其註冊回應 MUST 完全相同。

登入 MUST 僅在該商家範圍內比對憑證；`password_hash` 為 NULL 的身分 MUST 無法登入。登入成功簽發 `aud=shop:{shop_id}` 的 access token 與綁定該店的 refresh token。

`/api/v1/shop/auth/register` 與 `/api/v1/shop/auth/login` MUST 施加速率限制，鍵值 MUST 納入已解析的 `shop_id`（以及來源 IP、提交的 email 組合），確保不同商家的流量使用互相獨立的限流計數，任一商家的流量（正常或惡意）不得影響其他商家用戶的請求配額。規則維度比照後台登入：以「shop + IP」為界的寬鬆規則與「shop + IP + email」為界的嚴格規則並行套用，任一超限即 MUST 回 429 並帶 `Retry-After` header。速率限制器故障時 MUST fail-open。

#### Scenario: 全新會員註冊
- **WHEN** 於 shop A 網域以該商家未使用過的 email 註冊
- **THEN** 建立一筆 `shop_id` 為 shop A 的 `members`，回傳該店 token

#### Scenario: 他店已存在的 email 於本店註冊視同全新
- **WHEN** 已於 shop A 註冊的 email 於 shop B 網域以任意新密碼註冊
- **THEN** 於 shop B 建立一筆全新且獨立的 `members`，MUST NOT 要求驗證其於 shop A 的密碼，回應與全新註冊同構

#### Scenario: 本店已設定密碼的 email 重複註冊被拒
- **WHEN** 已於 shop A 註冊並設定密碼的 email 再次於 shop A 網域註冊
- **THEN** 回應為統一的註冊失敗，與其他失敗情形同構，不揭露該 email 已為本店顧客

#### Scenario: 註冊認領本店的無密碼身分
- **WHEN** 某 email 於 shop A 僅存在一筆由訪客結帳建立的無密碼身分，該 email 於 shop A 註冊
- **THEN** 該身分被設定密碼並簽發 token，`member_id` 不變，回應與全新註冊同構

#### Scenario: 認領後既有訂單與點數自動可見
- **WHEN** 認領完成後，該會員以取得的 token 查詢自己的訂單列表與點數餘額
- **THEN** 認領前以訪客身分建立的訂單與其已核發的點數皆出現在結果中

#### Scenario: 並發認領僅一次成功
- **WHEN** 同一無密碼身分被兩個並發的註冊請求同時認領
- **THEN** 恰有一個請求取得 token，另一個回統一的註冊失敗，該身分僅有一份 `password_hash`

#### Scenario: 無密碼身分無法登入
- **WHEN** 以一筆 `password_hash IS NULL` 的身分的 email 加上任意密碼登入
- **THEN** 回 401，MUST NOT 簽發任何 token

#### Scenario: 他店密碼無法登入本店
- **WHEN** 以顧客於 shop A 設定的 email 與密碼向 shop B 網域登入，而該 email 尚未於 shop B 註冊
- **THEN** 登入失敗，MUST NOT 簽發任何 shop B 的 token

#### Scenario: 以金鑰指定商家的註冊
- **WHEN** 自帶前端以 shop A 的 publishable key 呼叫註冊端點
- **THEN** 於 shop A 建立身分並回傳 shop A 的 token，行為與以 shop A 網域註冊完全相同

#### Scenario: 單一商家超限
- **WHEN** 同一來源 IP 對 shop A 的同一 email 在門檻視窗內的登入或註冊嘗試次數超過嚴格規則上限
- **THEN** 回 429 並帶 `Retry-After` header

#### Scenario: 跨商家流量互不干擾
- **WHEN** shop A 的某個「IP + email」組合已達到速率限制上限
- **THEN** shop B 以相同的「IP + email」組合發出的請求 MUST NOT 被計入 shop A 的限流計數，仍可正常放行（直到 shop B 自己的計數獨立超限為止）

### Requirement: Token isolation between admin and member

系統的每一個 token 家族 MUST 以互斥的 audience 區隔，且後台與前台兩套家族 MUST 使用完全不同的簽章金鑰。現行家族為：後台 access token（`aud=admin`）、預覽 token（`aud=preview`）、會員 access token（`aud=shop:{shop_id}`）、訂單存取 token（`aud=order:{shop_id}`，見 spec guest-checkout）。

任一家族的 token 用於另一家族的驗證路徑 MUST 回 401，MUST NOT 因其簽章有效而取得任何存取權；綁定商家的家族（會員、訂單存取）跨商家重用 MUST 回 401。

#### Scenario: 會員 token 打後台 API
- **WHEN** 以 `aud=shop:1` 的 token 呼叫後台管理 API
- **THEN** 回 401

#### Scenario: 跨店重用會員 token
- **WHEN** 以 shop A 簽發的會員 token 呼叫 shop B 網域的會員 API
- **THEN** audience 不符，回 401

#### Scenario: 訂單存取 token 打會員 API
- **WHEN** 以訂單存取 token 呼叫需要會員身分的端點
- **THEN** 回 401

#### Scenario: 會員 token 打訂單存取端點
- **WHEN** 以會員 access token 呼叫以訂單存取 token 授權的端點
- **THEN** 回 401

#### Scenario: 跨店重用訂單存取 token
- **WHEN** 以 shop A 簽發的訂單存取 token 於 shop B 的商家上下文呼叫訪客端點
- **THEN** 回 401

### Requirement: Refresh token rotation and revocation

Refresh token MUST 於每次使用時輪替（舊 token 標記 `revoked_at`、新 token 記錄 `rotated_from`）；已輪替 token 的重放 MUST 使該輪替鏈全部撤銷；登出 MUST 撤銷當前 refresh token。

`/api/v1/admin/auth/refresh` MUST 以來源 IP 為界施加速率限制；`/api/v1/shop/auth/refresh` MUST 以「shop + IP」為界施加速率限制（鍵值 MUST 納入已解析的 `shop_id`，理由與跨商家隔離要求同上）。超限 MUST 回 429 並帶 `Retry-After` header。速率限制器故障時 MUST fail-open。

#### Scenario: 正常輪替
- **WHEN** 以有效 refresh token 換發
- **THEN** 取得新 access + refresh token，舊 refresh token 隨即失效

#### Scenario: 重放偵測
- **WHEN** 已被輪替的 refresh token 再次被使用
- **THEN** 回 401 且該鏈上所有未過期 token 被撤銷

#### Scenario: refresh 端點超限
- **WHEN** 同一來源 IP（會員情境下為同一 shop + IP）在門檻視窗內呼叫 refresh 的次數超過上限
- **THEN** 回 429 並帶 `Retry-After` header，不進入 token 輪替邏輯

### Requirement: Password hashing
`users` 與 `members` 的密碼 MUST 以 Argon2id 雜湊儲存（OWASP 建議參數起步）；系統 MUST NOT 以任何形式儲存或記錄明文密碼；`members.password_hash` 允許 NULL（預留社群登入）。

#### Scenario: 儲存格式
- **WHEN** 建立或變更密碼
- **THEN** 資料庫僅存 `$argon2id$` 開頭的雜湊字串

### Requirement: Disabled account rejection
`users.status = 0` 或 `members.status = 0` 的帳號 MUST 無法登入，且其既有 refresh token MUST 無法換發新 token。

#### Scenario: 停用後 refresh 失效
- **WHEN** 帳號被停用後以先前取得的 refresh token 換發
- **THEN** 回 401

