## ADDED Requirements

### Requirement: 平台管理員的商家與帳號視圖

後台 MUST 提供兩個平台視圖，其可見性來源為 `GET /admin/me` 的 `platform_permissions`（不持有對應節點者 MUST NOT 看到分頁）：
- 「商家」：商家列表；開店精靈（名稱、主題、網域／路徑前綴、owner email、選填 storefront origin，一次送出至開店端點）；每家店的檢查清單（`setup-status`）。
- 「帳號」：帳號列表（email 搜尋、分頁、狀態、是否已設定密碼）；建立帳號（可同時指派商家與角色）；停用／啟用；產生重設連結；新增與移除成員資格。

會產生連結的操作，在 `delivery = "manual"` 時 MUST 將連結顯示一次並提供複製按鈕，並明示「此連結只顯示這一次、X 小時後失效、請以安全管道交給本人」；離開畫面後 MUST NOT 能再取回同一連結（只能重新產生）。

#### Scenario: 開店精靈
- **WHEN** 平台管理員在開店精靈填入名稱、網域與 owner email 並送出
- **THEN** 畫面顯示新商家、檢查清單，以及 owner 的設定連結與複製按鈕

#### Scenario: 商家 owner 看不到平台視圖
- **WHEN** 商家 owner 登入後台
- **THEN** 導覽中沒有「商家」與「帳號」分頁

### Requirement: 未登入可進入的密碼設定頁

後台 MUST 提供 `#/set-password/{token}` 頁，無需登入即可進入：先以 inspect 端點顯示帳號 email，再讓使用者輸入兩次新密碼並送出；成功後導向登入頁並預填 email。token 無效時 MUST 顯示「連結無效或已過期，請聯絡管理員重新產生」。此頁 MUST NOT 把 token 寫入 `localStorage` 或任何持久儲存。

#### Scenario: 經連結設定密碼
- **WHEN** 新 owner 開啟設定連結並設定密碼
- **THEN** 導向登入頁，以新密碼登入後看到自己商家的後台

#### Scenario: 過期連結
- **WHEN** 開啟已使用過的連結
- **THEN** 顯示連結無效的說明，不顯示密碼輸入框
