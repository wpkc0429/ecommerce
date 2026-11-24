## Context

- `shipping.Service.CreateShipment` 只在訂單已取消、無可出數量、已有退貨出貨時回 409。`payment_status` 的值：`0` 未付款、`1` 已付款、`2/3` 退款（保留值，今天沒有寫入路徑）。
- `account.Service.ListInvitations` 過濾 `used_at IS NULL AND expires_at > now`；`RevokeInvitation` 只過濾 `used_at IS NULL`。撤回、接受、被新邀請取代，都是設 `used_at`。
- `payment.Service.HandleWebhook`：`recordProviderFacts` 只在 provider 回報時寫 `paid_at`；mock 從不回報，綠界在 `PaymentDate` 解析失敗時也不回報。
- `order_items` 有 `sku_code` 快照，沒有選項；`product_skus.options` 是 jsonb 物件（`{"size":"M"}`），預設 `{}`。

## Goals / Non-Goals

**Goals:** 四個缺口各自最小修補；每個都有單元與整合測試；v1 只新增。
**Non-Goals:** 貨到付款；邀請的實體刪除；SKU options 的版本歷史；改寫通知信。

## Decisions

### D1：出貨只在「已付款」時允許；貨到付款的位置先留好、不實作

守衛放在 service 層（`CreateShipment`），在鎖住訂單列**之後**檢查 `payment_status == payment.OrderPaymentStatusPaid`，所以並發的付款確認在鎖下被一致地看見；HTTP 層只是把 `ConflictError` 對映成 409（既有慣例，訊息 `order is not paid`）。已退款（2）與部分退款（3）同樣不算已付款：出貨一筆已退款的訂單沒有任何情境是對的。

**貨到付款今天怎麼辦：** 資料模型裡沒有任何可以表達它的東西——`orders` 沒有付款方式欄位、`payments.provider` 只描述「已發起的線上付款嘗試」而不是「這張訂單打算怎麼付」，沒有 COD 旗標。依指示，規則維持嚴格（只有已付款），不發明 COD。**日後延伸點只有一處**：`shipping.isPaidForShipping(order)`。屆時需要一個訂單層的、結帳時快照的欄位（例如 `orders.payment_method`，與運送方式快照同一種做法），該函式改為「已付款，或付款方式為貨到付款」。這是一個 additive 的修改，不影響本變更的契約。

AdvanceShipment（送達／退貨）不加守衛：出貨已存在就代表當時已付款，而退貨後的訂單狀態不應被付款檢查卡住。

### D2：邀請清單回傳 pending ＋ expired，以 `status` 區分；撤回即「移出清單」

- 清單條件改為 `used_at IS NULL`（不再過濾到期）；`status` 在讀取時由 `expires_at` 與現在時間推導（`pending`／`expired`），不儲存——儲存的狀態需要背景工作來翻轉，而到期是時間的純函式。
- 已接受、已撤回、被新邀請取代者都帶 `used_at`，不出現在清單（維持原行為）。
- **撤回／刪除**：選擇「撤回」而不是實體刪除。`RevokeInvitation` 原本就只要求 `used_at IS NULL`，所以對已過期的邀請**本來就能用**，缺的只是看得見它。撤回保留列（審計與 `admin_credential_tokens` 的設計一致），並讓清單乾淨。不新增端點、不新增權限（沿用 `user.manage_members`）。
- **向後相容**：回應鍵、既有欄位全部不變，只新增 `status`。唯一行為差異是清單多了已過期的列；只認原有欄位的舊客戶端會把它們當作待接受顯示——這是「讓過期邀請可見」這個需求本身的結果，無法在不隱藏它們的前提下避免。我們**沒有**加 `?status=` 篩選參數（沒有消費者要求；加了就要永遠維護，且鎖進 v1 表面）；呼叫端依 `status` 過濾即可，日後需要時 additive 地加。已過期者再次邀請同一 email 仍會 supersede 它（既有行為）。

### D3：成功的付款一定有 `paid_at`，provider 的時間優先，否則用處理時間

