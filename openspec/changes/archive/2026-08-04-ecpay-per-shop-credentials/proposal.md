## Why

綠界（ECPay）的商家憑證（`MerchantID`/`HashKey`/`HashIV`/`ChoosePayment`）今天是平台層設定（`ECPAY_*` 環境變數），啟動時建成單一一份 `ecpay.Provider` 實例，全平台共用。這是 `ecpay-payment-provider` change 自己的 design 文件已明說的已知限制——「**必須在第二個商家上線前解決**」，否則兩個商家的錢會進同一個綠界帳號。本 change 把 ECPay 憑證改成每個商家各自設定，並解掉「每商家一把密鑰」會立刻撞上的 webhook 驗簽雞生蛋問題。

## What Changes

- `payment.Service` 的 provider 解析從「一個 provider 名稱對應一份固定實例」改為在 `InitiatePayment` 當下依 `shopID` 解析出該商家自己的憑證（`InitiateRequest.ShopID` 早已存在但至今沒有任何程式讀它）。**不得**在 handler 或 Service 裡對 provider 名稱做 `if providerName == "ecpay"` 特判——這正是 `ecpay-payment-provider` design D1 已經守住的界線；per-shop 解析必須做在介面層（一個新的、provider 可選擇實作的介面），而不是呼叫端的名稱分支。
- 新增商家自有資料：ECPay `MerchantID`／`HashKey`／`HashIV`／`ChoosePayment` 成為每商家可自行設定的值，其中 `HashKey`／`HashIV` 是**真正的密鑰**（不同於「識別碼不是憑證」的 publishable key），加密後存進租戶資料庫——這是本系統第一次把商家的密鑰存進資料庫。
- 新增後台 API：商家設定／更新／檢視（密鑰欄位只寫不讀，檢視只回「是否已設定」）自己的 ECPay 憑證，守衛是新的權限節點，**只授予 `merchant_owner`**（比照 `publishable_key.*` 的理由：設定金流帳號是整合動作，不是內容編輯，不給 `editor`）。
- **Webhook 路由新增一個 additive 的形狀**：`POST /api/v1/webhooks/payments/ecpay/{shopID}`——因為每商家一把密鑰時，驗簽前必須先知道要用哪一把，而 shop id 得從某個地方先取得。這個 shop id 帶在我們自己產生的 `ReturnURL` 裡（綠界只會原樣回傳），所以是我們自己控制、不需要綠界任何配合。**`mock` provider 完全不受影響**，既有的 `POST /api/v1/webhooks/payments/{provider}` 路由與行為原樣保留。這個 shop id 在驗簽之前是未經驗證的，但這不構成安全問題——它只決定「拿哪一把鑰匙去驗」，用錯商家的 URL 只會導致用錯誤的密鑰驗簽、必然驗簽失敗，不會讓任何未驗證的東西被信任。
- `CheckoutURL`（收銀台指向模擬環境或綠界正式/測試站）維持平台層設定：這是「整個部署跟哪個環境對話」的維運開關，不是商家身分。
- 上線方式：尚未設定 ECPay 憑證的商家**無法**使用 `ecpay` provider（明確失敗），**不**回退到平台預設或其他商家的憑證。今天平台層 `ECPAY_MERCHANT_ID`/`ECPAY_HASH_KEY`/`ECPAY_HASH_IV`/`ECPAY_CHOOSE_PAYMENT` 這幾個環境變數的值其實是**綠界官方公開的測試範例憑證**（文件範例向量用的就是它們），並非任何真正商家的秘密——目前沒有任何商家在正式環境使用 `ecpay` provider，因此本 change 對 webhook 路由是**乾淨切齊**，不需要維護新舊兩條路由並存的 provider 特判分支。
- 明確排除：退款人工流程、真實（非模擬）綠界端點驗證、Gmail 寄信額度與寄件人身分——這些是另外追蹤的 ONDO 上線阻塞項，與本 change 無關。

## Capabilities

### New Capabilities
- `shop-payment-credentials`：每商家的付款商設定憑證儲存（加密）、後台管理（設定／更新／遮罩檢視）與其權限節點。

### Modified Capabilities
- `ecpay-payment-provider`：`InitiatePayment` 與 webhook 驗簽改為依商家解析 `MerchantID`/`HashKey`/`HashIV`/`ChoosePayment`，不再讀取單一平台設定；`CheckMacValue` 演算法、`1|OK` 回應約定、`OutcomePending` 語意等既有行為不變。
- `payment-integration`：webhook 路由的 URL 形狀新增 provider+shop 的變體（`/api/v1/webhooks/payments/{provider}/{shopID}`，目前只有 `ecpay` 使用），該路由仍然 MUST NOT 要求會員或商家 JWT、MUST NOT 依賴租戶網域解析——身分驗證仍然完全由 provider 的簽章機制完成，URL 中的 shop id 本身不構成信任邊界，只決定要用哪一把密鑰驗證。

## Impact

- **程式碼**：`internal/payment/service.go`（provider 解析機制）、`internal/payment/ecpay/ecpay.go`（`Config` → 依商家解析）、`internal/payment/provider.go`（新增一個可選介面）、`internal/httpapi/payment.go` + `internal/httpapi/router.go`（webhook 新路由）、`internal/app/wire.go`（provider 建構方式）、`internal/ent/schema`（新增加密憑證表）、`internal/config/config.go`（新增加密金鑰環境變數；既有 `ECPAY_MERCHANT_ID`/`ECPAY_HASH_KEY`/`ECPAY_HASH_IV`/`ECPAY_CHOOSE_PAYMENT` 移除，`ECPAY_CHECKOUT_URL`/`ECPAY_CLIENT_BACK_URL`/`ECPAY_RETURN_URL` 保留）、`internal/seed`（新增權限節點、demo 商家的憑證種子資料）、`admin/`（新的商家 ECPay 設定畫面）。
- **API 契約**：`api/openapi/openapi.yaml` 與 `api/openapi/v1-surface.txt` 新增 webhook 新路由形狀與後台憑證管理端點——皆為 additive。
- **資料**：新表儲存每商家的加密 ECPay 憑證；`payments`/`orders` 既有表不受影響。
- **維運**：新增一個必要的加密金鑰環境變數；既有 `ECPAY_*` 環境變數的角色縮減（憑證類移除，環境/收銀台類保留）。
