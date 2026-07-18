## ADDED Requirements

### Requirement: Member-owned cart access control
購物車 MUST 屬於單一 shop 內的單一會員（`shop_id` + `member_id`，`member_id` 指向平台級 `Member`）。存取控制 MUST 僅依賴已驗證的會員 JWT（`aud=shop:{shop_id}`）中的 `member_id` 與請求所屬 shop 是否與購物車的擁有者相符，MUST NOT 套用 RBAC 角色權限節點系統。未帶有效會員 JWT，或會員 JWT 的 shop 與請求 shop 不符的請求 MUST 回 401。

#### Scenario: 會員可存取自己的購物車
- **WHEN** 會員以自己 shop 的有效 JWT 呼叫 `GET /api/v1/shop/cart`
- **THEN** 回傳該會員在該 shop 的購物車內容

#### Scenario: 未登入請求購物車 API 被拒
- **WHEN** 未帶 `Authorization` header 呼叫任一 `/api/v1/shop/cart/...` 端點
- **THEN** 回 401

#### Scenario: 跨會員存取品項被拒
- **WHEN** 會員 A 嘗試以 `PUT`/`DELETE` 操作屬於會員 B 購物車的 `itemID`
- **THEN** 回 404（不洩漏該品項屬於其他會員）

#### Scenario: 跨店重用會員 token 被拒
- **WHEN** 以 shop A 簽發的會員 token 呼叫 shop B 網域的購物車 API
- **THEN** audience 不符，回 401

### Requirement: Cart lookup without forced creation
`GET /api/v1/shop/cart` MUST 在該會員於該 shop 沒有 `active` 購物車時，回傳一個空購物車視圖（品項為空陣列、小計與總計為 0），MUST NOT 因為呼叫此端點而建立購物車資料列。購物車資料列 MUST 僅在會員首次加入品項時才建立。

#### Scenario: 尚未加入任何品項時查詢購物車
- **WHEN** 會員從未呼叫過 `POST /cart/items`，呼叫 `GET /api/v1/shop/cart`
- **THEN** 回 200，`items` 為空陣列，`subtotal`/`total` 為 0

#### Scenario: 加入品項後購物車列真正建立
- **WHEN** 會員呼叫 `POST /cart/items` 加入一個品項
- **THEN** 該會員在該 shop 產生一筆 `status = 0`（active）的購物車資料列

### Requirement: Single active cart per member per shop
系統 MUST 保證同一 `(shop_id, member_id)` 組合至多存在一筆 `status = 0`（active）的購物車。

#### Scenario: 重複加入品項沿用同一個 active 購物車
- **WHEN** 會員先後兩次呼叫 `POST /cart/items` 加入不同 SKU
- **THEN** 兩個品項都掛在同一筆購物車資料列下

### Requirement: Cart currency lock
購物車的 `currency` MUST 由第一個成功加入的品項的 SKU 幣別決定並固定；後續加入品項時，若該 SKU 的 `currency` 與購物車現有 `currency` 不同，MUST 拒絕加入並回 422，MUST NOT 允許多幣別品項並存或進行幣別換算。

#### Scenario: 第一個品項決定購物車幣別
- **WHEN** 會員加入的第一個品項的 SKU `currency = "TWD"`
- **THEN** 購物車 `currency` 被設為 `"TWD"`

#### Scenario: 混幣別加入被拒
- **WHEN** 購物車 `currency` 已為 `"TWD"`，會員嘗試加入 `currency = "USD"` 的 SKU
- **THEN** 回 422，購物車內容不受影響（新品項未被加入）

### Requirement: Add item validation
加入購物車品項時，系統 MUST 驗證：SKU 屬於請求所屬的 shop、SKU `is_active = true`、SKU 所屬商品 `status = 1`（已發佈）、加入後的品項數量不超過 SKU 目前的 `stock_qty`。任一驗證失敗 MUST 回 422（不得回 500），且 MUST 說明具體違反的條件。此驗證僅為提示性檢查，MUST NOT 產生庫存鎖定或預留效果。

