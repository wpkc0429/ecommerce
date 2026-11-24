## ADDED Requirements

### Requirement: 建立出貨需要訂單已付款

`POST /api/v1/admin/shops/{shopID}/orders/{orderID}/shipments` MUST 只在訂單 `payment_status` 為已付款（`1`）時成立；`0`（未付款）、`2`（已退款）、`3`（部分退款）MUST 回 409，且 MUST NOT 寫入任何出貨、出貨品項，也 MUST NOT 改變訂單的 `fulfillment_status` 或品項的 `shipped_quantity`。檢查 MUST 在服務層、於鎖住訂單列之後執行。目前資料模型沒有付款方式旗標，所以不得有「貨到付款」例外。

#### Scenario: 未付款訂單不能出貨
- **WHEN** 對 `payment_status = 0` 的訂單建立出貨
- **THEN** 回 409，沒有出貨列被建立，訂單 `fulfillment_status` 仍為 0

#### Scenario: 付款後可以出貨
- **WHEN** 同一張訂單的付款成功後再次建立出貨
- **THEN** 回 201
