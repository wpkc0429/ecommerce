## ADDED Requirements

### Requirement: v1 表面鎖允許且僅允許 `v1-removed.txt` 所列的一次性例外

v1 表面鎖（`api/openapi/v1-surface.txt`）MUST 維持「每一行都必須還在」的語意，唯一的例外是 `api/openapi/v1-removed.txt` 所列的行：該檔記錄經使用者明確核准、並由決策紀錄（ADR 0002）說明理由的一次性移除。`v1-removed.txt` 內的每一行 MUST NOT 出現在目前產生的表面中，且其行數 MUST 等於 ADR 0002 記錄的數字。未列入該檔的任何移除 MUST 仍使鎖檔檢查失敗；此例外 MUST NOT 被視為重新開啟先前已關閉的破例窗口。

#### Scenario: 已移除的行不得悄悄回來
- **WHEN** 有人把 `v1-removed.txt` 所列的某條路由重新加回契約
- **THEN** 契約測試失敗

#### Scenario: 未列入的移除仍被擋下
- **WHEN** 有人刪除 `v1-surface.txt` 中不在 `v1-removed.txt` 的一行
- **THEN** 鎖檔檢查失敗

#### Scenario: 例外清單被偷偷擴充
- **WHEN** `v1-removed.txt` 的行數與 ADR 0002 記錄的數字不一致
- **THEN** 契約測試失敗
