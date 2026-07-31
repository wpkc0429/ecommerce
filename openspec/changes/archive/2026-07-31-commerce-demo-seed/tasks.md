## 1. 前置查證

- [x] 1.1 實測 `make seed-demo` 後 `shipping_methods` / `member_tiers` / `publishable_keys` / `shop_allowed_origins` / `members` 皆為空（design Context 的事實前提）
- [x] 1.2 確認 `api/internal/app/wire.go` 零測試覆蓋——盤點每一支 integration test 是否自建 dispatcher（design D1 的立論基礎）
- [x] 1.3 手動跑通整條鏈（訪客路徑），確認事件鏈本身無缺陷，並記錄實際的金額與點數數字
- [x] 1.4 手動跑通會員路徑（購物車、同 SKU 不同卡片、等級升降、webhook 重送、取消歸還庫存）
- [x] 1.5 查證 mock webhook 金鑰的實際解析來源（`.env` / `.env.example` / `config.Load()` 預設三者是否一致，design D6）
- [x] 1.6 查證公開商品端點的 `skus[]` 是否有確定順序（design D8）
- [x] 1.7 盤點既有 demo 目錄的 slug／sku_code 是否被 seed 以外的程式碼引用（決定新增 vs 取代，design D10）

## 2. Demo seed：目錄（spec: demo-seed）

- [x] 2.1 `internal/seed/seed.go`：新增「花藝禮品」分類與兩款花束商品，各含 S/M/L 三個 SKU（價格取自 ONDO 的 `P[].price` + `SIZES[].d`，design D3/D10）
- [x] 2.2 花藝品項的庫存訂在可支撐 e2e 連續執行多次的量（design D7），且不使用失真的大數
- [x] 2.3 既有的鞋類／包款／草稿商品**一列不動**；`classic-runner` 仍為整段既有目錄的冪等守衛
- [x] 2.4 花藝區塊獨立為 `seedDemoFloral`，帶自己的冪等守衛（`anniversary-bouquet`），使既有資料庫也能補齊（design D4）

## 3. Demo seed：運送方式與會員等級（spec: demo-seed）

- [x] 3.1 新增 `seedDemoShipping`：四筆運送方式，三筆啟用（200／150／0）＋ 一筆停用（350），逐筆以 `(shop_id, name)` 冪等
- [x] 3.2 停用那筆的用途以註解寫明，比照既有草稿商品註解的形式（design D9）
- [x] 3.3 新增 `seedDemoTiers`：三級等級（0／30／100），逐筆以 `(shop_id, name)` 冪等
- [x] 3.4 在程式碼註解中寫明門檻與目錄定價的耦合關係與其理由（design D3）
- [x] 3.5 既有列 MUST NOT 被覆寫（design D4 的刻意不對稱）

## 4. Demo seed：前台客戶端與顧客身分（spec: demo-seed）

- [x] 4.1 新增 `seedDemoStorefrontClient`：固定值 publishable key（僅 a–z，落在 `ValidKeyFormat` 內）＋ 一筆 allowed origin，各以其唯一鍵冪等（design D5）
- [x] 4.2 註解寫明「固定值不增加暴露面」的理由與「不用底線分段」的理由（design D5）
- [x] 4.3 新增 `seedDemoCustomer`：具密碼的顧客身分，以 `(shop_id, email)` 冪等，密碼命名沿用 `-change-me` 慣例
- [x] 4.4 `seedDemoShop` 依序呼叫五個新函式，並確認全部落在既有的單一交易內

## 5. Demo seed 契約的自動化覆蓋（spec: demo-seed）

- [x] 5.1 新增 `internal/seed/seed_integration_test.go`：佈建完整性（三筆以上啟用＋至少一筆停用、免運與收費各一、三級階梯、key／origin／顧客）
- [x] 5.2 同檔：**門檻可達性不變式**——單筆單品訂單的核發點數 MUST 跨過最低的非零門檻（design D3 的耦合由測試守著，而非只寫在註解裡）
- [x] 5.3 同檔：冪等性——連跑兩次後全表列數不變；手動改過的運費在重跑後 MUST NOT 被蓋回
- [x] 5.4 同檔：不帶 `-demo` 的基準 seed MUST NOT 產生任何 demo 資料，但仍建立權限目錄與 starter 主題
- [x] 5.5 反證門檻可達性測試：把 `銀卡會員` 門檻暫時改為 500，確認該測試轉紅並印出可據以修正的訊息，然後還原

