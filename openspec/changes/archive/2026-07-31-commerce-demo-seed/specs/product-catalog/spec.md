## ADDED Requirements

### Requirement: Deterministic SKU ordering in product responses

任何回傳商品之 SKU 集合的端點（公開商品清單、公開商品詳情、後台商品清單、後台商品詳情）MUST 以確定且穩定的順序回傳該集合：依 SKU 建立順序（識別碼遞增）排列。系統 MUST NOT 將該順序留給資料庫決定。

四個端點的順序 MUST 一致——同一筆商品在公開端點與後台端點看到的 SKU 順序 MUST 相同。此順序 MUST NOT 因商品或 SKU 的更新而改變（更新既有 SKU 不改變其識別碼，因而不改變其位置）。

#### Scenario: 同一筆商品重複查詢的 SKU 順序一致

- **WHEN** 對同一筆含多個 SKU 的商品連續呼叫公開商品詳情端點多次
- **THEN** 每次回應的 `skus` 陣列順序完全相同，且為識別碼遞增

#### Scenario: 公開與後台端點的 SKU 順序一致

- **WHEN** 分別以公開端點與後台端點取得同一筆商品
- **THEN** 兩者的 `skus` 陣列順序相同

#### Scenario: 更新 SKU 不改變既有 SKU 的相對順序

- **WHEN** 商品原有 SKU `[A, B]`，更新其中 A 的庫存後重新查詢
- **THEN** `skus` 順序仍為 `[A, B]`
