## ADDED Requirements

### Requirement: demo 目錄含「本季新品」分類

`-demo` seed MUST 建立 slug 為 `new-arrivals`、名稱為「本季新品」的分類，並將至少兩個已發佈的 demo 商品關聯至該分類；demo 花藝商品 MUST 帶非空 `subtitle`。此佈建 MUST 冪等。

#### Scenario: 重跑 seed 不重複建立
- **WHEN** `make seed-demo` 執行兩次
- **THEN** 只存在一個 `new-arrivals` 分類，關聯商品數不變