## 6. SKU 排序（spec: product-catalog）

- [x] 6.1 `internal/catalog/service.go`：`GetProduct` / `ListProducts` / `ListPublishedProducts` / `GetPublishedProductBySlug` 四處 `WithSkus()` 改用具名的 `orderSkusByID`（design D8）
- [x] 6.2 註解寫明為何是 id 序而非 `sku_code` 序或價格序
- [x] 6.3 新增整合測試：同一筆商品多次查詢的 `skus` 順序一致且為 id 遞增；公開清單／公開詳情／後台清單／後台詳情四者一致；就地更新 SKU 不改變相對順序

## 7. E2E：共用基礎（design D2/D6/D7）

- [x] 7.1 新增 `e2e/lib.mjs`：唯一的 `failures` 計數器與 `check()`／`fail()`；HTTP 輔助（JSON 收送、標頭合併、`brief()` 印出回應內容）
- [x] 7.2 `lib.mjs`：mock webhook 簽章（HMAC-SHA256 hex）與金鑰解析——`process.env` → 專案根 `.env` → 與 `config.Load()` 逐字相同的內建預設；**不讀 `.env.example`**（design D6）
- [x] 7.3 `lib.mjs`：webhook 回 401 時印出明確診斷（伺服器與 e2e 解析到不同金鑰，並列出 e2e 用了哪個來源）
- [x] 7.4 `lib.mjs`：由 API 回傳的等級階梯推導「某餘額應對應哪一級」的 `tierFor()`（design D7 — 門檻不寫死在 e2e）

## 8. E2E：電商路徑（design D11）

- [x] 8.1 新增 `e2e/commerce-path.mjs`，可 `node commerce-path.mjs` 單獨執行，亦可被 import；以 `pathToFileURL(process.argv[1])` 判斷是否為進入點（design D2）
- [x] 8.2 前置：以 **demo 商家 owner** 登入（design D7 的修訂）、由 publishable key 解析商家、斷言 seed 的運送方式／等級／key／顧客皆已存在（不存在時明確指出要重跑 `make seed-demo`）
- [x] 8.3 以 publishable key 呼叫公開端點：運送方式清單只含啟用者、商品清單與詳情、`skus` 依 id 遞增
- [x] 8.4 訪客路徑：以帶時間戳的唯一 email 結帳（卡片訊息 + 運送方式 + 配送日），斷言 `subtotal + fee === total`、運費與名稱快照、配送日回傳為裸日期
- [x] 8.5 訪客路徑：發起付款，斷言請款金額 `=== total_amount`（含運費）
- [x] 8.6 訪客路徑：簽章 webhook → 訂單 `payment_status` 轉為已付款
- [x] 8.7 訪客路徑：斷言點數 `=== floor(subtotal/100)`（**不是** `total/100`，並另立一條斷言證明兩者在本資料下確實不同），流水帳出現 `kind=0` 且 `points_delta` 與 `order_id` 相符
- [x] 8.8 訪客路徑：出貨 → 以訂單存取 token 查得出貨紀錄（含追蹤號）
- [x] 8.9 訪客路徑：退貨 → 斷言餘額回到 0、流水帳為兩列（核發列仍在）、扣回列 `kind=1` 且為原核發量的負值
- [x] 8.10 停用的運送方式結帳 → 422 指向 `/shipping_method_id`，且庫存未動
- [x] 8.11 會員路徑：以 seed 顧客登入（不註冊，design D7），加入購物車兩列同 SKU 不同卡片訊息，斷言為兩列而非數量 2
- [x] 8.12 會員路徑：記錄結帳前的點數與等級 → 以 0 元運費方式結帳（斷言 `total === subtotal`）→ 付款 → 斷言點數增量與等級**升級**至階梯推導出的那一級
- [x] 8.13 會員路徑：重送同一份 webhook → 點數與流水帳筆數皆不變
- [x] 8.14 會員路徑：出貨 → 會員自助查出貨 → 退貨 → 點數回到結帳前的值、等級回到階梯對該餘額的答案、該訂單恰好貢獻兩列流水帳
- [x] 8.15 取消路徑：不指定運送方式的訂單 `shipping_method_id === null` 且 `total === subtotal`；取消後庫存歸還
- [x] 8.16 庫存不足時明確報「庫存耗盡，請重跑 `make seed-demo`」而非讓 422 看起來像功能故障（design D7）
- [x] 8.17 後台登入回 429 時明確報出是速率限制與其窗口（design D7 的修訂）

