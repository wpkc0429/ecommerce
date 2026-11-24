## ADDED Requirements

### Requirement: 後台登入的帳號層級鎖定

除既有的來源 IP 與「IP＋email」速率限制外，`/api/v1/admin/auth/login` MUST 對每個正規化後的 email **跨所有來源 IP** 計算登入失敗次數；在設定的視窗內（預設 15 分鐘）失敗次數達上限（預設 10）後，該 email 的登入嘗試 MUST 回 429 並帶 `Retry-After`，直到視窗滑出為止，且 MUST NOT 進行密碼驗證。計數 MUST 只計失敗；成功登入 MUST 清除該 email 的計數。不存在的 email MUST 以相同方式計數與鎖定。計數鍵 MUST NOT 以明文 email 儲存；日誌 MUST NOT 記錄明文 email。限流器故障時 MUST fail-open。

#### Scenario: 分散式猜測被鎖定
- **WHEN** 10 個不同來源 IP 各對同一 email 以錯誤密碼登入 1 次後，第 11 個 IP 以**正確**密碼登入
- **THEN** 回 429，不簽發 token

#### Scenario: 不存在的帳號行為相同
- **WHEN** 對不存在的 email 重複失敗登入達上限
- **THEN** 之後同樣回 429，回應格式與存在的帳號相同

#### Scenario: 成功登入清除計數
- **WHEN** 某 email 失敗 9 次後成功登入，接著再失敗 1 次
- **THEN** 第 11 次嘗試不被鎖定

### Requirement: 後台 refresh token 可選擇以 HttpOnly cookie 交付

後台登入 MUST 接受選填的 `refresh_transport`（`body` 或 `cookie`，預設 `body`）。`cookie` 時，回應 MUST NOT 含 `refresh_token`，refresh token MUST 以 `__Secure-admin_refresh` cookie 交付，屬性 MUST 為 `HttpOnly; Secure; SameSite=Strict; Path=/api/v1/admin/auth`，`Max-Age` 等於 refresh token 有效期。`/api/v1/admin/auth/refresh` 與 `/logout` 在 body 未帶 `refresh_token` 時 MUST 讀取該 cookie，且此時 MUST 要求請求帶 `X-Admin-Refresh: 1`，否則回 401；refresh 成功時 MUST 以新 token 覆寫 cookie，logout MUST 清除 cookie。未指定 `refresh_transport` 時行為 MUST 與本變更前逐位元組相同。

#### Scenario: cookie 模式登入
- **WHEN** 以 `{"email":..., "password":..., "refresh_transport":"cookie"}` 登入成功
- **THEN** 回應 body 含 `access_token` 但不含 `refresh_token`，並帶符合上述屬性的 `Set-Cookie`

#### Scenario: cookie 模式缺少防 CSRF 標頭
- **WHEN** 只帶 cookie、未帶 `X-Admin-Refresh` 呼叫 refresh
- **THEN** 回 401，refresh token 未被輪替

#### Scenario: 預設模式不變
- **WHEN** 未帶 `refresh_transport` 登入
- **THEN** 回應含 `refresh_token` 且不帶 `Set-Cookie`
