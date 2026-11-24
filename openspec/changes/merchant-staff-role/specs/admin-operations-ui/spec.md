## ADDED Requirements

### Requirement: owner 的成員管理視圖

後台 MUST 提供「成員」視圖，可見性為 `user.manage_members`：列出本店成員（email、角色、成員資格狀態）、邀請（email ＋ 只列出可委派角色的下拉）、待接受邀請（可撤回）、停用／啟用、變更角色。持有不可委派角色的成員（例如 owner）的列 MUST 不提供任何操作按鈕。邀請連結於 `delivery = "manual"` 時 MUST 一次性顯示並可複製。

#### Scenario: 邀請並撤回
- **WHEN** owner 邀請一位店員後在待接受清單撤回
- **THEN** 清單不再顯示該邀請

### Requirement: 邀請接受頁

後台 MUST 提供未登入可進入的 `#/accept-invite/{token}` 頁：依 inspect 結果顯示商家名稱與角色；`requires_login = false` 時讓使用者設定密碼並接受；`requires_login = true` 時要求登入，登入後自動以已認證端點接受並切換到該商家。token 無效時顯示說明。

#### Scenario: 既有帳號接受邀請
- **WHEN** 已有帳號的受邀者開啟邀請連結並登入
- **THEN** 接受成功，商家選單出現該商家

### Requirement: staff 登入後的畫面範圍

以 `staff` 登入時，導覽 MUST 只出現訂單相關視圖；訂單詳情 MUST NOT 顯示取消按鈕（無 `order.cancel`），出貨列 MUST NOT 顯示「標記退貨」按鈕（無 `shipment.return`），但 MUST 顯示建立出貨、標記送達與付款紀錄。

#### Scenario: staff 的訂單詳情
- **WHEN** staff 開啟一筆已付款訂單並建立出貨
- **THEN** 畫面有「標記送達」，沒有「取消訂單」與「標記退貨」
