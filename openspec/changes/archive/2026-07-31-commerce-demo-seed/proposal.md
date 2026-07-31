## Why

Phase 2 的電商核心由 9 個 change 逐一疊上來，近期又加了 4 個。**每一個 change 的 integration test 都綠，但每一個都建自己的測試 fixture**——於是「下單 → 付款 → 出貨 → 發點數 → 退貨 → 扣回點數」這條完整的鏈，從來沒有在一份**真實的 seed 資料**上被跑通過一次。

這件事對本專案特別危險，因為其中最關鍵的一段是**刻意鬆耦合**的：`payment` 發布 `events.OrderPaymentSucceeded`、`points` 訂閱並核發；`shipping` 發布 `events.OrderReturned`、`points` 訂閱並扣回。三個套件互不 import——這是好設計，但它把「誰接上了誰」這件事從編譯期移到了**執行期的一行 `dispatcher.Subscribe`**。而那一行只存在於 `api/internal/app/wire.go`，**該檔案零測試覆蓋**：每個 integration test 都在檔頭寫著「dispatcher wiring mirrors app.wire.go」然後自己重建一份。刪掉 `wire.go` 裡的 `dispatcher.Subscribe(pointsService.Handle)`，`make test-int` 全綠，真實伺服器從此靜默不發點數。

同時 `make seed-demo` 的產物根本不足以走完這條鏈：實測為空的有 `shipping_methods`、`member_tiers`、`publishable_keys`、`shop_allowed_origins` 四張表。姊妹 change `order-extras` 剛讓結帳可選運送方式並把運費併入 `total_amount`，`storefront-client-auth` 剛把前台租戶解析改為 publishable key 優先——這兩項新能力在 demo 資料上**一次都沒被走過**。而 `member_tiers` 為空更讓點數等級評估在 demo 上永遠只走「無等級符合 → `level_id = NULL`」那一條分支。

## What Changes

- **擴充 demo seed**（僅 `-demo`，正式 seed 不受影響），補齊走完整條電商鏈所需的前置資料：ONDO 形狀的高單價花藝商品（含 S/M/L 尺寸 SKU）、四種運送方式（**其中一種刻意停用**）、三級會員等級階梯、一把 publishable key、一個已登記的瀏覽器 origin，以及一位有密碼的 demo 顧客。維持 seed 既有的冪等性紀律。
- **等級門檻與商品定價一起決定**（design D3）。這不是湊數字：現行 demo 目錄最貴的品項是 1290，一筆合理訂單只發 25 點——任何看起來正常的等級階梯都永遠跨不過去，等級評估邏輯在 demo 上等同不存在。新增的花藝品項單價 3200–4400，一筆單品訂單即可跨過 30 點門檻，**升級與降級因此都能在 seed 資料上被觀察到**。
- **新增 `e2e/commerce-path.mjs`**：不開瀏覽器（電商鏈的每一步都是 API 呼叫）、可獨立執行、**可重複執行**（斷言全為相對值，身分每次唯一）。涵蓋瀏覽商品 → 訪客結帳 → 付款 → 出貨 → 點數核發 → 退貨 → 點數扣回，以及會員路徑的購物車、卡片訊息合併鍵、等級升降、webhook 重送冪等、取消訂單歸還庫存。斷言驗到**餘額與流水帳的每一列**，不只 HTTP 狀態碼。
- **`e2e/happy-path.mjs` 維持既有進入點且維持瀏覽器 CMS 流程**，並在其後接著跑電商路徑，兩者的失敗數合併計算。`cd e2e && node happy-path.mjs` 因此仍然是「跑全部」的那一個指令，CLAUDE.md 既有記載不失效；`node commerce-path.mjs` 則是開發時不需要瀏覽器的快速子集。
- **修正公開商品端點的 SKU 排序未定義**（design D5）。實測同一筆商品的 `skus[]` 回傳順序與建立順序不符（觀察到 `[id=2, id=1]`）：`WithSkus()` 四處呼叫皆未指定排序。對 ONDO 而言這就是尺寸選單 S/M/L 每次重新整理都換位置。四處補上依 id 排序，與 `ListShippingMethods` / `ListMemberTiers` 既有慣例一致。
- **e2e 以與伺服器相同的順序解析 mock webhook 簽章金鑰**（design D6）：`PAYMENT_MOCK_WEBHOOK_SECRET` 環境變數 → 專案根 `.env` → 與 `config.Load()` 相同的內建預設值。這是實測踩到的坑——本機 `.env` 早於 payment-integration，不含該鍵，伺服器因此使用內建預設值，而 `.env.example` 裡的值是另一個字串；照著 `.env.example` 簽的 webhook 一律 401。

