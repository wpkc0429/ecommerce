## Context

`internal/payment/ecpay` 是這個系統遇到的第一家真實金流商，`ecpay-payment-provider` change 把 `Provider` 介面撞開了三處（`RedirectForm`／`WebhookAck`／`OutcomePending`），但憑證本身從一開始就是**平台層**設定：`internal/config/config.go` 讀 `ECPAY_MERCHANT_ID`/`ECPAY_HASH_KEY`/`ECPAY_HASH_IV`，`internal/app/wire.go` 用它建出**一份** `ecpay.Provider`，登記進 `payment.Service.Providers["ecpay"]`——全平台商家共用同一個綠界帳號。這件事在 `ecpay.go` 的 `Config` 文件註解裡已經寫死：「this MUST be solved before a second merchant goes live — the fix is encrypted per-shop credentials resolved from `InitiateRequest.ShopID`, which needs a key management story this system does not yet have」。`InitiateRequest.ShopID` 這個欄位當時就已經加了，只是沒有任何程式讀它。

查證發現一個重要的事實，決定了本 change 不需要處理「遷移既有商家的真實密鑰」這件事：今天 `ECPAY_MERCHANT_ID`/`ECPAY_HASH_KEY`/`ECPAY_HASH_IV` 的預設值（`3002607`/`pwFHCqoQZGmho4w6`/`EkRm7iFT261dpevs`）**就是綠界官方文件公開的範例測試憑證**——`ecpay-payment-provider` spec 的 `CheckMacValue` 官方範例向量測試用的正是這三個值。沒有任何商家在正式環境用 `ecpay` provider 收過真錢，這是一塊乾淨的地。

另一個決定本 change 形狀的事實是 webhook 的執行順序：`internal/httpapi/payment.go` 的 `Webhook` handler 先讀 raw body、呼叫 `prov.VerifyWebhook`（用**一份寫死**的 HashKey/HashIV 驗簽），驗簽通過**之後**才呼叫 `Service.HandleWebhook`，而只有在那裡才由 `(provider, provider_reference)` 查出這筆付款屬於哪個商家。一旦密鑰變成每商家一把，這個順序本身就是問題：驗簽時還不知道要用哪把密鑰，而「先查出是哪個商家再驗」又要求信任一個尚未驗證的欄位。

## Goals / Non-Goals

**Goals:**
- 每個商家可以設定並使用自己的 ECPay `MerchantID`/`HashKey`/`HashIV`/`ChoosePayment`。
- Webhook 驗簽正確解析出「該用哪個商家的密鑰」，且**不打破**既有「先驗簽才信任」的順序。
- 密鑰加密存放，且**任何**讀取路徑都不把明文密鑰序列化回去。
- `CheckMacValue` 演算法、`1|OK` 回應約定、`OutcomePending` 語意、`RedirectForm`／`WebhookAck` 介面等既有行為**逐字不變**。
- `mock` provider（無真實密鑰）完全不受影響，行為與路由逐位元組不變。

**Non-Goals:**
- 每商家各自的 `CheckoutURL`（讓一個商家仍在模擬/測試環境、另一個已上正式站同時運作）——今天只有一個部署層級的環境開關，這個需求要等到真的有兩個商家需要不同綠界環境時才值得做，屬於過度設計。
- KMS／Vault／HSM 之類的金鑰管理服務整合——應用層對稱加密＋一把環境變數金鑰即可，符合本專案「不做超前部署的抽象」的一貫立場。
- 遷移任何「真實」的既有商家密鑰——不存在這樣的資料（見 Context）。
- 退款自動化、真實綠界端點驗證、Gmail 寄信額度——另有追蹤，與本 change 無關。
- 同一商家同時存在多組 ECPay 憑證（例如同時保留 sandbox 與正式兩組）——`(shop_id, provider)` 唯一，一商家一組。

## Decisions

### D1: Per-shop 解析做在一個新的可選介面上，不是 provider 名稱的特判

