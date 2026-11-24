## ADDED Requirements

### Requirement: 成功的付款一定有 paid_at

任一 provider（含 mock 與綠界）的付款轉為 `succeeded` 時，`payments.paid_at` MUST 有值：provider 回報時間時 MUST 使用它；否則 MUST 為平台記錄該次成功的時間。既有值 MUST NOT 被覆蓋。失敗與待處理的付款 `paid_at` MUST 維持 `null`。對已成功但缺 `paid_at` 的列，後續重送 MUST 補上。

#### Scenario: mock 付款成功
- **WHEN** mock provider 的成功 webhook 被處理
- **THEN** 付款列 `paid_at` 約為處理當下，後台付款清單回傳該值

#### Scenario: 綠界帶 PaymentDate
- **WHEN** 綠界回呼帶有 `PaymentDate`
- **THEN** `paid_at` 為該時間（Taipei 時區換算），不被處理時間取代

#### Scenario: 綠界缺 PaymentDate
- **WHEN** 綠界成功回呼缺少 `PaymentDate`
- **THEN** `paid_at` 為處理當下
