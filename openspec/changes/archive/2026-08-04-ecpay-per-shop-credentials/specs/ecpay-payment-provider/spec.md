## MODIFIED Requirements

### Requirement: ECPay AIO provider implements the payment provider abstraction

系統 MUST 提供一個名為 `ecpay` 的 `Provider` 實作，涵蓋綠界全方位金流（AIO）的**真實線路形狀**：`AioCheckOut V5` 的欄位集、`CheckMacValue` 檢查碼、以及付款結果通知（`ReturnURL`）的解析與回應約定。它 MUST 註冊進既有的 provider registry，並且 MUST NOT 需要修改 `payment.Service.HandleWebhook` 的控制流程——**每商家憑證解析**（change ecpay-per-shop-credentials）例外於這條保證的是路由本身，而不是控制流程：`ecpay` 的憑證是逐商家解析的，webhook 驗簽在資訊上必須先知道要用哪個商家的密鑰，因此 `ecpay` 的 webhook 額外使用一個帶 shop id 的路由形狀（見 spec `payment-integration`）；`HandleWebhook` 內部由 `(provider, provider_reference)` 定位付款記錄、CAS 推進狀態的邏輯逐字不變。發起付款時 MUST 送出綠界要求的全部必填欄位，且 `PaymentType` MUST 固定為 `aio`、`EncryptType` MUST 固定為 `1`（SHA256）。

#### Scenario: 發起付款產生完整的綠界欄位集
- **WHEN** 對一張 TWD 訂單以 `ecpay` provider 發起付款
- **THEN** 回傳的表單欄位集包含 `MerchantID`、`MerchantTradeNo`、`MerchantTradeDate`、`PaymentType`、`TotalAmount`、`TradeDesc`、`ItemName`、`ReturnURL`、`ChoosePayment`、`EncryptType`、`CheckMacValue`，且 `PaymentType` 為 `aio`、`EncryptType` 為 `1`

#### Scenario: 表單送出方式是 POST
- **WHEN** 對一張訂單以 `ecpay` provider 發起付款
- **THEN** 回傳的導向資訊宣告方法為 `POST`、目標為設定的收銀台網址

## ADDED Requirements

### Requirement: ECPay credentials are resolved per shop, not from one platform-wide configuration

`ecpay` provider 的 `MerchantID`／`HashKey`／`HashIV`／`ChoosePayment` MUST 依 `InitiatePayment` 呼叫者提供的 `shopID`（發起付款）或 webhook URL 中的 shop id（驗證通知）解析，MUST NOT 有任何路徑讀取單一平台層共用設定作為憑證來源。尚未設定憑證的商家嘗試使用 `ecpay` provider 發起付款時，MUST 明確回報錯誤，MUST NOT 回退到平台範例憑證或任何其他商家已設定的憑證，MUST NOT 建立付款記錄或產生任何綠界欄位。

驗證通知時，MUST 使用 webhook URL 中 shop id 對應的商家憑證計算並比對 `CheckMacValue`；使用錯誤商家的憑證驗證屬於另一商家簽出的通知內容時，MUST 驗證失敗——兩個商家的密鑰互不相通，一個商家的 URL 不能被拿來驗證另一個商家的通知。

#### Scenario: 未設定憑證的商家無法以 ecpay 發起付款
- **WHEN** 商家尚未設定自己的 ECPay 憑證，卻對自己一張訂單以 `ecpay` provider 發起付款
- **THEN** 回報錯誤，不建立付款記錄，也不產生任何綠界欄位

#### Scenario: 兩個商家各自的簽章互不相通
- **WHEN** 以商家 A 的 `HashKey`/`HashIV` 對一組欄位計算出合法的 `CheckMacValue`，將整組欄位當作通知投遞到商家 B 的 webhook 路徑
- **THEN** 驗證失敗，不更新任何付款或訂單資料

#### Scenario: 商家各自的憑證各自生效
- **WHEN** 商家 A 與商家 B 分別設定了不同的 `MerchantID`，各自對自己一張訂單以 `ecpay` provider 發起付款
- **THEN** 兩者回傳的表單欄位集裡的 `MerchantID` 分別等於各自設定的值
