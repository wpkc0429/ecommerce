## ADDED Requirements

### Requirement: 平台後台的開店申請視圖

平台後台 SHALL 提供「開店申請」視圖，僅對持有 `shop_application.view` 的使用者可見（可見性來自 `GET /admin/me` 的 `platform_permissions`，伺服器授權為唯一權威）。視圖 MUST 列出申請（狀態篩選、未驗證標示、風險旗標、待處理數徽章），詳情 MUST 顯示事件時間線與待人工寄出的訊息。

#### Scenario: 無權限者看不到
- **WHEN** 只持有商家層角色的使用者登入
- **THEN** 導覽沒有「開店申請」

### Requirement: 核准與拒絕操作

視圖 SHALL 提供核准（可調整店名與初始狀態，owner 已有帳號時須勾選確認）與拒絕（理由必填）。核准成功後 MUST 一次性顯示 owner 的 `setup_url` 並提供複製，且 MUST NOT 把它寫入 localStorage、sessionStorage 或 URL。

#### Scenario: 連結只顯示一次
- **WHEN** 核准成功後離開並重新開啟該申請
- **THEN** 畫面不再顯示 `setup_url`

### Requirement: 人工通知後備

視圖 MUST 讓操作員複製待寄訊息、標記為已寄出，並在申請者未收到驗證信時執行人工確認（記稽核事件）。

#### Scenario: 標記已寄出
- **WHEN** 操作員對一筆 `pending_manual` 訊息按「標記為已寄出」
- **THEN** 訊息狀態變為 `sent` 並記錄 `message_marked_sent` 事件