## 9. E2E：既有進入點（design D2）

- [x] 9.1 `e2e/happy-path.mjs` 改用 `lib.mjs` 的 `check()`／`fail()`／`req()` 與共用計數器，既有的 17 項 CMS 斷言與 `pageerror` 監聽逐項保留
- [x] 9.2 `happy-path.mjs` 在 CMS 流程之後接續執行電商路徑，失敗數合併計算後統一結束行程
- [x] 9.3 CMS 段以 try/catch 收斂：admin/web 未啟動時記為失敗但**仍繼續跑**電商路徑（電商只需 API）
- [x] 9.4 確認 `cd e2e && node happy-path.mjs` 仍為「跑全部」的單一指令，且 CLAUDE.md 既有記載不失效

## 10. 驗證

- [x] 10.1 `make test` 全綠
- [x] 10.2 `make test-int` 全綠（`-p 1`）
- [x] 10.3 `make lint` — `0 issues.`
- [x] 10.4 確認無 migration 產生、`api/migrations/` 與 `atlas.sum` 未變（`git status` 對該目錄為空）
- [x] 10.5 **乾淨資料庫**：`make migrate && make seed-demo` → `node commerce-path.mjs` 全綠（59 項）
- [x] 10.6 連續第二次 `make seed-demo` → 各表列數完全不變
- [x] 10.7 `node commerce-path.mjs` 連續執行兩次皆全綠（可重複執行的驗收）
- [x] 10.8 三個服務全開時 `cd e2e && node happy-path.mjs` 全綠（17 CMS ＋ 59 電商 ＝ 76 項）
- [x] 10.9 反證 `wire.go` 覆蓋：移除 `dispatcher.Subscribe(pointsService.Handle)` 後 `make test-int` **仍全綠**而電商 e2e 轉紅 12 項，然後還原

## 11. 收尾

- [x] 11.1 更新 `CLAUDE.md`：新增 `node commerce-path.mjs` 指令、demo seed 的內容與固定識別值、`wire.go` 事件接線只由 e2e 覆蓋這條事實
- [x] 11.2 更新 `openspec/project.md`：E2E 結構、wire.go 覆蓋缺口、demo seed 為可測契約、集合端點必須明訂順序
- [x] 11.3 整理 ONDO 接入對照說明（見本檔末）

### 實作過程中的補充決定（與 design 的差異）

| 項目 | 決定 | 理由 |
| --- | --- | --- |
| 電商 e2e 的後台身分 | 由平台 super admin 改為 **demo 商家 owner**（`demo-owner@example.com`），design D7 已同步補記 | 實測連跑第二次 `happy-path.mjs` 時後台登入回 **429**：登入的嚴格規則是每 `(shop, IP, email)` 每 5 分鐘 5 次，而 happy path 本來就用掉兩次 `admin@example.com`。改用 owner 除了分開配額，更重要的是**它才是真實情境**——出貨、標記退貨、查顧客點數都是商家操作，用 super admin 會直接跳過 RBAC，於是「seed 出來的 `merchant_owner` 角色到底有沒有 `shipment.create`／`shipment.update`／`point.view`」從頭到尾沒被驗證過。 |
| 新增 `internal/seed` 整合測試 | 原計畫只靠 e2e 與人工步驟驗收 seed，改為同時寫成 Go 整合測試（本檔第 5 節） | `demo-seed` spec 的四條需求全部可在無 HTTP 的情況下驗證，放進 `make test-int` 就會被每次跑到；只留在人工檢查清單裡的保證等於沒有保證。**門檻可達性**（design D3 的耦合）尤其如此：那是一條最容易被善意編輯破壞、又完全不會報錯的不變式。 |
| 花藝品項獨立為 `seedDemoFloral` | 未併入 `seedDemoCatalog` | `seedDemoCatalog` 以 `classic-runner` 整段跳過，若併入，**既有的開發資料庫永遠拿不到新資料**。獨立函式＋自己的自然鍵守衛才符合 design D4。 |
| `orderSkusByID` 抽成具名函式 | 四處共用一個具名的 eager-load 修飾函式，而非四份匿名閉包 | 排序理由需要一段說明，寫四次是四份會分岔的註解。 |
| `.env` 缺鍵未代為補上 | 只在 e2e 端做同源解析與診斷 | `.env` 在 `.gitignore` 內，是開發者自己的檔案（design D6）。 |

