## MODIFIED Requirements

### Requirement: 訪客結帳以 email 建立或沿用無密碼顧客身分

系統 SHALL 提供未認證的訪客結帳端點（`POST /api/v1/shop/guest/checkout`），接受 email、品項清單與收件資訊，於單次請求內完成下單。商家上下文 MUST 依既有前台租戶解析規則決定（spec multi-tenancy：publishable key 優先、否則 `Host`）；該端點 MUST NOT 額外要求 publishable key。

系統 MUST 以 `(shop_id, email)` 找出既有顧客身分；不存在時 MUST 建立一筆 `password_hash` 為 NULL、`status = 1` 的身分。email MUST 為必填且經格式驗證；系統 MUST NOT 將請求中的收件電話寫入 `members.phone`（該值屬於訂單收件資訊，不是身分屬性）。

建立的訂單 MUST 以該身分的 `member_id` 記錄，MUST NOT 以任何「無會員訂單」的形式存在。訂單建立後的一切後續機制（點數核發、金流、物流、商家後台查詢）MUST 與會員下單完全相同。

回應 MUST 在「email 為全新」「email 已是本店無密碼身分」「email 已是本店有密碼會員」三種情形下同構，MUST NOT 洩漏該 email 在本店的身分狀態。**帶冪等鍵的重送所回放的回應（spec checkout-idempotency）MUST 與上述三者同構**：狀態碼同為 `201`，主體形狀相同，MUST NOT 因為它是回放而改變狀態碼或主體結構。

回放時系統 MUST **重新簽發**訂單存取權杖，MUST NOT 回傳原本簽發的那一枚——原本那一枚不被儲存（它是 bearer 憑證），且其有效期可能已耗盡，回傳一枚已過期的權杖會構成一個形狀像成功、實際無用的回應。重新簽發的權杖 MUST 指向同一筆訂單、具備與原本完全相同的存取範圍，MUST NOT 因回放而取得任何額外能力；回應中的有效期 MUST 描述本次回應所附的那一枚權杖。

#### Scenario: 全新 email 的訪客結帳

- **WHEN** 以本店未使用過的 email 發出訪客結帳請求，品項可購買且庫存充足
- **THEN** 建立一筆 `password_hash IS NULL` 的顧客身分與一筆掛在該身分名下的訂單，回應 201

#### Scenario: 回頭訪客沿用同一身分

- **WHEN** 同一 email 第二次發出訪客結帳請求
- **THEN** 不建立新的顧客身分，第二筆訂單掛在同一個 `member_id` 上

#### Scenario: email 已是本店有密碼會員

- **WHEN** 以一個已註冊且已設定密碼的本店 email 發出訪客結帳請求
- **THEN** 訂單建立成功並掛在該會員名下，回應與全新 email 的情形同構，MUST NOT 揭露該 email 已有帳號，且 MUST NOT 回傳任何可存取該會員其他訂單或個資的憑證

#### Scenario: 收件電話不寫入身分

- **WHEN** 訪客結帳的收件資訊含電話，且該電話已為本店另一位顧客的 `members.phone`
- **THEN** 結帳成功，MUST NOT 因電話唯一約束而失敗，該電話僅存於訂單收件資訊中

#### Scenario: 缺少 email 被拒

- **WHEN** 訪客結帳請求未附 email 或 email 格式不合法
- **THEN** 回 422，不建立任何身分或訂單

#### Scenario: 同鍵回放的回應與首次同構

- **WHEN** 一次帶冪等鍵的訪客結帳成功後，以相同鍵與相同主體重送
- **THEN** 回 201，主體含相同的訂單與一枚可用的訂單存取權杖，其形狀與第一次完全相同，MUST NOT 建立第二筆訂單或第二個顧客身分

#### Scenario: 回放重新簽發訂單存取權杖

- **WHEN** 一次帶冪等鍵的訪客結帳成功後，以相同鍵與主體重送
- **THEN** 回應中的訂單存取權杖為重新簽發的一枚，其可存取範圍仍嚴格限於同一筆訂單（可讀該單、可對該單發起付款、可讀該單出貨），MUST NOT 能列出訂單歷史、取消訂單或存取其他訂單