本變更**純 additive**：不新增／不修改／不移除任何 API 端點的路徑、請求形狀、狀態碼或授權規則；不產生 migration，`atlas.sum` 不變。唯一的行為變更是「同一份回應內 SKU 陣列的順序由未定義變為確定」——先前沒有任何契約保證過該順序。D13 破例窗口已於 `order-extras` 關閉，本變更不觸碰它。

不在本次範圍內（明確記錄，非遺漏）：

- **不把 demo shop 改造成 ONDO**。新增的是「高單價、有尺寸選項、會附卡片訊息」這組**形狀**，用以撐起等級階梯與卡片訊息合併鍵的驗證；既有的鞋類／包款目錄一列不動（它同時是 `seedDemoCatalog` 的冪等性錨點）。
- **不為 `wire.go` 補 Go 測試**。它需要真實 PostgreSQL + Redis + 完整設定才有意義，那正是 e2e 的定義；用 Go 測試重建一份就又回到「自己建 fixture」的原問題（design D1）。
- **不動 `.env` / `.env.example`**（`.env` 為 gitignore 的本機檔案，不由本變更代為修改）。e2e 改為與伺服器同源解析，並在 401 時印出可據以排查的診斷。
- **不做 seed 的多商家情境**（第二家 demo 商家用以驗證跨租戶隔離）。跨租戶隔離已有大量 integration test 覆蓋，而第二家商家會讓每一項 e2e 斷言都得先說明自己在講哪一家。
- **不把電商路徑塞進瀏覽器測試**。它的每一步都是 API 呼叫，開瀏覽器只會更慢更脆（design D2）。

## Capabilities

### New Capabilities

- `demo-seed`: `make seed-demo` 這個指令本身的行為契約——它 MUST 佈建哪些資料才算完成、這些資料之間 MUST 成立的關係（最關鍵的一條：等級門檻 MUST 能被 seed 目錄裡的單筆訂單跨過，否則等級邏輯在 demo 上不可觀察）、以及重複執行的冪等性。這是一個目前完全未被規格化、卻被 e2e 與所有本機開發直接依賴的介面。

### Modified Capabilities

- `product-catalog`: 商品回應中 SKU 集合的順序由未定義收斂為「依建立順序（id 遞增）」，並明訂該順序在公開與後台端點一致。

## Impact

**Schema / migration**

- **無**。純資料與程式碼；不新增資料表、不改欄位、不產生 migration，`atlas.sum` 不變。

**Go packages**

- `api/internal/seed/seed.go`：新增 `seedDemoFloral`、`seedDemoShipping`、`seedDemoTiers`、`seedDemoStorefrontClient`、`seedDemoCustomer`，各自帶自己的冪等性守衛（既有的 `seedDemoCatalog` 與其資料一列不動）。
- `api/internal/seed/seed_integration_test.go`（新檔）：把 `demo-seed` spec 的四條需求變成 `make test-int` 會跑到的測試——佈建完整性、**門檻可達性不變式**、冪等性（含「不覆寫被調整過的值」）、以及基準 seed 不含 demo 資料。
- `api/internal/catalog/service.go`：四處 `WithSkus()` 改用具名的 `orderSkusByID`。
- `api/internal/httpapi/products_integration_test.go`：新增 SKU 排序確定性的整合測試（四個端點一致、就地更新不改變相對順序）。

**E2E**

- `e2e/lib.mjs`（新檔）：`check`／HTTP 輔助／webhook 簽章／設定解析，供兩支 e2e 共用。
- `e2e/commerce-path.mjs`（新檔）：電商鏈，無瀏覽器，可獨立且可重複執行。
- `e2e/happy-path.mjs`：改用共用輔助，並於 CMS 流程後接續執行電商路徑。

**文件**

- `CLAUDE.md`：新增 `node commerce-path.mjs` 指令、demo seed 的內容與其固定識別值、以及「`wire.go` 的事件接線只由 e2e 覆蓋」這條必須被知道的事實。

**下游相依**

- ONDO 接入面：seed 的運送方式、尺寸 SKU 與等級階梯即為 ONDO 上線資料的對照組；`e2e/commerce-path.mjs` 本身就是一份可執行的接入範例（含卡片訊息合併鍵與運費／點數基準的正確用法）。