`ecpay-payment-provider` design D1 已經守住一條界線：「不得改成 handler 裡的 `if provider == "ecpay"`」。本 change 延續同一條界線——`payment.Service` 不會在任何地方檢查 `providerName == "ecpay"`。做法是新增一個小介面：

```go
// ShopScopedProvider is implemented by providers whose credentials are
// resolved per shop rather than fixed at process startup.
type ShopScopedProvider interface {
    ResolveForShop(ctx context.Context, shopID int) (Provider, error)
}
```

`payment.Service` 在需要一個 `Provider` 實例時（`InitiatePayment`、webhook handler）一律先對 registry 裡的值做型別斷言：實作了 `ShopScopedProvider` 就呼叫 `ResolveForShop(ctx, shopID)` 拿到一份臨時、已灌入該商家憑證的 `*ecpay.Provider`；沒實作就直接用原本的固定實例（`mock` 走這條路，逐字不變）。這個判斷是對**抽象能力**的判斷（「這個 provider 需不需要商家解析」），不是對名稱字串的判斷，所以沒有違反既有的界線，且日後第二個需要 per-shop 憑證的 provider 只要實作同一個介面就好，呼叫端一行不用改。

`ecpay.New` 建構一個 `*ecpay.Provider`是純函式、無網路呼叫（`ecpay-payment-provider` D1 已經確立），所以每次請求現建一份的成本可以忽略。

### D2: Webhook 路由新增 provider+shop 的形狀，`ecpay` 乾淨切齊、`mock` 原樣不動

`POST /api/v1/webhooks/payments/{provider}/{shopID}` 是新增的路由，只有 `ecpay` 使用；既有 `POST /api/v1/webhooks/payments/{provider}` 保留，`mock` 繼續走它，行為逐位元組不變。

`ecpay` 的 `ReturnURL` 不再是設定檔裡的固定字串，而是在 `InitiatePayment` 當下用平台設定的**基底**（`ECPAY_RETURN_URL`）加上 `/{shopID}` 組出來——綠界只會原樣把它當作回呼位址使用，這個 URL 從頭到尾都是我們自己控制的值，不需要綠界任何配合。

考慮過的替代方案（B）：不改路由，在 webhook handler 內先用**未驗證**的 body 解析出 `MerchantTradeNo`，查 `payments` 表拿到 `shop_id`，再用那個商家的密鑰驗簽。技術上可行，但讓一個尚未經過任何驗證的欄位去決定「該用哪把密鑰嘗試驗證」，多了一個不必要的信任跳躍，且需要在驗簽前後各查一次 `payments` 表（`HandleWebhook` 內本來就會再查一次）。方案（A）把 shop id 放進我們自己產生、綠界只是照抄的 URL，完全不需要相信 payload 的任何內容就能決定「用哪把鑰匙」——被選中的鑰匙本身仍然要通過真正的簽章驗證，兩者不衝突但（A）更直接、不新增查詢也不新增信任跳躍，故採用（A）。

**為什麼 URL 裡的 shop id 不驗證也安全**：驗簽用的是「URL 裡宣稱的商家」對應的密鑰，去驗證「payload 的簽章」。如果有人拿商家 A 的合法回呼內容打到 `/webhooks/payments/ecpay/{商家B的id}`，驗證時系統會用商家 B 的密鑰去驗一個用商家 A 密鑰算出來的簽章——**必然驗不過**，直接在 `VerifyWebhook` 這一關就被拒絕，根本進不到 `HandleWebhook`。URL 裡的 shop id 因此不是信任邊界的一部分，它只是「該試哪把鑰匙」的路由資訊，錯了的後果只是驗簽失敗，不是任何資料被信任。

`Service.HandleWebhook` 內既有的 `(provider, provider_reference)` 查找邏輯不變；額外加一條防禦性斷言——查到的付款列 `shop_id` 若與 URL 裡的 shop id 不符，視同查無此付款列處理（`ErrNotFound`，維持既有「認不出的交易一律 `1|OK`」語意）。這條斷言在正常情況下必然成立（見上一段的驗簽論證），純粹是多一層防呆，不是新的安全邊界。

