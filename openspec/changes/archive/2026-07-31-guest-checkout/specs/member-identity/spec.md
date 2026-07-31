## ADDED Requirements

### Requirement: 無密碼顧客身分

系統 SHALL 允許商家內存在 `password_hash IS NULL` 的顧客身分，作為訪客結帳（spec guest-checkout）所建立的身分載體。

無密碼身分 MUST 與一般顧客身分共用同一張 `members`、同一組唯一性約束與同一套租戶隔離規則，MUST NOT 以另一張表或另一種識別碼表達。訂單、購物車、點數與等級對它的參照 MUST 與對一般身分完全相同。

無密碼身分 MUST NOT 能通過登入：`password_hash` 為 NULL 的身分，任何密碼皆 MUST NOT 使其取得 token。「尚未設定密碼」MUST 以 `password_hash IS NULL` 表達，MUST NOT 借用 `status` 欄位（`status = 0` 的語意固定為「商家已停用此顧客」）。

無密碼身分建立時 `status` MUST 為 `1`（啟用），使其可被立即認領。

#### Scenario: 無密碼身分無法登入

- **WHEN** 以一筆 `password_hash IS NULL` 的身分的 email 加上任意密碼呼叫登入
- **THEN** 回 401，MUST NOT 簽發任何 token

#### Scenario: 無密碼身分與有密碼身分共存於同一商家

- **WHEN** 同一商家內同時存在有密碼與無密碼的顧客身分
- **THEN** 兩者皆受 `UNIQUE(shop_id, email)` 管轄，且皆納入租戶隔離自動範圍化

#### Scenario: 停用語意不被無密碼狀態借用

- **WHEN** 檢視一筆剛由訪客結帳建立的身分
- **THEN** 其 `status = 1`、`password_hash IS NULL`，兩者分別表達「未被停用」與「尚未設定密碼」

### Requirement: 無密碼身分的認領

無密碼身分 SHALL 可由該 email 的註冊行為認領（見 spec authentication）。認領 MUST 只設定 `password_hash`，MUST NOT 改變 `member_id`、`shop_id`、既有訂單歸屬、點數餘額或等級。

認領 MUST 為單向且僅可發生一次：已設定密碼的身分 MUST NOT 再被任何認領路徑改寫其密碼。

在系統具備 email 驗證能力之前，認領 MUST NOT 被描述為「已驗證該 email 的擁有者」——此關聯為未經驗證的關聯，其風險與邊界記錄於本變更的 design D6。

#### Scenario: 認領不改變身分識別碼

- **WHEN** 一筆無密碼身分被註冊認領
- **THEN** 該身分的 `member_id` 與 `shop_id` 皆不變，其名下訂單的 `member_id` 亦不變

#### Scenario: 認領不重設既有密碼

- **WHEN** 以一筆已設定密碼的身分的 email 嘗試註冊
- **THEN** 該身分的 `password_hash` MUST NOT 被改寫，回應為統一的註冊失敗

#### Scenario: 認領後的身分與一般會員無異

- **WHEN** 認領完成後，該會員登入、查詢訂單、查詢點數、取消訂單
- **THEN** 行為與一開始就以註冊建立的會員完全相同
