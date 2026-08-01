# guest-checkout Specification

## Purpose
TBD - created by archiving change guest-checkout. Update Purpose after archive.
## Requirements
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

### Requirement: 訪客結帳複用既有結帳路徑，不得存在第二條扣庫存實作

訪客結帳 MUST 呼叫與會員結帳完全相同的結帳服務方法完成建單與扣庫存。系統 MUST NOT 為訪客提供任何略過或重複實作「條件式原子扣庫存」的路徑。

spec order-management 的「Checkout stock deduction is concurrency-safe and never oversells」全部要求 MUST 原樣適用於訪客結帳，包含：庫存 MUST NOT 為負、任一品項庫存不足時整筆失敗且已扣減的庫存 MUST 一併回滾。

訪客結帳的品項 MUST 先以單一資料庫交易整批取代該身分 active 購物車的內容（刪除既有品項後寫入新品項），再進入結帳。該取代 MUST 為原子操作：任一品項驗證失敗時，購物車內容 MUST 與請求前完全相同。

#### Scenario: 併發訪客結帳搶購同一 SKU 不超賣

- **WHEN** 某 SKU `stock_qty = 5`，10 位不同 email 的訪客同時各自對數量為 1 的該 SKU 發起訪客結帳
- **THEN** 恰好 5 筆成功、5 筆因庫存不足失敗（422），該 SKU 最終 `stock_qty = 0`（不為負數），建立的訂單數恰為 5

#### Scenario: 訪客與會員併發搶購同一 SKU 共用同一份庫存

- **WHEN** 會員結帳與訪客結帳同時搶購同一個庫存有限的 SKU
- **THEN** 兩者共用同一份庫存，成功總數不超過扣減前的庫存量

#### Scenario: 品項驗證失敗不留下半套購物車

- **WHEN** 訪客結帳的品項清單中，第一項可購買、第二項的 SKU 不屬於本店
- **THEN** 回 422，不建立訂單，該身分的購物車內容與請求前完全相同（不含第一項）

#### Scenario: 訪客結帳取代既有購物車內容

- **WHEN** 某身分已有含品項的 active 購物車，以該身分的 email 發起訪客結帳並送出不同的品項清單
- **THEN** 訂單內容僅為本次請求送出的品項，先前購物車內的品項 MUST NOT 出現在訂單中

### Requirement: 訂單存取 token 只能存取單一訂單

訪客結帳成功時，系統 MUST 一併簽發訂單存取 token，其可存取範圍 MUST 嚴格限於該次建立的訂單。

該 token MUST 為短效且 MUST NOT 可刷新；系統 MUST NOT 為訪客結帳簽發會員 access token 或 refresh token。token MUST 僅由 `Authorization: Bearer` 標頭傳遞，系統 MUST NOT 接受以查詢參數傳遞。

以該 token 授權的端點 MUST NOT 接受來自路徑、查詢參數或請求 body 的訂單識別碼——目標訂單 MUST 完全由 token 自身決定。

該 token MUST 能：讀取該筆訂單、對該筆訂單發起付款、讀取該筆訂單的出貨資訊。該 token MUST NOT 能：列出訂單歷史、取消訂單、讀取或變更顧客身分資料、讀取或兌換點數、換發任何其他 token。

#### Scenario: 以訂單存取 token 讀取該筆訂單

- **WHEN** 以訪客結帳回傳的 token 呼叫 `GET /api/v1/shop/guest/order`
- **THEN** 回傳該筆訂單的完整內容（含品項與收件資訊）

#### Scenario: 以訂單存取 token 發起付款

- **WHEN** 以該 token 呼叫 `POST /api/v1/shop/guest/order/payments`
- **THEN** 建立付款紀錄並回傳 `redirect_url`，行為與會員對自己訂單發起付款相同

#### Scenario: 訂單存取 token 無法列出訂單歷史

- **WHEN** 以訂單存取 token 呼叫會員自助訂單列表端點
- **THEN** 回 401

#### Scenario: 訂單存取 token 無法取消訂單

- **WHEN** 以訂單存取 token 嘗試取消訂單
- **THEN** 請求被拒（該 token 不存在可用於取消的端點，且以其呼叫會員取消端點回 401），訂單狀態不變

#### Scenario: 訂單存取 token 無法存取其他訂單

- **WHEN** 同一 email 先後完成兩次訪客結帳，以第一筆訂單的 token 存取
- **THEN** 僅能取得第一筆訂單，MUST NOT 取得第二筆

#### Scenario: 過期的訂單存取 token

- **WHEN** 以已超過有效期的訂單存取 token 呼叫任一訪客端點
- **THEN** 回 401

#### Scenario: 訂單存取 token 不接受查詢參數

- **WHEN** 以查詢參數（而非 `Authorization` 標頭）帶入有效的訂單存取 token
- **THEN** 回 401

### Requirement: 訂單存取 token 與會員／後台 token 完全隔離

訂單存取 token 的 audience MUST 與會員 token（`shop:{shop_id}`）、後台 token（`admin`）、預覽 token（`preview`）互斥，且 MUST 綁定簽發時的商家。

任一 token 家族的值用於另一家族的驗證路徑 MUST 一律回 401，且 MUST NOT 因值本身有效而取得任何存取權。

#### Scenario: 會員 token 無法存取訪客端點

