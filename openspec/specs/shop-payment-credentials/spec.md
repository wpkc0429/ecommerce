# shop-payment-credentials Specification

## Purpose
TBD - created by archiving change ecpay-per-shop-credentials. Update Purpose after archive.
## Requirements
### Requirement: Shop-scoped payment provider credentials are stored encrypted

系統 MUST 提供一個 shop-scoped 資料表儲存每個商家自己的付款商憑證（本 change 唯一消費者是 `ecpay`：`MerchantID`／`HashKey`／`HashIV`／`ChoosePayment`），`(shop_id, provider)` 組合 MUST 唯一。`HashKey`／`HashIV` 屬於密鑰，MUST 加密後存放，MUST NOT 以明文存於資料庫。`MerchantID`／`ChoosePayment` 不含密鑰性質，可明文存放。

加密金鑰來自環境設定，若該設定在啟動時缺失或格式不正確，系統 MUST 拒絕啟動，MUST NOT 以未加密或金鑰缺失的狀態接受任何請求。

#### Scenario: 重複設定同一 provider 的憑證是更新而非新增
- **WHEN** 商家對自己已設定過 `ecpay` 憑證再次呼叫設定 API
- **THEN** 既有那一列被更新，不產生第二列

#### Scenario: 加密金鑰缺失或格式錯誤時服務拒絕啟動
- **WHEN** 啟動時憑證加密金鑰的環境設定未提供，或提供但長度/編碼不正確
- **THEN** 服務啟動失敗，不接受任何請求

### Requirement: Merchant can manage its own ECPay credentials without ever reading the secret back

商家後台 MUST 提供設定/更新自己商家 ECPay 憑證的 API，以及檢視 API，兩者 MUST 依既有三層 RBAC 判定，需要新的權限節點 `payment_credential.manage`；跨商家操作 MUST 依既有 cross-shop access guard 回 403。

檢視 API 的回應 MUST NOT 包含 `HashKey`／`HashIV` 的明文或任何片段——只能表達「是否已設定」（布林值）；`MerchantID`／`ChoosePayment` 可於檢視回應中以明文呈現。設定/更新 API 對密鑰欄位 MUST 是只寫操作：呼叫者送入新值即覆蓋，系統 MUST NOT 提供任何讀出已存密鑰明文或片段的路徑。

該權限節點 MUST ONLY 授予 `merchant_owner`，MUST NOT 授予 `editor`——設定金流帳號憑證是整合操作而非內容編輯。

#### Scenario: 檢視回應不含密鑰明文
- **WHEN** 具 `payment_credential.manage` 權限者檢視自己商家已設定的 ECPay 憑證
- **THEN** 回應包含 `merchant_id`、`choose_payment`，以及兩個表示「是否已設定」的布林欄位，回應位元組中不含任何密鑰字元

#### Scenario: editor 不具備此權限
- **WHEN** 只具有 `editor` 角色（未含 `payment_credential.manage`）的使用者呼叫設定或檢視 API
- **THEN** 回 403

#### Scenario: 跨商家操作被拒
- **WHEN** 僅屬 shop A 的使用者對 shop B 呼叫設定或檢視 API
- **THEN** 回 403

#### Scenario: 商家設定憑證後即可用該憑證發起付款
- **WHEN** 商家透過設定 API 寫入自己的 ECPay 憑證後，對一張自己的訂單以 `ecpay` provider 發起付款
- **THEN** 發起成功，回傳的欄位集使用該商家剛設定的 `MerchantID`/`ChoosePayment` 計算而成
