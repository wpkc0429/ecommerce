# demo-seed Specification

## Purpose

`make seed-demo` 的行為契約。它不是佈景，而是本機開發與 e2e 直接依賴的介面：demo 佈建之後的資料狀態必須足以在不另外建立任何資料的前提下走完整條電商鏈（瀏覽 → 結帳 → 付款 → 出貨 → 點數核發 → 退貨 → 點數扣回）。

因此本能力規範的不只是「有哪些資料」，還包括這些資料之間必須成立的關係——最關鍵的一條是**等級門檻必須能被 seed 目錄裡的單筆訂單跨過**：不成立時等級評估仍會執行，但永遠回傳同一個答案，於是整段邏輯在 demo 資料上不可觀察。

## Requirements
### Requirement: Demo seeding is opt-in and never affects baseline seeding

系統 MUST 提供一個獨立於基準 seed 的 demo 佈建模式（`make seed-demo` / `go run ./cmd/seed -demo`）。基準 seed（權限目錄、平台角色、平台管理員）MUST NOT 因本能力而改變其產出；未指定 demo 模式時，系統 MUST NOT 佈建任何 demo 商家資料。

demo 佈建的資料 MUST 全部是商家透過既有後台 API 也能自行建立的普通資料——本能力 MUST NOT 佈建任何無法經由 API 產生的狀態。

#### Scenario: 基準 seed 不含 demo 資料

- **WHEN** 於乾淨資料庫執行不帶 demo 模式的 seed
- **THEN** 權限目錄、平台角色與平台管理員建立完成，且 MUST NOT 存在任何 demo 商家、商品、運送方式、會員等級、publishable key 或顧客身分

#### Scenario: demo 佈建包含基準 seed

- **WHEN** 於乾淨資料庫執行 demo 模式的 seed
- **THEN** 基準 seed 的全部產出與 demo 資料一併建立完成

### Requirement: Demo seed provisions the full commerce chain's prerequisites

demo 佈建完成後的資料狀態 MUST 足以讓下列完整流程在不另外建立任何資料的前提下走完：瀏覽商品 → 結帳（訪客與會員兩條路徑）→ 付款 → 出貨 → 點數核發 → 退貨 → 點數扣回。

為此，demo 佈建 MUST 至少包含：

- 已發佈的商品與其 SKU（含可售庫存），以及至少一筆草稿商品供驗證公開端點的草稿邊界；
- 至少三筆**啟用中**的運送方式，其固定運費 MUST 至少涵蓋一筆為 0（免運）與一筆大於 0；
- 至少一筆**已停用**的運送方式，供驗證「停用者不出現在公開清單且結帳選用時被拒」；
- 至少三級會員等級，構成 `min_points` 互不相同的階梯；
- 至少一把未撤銷的 publishable key，供前台以 `X-Publishable-Key` 解析商家；
- 至少一個已登記的瀏覽器 origin；
- 至少一位可登入的顧客身分（具密碼），供驗證會員結帳路徑而無須先呼叫註冊端點。

#### Scenario: 公開運送方式清單在 demo 資料上非空且已過濾

- **WHEN** demo 佈建後，以該商家的上下文呼叫公開的運送方式清單端點
- **THEN** 回傳所有啟用中的運送方式，且 MUST NOT 包含任何已停用者

#### Scenario: 以停用的運送方式結帳被拒

- **WHEN** 結帳時指定 demo 佈建的那筆已停用運送方式
- **THEN** 回 422 且指向該運送方式輸入欄位，訂單 MUST NOT 建立、庫存 MUST NOT 變動

#### Scenario: demo 顧客可直接登入

- **WHEN** 以 demo 佈建的顧客身分與其預設密碼呼叫該商家的會員登入端點
- **THEN** 登入成功並取得可用於購物車與結帳的存取權杖

#### Scenario: publishable key 可解析商家上下文

- **WHEN** 以 demo 佈建的 publishable key 呼叫任一前台端點
- **THEN** 該請求被解析到 demo 商家，且不需要任何其他識別標頭

### Requirement: Seeded tier thresholds are reachable from the seeded catalog

會員等級的門檻 MUST 與 demo 目錄的定價一起決定，使得**單筆合理訂單**（單一品項、數量為個位數）的點數核發即可跨越至少一個等級門檻。demo 佈建 MUST NOT 產生「任何合理訂單都跨不過最低門檻」的組合——該組合會讓等級評估邏輯在 demo 資料上恆定回傳同一結果，等同未被驗證。

同理，等級階梯的最低一級 MUST 使「已有點數異動的顧客」能對應到某一級，而尚未有任何點數異動的顧客 MUST 仍維持無等級——兩種狀態皆須在 demo 資料上可觀察。

#### Scenario: 單筆訂單付款後升級

- **WHEN** demo 顧客以 demo 目錄中的單一品項下單並完成付款
- **THEN** 其點數餘額跨過至少一個等級門檻，且其等級由付款前的等級變更為階梯中 `min_points` 不大於新餘額的最高一級

#### Scenario: 退貨扣回後降級

- **WHEN** 該筆訂單被標記為退貨
- **THEN** 點數扣回後餘額回到付款前的值，且其等級回到付款前的那一級

#### Scenario: 尚未消費的顧客沒有等級

- **WHEN** 查詢一位在 demo 佈建後從未有過任何點數異動的顧客
- **THEN** 其等級為無（未指派任何一級）

### Requirement: Demo seeding is idempotent per provisioned resource

重複執行 demo 佈建 MUST NOT 產生重複資料，MUST NOT 使任何既有資料列失效。每一類佈建資源 MUST 各自以其自然鍵判斷是否已存在，MUST NOT 倚賴單一全域旗標決定整體是否跳過。

對於已存在的資源，demo 佈建 MUST NOT 覆寫其現值——這些是商家可在後台編輯的營運資料，重跑 seed MUST NOT 把開發者或商家調整過的值改回預設。

#### Scenario: 連續兩次 demo 佈建

- **WHEN** 對同一資料庫連續執行兩次 demo 佈建
- **THEN** 第二次執行成功且資料庫內容與第一次執行後完全相同（無新增列、無變更列）

#### Scenario: 對既有資料庫補齊新增的佈建項目

- **WHEN** 對一個先前已 demo 佈建、但尚無運送方式與會員等級的資料庫再次執行 demo 佈建
- **THEN** 缺少的運送方式與會員等級被補齊，既有的商品、頁面與商家成員資料 MUST NOT 被改動

#### Scenario: 不覆寫被調整過的值

- **WHEN** 開發者將某筆 demo 運送方式的運費由預設值改為其他值後，再次執行 demo 佈建
- **THEN** 該筆運送方式維持被調整後的值

