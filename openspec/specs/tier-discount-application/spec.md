# tier-discount-application Specification

## Purpose
TBD - created by archiving change tier-discount-application. Update Purpose after archive.
## Requirements
### Requirement: 結帳時套用顧客所屬會員等級的折扣

結帳時，系統 MUST 解析下單顧客在該商家目前所屬的會員等級（`members.level_id`），並將該等級的 `discount_percent` 套用於本筆訂單。此行為 MUST 對**所有**下單路徑一致（會員自助結帳與訪客結帳），且 MUST 由單一結帳實作（`order.Service.Checkout`）完成，MUST NOT 存在第二條套用折扣的路徑。

顧客不屬於任何等級（`level_id` 為 NULL）時，折抵金額 MUST 為 `0`，且訂單的行為與本能力存在之前**逐位元組相同**。

等級的解析 MUST 讀取由點數機制維護的 `members.level_id`（spec member-tiers-and-points），MUST NOT 在結帳路徑上以當下的點數餘額重新評估等級——兩個判定來源必然會分歧。

等級與折扣率的解析 MUST 發生在結帳交易**之外**（與運送方式解析同一階段）：解析期間商家調整等級設定 MUST NOT 導致結帳失敗，顧客 MUST 被套用其送出請求當下的折扣率。

#### Scenario: 持有折扣等級的顧客結帳

- **WHEN** 顧客所屬等級的 `discount_percent` 為 `10`，其購物車品項小計為 `3400`
- **THEN** 訂單的 `discount_amount` 為 `340`、`discount_percent` 為 `10`，`total_amount` 為 `3400 - 340 + shipping_fee`

#### Scenario: 不屬於任何等級的顧客結帳

- **WHEN** 顧客的 `level_id` 為 NULL（例如首次購買、從未核發過點數）
- **THEN** 訂單的 `discount_amount` 為 `0`、`discount_percent` 為 `null`、`member_tier_name` 為 `null`，`total_amount` 等於 `subtotal_amount + shipping_fee`

#### Scenario: 等級存在但未設定折扣

- **WHEN** 顧客所屬等級的 `discount_percent` 為 NULL 或 `0`
- **THEN** 訂單的 `discount_amount` 為 `0`，`discount_percent` 為 `0`，`member_tier_name` 為該等級名稱——「屬於一個沒有折扣的等級」與「不屬於任何等級」MUST 在訂單上可分辨

#### Scenario: 兩條下單路徑套用相同規則

- **WHEN** 同一個顧客身分分別以會員結帳與訪客結帳下兩筆品項相同的訂單
- **THEN** 兩筆訂單的 `discount_percent`、`discount_amount` 與金額組成完全相同

### Requirement: 折抵金額只套用於品項小計，運費不打折

折抵金額 MUST 依 `subtotal_amount`（品項小計）計算，MUST NOT 將 `shipping_fee` 納入折扣基礎。`shipping_fee` MUST 保持為所選運送方式 `flat_rate` 的原值快照，MUST NOT 因折扣而改變。

#### Scenario: 折扣不改變運費

- **WHEN** 一筆品項小計 `3600`、運費 `200` 的訂單套用 10% 等級折扣
- **THEN** `discount_amount` 為 `360`（依 `3600` 計算），`shipping_fee` 仍為 `200`，`total_amount` 為 `3440`

#### Scenario: 折扣金額不因配送方式而異

- **WHEN** 兩筆品項完全相同的訂單分別選定運費 `200` 與運費 `0` 的運送方式，兩者顧客等級相同
- **THEN** 兩筆訂單的 `discount_amount` 完全相同

### Requirement: 折抵金額以整數運算計算，且採無條件捨去

折抵金額 MUST 以下式計算，全程整數運算，MUST NOT 使用浮點數：

```
discount_amount = subtotal_amount × discount_percent / 100
```

除法 MUST 為**無條件捨去**（floor，整數除法），MUST NOT 為四捨五入或無條件進位。乘法 MUST 在除法之前執行，MUST NOT 先除以 100（先除會丟失小於 100 的尾數，造成大 100 倍的誤差）。

