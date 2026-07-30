## ADDED Requirements

### Requirement: 客戶端以 publishable key 宣告所代表的商家

不由平台託管的前台客戶端（商家自行撰寫的靜態站／SPA、原生 APP）SHALL 以 `X-Publishable-Key` 請求標頭宣告其代表的商家。前台 API 群組（`/api/v1/shop/*`）MUST 在該標頭有值時，以金鑰解析出的商家作為租戶上下文，且 MUST NOT 因解析失敗而回退至其他通道。

金鑰 MUST 僅由請求標頭傳遞，系統 MUST NOT 接受以查詢參數或請求 body 傳遞金鑰。

未知、格式不符或已撤銷的金鑰 MUST 一律回 404 且回應同構，MUST NOT 區分「不存在」與「已撤銷」。解析成功後的商家 MUST 依既有 `shops.status` 放行規則判定（1 服務、0 回 503、2 回 404）。

#### Scenario: 有效金鑰解析出商家

- **WHEN** 前台請求帶 `X-Publishable-Key` 為 shop A 的有效金鑰
- **THEN** 租戶上下文為 shop A，後續資料存取自動範圍化至 shop A

#### Scenario: 金鑰優先於 Host

- **WHEN** 請求帶 shop A 的有效金鑰，同時 `Host` 為已綁定 shop B 的網域
- **THEN** 租戶上下文為 shop A

#### Scenario: 已撤銷金鑰被拒且不回退

- **WHEN** 請求帶一把已撤銷的金鑰，同時 `Host` 為已綁定 shop B 的網域
- **THEN** 回應 404，MUST NOT 以 `Host` 解析為 shop B

#### Scenario: 未知金鑰與已撤銷金鑰的回應同構

- **WHEN** 分別以「從未存在的金鑰字串」與「已撤銷的金鑰」發出同一個請求
- **THEN** 兩者的狀態碼與錯誤 code 完全相同

#### Scenario: 停用商家的金鑰

- **WHEN** 請求帶 `status = 0`（停用）商家的有效金鑰
- **THEN** 回應 503

#### Scenario: 金鑰不接受查詢參數

- **WHEN** 請求以查詢參數（而非標頭）帶入有效金鑰，且 `Host` 未綁定任何站點
- **THEN** 回應 404，該查詢參數 MUST NOT 被視為租戶宣告

### Requirement: publishable key 是識別碼，不是憑證

publishable key MUST NOT 作為任何授權判定的依據。持有某商家金鑰的請求，其可存取的資料範圍 MUST 與匿名訪客造訪該商家公開網域完全相同——需要會員身分的端點仍 MUST 要求有效的會員 JWT，需要後台身分的端點仍 MUST 要求管理者 JWT 與 RBAC 判定。

金鑰 MUST 明文儲存並可於後台完整顯示（它是公開值，遮罩或雜湊儲存 MUST NOT 被採用）。系統 MUST NOT 對金鑰施加有效期或自動輪替。

金鑰 MUST NOT 擴大任何速率限制配額：同一商家的不同金鑰所發出的請求 MUST 計入同一組以 `shop_id` 為界的計數。

#### Scenario: 金鑰不取代會員認證

- **WHEN** 請求帶 shop A 的有效金鑰，但未帶會員 access token，存取需要會員身分的端點
- **THEN** 回應 401

#### Scenario: 金鑰不影響 token audience 判定

- **WHEN** 請求帶 shop A 的有效金鑰與一個 `aud = shop:B` 的會員 access token
- **THEN** 回應 401

#### Scenario: 金鑰不擴大速率限制配額

- **WHEN** 同一來源 IP 先以 shop A 的金鑰甲耗盡該商家的限流配額，再改以 shop A 的金鑰乙發出同類請求
- **THEN** 仍回 429，兩把金鑰 MUST NOT 各自擁有獨立配額

### Requirement: 金鑰生命週期與輪替

系統 SHALL 允許一家商家同時持有多把有效金鑰，以支援輪替過渡期（先產生新金鑰 → 前端完成部署 → 撤銷舊金鑰）。

金鑰值 MUST 由密碼學安全亂數產生、MUST 以 `pk_` 前綴起始、MUST NOT 可由商家識別碼或任何商家屬性推導。產生時 MUST 回傳完整金鑰值。

撤銷 MUST 即時生效，MUST NOT 等待任何快取存活時間到期。撤銷 MUST 為不可逆：已撤銷的金鑰值 MUST NOT 被重新啟用，亦 MUST NOT 被指派給任何商家。

#### Scenario: 輪替期間新舊金鑰並存

- **WHEN** shop A 已有金鑰甲，再產生金鑰乙
- **THEN** 甲與乙皆能解析出 shop A

#### Scenario: 撤銷舊金鑰後僅新金鑰有效

- **WHEN** 撤銷金鑰甲
- **THEN** 帶甲的請求回 404，帶乙的請求仍正常解析為 shop A

