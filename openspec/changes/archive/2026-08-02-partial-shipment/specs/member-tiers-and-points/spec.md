## MODIFIED Requirements

### Requirement: Order return claws back previously awarded points, capped at the current balance

訂單 `fulfillment_status` 透過 `order.Service` 的受控入口轉為已退貨（`3`）時，系統 MUST 嘗試扣回該訂單先前核發的點數：扣回量 = `min(該訂單原本核發的點數, 會員目前的 points)`，MUST NOT 造成 `members.points` 變為負數。

該轉換的觸發點是**任一筆出貨被標記為已退貨**（spec shipping-logistics 的推導規則：任一筆出貨為已退貨即使訂單為 `3`）。因此在一張訂單有多筆出貨時，**只退回其中一箱也會扣回整筆訂單已核發的點數**——這是刻意接受的粗粒度：扣回量取自訂單層的核發列，而系統沒有把該核發量分攤到品項的口徑。部分退貨與部分退款不在本能力範圍內。

扣回量 MUST 取自該訂單**原本那一列核發記錄的 `points_delta`**，MUST NOT 以任何當下的金額或折扣率重新計算——因此帶折扣的訂單退貨時扣回的恰好是它當初核發的數字。

扣回 MUST 是冪等的——同一筆訂單的退貨轉換 MUST NOT 造成扣回被重複執行，**包含同一張訂單的第二筆出貨也被標記退貨的情形**。訂單若從未核發過點數（例如訂單從未付款即被出貨後退貨），扣回量為 0，`members.points` 不變。

#### Scenario: 已核發點數的訂單退貨後扣回等量點數
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，且會員目前餘額 >= 12
- **THEN** 該會員 `points` 減少 12，`point_transactions` 新增一筆該訂單的扣回記錄

#### Scenario: 帶折扣的訂單退貨扣回的是當初核發的數字
- **WHEN** 一筆折後基準核發 36 點的訂單轉為已退貨，且期間商家已把該等級的折扣率改成別的值
- **THEN** 扣回量為 36，MUST NOT 依新的折扣率重新計算

#### Scenario: 扣回量不超過會員目前餘額
- **WHEN** 一筆已核發 12 點的訂單轉為已退貨，但會員目前餘額因其他異動已降為 5
- **THEN** 該會員 `points` 最多扣回至 0（扣回 5 點，而非 12 點），MUST NOT 變為負數

#### Scenario: 重複觸發退貨轉換不重複扣回
- **WHEN** 同一筆訂單的已退貨轉換因重複觸發被處理第二次
- **THEN** 該會員的 `points` 不因第二次觸發而重複扣回

#### Scenario: 同一訂單的第二筆出貨退貨不重複扣回
- **WHEN** 一張含兩筆出貨的訂單，其兩筆出貨先後皆被標記為已退貨
- **THEN** 該訂單的扣回只發生一次，`members.points` MUST NOT 被扣兩次
