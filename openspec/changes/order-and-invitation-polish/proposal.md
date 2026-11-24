## Why

Bouquet 後台的串接工作找到四個小缺口，都是「API 讓前端自己猜」或「API 允許一件不該允許的事」：

1. **未付款的訂單可以建立出貨。** `POST …/orders/{id}/shipments` 對 `payment_status = 0` 的訂單回 201。Bouquet 後台在 UI 擋住了（`shipBlocker`），但那是體驗層不是授權——另一個後台前端就能出貨給沒付錢的人。
2. **邀請清單看不到已過期的邀請。** `GET …/invitations` 只列未過期者，過期的邀請無法被看見，也就無法被撤回；owner 只能靠重新邀請同一個 email 去覆蓋它。
3. **mock 金流付款成功後 `paid_at` 仍是 `null`。** 訂單已是 paid、付款列已是 succeeded，卻沒有付款時間。綠界在 `PaymentDate` 缺席時也一樣。
4. **訂單品項只有 `sku_code`，客戶端從 `-S/-M/-L` 尾碼猜尺寸。** SKU 有 `options`，但訂單品項沒有快照。

## What Changes

- **出貨守衛（D1）**：`shipping.Service.CreateShipment` 在已鎖定的訂單列上檢查 `payment_status = 1`（已付款），否則 409。已退款／部分退款同樣 409。資料模型目前沒有付款方式或貨到付款旗標，所以規則嚴格為「已付款才能出貨」，貨到付款記為日後延伸；**不發明 COD 功能**。
- **邀請清單（D2）**：`GET …/invitations` 同時回傳未過期與已過期（但未使用、未撤回）的邀請，每筆新增 `status`：`pending` ｜ `expired`。既有欄位一個字不變。`DELETE …/invitations/{id}` 對已過期的邀請同樣有效（它本來就只要求「未使用」）；撤回後從清單消失。
- **`paid_at`（D3）**：成功的付款一定有 `paid_at`。provider 回報時間就用它；否則由 `payment.Service.HandleWebhook` 在記錄成功的當下寫入處理時間（只在目前為 NULL 時寫，重送也會補上歷史上缺的）。失敗與待處理的付款不變（NULL）。migration 一併把歷史上「成功但 `paid_at` 為 NULL」的列以 `updated_at` 近似回填。
- **訂單品項 `options`（D4）**：`order_items.options`（jsonb，可為 NULL）在建單時快照 SKU 的 `options`；公開（顧客、訪客）與後台的訂單回應每個品項新增 `options`（物件；SKU 沒有選項時為 `{}`；舊訂單為 `null` 或由 migration 以 SKU 目前的選項盡力回填）。

## Capabilities

### Modified Capabilities
- `shipping-logistics`：建立出貨需要訂單已付款。
- `payment-integration`：成功付款必有 `paid_at`。
- `order-management`：訂單品項帶選項快照。
- `shop-team-management`：邀請清單含已過期者與 `status`，已過期者可撤回。

## Impact

- **資料**：`order_items.options` jsonb NULL（一個 ADD COLUMN）＋同一個 migration 內兩段冪等的資料回填（品項選項、`payments.paid_at`）。down 只移除欄位。
- **程式碼**：`api/internal/shipping/service.go`、`api/internal/account/invite.go`、`api/internal/httpapi/{team,order}.go`、`api/internal/payment/service.go`、`api/internal/order/service.go`、ent schema（`order.go`、註解 `payment.go`）、`admin/views/team.js`（顯示「已過期」）。
- **API 契約**：沒有新路由。`Invitation.status`、`OrderItem.options` 兩個新屬性（`v1-surface.txt` 只新增兩行）；`createShipment` 的 409 清單、`listShopInvitations`／`revokeShopInvitation`、`Payment.paid_at` 的敘述更新。
- **行為變更需知會**：①出貨 API 對未付款訂單由 201 變 409（授權／狀態收緊，不是形狀變更）；②邀請清單多回已過期的列——只認原有欄位的呼叫端會把它們當作待接受顯示，需依 `status` 過濾（欄位本身 additive）；③`paid_at` 由 `null` 變有值。
- **不在範圍**：貨到付款、邀請的實體刪除、`options` 的 SKU 編輯歷史、通知信內容。
