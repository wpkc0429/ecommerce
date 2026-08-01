## MODIFIED Requirements

### Requirement: Merchant back-office order access is scoped by shop and RBAC

商家後台訂單 API（`GET /api/v1/admin/shops/{shopID}/orders`、`GET /api/v1/admin/shops/{shopID}/orders/{id}`、`POST /api/v1/admin/shops/{shopID}/orders/{id}/cancel`）MUST 依既有三層 RBAC 判定（spec rbac）：檢視需要 `order.view`，取消需要 `order.cancel`。商家 MUST 只能操作 URL 中 `shopID` 所屬的訂單；跨商家操作 MUST 依既有 cross-shop access guard 回 403。訂單列表 MUST 支援分頁，MAY 依訂單 `status` 篩選。

訂單列表 SHALL 額外支援下列**選填**篩選，且每一項 MUST 作用於該商家的整個訂單集合（而非單一頁）：

- 依 `payment_status` 精確比對
- 依 `fulfillment_status` 精確比對
- 依 `requested_delivery_date` 的閉區間（以日曆日表達，區間端點皆為選填且可單邊給定）
- 依關鍵字：可解析為正整數時 MUST 比對訂單編號；同時 MUST 對收件人姓名與收件人電話做大小寫不敏感的子字串比對

未提供任何上述選填參數時，列表的查詢與回應 MUST 與本需求變更前完全相同。

#### Scenario: 具權限商家檢視自己商家的訂單
- **WHEN** 持有 shop A `order.view` 權限的使用者呼叫 `GET /api/v1/admin/shops/{shopA}/orders`
- **THEN** 回傳僅屬於 shop A 的訂單，分頁

#### Scenario: 無權限操作被拒
- **WHEN** 使用者不具 `order.cancel` 權限，嘗試取消 shop A 的訂單
- **THEN** 回 403

#### Scenario: 跨店操作被拒
- **WHEN** 僅屬 shop A 的使用者呼叫 shop B 的訂單管理 API
- **THEN** 回 403

#### Scenario: 依狀態篩選訂單列表
- **WHEN** 以 `status=1` 查詢 shop A 的訂單列表
- **THEN** 僅回傳該商家 `status = 1`（已取消）的訂單

#### Scenario: 依付款與出貨狀態篩選出待出貨清單
- **WHEN** 以「已付款」的 `payment_status` 與「未出貨」的 `fulfillment_status` 併用查詢
- **THEN** 僅回傳同時符合兩者的訂單

#### Scenario: 依指定配送日區間篩選
- **WHEN** 以某一日曆日同時作為區間起訖查詢
- **THEN** 僅回傳 `requested_delivery_date` 等於該日的訂單；**未指定配送日的訂單 MUST NOT 被回傳**

#### Scenario: 關鍵字比對收件人姓名或電話
- **WHEN** 以收件人姓名或電話的一部分作為關鍵字查詢
- **THEN** 回傳收件資訊中姓名或電話包含該子字串的訂單，比對 MUST 大小寫不敏感

#### Scenario: 關鍵字為數字時亦比對訂單編號
- **WHEN** 以一個存在於該商家的訂單編號作為關鍵字查詢
- **THEN** 該訂單 MUST 出現在結果中

#### Scenario: 不帶新參數時行為不變
- **WHEN** 呼叫端未提供任何新增的選填篩選參數
- **THEN** 回應 MUST 與這些參數存在之前完全相同