- **WHEN** 以有效的會員 access token 呼叫 `GET /api/v1/shop/guest/order`
- **THEN** 回 401

#### Scenario: 跨商家使用訂單存取 token

- **WHEN** 以 shop A 簽發的訂單存取 token，在 shop B 的商家上下文呼叫訪客端點
- **THEN** 回 401

#### Scenario: 後台 token 無法存取訪客端點

- **WHEN** 以有效的後台 access token 呼叫訪客端點
- **THEN** 回 401

### Requirement: 訪客結帳的速率限制與停用顧客的處置

`POST /api/v1/shop/guest/checkout` MUST 施加速率限制，鍵值 MUST 納入已解析的 `shop_id`：以「shop + IP」為界的寬鬆規則與以「shop + IP + email」為界的嚴格規則並行套用，任一超限即 MUST 回 429 並帶 `Retry-After` header。不同商家的計數 MUST 互相獨立。速率限制器故障時 MUST fail-open。

以一個 `status = 0`（已停用）顧客身分的 email 發起訪客結帳 MUST 被拒絕，MUST NOT 建立訂單——停用意圖 MUST NOT 因不需要密碼的路徑而被繞過。

#### Scenario: 同一 email 大量建單被限流

- **WHEN** 同一來源 IP 對同一商家的同一 email 在門檻視窗內發起的訪客結帳次數超過嚴格規則上限
- **THEN** 回 429 並帶 `Retry-After` header，不建立訂單

#### Scenario: 跨商家的訪客結帳限流互不干擾

- **WHEN** shop A 的某個「IP + email」組合已達上限
- **THEN** 同一「IP + email」對 shop B 的訪客結帳 MUST NOT 被計入 shop A 的計數

#### Scenario: 停用顧客無法以訪客結帳下單

- **WHEN** 以一個 `status = 0` 的本店顧客身分的 email 發起訪客結帳
- **THEN** 回 422，不建立訂單，不變更任何庫存

### Requirement: 訪客訂單於認領後歸屬不變

訪客訂單自建立起即掛在顧客身分的 `member_id` 上。當該 email 日後經註冊認領該身分（spec authentication）後，系統 MUST 不需任何資料搬移即讓先前的訪客訂單與其累積點數出現在該會員的自助查詢結果中。

訂單的 `member_id` MUST NOT 因認領而改變。

#### Scenario: 認領後訪客訂單出現在訂單歷史

- **WHEN** 某 email 完成訪客結帳後，以同一 email 於同一商家註冊
- **THEN** `GET /api/v1/shop/orders` 回傳該筆訪客訂單，其 `member_id` 與訪客結帳當下相同

#### Scenario: 認領後訪客訂單的點數已在餘額中

- **WHEN** 訪客訂單付款成功已核發點數，該 email 隨後註冊認領該身分
- **THEN** `GET /api/v1/shop/points` 回傳的餘額包含該筆核發

#### Scenario: 認領後可取消先前的訪客訂單

- **WHEN** 認領完成後，會員以自己的 access token 取消先前那筆未付款未出貨的訪客訂單
- **THEN** 取消成功，庫存歸還（與一般會員訂單的取消行為完全相同）

### Requirement: 訪客結帳支援與會員結帳相同的訂單附加輸入

訪客結帳請求（`POST /api/v1/shop/guest/checkout`）MUST 支援與會員結帳完全相同的三項選填輸入：每個品項的贈禮訊息（卡片留言）、整筆訂單的運送方式、整筆訂單的指定配送日。

三者的驗證規則、錯誤狀態碼與錯誤指標 MUST 與會員路徑完全相同（spec order-management、spec shopping-cart），系統 MUST NOT 為訪客路徑另訂一套規則或另寫一份實作。訪客結帳送出的品項在整批取代購物車時，同一 SKU 但贈禮訊息不同的品項 MUST 維持為獨立品項，MUST NOT 被合併。

訪客訂單的金額組成（`subtotal_amount`／`shipping_fee`／`total_amount`）與點數核發基準 MUST 與會員訂單完全相同。

#### Scenario: 訪客結帳附帶運送方式與指定配送日

- **WHEN** 訪客結帳請求附帶一個本店啟用中的運送方式識別碼與一個合法的指定配送日
- **THEN** 訂單建立成功，其 `shipping_fee` 為該運送方式的固定運費、`total_amount` 為品項小計加運費，指定配送日可於訂單詳情讀回

#### Scenario: 訪客結帳的品項各自附帶贈禮訊息

- **WHEN** 訪客結帳請求送出同一個 SKU 的兩個品項，各附不同的贈禮訊息
- **THEN** 訂單中產生兩個獨立的訂單品項，各自保有自己的贈禮訊息，其數量 MUST NOT 被合併

#### Scenario: 訪客路徑的驗證錯誤與會員路徑一致

- **WHEN** 訪客結帳請求附帶一個已停用的運送方式識別碼
- **THEN** 回 422，錯誤指標與會員結帳遇到同一問題時完全相同，不建立任何訂單與身分以外的資料

#### Scenario: 訪客訂單的應付總額即為請款金額

- **WHEN** 訪客以訂單存取 token 對一筆含運費的訂單發起付款
- **THEN** 請款金額為該訂單的 `total_amount`（含運費），MUST NOT 為品項小計

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

