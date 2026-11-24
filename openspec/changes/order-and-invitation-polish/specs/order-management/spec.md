## ADDED Requirements

### Requirement: 訂單品項帶選項快照

訂單品項 MUST 在建單時快照 SKU 的 `options`，並於顧客、訪客與後台的訂單回應中以 `options` 回傳（物件；SKU 沒有選項時為 `{}`）。快照 MUST NOT 因日後 SKU 的編輯或刪除而改變。建立於此功能之前的訂單 MUST 回傳 `options`（可為 `null`），且讀取 MUST NOT 因此失敗。

#### Scenario: SKU 日後被修改
- **WHEN** 下單後該 SKU 的 options 被改成別的值，或 SKU 被刪除
- **THEN** 訂單品項的 `options` 仍是下單當時的值

#### Scenario: 舊訂單
- **WHEN** 讀取早於此功能且無法回填的訂單
- **THEN** 品項的 `options` 為 `null`，回應仍為 200