其結果是：顧客實際獲得的折扣比例 MUST NOT 超過商家設定的 `discount_percent`；不整除時的差額 MUST 小於一個最小貨幣單位。

此捨入方向 MUST 與點數核發（`floor(基準 / 100)`）一致——本系統 MUST 只存在一種捨入方向。

#### Scenario: 整除的折抵

- **WHEN** `subtotal_amount = 3400`、`discount_percent = 10`
- **THEN** `discount_amount = 340`

#### Scenario: 不整除的折抵無條件捨去

- **WHEN** `subtotal_amount = 3333`、`discount_percent = 10`（精確值為 `333.3`）
- **THEN** `discount_amount = 333`，MUST NOT 為 `334`

#### Scenario: 不整除且小數部分大於一半仍然捨去

- **WHEN** `subtotal_amount = 3339`、`discount_percent = 10`（精確值為 `333.9`）
- **THEN** `discount_amount = 333`，MUST NOT 為 `334`

#### Scenario: 折抵不足一個最小貨幣單位時為零

- **WHEN** `subtotal_amount = 5`、`discount_percent = 10`（精確值為 `0.5`）
- **THEN** `discount_amount = 0`，`total_amount` 等於 `subtotal_amount + shipping_fee`

#### Scenario: 百分之百折扣

- **WHEN** `subtotal_amount = 3400`、`discount_percent = 100`
- **THEN** `discount_amount = 3400`，`total_amount` 等於 `shipping_fee`

#### Scenario: 零金額訂單

- **WHEN** `subtotal_amount = 0` 且顧客持有任意折扣等級
- **THEN** `discount_amount = 0`，寫入 MUST 成功

### Requirement: 套用結果以快照形式記錄於訂單，且永不重算

訂單 MUST 記錄下列三個折扣快照欄位：

- `discount_amount`：實際折抵金額，`int64` 最小貨幣單位，MUST NOT 為 NULL（無折扣時為 `0`）。
- `discount_percent`：當時**實際套用**的折扣率整數。顧客當時不屬於任何等級時 MUST 為 NULL；屬於某等級時 MUST 為一個 0–100 的數字（該等級未設定折扣時為 `0`）。
- `member_tier_name`：當時所屬等級的名稱快照。顧客當時不屬於任何等級時 MUST 為 NULL。

`discount_percent` 與 `member_tier_name` MUST 同時為 NULL 或同時非 NULL；此配對關係 MUST 由資料庫層約束強制。

這三個值 MUST 於結帳當下一次算定並永久快照。訂單建立後，商家調整或刪除該會員等級、顧客升級／降級／等級被清空，MUST NOT 改變任何既有訂單的 `discount_amount`、`discount_percent`、`member_tier_name` 或 `total_amount`。系統 MUST NOT 提供任何以現值重算既有訂單折扣的路徑。

#### Scenario: 商家事後調高折扣率不影響既有訂單

- **WHEN** 一筆以 `discount_percent = 10` 成立的訂單建立後，商家把該等級的 `discount_percent` 改為 `20`
- **THEN** 該訂單的 `discount_percent` 仍為 `10`、`discount_amount` 與 `total_amount` 完全不變

#### Scenario: 刪除等級不影響既有訂單

- **WHEN** 一筆訂單所依據的會員等級被商家刪除（持有該等級的會員 `level_id` 被清為 NULL）
- **THEN** 該訂單的 `member_tier_name`、`discount_percent`、`discount_amount` 與 `total_amount` 完全不變

#### Scenario: 顧客升降級不影響既有訂單

- **WHEN** 顧客於訂單建立後因點數異動而升級或降級
- **THEN** 既有訂單的折扣快照與金額完全不變；只有其後建立的新訂單才依新等級計算

### Requirement: 訂單金額恆等式納入折抵項，並由資料庫層強制

訂單的四個金額欄位 MUST 滿足：