#### Scenario: 加入不存在或跨店的 SKU 被拒
- **WHEN** 會員嘗試加入不屬於當前 shop（或不存在）的 `sku_id`
- **THEN** 回 422

#### Scenario: 加入已下架 SKU 被拒
- **WHEN** 會員嘗試加入 `is_active = false` 的 SKU
- **THEN** 回 422

#### Scenario: 加入非已發佈商品的 SKU 被拒
- **WHEN** 會員嘗試加入所屬商品 `status = 0`（草稿）的 SKU
- **THEN** 回 422

#### Scenario: 加入數量超過庫存被拒
- **WHEN** SKU `stock_qty = 5`，會員嘗試加入 `quantity = 6`
- **THEN** 回 422，購物車內容不受影響

#### Scenario: 加入數量在庫存範圍內成功
- **WHEN** SKU `stock_qty = 5`，會員加入 `quantity = 5`
- **THEN** 加入成功

### Requirement: Add-item accumulation semantics
重複對同一 SKU 呼叫加入品項端點時，系統 MUST 將新的 `quantity` 累加到該購物車既有品項的數量上（而非拒絕、也非覆寫），並對累加後的總數量重新套用庫存驗證。

#### Scenario: 重複加入同一 SKU 累加數量
- **WHEN** 會員先加入 `quantity = 2` 的某 SKU，再次加入同一 SKU `quantity = 3`
- **THEN** 該品項最終數量為 5（單一品項列，非兩筆）

#### Scenario: 累加後超過庫存被拒
- **WHEN** SKU `stock_qty = 5`，購物車內該 SKU 已有 `quantity = 3`，會員再加入 `quantity = 3`
- **THEN** 回 422，既有品項數量維持 3 不變

### Requirement: Update item quantity is an absolute set
`PUT /api/v1/shop/cart/items/{itemID}` MUST 將品項數量設定為請求給定的絕對值（非累加），MUST 重新驗證新數量不超過 SKU 目前庫存，`quantity <= 0` MUST 回 422（移除品項須使用刪除端點）。

#### Scenario: 更新數量為有效值
- **WHEN** 會員將購物車內某品項數量以 `PUT` 更新為 2（SKU 庫存足夠）
- **THEN** 該品項數量變為 2

#### Scenario: 更新數量超過庫存被拒
- **WHEN** SKU 目前庫存為 3，會員嘗試 `PUT` 該品項數量為 4
- **THEN** 回 422，品項數量不變

#### Scenario: 更新數量為 0 或負數被拒
- **WHEN** 會員嘗試 `PUT` 品項數量為 0
- **THEN** 回 422

### Requirement: Remove item and clear cart
`DELETE /api/v1/shop/cart/items/{itemID}` MUST 移除單一品項；`DELETE /api/v1/shop/cart/items` MUST 移除該會員購物車內所有品項。清空一個不存在（或已無品項）的購物車 MUST 是幂等操作（成功回應，不視為錯誤）。

#### Scenario: 移除單一品項
- **WHEN** 會員刪除購物車內某一品項
- **THEN** 該品項不再出現於購物車內容中，其餘品項不受影響

#### Scenario: 清空購物車
- **WHEN** 會員的購物車內有多個品項，呼叫清空端點
- **THEN** 該購物車所有品項被移除，購物車視圖回到空狀態

#### Scenario: 清空沒有品項的購物車是幂等操作
- **WHEN** 會員尚未加入任何品項就呼叫清空端點
- **THEN** 回應成功，不回錯誤

### Requirement: Price snapshot on add
加入購物車品項時，系統 MUST 將該 SKU 當下的 `price_amount`（`int64`/`BIGINT` 整數最小貨幣單位，沿用 spec product-catalog 的金額表示法）與 `currency` 複製為該品項自己的快照欄位，MUST NOT 僅儲存 SKU 的外鍵並於每次讀取時即時查詢 SKU 現價。