### D3: 密鑰與識別碼分開對待——`MerchantID`/`ChoosePayment` 明文，`HashKey`/`HashIV` 加密

沿用本專案在 `storefront-client-auth` 已經定案的區分：publishable key 是「識別碼不是憑證」，明文儲存、可完整顯示。ECPay 的 `MerchantID` 屬於同一類——單獨知道它做不了任何事，後台可以明文顯示、可編輯。`HashKey`/`HashIV` 則是真正的密鑰，是 `CheckMacValue` 演算法的輸入，知道它們就能偽造任意合法簽章——這是本系統第一次要存放這種等級的商家密鑰。`ChoosePayment` 是純設定值，同樣明文。

後台檢視 API **只回報這兩個密鑰欄位是否已設定**（布林值），從不回傳明文或任何片段（不做「末四碼顯示」——對一組直接參與 HMAC 運算的共享密鑰，片段沒有實用價值，只會白白減少熵）；設定/更新是**只寫**操作。這與 `password_hash` 從不序列化的先例同構，且比照 `member-directory` 的作法：不外洩的證明要落在**回應位元組**上的整合測試，不能只靠讀程式碼。

### D4: 加密方式——應用層 AES-256-GCM，環境變數金鑰，不做 KMS

`HashKey`/`HashIV` 各自以 AES-256-GCM 加密後存進 `bytea` 欄位（nonce 與密文串接存放）。金鑰來自新的環境變數 `PAYMENT_CREDENTIALS_ENCRYPTION_KEY`（32 bytes，hex 或 base64 編碼），仿照其他密鑰類設定提供一組僅供本機開發用的預設值，正式環境必須覆蓋。金鑰長度或編碼錯誤時**拒絕啟動**——這是本專案的既有慣例（`shoptz`/`REPORT_DEFAULT_TIMEZONE` 已經這樣做過一次：「唯一一個值錯了會讓服務拒絕啟動的新設定」）。

考慮過但拒絕的替代方案：導入 KMS/Vault 做信封加密。本系統今天是單一部署、沒有金鑰輪替的營運需求，這類基礎設施只會增加一個新的外部依賴（違反「無外部 SaaS 依賴」的既有約束）卻換不到今天用得上的能力，屬於超前部署。等真的需要金鑰輪替或多環境金鑰隔離時再重新評估。

### D5: 新表而不是 `shops` 的新欄位

新增 `shop_payment_provider_credentials`（暫定表名，`shop_id`、`provider`、`merchant_id`、`hash_key_ciphertext`、`hash_iv_ciphertext`、`choose_payment`，`UNIQUE(shop_id, provider)`），比照 `publishable_keys`／`shop_allowed_origins` 的先例——這類「每商家可能不只一份、且與 `shops` 主體生命週期不同」的資料一律是自己的表，不是 `shops` 上的欄位。`provider` 欄位讓資料層天然支援日後第二個需要憑證的 provider，不需要重新設計表結構；但如 Non-Goals 所述，本 change 只有 `ecpay` 一個消費者，後台端點暫時只做 `ecpay`（見 Open Questions）。

需要在 `internal/tenant.tenantOwned` 登記這張新表（`shop_id` 欄位存在，`enttenancy_internal_test.go` 會自動要求分類，忘了登記會直接轉紅——這是既有機制，不是本 change 新增的檢查）。

### D6: RBAC——新節點只給 `merchant_owner`

新增權限節點（暫定 `payment_credential.manage`，見 Open Questions），只授予 `merchant_owner`，**不**授予 `editor`。理由與 `publishable_key.*` 完全相同：設定/更新金流帳號憑證是整合層面的操作，不是內容編輯；`report.view`/`member.view` 授予 `editor` 的理由（「editor 已經能從別的路徑看到同等或更寬的資訊，不給只是製造一個攔不住任何人的假邊界」）在這裡不成立——`editor` 今天**沒有任何路徑**能看到或推得出 ECPay 密鑰，所以這裡的界線是真實的，應該守住。