`HandleWebhook` 在 CAS 與重讀之後：若付款列已是 `succeeded` 且 `paid_at IS NULL`，以 `UPDATE … WHERE id = ? AND paid_at IS NULL` 設為 `now()`。順序保證：`recordProviderFacts` 先跑，所以綠界的 `PaymentDate` 勝出；條件式 UPDATE 保證不覆蓋任何既有值（包含並發寫入者）。這段位於「已取消訂單」分支之前——錢確實動了，所以付款列仍有時間，即使訂單不被推進。它在每次重送都檢查，所以歷史上缺值的列在下一次重送時自癒。

這**修改了** `payments.paid_at` 的語意：原本是「provider 宣告的時間，沒有就 NULL」（`ecpay-payment-provider` D12，並且明確拒絕以 `updated_at` 代替）；現在是「provider 宣告的時間，沒有就是我們記錄成功的時間」。兩個來源在 OpenAPI 敘述與 schema 註解都寫明；它不是把 `updated_at` 當付款時間用，而是在記錄成功的那一刻寫下一個專屬的值。

**歷史列回填**：migration 內 `UPDATE payments SET paid_at = updated_at WHERE status = 1 AND paid_at IS NULL`（冪等）。這裡**確實**使用 `updated_at` 作為近似——對一筆成功後就不再被寫入的付款列，它等於記錄成功的時間，且那是歷史資料中唯一可得的時間；這是一次性的近似，已在 migration 註解、schema 註解與本文件明說。替代方案是一個一次性的指令；選 migration 是因為它與欄位同版本、冪等、且可被整合測試以「從檔案讀出的 SQL」驗證。**不在任何真實資料上執行；使用者需在主 DB 套用 migration。**

### D4：訂單品項選項用快照欄位，不用讀取時 join

選擇 **`order_items.options` jsonb NULL，建單時由 `order.Service.Checkout` 複製 SKU 的 options**。

- 與既有的 `product_title`／`sku_code`／`price_amount` 同一原則（order-management D4）：訂單是歷史記錄，商家日後改規格（把 `M` 改成 `Medium`、調整 SKU、刪除 SKU）不得改變顧客買到什麼。讀取時 join 做不到——`order_items.sku_id` 是**刻意沒有 FK 的資訊性欄位**，SKU 被刪除後 join 回 NULL，且 SKU 被編輯後 join 回的是新值，兩者都錯。
- 讀取時 join 的唯一優點是「舊訂單免回填」，但它給舊訂單的是**目前**的值，與回填給舊訂單的值相同，卻讓新訂單也錯。快照對新訂單正確，對舊訂單的行為一樣（見下），所以嚴格優於 join。
- 成本：一個欄位、每個品項多存一小段 JSON；沒有新表、沒有索引。
- **NULL 的語意**：NULL 只代表「這一列早於此欄位」。新訂單永遠非 NULL（SKU 沒有選項時是 `{}`），所以客戶端可以區分「沒有選項」與「未知」。
- **舊訂單**：migration 以 `UPDATE … FROM product_skus` 回填「SKU 目前的 options」（SKU 仍存在者）；SKU 已刪除者維持 NULL。這是**盡力而為**——若 SKU 在下單後被編輯過，回填值不是下單當時的值，而且當時的值已不可復原。API 契約因此說「舊訂單可能為 `null`，且可能是近似值」。API 讀取從不因 NULL 失敗。
- 公開與後台回應共用同一個 `orderItemJSON`，所以顧客、訪客、後台三處同時得到 `options`；通知信不變。

## Risks / Trade-offs

- 出貨守衛是狀態收緊：呼叫端若對未付款訂單出貨（例如測試與 e2e 的舊腳本）會得到 409——e2e 與整合測試 fixture 已改為先付款。
- 邀請清單多列：見 D2。
- `paid_at` 回填使用 `updated_at` 近似：見 D3，唯一的資料品質折衷。
- `options` 回填可能是近似值：見 D4。

## Migration Plan

1. `make migrate`（主 DB，使用者執行）：新增欄位並執行兩段冪等回填。
2. 不需要 re-seed。
3. 回滾：`make migrate-down` 只移除 `options` 欄位；`paid_at` 的回填是資料不是結構，刻意不還原。
