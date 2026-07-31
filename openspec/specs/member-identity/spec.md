# member-identity Specification

## Purpose
TBD - created by archiving change member-identity-per-shop. Update Purpose after archive.
## Requirements
### Requirement: 前台顧客身分以商家為界

前台顧客身分（`members`）MUST 為 shop 擁有資料，每筆身分 MUST 隸屬於恰好一個商家。email 與 phone 的唯一性 MUST 以 `(shop_id, email)` 與 `(shop_id, phone)` 為界，MUST NOT 為全平台唯一。

同一 email 於不同商家 MUST 構成兩筆互不相干的身分，各自擁有獨立的 `password_hash`、`status`、`points` 與等級。系統 MUST NOT 提供任何將既有商家顧客身分「沿用」至另一商家的路徑。

`email` 與 `phone` 皆為 nullable。同一商家內 MUST 允許存在多筆 `email IS NULL` 或 `phone IS NULL` 的身分（Postgres 預設的 NULLS DISTINCT 語意），以支援僅以 phone 註冊及後續的無密碼身分。

#### Scenario: 同一 email 於兩家商家為獨立身分

- **WHEN** 同一個 email 分別於 shop A 與 shop B 註冊
- **THEN** 建立兩筆獨立的 `members` 列，各自持有不同的 `password_hash`，兩者的 `points` 與等級互不影響

#### Scenario: 同商家內 email 重複被拒

- **WHEN** 於 shop A 以該商家已存在的 email 建立第二筆身分
- **THEN** 寫入失敗（unique violation）

#### Scenario: 同商家內多筆 email 為 NULL 被允許

- **WHEN** 於 shop A 建立第二筆 `email IS NULL` 的身分
- **THEN** 寫入成功，唯一約束不將兩筆 NULL 視為衝突

### Requirement: 顧客憑證不跨商家共用

任一商家（含該商家自行撰寫並託管的前台）所取得的顧客明文密碼，MUST NOT 可用於在其他商家完成登入或註冊。

系統 MUST NOT 存在任何以「跨商家已知憑證」換取本商家會員身分或 token 的流程。

#### Scenario: 他店密碼無法登入本店

- **WHEN** 以顧客於 shop A 設定的 email 與密碼，向 shop B 的網域發出登入請求
- **THEN** 回應為登入失敗，MUST NOT 簽發任何 shop B 的 token（縱使該 email 與密碼於 shop A 完全正確）

#### Scenario: 他店 email 於本店可自由註冊

- **WHEN** 某 email 已為 shop A 的顧客，該 email 於 shop B 的網域以任意新密碼註冊
- **THEN** 註冊成功，於 shop B 建立一筆全新身分，MUST NOT 要求驗證其於 shop A 的密碼

### Requirement: 商家顧客資料集自我完備

一家商家的全部顧客資料 MUST 可僅以 `shop_id` 條件完整界定，MUST NOT 存在由多家商家共享的顧客資料列。

此不變式 MUST 使下列操作可在不影響其他商家的前提下執行：匯出單一商家的全部顧客資料、刪除單一商家的全部顧客資料、將單一商家的資料搬遷至獨立資料庫。

顧客個資刪除請求 MUST 僅影響提出該請求所針對的商家。

#### Scenario: 刪除單一商家顧客資料不影響其他商家

- **WHEN** 刪除 shop A 的全部顧客資料
- **THEN** shop B 的顧客資料筆數與內容完全不變，包含與 shop A 使用相同 email 的顧客

#### Scenario: 顧客要求刪除個資僅及於該商家

- **WHEN** 某顧客要求 shop A 刪除其個人資料，而同一 email 亦為 shop B 的顧客
- **THEN** shop A 的該筆身分被刪除，shop B 的身分不受影響

### Requirement: 顧客身分納入租戶隔離

`members` MUST 納入既有租戶隔離機制（spec multi-tenancy）：於租戶上下文中，任一對 `members` 的查詢或寫入 MUST 被資料存取層強制附加當前 `shop_id` 條件。

後台 API 不於租戶上下文執行，其對 `members` 的查詢 MUST 顯式帶入由授權上下文決定的 `shop_id`，MUST NOT 依賴資料層自動附加，且 MUST NOT 信任 client 傳入的 shop 識別。

`orders.member_id` 與 `carts.member_id` 對 `members` 的參照 MUST 落於同一 `shop_id` 範圍內。

#### Scenario: 租戶上下文自動範圍化顧客查詢

- **WHEN** 應用程式碼在 shop A 的租戶上下文中查詢 `members` 但未顯式指定 shop 條件
- **THEN** 資料層自動附加 `shop_id = shop A` 條件，結果不含任何 shop B 的顧客

#### Scenario: 後台跨商家查詢顧客被拒

- **WHEN** 僅屬 shop A 的後台使用者查詢 shop B 的某位顧客
- **THEN** 依既有 cross-shop access guard 回 403

#### Scenario: 訂單與購物車的顧客參照不跨商家

- **WHEN** 查詢 shop A 某筆訂單所屬的顧客身分
- **THEN** 該顧客身分的 `shop_id` 為 shop A

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