### D7: `CheckoutURL` 維持平台層設定

`CheckoutURL`（模擬收銀台 vs. 綠界測試/正式站）決定的是「整個部署跟誰對話」，同一個綠界正式站網址對所有走正式流程的商家都相同——這不是商家身分的一部分。維持現狀（平台環境變數），列入 Non-Goals。

## Risks / Trade-offs

- **[風險]** 遺失或輪替 `PAYMENT_CREDENTIALS_ENCRYPTION_KEY` 會讓既有加密憑證無法解密 → **緩解**：商家自己手上本來就留著 ECPay 後台給的 `HashKey`/`HashIV`，金鑰事故的復原方式是請商家重新填入一次，不需要系統自己能復原明文——這與遺失 `password_hash` 對應的原始密碼時的處置邏輯一致（重設，不復原）。
- **[風險]** 商家在尚未設定憑證時嘗試以 `ecpay` 發起付款 → **緩解**：`InitiatePayment` 對缺憑證的商家明確回報錯誤（比照現有 `ErrValidation`/`ConflictError` 分類），不得回退到平台範例憑證或任何其他商家的憑證。
- **[風險]** 後台回應意外把密鑰欄位明文序列化出去 → **緩解**：比照 `password_hash`/`member-directory` 的先例，用回應位元組層級的整合測試釘住（斷言回應中不含任何密鑰片段），不只靠程式碼審查。
- **[風險]** 新表忘記登記進 `internal/tenant.tenantOwned` → 這是既有機制自動攔的（`enttenancy_internal_test.go`），不是本 change 需要新發明的防線，此處只是提醒實作時會遇到它。

## Migration Plan

1. 新 ent schema + migration：新增 `shop_payment_provider_credentials` 表（additive，不改動既有表）。
2. `internal/config`：新增 `PAYMENT_CREDENTIALS_ENCRYPTION_KEY`（附開發用預設值）；移除 `ECPAY_MERCHANT_ID`/`ECPAY_HASH_KEY`/`ECPAY_HASH_IV`/`ECPAY_CHOOSE_PAYMENT`；`ECPAY_CHECKOUT_URL`/`ECPAY_CLIENT_BACK_URL`/`ECPAY_RETURN_URL`（作為 webhook 基底 URL）保留。
3. `internal/seed`：新增權限節點並授予 `merchant_owner`；demo 商家（`make seed-demo`）種一筆憑證資料，值沿用今天的綠界官方範例憑證，讓本機開發／CI／e2e 行為不變。
4. `internal/payment`：`ShopScopedProvider` 介面、`ecpay.Provider` 支援以憑證覆寫建構、`Service` 的 provider 解析改為型別斷言 + `ResolveForShop`。
5. `internal/httpapi`：新增 webhook 路由 `.../{provider}/{shopID}`（`ecpay` 專用）；新增後台憑證管理端點；`ReturnURL` 組出商家專屬形狀。
6. `api/openapi/openapi.yaml` + `v1-surface.txt`：新增上述路由（additive）。
7. `admin/`：新增商家 ECPay 設定畫面（密鑰欄位只寫、檢視只顯示「已設定」狀態）。

**回滾**：全部是新增（新表、新路由、新設定），回滾即為還原對應檔案與一次 down migration，不影響既有資料或既有 `mock`/`ecpay` 行為。

## Open Questions

- 權限節點與後台端點的確切命名——本文件暫定 `payment_credential.manage` 與 `PUT/GET /api/v1/admin/shops/{shopID}/ecpay-credentials`（先綁定 `ecpay`，不做成 `{provider}` 泛型端點，避免在只有一個消費者時做超前抽象；表結構已經是 `(shop_id, provider)` 泛型，日後加第二個 provider 時端點可以additive 地泛化）。實作前應在 tasks.md 定案。
- `hash_key_ciphertext`/`hash_iv_ciphertext` 是各自一欄，或合併成單一 JSON 加密欄位——傾向各自一欄（與既有 `payments` 表「每個欄位各自表達一件事」的風格一致），留待 tasks 階段定案。
