## MODIFIED Requirements

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

前台會員的註冊與登入 MUST 於商家上下文（Host 解析）進行。註冊時 email/phone 命中既有 `members` 者 SHALL 沿用該身分並建立該店 `shop_member`；全新者建立 `members` + `shop_member`；兩種情形的回應 MUST 同構，不得洩漏該帳號已存在於其他商家。登入成功簽發 `aud=shop:{shop_id}` 的 access token 與綁定該店的 refresh token。

`/api/v1/shop/auth/register` 與 `/api/v1/shop/auth/login` MUST 施加速率限制，鍵值 MUST 納入已解析的 `shop_id`（以及來源 IP、提交的 email 組合），確保不同商家的流量使用互相獨立的限流計數，任一商家的流量（正常或惡意）不得影響其他商家用戶的請求配額。規則維度比照後台登入：以「shop + IP」為界的寬鬆規則與「shop + IP + email」為界的嚴格規則並行套用，任一超限即 MUST 回 429 並帶 `Retry-After` header。速率限制器故障時 MUST fail-open。

#### Scenario: 全新會員註冊
- **WHEN** 於 shop A 網域以未使用過的 email 註冊
- **THEN** 建立 `members` 與（shop A, member）的 `shop_member`，回傳該店 token

#### Scenario: 既有平台身分於第二家店註冊
- **WHEN** 已於 shop A 註冊的 email 於 shop B 網域註冊（密碼驗證通過）
- **THEN** 不建立新 `members`，僅新增 shop B 的 `shop_member`，回應與全新註冊同構

#### Scenario: 單一商家超限
- **WHEN** 同一來源 IP 對 shop A 的同一 email 在門檻視窗內的登入或註冊嘗試次數超過嚴格規則上限
- **THEN** 回 429 並帶 `Retry-After` header

#### Scenario: 跨商家流量互不干擾
- **WHEN** shop A 的某個「IP + email」組合已達到速率限制上限
- **THEN** shop B 以相同的「IP + email」組合發出的請求 MUST NOT 被計入 shop A 的限流計數，仍可正常放行（直到 shop B 自己的計數獨立超限為止）

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
