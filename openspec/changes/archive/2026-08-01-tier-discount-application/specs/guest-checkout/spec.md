## MODIFIED Requirements

### Requirement: 訪客結帳以 email 建立或沿用無密碼顧客身分

系統 SHALL 提供未認證的訪客結帳端點（`POST /api/v1/shop/guest/checkout`），接受 email、品項清單與收件資訊，於單次請求內完成下單。商家上下文 MUST 依既有前台租戶解析規則決定（spec multi-tenancy：publishable key 優先、否則 `Host`）；該端點 MUST NOT 額外要求 publishable key。

系統 MUST 以 `(shop_id, email)` 找出既有顧客身分；不存在時 MUST 建立一筆 `password_hash` 為 NULL、`status = 1` 的身分。email MUST 為必填且經格式驗證；系統 MUST NOT 將請求中的收件電話寫入 `members.phone`（該值屬於訂單收件資訊，不是身分屬性）。

建立的訂單 MUST 以該身分的 `member_id` 記錄，MUST NOT 以任何「無會員訂單」的形式存在。訂單建立後的一切後續機制（點數核發、等級折扣、金流、物流、商家後台查詢）MUST 與會員下單完全相同。

回應在「email 為全新」「email 已是本店無密碼身分」「email 已是本店有密碼會員」三種情形下 MUST 具備相同的形狀與狀態碼，且 MUST NOT 洩漏該 email 在本店**是否已設定密碼**。**已知且已接受的例外：回應中的訂單金額（`discount_amount`／`discount_percent`／`member_tier_name`／`total_amount`）會因該身分目前所屬的會員等級而異**，因此對已知該 email 的請求者揭露了「這個 email 在本商家屬於哪一個等級」。此揭露 MUST 被視為套用折扣的必然結果（顧客必須看得到自己付多少），MUST NOT 以隱藏金額的方式「修正」；其成本由「必須真的建立一筆訂單才看得到」以及本 spec 既有的兩條速率限制界定。**帶冪等鍵的重送所回放的回應（spec checkout-idempotency）MUST 與首次同構**：狀態碼同為 `201`，主體形狀相同，MUST NOT 因為它是回放而改變狀態碼或主體結構。

回放時系統 MUST **重新簽發**訂單存取權杖，MUST NOT 回傳原本簽發的那一枚——原本那一枚不被儲存（它是 bearer 憑證），且其有效期可能已耗盡，回傳一枚已過期的權杖會構成一個形狀像成功、實際無用的回應。重新簽發的權杖 MUST 指向同一筆訂單、具備與原本完全相同的存取範圍，MUST NOT 因回放而取得任何額外能力；回應中的有效期 MUST 描述本次回應所附的那一枚權杖。

#### Scenario: 全新 email 的訪客結帳

- **WHEN** 以本店未使用過的 email 發出訪客結帳請求，品項可購買且庫存充足
- **THEN** 建立一筆 `password_hash IS NULL` 的顧客身分與一筆掛在該身分名下的訂單，回應 201，且該訂單 `discount_amount = 0`、`member_tier_name` 為 `null`（全新身分尚未核發過任何點數，因此不屬於任何等級）

#### Scenario: 回頭訪客沿用同一身分

- **WHEN** 同一 email 第二次發出訪客結帳請求
- **THEN** 不建立新的顧客身分，第二筆訂單掛在同一個 `member_id` 上

#### Scenario: 回頭訪客套用其已累積的等級折扣

- **WHEN** 某 email 的無密碼身分先前已因付款訂單累積點數而升上一個帶折扣的等級，該 email 再次以訪客身分結帳
- **THEN** 該筆新訂單 MUST 套用該等級的折扣，其 `discount_percent`／`member_tier_name`／`discount_amount` 與該顧客以會員身分登入結帳所得的結果完全相同

#### Scenario: email 已是本店有密碼會員

- **WHEN** 以一個已註冊且已設定密碼的本店 email 發出訪客結帳請求
- **THEN** 訂單建立成功並掛在該會員名下（含該會員目前等級的折扣），回應形狀與全新 email 的情形相同，MUST NOT 揭露該 email 已設定密碼，且 MUST NOT 回傳任何可存取該會員其他訂單、點數餘額或個資的憑證

#### Scenario: 收件電話不寫入身分

- **WHEN** 訪客結帳的收件資訊含電話，且該電話已為本店另一位顧客的 `members.phone`
- **THEN** 結帳成功，MUST NOT 因電話唯一約束而失敗，該電話僅存於訂單收件資訊中

#### Scenario: 缺少 email 被拒

- **WHEN** 訪客結帳請求未附 email 或 email 格式不合法
- **THEN** 回 422，不建立任何身分或訂單

#### Scenario: 同鍵回放的回應與首次同構

- **WHEN** 一次帶冪等鍵的訪客結帳成功後，以相同鍵與相同主體重送
- **THEN** 回 201，主體含相同的訂單（含相同的折扣快照）與一枚可用的訂單存取權杖，其形狀與第一次完全相同，MUST NOT 建立第二筆訂單或第二個顧客身分

#### Scenario: 回放重新簽發訂單存取權杖

- **WHEN** 一次帶冪等鍵的訪客結帳成功後，以相同鍵與主體重送
- **THEN** 回應中的訂單存取權杖為重新簽發的一枚，其可存取範圍仍嚴格限於同一筆訂單（可讀該單、可對該單發起付款、可讀該單出貨），MUST NOT 能列出訂單歷史、取消訂單或存取其他訂單

## ADDED Requirements

### Requirement: 訪客結帳不提供結帳前的折扣預估

系統 MUST NOT 提供任何讓未認證的請求者以 email 查詢該身分折扣率或等級的端點或欄位。訪客要得知折抵金額，唯一的途徑 MUST 是實際建立訂單後讀取回應中的訂單金額。

理由 MUST 記錄於此以免日後被「順手加一個預估端點比較方便」推翻：這樣的端點會把上一項需求所記錄的、已接受的等級揭露，從「必須真的建立一筆訂單（扣庫存、留下商家看得見的訂單、受速率限制）」降級為「送一個請求就有答案」，代價與其換到的一個畫面預覽不成比例。

已認證的會員 MUST 改由既有的 `GET /api/v1/shop/points`（回應含目前等級與其 `discount_percent`）自行預估，系統 MUST NOT 為此另設端點。

#### Scenario: 沒有以 email 查詢折扣的途徑

- **WHEN** 未認證的請求者嘗試以任何既有端點、以一個 email 取得該身分的等級或折扣率
- **THEN** 系統 MUST NOT 提供這樣的端點；該資訊只能經由實際建立訂單後的訂單金額間接得知

#### Scenario: 會員以既有點數端點自行預估

- **WHEN** 已認證會員呼叫 `GET /api/v1/shop/points`
- **THEN** 回應含其目前等級與 `discount_percent`，足以在結帳前以與伺服器相同的整數規則預估折抵金額
