## ADDED Requirements

### Requirement: 保存期限與清除指令

系統 SHALL 提供 `cmd/purge-design-history`，預設為 dry run（只印計數與時間範圍），`-apply` 才寫入，支援 `-shop N`，分批處理（每批 1000）、冪等。預設保存期（皆可由 `DESIGN_*_RETENTION` 設定且有上下限檢查）：prompt 全文 365 天後遮蔽為 NULL（保留 `prompt_sha256`）、已發佈請求的截圖 365 天、其他終態請求的截圖 30 天、請求與事件 730 天、未追蹤變更 730 天、已撤銷或過期的服務憑證 90 天。非終態請求 MUST 永不被清除。清除 MUST 在資料庫交易內明示放行 trigger，物件刪除 MUST 在 commit 之後 best-effort 執行，失敗只留下不含簽名網址的 WARN。

#### Scenario: 預設為 dry run
- **WHEN** 不帶 `-apply` 執行
- **THEN** 不刪除任何列或物件，輸出只含計數與時間範圍

#### Scenario: 保存邊界
- **WHEN** 有一筆 364 天前與一筆 366 天前發佈的請求的截圖
- **THEN** `-apply` 只清除 366 天前者的截圖物件與列內容（設定 `purged_at`），請求與事件仍在

#### Scenario: 進行中的請求不被清除
- **WHEN** 有一筆建立超過保存期但仍為 `drafting` 的請求
- **THEN** 不被清除

#### Scenario: 冪等
- **WHEN** 連續執行兩次 `-apply`
- **THEN** 第二次不再刪除任何東西

### Requirement: 匯出與刪除

清除指令 SHALL 支援 `-export-shop N`（輸出該店全部請求、事件與截圖清單的 JSON，MUST NOT 含簽名網址）與 `-all-for-shop N -apply`（刪除該店全部設計歷史與物件，供商家下架或個資請求）。`-all-for-shop` MUST 只影響指定商家。

#### Scenario: 刪除單店
- **WHEN** 對 shop A 執行 `-all-for-shop A -apply`
- **THEN** shop A 的歷史與物件消失，shop B 的完全不變

### Requirement: 清掃工作

系統 SHALL 提供 `cmd/design-sweep`（dry run 預設）：把認領租約過期的請求標為 `failed`（`service_lost`）、把 `awaiting_approval` 逾時者標為 `failed`（`approval_timeout`）、把核准逾期者標為 `failed`（`approval_expired`）（皆以 `actor_type = system` 追加事件）、報告孤兒物件，並以 `-verify` 重算事件雜湊串。

#### Scenario: 核准逾期
- **WHEN** 一筆 `approved` 請求已過 `approval_expires_at`
- **THEN** `-apply` 追加 `expired` 事件並轉為 `failed`
