## MODIFIED Requirements

### Requirement: Member registration and login in shop context

前台會員的註冊與登入 MUST 於商家上下文（Host 解析）進行，且 MUST 完全在該商家範圍內判定。註冊時 email/phone 命中**該商家**既有 `members` 者 MUST 被拒；未命中者建立一筆隸屬該商家的 `members`。同一 email/phone 於其他商家是否已存在 MUST NOT 影響本商家的註冊結果，系統 MUST NOT 提供任何沿用其他商家身分的路徑。

註冊成功與各種失敗情形的回應 MUST 同構，不得洩漏該 email/phone 是否已為本商家顧客。登入成功簽發 `aud=shop:{shop_id}` 的 access token 與綁定該店的 refresh token。

`/api/v1/shop/auth/register` 與 `/api/v1/shop/auth/login` MUST 施加速率限制，鍵值 MUST 納入已解析的 `shop_id`（以及來源 IP、提交的 email 組合），確保不同商家的流量使用互相獨立的限流計數，任一商家的流量（正常或惡意）不得影響其他商家用戶的請求配額。規則維度比照後台登入：以「shop + IP」為界的寬鬆規則與「shop + IP + email」為界的嚴格規則並行套用，任一超限即 MUST 回 429 並帶 `Retry-After` header。速率限制器故障時 MUST fail-open。

#### Scenario: 全新會員註冊
- **WHEN** 於 shop A 網域以該商家未使用過的 email 註冊
- **THEN** 建立一筆 `shop_id` 為 shop A 的 `members`，回傳該店 token

#### Scenario: 他店已存在的 email 於本店註冊視同全新
- **WHEN** 已於 shop A 註冊的 email 於 shop B 網域以任意新密碼註冊
- **THEN** 於 shop B 建立一筆全新且獨立的 `members`，MUST NOT 要求驗證其於 shop A 的密碼，回應與全新註冊同構

#### Scenario: 本店已存在的 email 重複註冊被拒
- **WHEN** 已於 shop A 註冊的 email 再次於 shop A 網域註冊
- **THEN** 回應為統一的註冊失敗，與其他失敗情形同構，不揭露該 email 已為本店顧客

#### Scenario: 他店密碼無法登入本店
- **WHEN** 以顧客於 shop A 設定的 email 與密碼向 shop B 網域登入，而該 email 尚未於 shop B 註冊
- **THEN** 登入失敗，MUST NOT 簽發任何 shop B 的 token

#### Scenario: 單一商家超限
- **WHEN** 同一來源 IP 對 shop A 的同一 email 在門檻視窗內的登入或註冊嘗試次數超過嚴格規則上限
- **THEN** 回 429 並帶 `Retry-After` header

#### Scenario: 跨商家流量互不干擾
- **WHEN** shop A 的某個「IP + email」組合已達到速率限制上限
- **THEN** shop B 以相同的「IP + email」組合發出的請求 MUST NOT 被計入 shop A 的限流計數，仍可正常放行（直到 shop B 自己的計數獨立超限為止）