#### Scenario: 加入當下固化價格
- **WHEN** SKU 加入購物車時 `price_amount = 1000`
- **THEN** 該購物車品項的 `price_amount` 欄位被設為 `1000`

### Requirement: Cart totals computed from snapshot
購物車的品項小計（`line_total`）與購物車總計（`subtotal`/`total`）MUST 一律以品項自身儲存的價格快照計算，MUST NOT 因 SKU 現價變動而改變既有購物車品項的顯示金額或購物車總計。

#### Scenario: SKU 現價變動不影響既有購物車品項金額
- **WHEN** 會員以 `price_amount = 1000` 加入品項後，該 SKU 現價被商家調整為 `1500`
- **THEN** 該會員購物車內該品項的 `price_amount` 與購物車總計仍以 `1000` 計算，不受現價變動影響

### Requirement: Deactivated or unpublished SKU stays in cart with a purchasability flag
SKU 被下架（`is_active = false`）或其商品變為非已發佈（`status != 1`）後，已存在於購物車內的對應品項 MUST 保留（系統 MUST NOT 自動移除或自動調整其數量）。讀取購物車時，系統 MUST 為每個品項即時計算一個唯讀的可購買性狀態，反映 SKU/商品當下狀態與庫存是否仍足夠支撐品項數量。

#### Scenario: SKU 下架後品項保留但標記不可購買
- **WHEN** 會員購物車內某品項對應的 SKU 被商家設為 `is_active = false`
- **THEN** 該品項仍出現在 `GET /cart` 回應中，但標記為不可購買

#### Scenario: 商品下架後品項保留但標記不可購買
- **WHEN** 會員購物車內某品項對應的商品 `status` 從已發佈改為草稿
- **THEN** 該品項仍出現在 `GET /cart` 回應中，但標記為不可購買

#### Scenario: 庫存降至低於購物車內數量時標記不可購買
- **WHEN** 會員購物車內某品項數量為 5，該 SKU 之後的庫存被調整為 3
- **THEN** 該品項標記為不可購買，但仍保留原數量 5 與原價格快照

### Requirement: SKU deletion preserves the cart item with its snapshot
SKU（或其所屬商品）被刪除時，系統 MUST NOT 刪除引用它的購物車品項；購物車品項的 `sku_id` MUST 被設為 NULL，其價格快照（`price_amount`/`currency`）與 `quantity` MUST 保留不變。讀取購物車時，該品項 MUST 標記為不可購買。

#### Scenario: 刪除商品後其 SKU 連帶被刪除，購物車品項保留
- **WHEN** 會員購物車內某品項對應的商品（含其 SKU）被商家刪除
- **THEN** 該品項仍出現在 `GET /cart` 回應中，`sku_id` 為 null，價格快照與數量不變，標記為不可購買

### Requirement: Cart status enumeration
`carts.status` MUST 僅為以下三個值之一：`0`（active，使用中）、`1`（converted，已轉換為訂單）、`2`（abandoned，已棄置）。本 change 建立的購物車 MUST 一律以 `status = 0` 建立，本 change 的任何操作 MUST NOT 將購物車狀態轉換為 `1` 或 `2`（狀態轉換是後續 proposal 的職責）。

#### Scenario: 新購物車以 active 狀態建立
- **WHEN** 會員加入第一個品項，系統建立購物車資料列
- **THEN** 該購物車 `status = 0`

### Requirement: Tenant data isolation for cart tables
`carts`、`cart_items` MUST 納入既有租戶隔離機制（spec multi-tenancy）：任一查詢或寫入 MUST 被強制限定於請求所屬的 shop。

#### Scenario: 跨店查詢看不到彼此的購物車資料
- **WHEN** shop A 的會員請求自己的購物車
- **THEN** 回應不含任何 shop B 的購物車或品項資料
