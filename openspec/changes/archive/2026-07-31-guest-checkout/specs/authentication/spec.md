## MODIFIED Requirements

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