### 實測紀錄

**乾淨資料庫（`DROP DATABASE` → `make migrate` → `make seed-demo`）後的 seed 產物**

| 表 | 內容 |
| --- | --- |
| `shipping_methods` | 台北市當日配送/自有車隊/200/active、全台隔日配送/黑貓宅急便/150/active、門市自取/門市/0/active、**離島加價配送/中華郵政/350/inactive** |
| `member_tiers` | 一般會員 0、銀卡會員 30 (5%)、金卡會員 100 (10%) |
| `publishable_keys` | `pk_demoseedstorefrontkeynotsecret`（未撤銷） |
| `shop_allowed_origins` | `http://demo.localhost:3000` |
| `members` | `demo-customer@example.com`，status 1，points 0，**level_id NULL** |
| `products` / `product_skus` | 既有 3 筆（含草稿）＋ 花藝 2 筆；SKU 9 筆，花藝為 3400/3800/4400 與 3200/3600/4200 |

**電商鏈的關鍵數字（訪客路徑，BQ-ANNI-M 3800 × 1 + 台北市當日配送 200）**

| 斷言 | 實測值 |
| --- | --- |
| 金額組成 | `subtotal 3800 + shipping_fee 200 = total 4000` |
| 向金流請款的金額 | **4000**（＝應付總額，含運費） |
| 點數核發 | **38**（`floor(3800/100)`），**不是 40**（`floor(4000/100)`）——運費不計點 |
| 流水帳（核發後） | 1 列：`kind=0, order_id=N, points_delta=+38, reason=訂單付款回饋` |
| 等級（核發後） | `(無等級)` → **銀卡會員**（階梯推導值） |
| 退貨後餘額 | **0** |
| 流水帳（退貨後） | 2 列：核發列原樣保留 ＋ `kind=1, points_delta=-38, reason=訂單退貨扣回` |
| 等級（退貨後） | **一般會員**（階梯對餘額 0 的答案） |

**會員路徑（BQ-ANNI-M × 2，兩張不同卡片，門市自取 0 元）**

| 斷言 | 實測值 |
| --- | --- |
| 購物車 | 兩列各 quantity 1（合併鍵 `(sku_id, gift_message)`），非一列 quantity 2 |
| 金額組成 | `subtotal 7600 + 0 = total 7600` |
| 點數增量 | **+76**，等級 `一般會員` → **銀卡會員** |
| webhook 重送 | 點數不變（76）、流水帳筆數不變 |
| 退貨後 | 餘額回到起始值、等級回到階梯對該餘額的答案、該訂單恰好兩列流水帳 |
| 取消未付款訂單 | 庫存精確歸還 |

**反證：`wire.go` 的覆蓋確實只來自 e2e**

| 動作 | 結果 |
| --- | --- |
| 移除 `wire.go` 的 `dispatcher.Subscribe(pointsService.Handle)` | 編譯通過 |
| `make test-int`（含 `internal/points`、`internal/httpapi` 全部） | **全綠**——每支 integration test 都自建 dispatcher |
| `node commerce-path.mjs` | **紅 12 項**，第一項即 `✗ points were awarded at all (payment → events → points, incl. wire.go subscription)` |
| 還原後 | 兩者皆綠 |

**反證：門檻可達性不變式確實被守著**

| 動作 | 結果 |
| --- | --- |
| 將 `銀卡會員` 門檻由 30 改為 500 | `TestDemoSeedTierThresholdsAreReachableFromTheCatalog` 轉紅，訊息直接指出「best SKU BQ-ANNI-L at 4400 awards 44 points, first threshold is 100」 |

**其他**

| 項目 | 結果 |
| --- | --- |
| `make test` / `make test-int` / `make lint` | 全綠 / 全綠 / `0 issues.` |
| `api/migrations/` 與 `atlas.sum` | 未變（本變更無 schema 改動） |
| `make seed-demo` 連跑兩次 | 各表列數完全相同 |
| `node commerce-path.mjs` 連跑兩次（乾淨 seed 後） | 皆 59 項全綠 |
| `node happy-path.mjs`（三服務全開） | 76 項全綠（17 CMS ＋ 59 電商） |

## ONDO 接入對照說明

`make seed-demo` 之後的 demo 商家就是 ONDO 上線資料的對照組。以下是「ONDO 前端現有的東西 → 平台上的對應物」。