#### Scenario: 撤銷即時生效

- **WHEN** 金鑰甲剛被成功使用過（其解析結果已被快取），隨即被撤銷
- **THEN** 下一個帶甲的請求立即回 404，而非等待快取到期

#### Scenario: 產生的金鑰互異且帶前綴

- **WHEN** 對同一商家連續產生兩把金鑰
- **THEN** 兩個金鑰值相異，且皆以 `pk_` 起始

### Requirement: 每商家瀏覽器 origin 登記

平台 SHALL 提供每商家的 allowed origin 登記，作為瀏覽器跨源存取（CORS）的授予依據。CORS 的允許集合 MUST 為「平台環境設定清單」與「全部已登記的商家 origin」的聯集。

登記值 MUST 正規化為 `scheme://host[:port]` 形式（scheme 與 host 小寫、無路徑、無尾斜線），scheme MUST 為 `http` 或 `https`。含路徑、含萬用字元或無法正規化的輸入 MUST 回 422。origin MUST 全域唯一，重複登記 MUST 回 422。

登記與移除 MUST 即時生效，MUST NOT 需要變更環境變數或重新啟動 API。

平台自身前端（後台 SPA、平台代管的多租戶前端）的 origin 屬於平台環境設定清單，MUST NOT 登記於任一商家名下。

#### Scenario: 登記後跨源請求獲得授權

- **WHEN** 已登記 `https://ondo.example` 為 shop A 的 origin，瀏覽器自該 origin 發出請求
- **THEN** 回應帶 `Access-Control-Allow-Origin: https://ondo.example`

#### Scenario: 移除登記後不再授權

- **WHEN** 移除該 origin 的登記後，自同一 origin 再次發出請求
- **THEN** 回應 MUST NOT 帶 `Access-Control-Allow-Origin`

#### Scenario: 未登記的 origin 不獲授權

- **WHEN** 瀏覽器自未登記且不在平台清單中的 origin 發出請求
- **THEN** 回應 MUST NOT 帶 `Access-Control-Allow-Origin`

#### Scenario: 重複登記被拒

- **WHEN** 將已登記於 shop A 的 origin 登記給 shop B
- **THEN** 回應 422，原登記不變

#### Scenario: 非法 origin 被拒

- **WHEN** 登記 `https://ondo.example/path`、`*.ondo.example` 或 `ftp://ondo.example`
- **THEN** 回應 422

### Requirement: CORS preflight 不依賴租戶上下文

瀏覽器的 `OPTIONS` preflight 不會攜帶自訂標頭的值，因此 MUST NOT 要求 `X-Publishable-Key` 存在。preflight 的放行判定 MUST 僅依 `Origin` 是否屬於允許集合。

回應的 `Access-Control-Allow-Headers` MUST 包含 `X-Publishable-Key`，否則瀏覽器將攔截後續實際請求。

#### Scenario: preflight 未帶金鑰仍放行

- **WHEN** 瀏覽器自已登記的 origin 送出 `OPTIONS` preflight（依規範不含 `X-Publishable-Key` 的值）
- **THEN** 回應 204 並帶 `Access-Control-Allow-Origin` 與含 `X-Publishable-Key` 的 `Access-Control-Allow-Headers`

#### Scenario: 未登記 origin 的 preflight 不放行

- **WHEN** 瀏覽器自未登記的 origin 送出 `OPTIONS` preflight
- **THEN** 回應 MUST NOT 帶任何 `Access-Control-Allow-*` 標頭

### Requirement: 金鑰與 origin 管理的授權分層

金鑰管理（產生／列出／撤銷）SHALL 為商家層能力，由 shop 範圍化 RBAC 判定，且 MUST 受既有 cross-shop access guard 管轄——對非所屬商家的金鑰操作 MUST 回 403。

allowed origin 的登記與移除 SHALL 為平台層能力，僅持有平台角色且具對應權限者可呼叫；商家層角色（含 `merchant_owner`）呼叫 MUST 回 403。理由：origin 是競爭的全域命名空間，與站點網域綁定同性質。

#### Scenario: 商家管理員產生自店金鑰

- **WHEN** shop A 的 `merchant_owner` 對 shop A 產生金鑰
- **THEN** 成功，回應含完整金鑰值

#### Scenario: 商家管理員跨店操作金鑰被拒

- **WHEN** shop A 的 `merchant_owner` 列出或撤銷 shop B 的金鑰
- **THEN** 回應 403

#### Scenario: 商家角色登記 origin 被拒

- **WHEN** shop A 的 `merchant_owner` 為 shop A 登記 allowed origin
- **THEN** 回應 403

#### Scenario: 平台角色登記 origin

- **WHEN** 持有 `shop.manage_client_origins` 的平台角色為 shop A 登記 origin
- **THEN** 登記成功並即時生效
