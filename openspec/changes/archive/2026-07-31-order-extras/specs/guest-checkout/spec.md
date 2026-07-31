## ADDED Requirements

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