### 一句話總結

**`e2e/commerce-path.mjs` 本身就是一份可執行的接入範例。** 它用的是 `fetch`、沒有任何框架、每一步都附了斷言說明為什麼要那樣呼叫——遇到「這個欄位到底該送哪裡」時，直接看那支檔案比看文件快。

### ① 商品與尺寸

| ONDO | 平台 |
| --- | --- |
| `P[] = {id, name, price, cat, img, desc}` | `GET /api/v1/shop/products` 的 `products[]`（`title`／`description`／`slug`） |
| `SIZES[] = {k, label, d}`（S −400 / M 0 / L +600） | 同一商品的多個 **SKU**，價差已內含在各 SKU 的 `price_amount` |
| `unitPrice(p, size) = p.price + s.d` | **不要自己算**：直接用選定 SKU 的 `price_amount` |

seed 的 `anniversary-bouquet`（3400／3800／4400）與 `daisy-day-bouquet`（3200／3600／4200）就是照 ONDO 的 `P[4]`／`P[3]` 加 `SIZES` 價差建的。

`skus[]` 的順序**現在是確定的**（依建立順序，即後台輸入順序），公開與後台一致，因此可以直接拿來當尺寸選單的順序——但送出時仍請以 `sku_code` 或 `id` 認人，不要用陣列位置。

### ② 運送方式

ONDO 的 `SHIPS`（`same` 200 / `next` 150 / `pick` 0）在 seed 裡就是三筆啟用的運送方式，運費逐一對應。**加碼一筆刻意停用的**（離島加價配送）：拿它來驗你的錯誤處理——公開清單不會回傳它，硬送它的 id 會得到 422 指向 `/shipping_method_id`。

`note`（「12:00 前下單…」）平台沒有對應欄位，那是文案，留在前端。

### ③ 身分與結帳

| ONDO | 平台 |
| --- | --- |
| 無登入 UI，`state.form = {name, phone, addr, date, card}` | `POST /api/v1/shop/guest/checkout`（訪客，不需任何憑證，需多一個 `email`） |
| 日後若做會員 | seed 的 `demo-customer@example.com` / `demo-customer-change-me` 可直接登入試會員路徑 |
| `state.cart[].msg`（卡片訊息） | `items[].gift_message`；**合併鍵是 `(sku_id, gift_message)`**，同款花配兩張不同卡片就是兩個品項 |
| `state.form.date` | 訂單層的 `requested_delivery_date`，必須是 `YYYY-MM-DD` |

前台租戶識別用 `X-Publishable-Key: pk_demoseedstorefrontkeynotsecret`（demo 用；正式站在後台自行產生一把）。瀏覽器直接呼叫還需要把你的 origin 登記進 `shop_allowed_origins`——seed 已登記 `http://demo.localhost:3000` 作為範例。

### ④ 金額與點數

- **合計不要自己算**：`total_amount` 就是應付總額（`subtotal_amount + shipping_fee`），也是金流實際請款的金額。三者關係由資料庫約束保證恆等。
- **點數以 `subtotal_amount` 計算，運費不計點**。demo 資料上的實例：3800 的花 + 200 運費 = 付 4000，拿 **38 點**（不是 40 點）。文案請寫「商品金額每 100 元 1 點（運費不計）」。
- 等級：seed 的階梯是 0／30／100，因此**一束花就會從「一般會員」升到「銀卡會員」**，退貨後降回去。要驗等級邏輯，用一束花即可，不必湊金額。

### ⑤ 完整鏈的呼叫順序（照抄自 e2e）

```
GET  /api/v1/shop/products                    → 挑商品
GET  /api/v1/shop/products/{slug}             → 挑 SKU（依 sku_code，別用索引）
GET  /api/v1/shop/shipping-methods            → 挑運送方式（只會回啟用中的）
POST /api/v1/shop/guest/checkout              → 下單，回傳訂單 + order_access_token
POST /api/v1/shop/guest/order/payments        → 發起付款（帶 order_access_token）
     …顧客在金流頁面完成付款，金流回呼平台的 webhook…
GET  /api/v1/shop/guest/order                 → 查訂單狀態（payment_status 轉 1）
GET  /api/v1/shop/guest/order/shipment        → 查出貨進度
```

出貨與退貨是商家後台的操作（`/api/v1/admin/shops/{id}/orders/{orderID}/shipments`），不在前端。