```
total_amount = subtotal_amount − discount_amount + shipping_fee
```

此組成關係 MUST 由資料庫層的約束強制，MUST NOT 僅依賴應用層計算。此外 MUST 由資料庫層強制：

- `discount_amount >= 0`
- `discount_amount <= subtotal_amount`（折抵 MUST NOT 使商品金額變為負數）
- `discount_percent` 為 NULL 或介於 0 與 100 之間

任何向金流請款的金額 MUST 使用 `total_amount`——它就是折抵後的應付總額。

#### Scenario: 金額組成不可被破壞

- **WHEN** 任何路徑嘗試建立或寫入一筆 `total_amount != subtotal_amount - discount_amount + shipping_fee` 的訂單
- **THEN** 寫入 MUST 失敗（資料庫約束拒絕），MUST NOT 產生不一致的訂單

#### Scenario: 折抵不得超過品項小計

- **WHEN** 任何路徑嘗試寫入 `discount_amount > subtotal_amount` 的訂單
- **THEN** 寫入 MUST 失敗（資料庫約束拒絕）

#### Scenario: 金流請款的是折扣後的金額

- **WHEN** 一筆 `subtotal_amount = 3400`、`discount_amount = 340`、`shipping_fee = 150` 的訂單發起付款
- **THEN** 送往金流的金額 MUST 為 `3210`，MUST NOT 為 `3550`

### Requirement: 既有訂單在本能力導入後其金額與已核發點數皆不變

導入本能力時，既有訂單的 `discount_amount` MUST 為 `0`、`discount_percent` 與 `member_tier_name` MUST 為 NULL，且 `subtotal_amount`、`shipping_fee`、`total_amount` 三者的既有值 MUST NOT 有任何改變。既有訂單依新恆等式 MUST 全部成立，MUST NOT 需要任何資料回填。

依新的點數核發基準（spec member-tiers-and-points）重新計算既有訂單所應核發的點數，MUST 與當初實際核發的點數完全相同。

#### Scenario: 既有訂單的金額未被改寫

- **WHEN** 本能力導入後檢查每一筆既有訂單
- **THEN** 每一筆的 `total_amount = subtotal_amount - discount_amount + shipping_fee` 成立，且 `total_amount`、`subtotal_amount`、`shipping_fee` 的值與導入前完全相同

#### Scenario: 既有訂單的已核發點數在新規則下仍然正確

- **WHEN** 以新的核發基準（折後品項小計）重新計算一筆導入前既已付款的訂單
- **THEN** 得到的點數與該訂單當初實際核發的點數相同

### Requirement: 折扣不削弱既有的並發扣庫存與冪等鍵保證

折扣的解析與計算 MUST NOT 改變結帳交易內的條件式扣庫存與其受影響列數檢查。並發結帳 MUST 仍然不超賣。

帶冪等鍵的重送 MUST NOT 重新解析等級或重新計算折扣：回放 MUST 回傳原訂單當下的折扣快照。冪等鍵的指紋計算 MUST NOT 因本能力而改變（本能力不新增任何請求鍵）。

#### Scenario: 並發結帳仍不超賣

- **WHEN** 多個持有折扣等級的顧客同時搶購庫存有限的同一個 SKU
- **THEN** 成功的訂單數 MUST 不超過庫存量，庫存 MUST NOT 變為負數，每一筆成功訂單的折抵金額 MUST 各自正確

#### Scenario: 回放回傳原本的折扣快照

- **WHEN** 一次帶冪等鍵的結帳成功後，顧客的等級改變或商家調整了折扣率，然後以相同鍵與相同主體重送
- **THEN** 回放回傳的訂單其 `discount_amount`、`discount_percent`、`member_tier_name` 與第一次完全相同

#### Scenario: 冪等鍵過期後的重送是一筆新訂單

- **WHEN** 同一把冪等鍵在有效期過後再次送出相同主體
- **THEN** 建立一筆**新**訂單，其折扣依**當下**的等級計算，MUST NOT 沿用先前那筆訂單的快照

