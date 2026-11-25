## ADDED Requirements

### Requirement: 排程綁定審閱過的草稿版本

排程 MUST 記錄建立時的 `draft_rev`（呼叫者以 `If-Match` 帶入的審閱版本），到點時只有文件的 `draft_rev` 仍相同才可發佈。排程之後的任何草稿儲存（含還原與相容端點的寫入）MUST 在同一交易內讓排程失效（`superseded`），所以未經審閱的修改不會被自動發佈。每份文件同時 MUST 至多有一筆等待中的排程。

#### Scenario: 排程後編輯
- **WHEN** 草稿已排程，之後有人儲存草稿
- **THEN** 排程立即變為 `superseded`（原因 `edited`），到點時不發佈任何東西

#### Scenario: 缺少審閱版本
- **WHEN** 排程請求沒有 `If-Match`
- **THEN** 回 428，不建立排程

### Requirement: 時間以 UTC 與資料庫時鐘處理

`publish_at` MUST 是帶 UTC 偏移的 RFC 3339（無偏移被拒絕），以 UTC 儲存並進位到整分鐘；MUST 至少在 2 分鐘後、至多在 90 天內。「到點」MUST 由資料庫的 `now()` 判斷，而不是各行程的牆上時鐘。

#### Scenario: 無偏移的時間
- **WHEN** `publish_at` 為 `2026-12-01T09:00:00`
- **THEN** 回 422，不猜時區

### Requirement: 到點走與手動發佈相同的路徑

排程器 MUST 透過與手動發佈相同的 `content.Service.Publish` 發佈，因此 revision、稽核、快取失效與發佈 hook 都相同；發佈前 MUST 對作用中 schema 重新驗證，且 MUST 重新確認排程者仍具有 `content.publish`。

#### Scenario: 到點驗證失敗
- **WHEN** 排程後 schema 變更，使草稿不再通過驗證，然後到點
- **THEN** 不發佈、文件標 `needs_attention`、排程為 `failed`（`invalid`）並寫稽核

#### Scenario: 排程者已被停用
- **WHEN** 排程者在到點前被移出商家
- **THEN** 不發佈，排程為 `failed`（`permission_revoked`）

### Requirement: 錯過視窗與冪等

排程器停機後恢復時，遲到在寬限（預設 1 小時）內的排程 MUST 照常發佈並記錄遲到秒數，超過寬限的 MUST 標為 `missed` 且不發佈。兩個排程器同時執行 MUST NOT 使同一筆排程發佈兩次；崩潰遺留的處理中排程 MUST 能被恢復，且不重複寫 revision。

#### Scenario: 兩個排程器
- **WHEN** 兩個排程器同時處理同一筆到點的排程
- **THEN** 只發佈一次，只有一筆 `publish` revision 與一筆 `doc.publish` 稽核

#### Scenario: 遲到超過寬限
- **WHEN** 排程器停機 3 小時後恢復
- **THEN** 該排程為 `missed`，文件維持未發佈
