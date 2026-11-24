## ADDED Requirements

### Requirement: 推進至已退貨需要獨立權限

將出貨推進為已退貨（`PUT /api/v1/admin/shops/{shopID}/orders/{orderID}/shipments/{shipmentID}`，`status = "returned"`）MUST 同時要求 `shipment.update` 與 `shipment.return`；推進為已送達 MUST 只要求 `shipment.update`。缺少 `shipment.return` 時 MUST 回 403，且出貨、訂單、點數 MUST 均不變。seed MUST 將 `shipment.return` 授予 `merchant_owner` 與 `editor`。

#### Scenario: staff 不能退貨
- **WHEN** 持 `staff`（有 `shipment.update`、無 `shipment.return`）的成員把已送達的出貨推進為已退貨
- **THEN** 回 403，訂單 `fulfillment_status` 與顧客點數不變

#### Scenario: editor 仍可退貨
- **WHEN** 持 `editor` 的成員把出貨推進為已退貨
- **THEN** 成功，行為與本變更前相同
