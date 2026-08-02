## Why

平台今天**沒有任何彙總查詢**——每一條讀取端點回的都是列。商家要回答「這個月做了多少生意」，只能把分頁的訂單列表翻完自己加。

這個缺口是**兩個 change 主動留下的**，而且兩次都留下了同一份帳：

- `member-directory` design D5 拒絕交付訂單數與終身消費額，理由是「口徑未定：含不含已取消／未付款／運費／折抵，而一個看起來權威的錯數字比沒有數字更糟——**那是報表不是欄位**」，並在 Open Questions 指名它與 `tier-discount-application` 留下的「這一級目前折出了多少錢」是同一份東西。
- `admin-commerce-operations` design D3/D4 把訂單列表的日期篩選釘在 `requested_delivery_date`（Postgres `date`）而**不是 `created_at`**，理由是「`shops` 沒有時區欄位，『某一天的訂單』在伺服器上無法無歧義定義」。

報表幾乎全部是「某一天／某一月的營業額」，所以第二條是硬阻塞：**沒有時區欄位，這份報表連第一個數字都算不出來。** 而第一條是本 change 的實質內容——每一個數字都必須先回答「它算了什麼、沒算什麼」，並且把那個答案放在螢幕上，而不是只放在這份文件裡。

## What Changes

- **`shops` 長出 `timezone`**（IANA 名稱，**可為 NULL**）。既有列**一列都不回填**——NULL 表示「這家商家沒有設定過」，與「他選了 Asia/Taipei」是兩件不同的事實，而把兩者壓成同一個值正是「無聲錯位幾小時」的來源。未設定時報表回落到平台預設（`REPORT_DEFAULT_TIMEZONE`，預設 `Asia/Taipei`），**並在回應與畫面上如實標示這是回落而不是選擇**。
- 新增商家層的 `PUT /api/v1/admin/shops/{shopID}/settings`（`shop.update`，既有節點）設定時區。**刻意不新增對應的 GET**（沒有消費者；生效中的時區由報表回應自己帶著）。
- 新增兩條報表端點（`report.view`，**本 change 唯一的新權限節點**）：
  - `GET /api/v1/admin/shops/{shopID}/reports/sales` — 期間摘要 ＋ 依商家當地日／月分組的明細。
  - `GET /api/v1/admin/shops/{shopID}/reports/products` — 同期間的商品銷售排行。
- **口徑逐項定案並寫進契約的 description、寫進 spec、寫在畫面上**：只計未取消（`status=0`）且已付款（`payment_status=1`）且未退貨（`fulfillment_status≠3`）的訂單；時間軸是 `orders.created_at`；金額**不縮減成單一「營業額」**，而是組合式的五個數字（小計／折扣／商品淨額／運費／請款總額）；退貨另計一行；商品排行只能是折扣前金額。
- admin 新增「報表」視圖（無建置步驟、原生 ES module、不新增框架）。`formatTime` 由 `views/` 的四份拷貝收進 `admin/lib/format.js`（tripwire #2 的舊債，`partial-shipment` design D11 明文記錄且刻意留給下一個 admin change）。
- **不改既有訂單列表的 `delivery_from`/`delivery_to` 語意**。時區欄位落地後 D3/D4 那條限制的前提消失了，但改既有選填參數的語意是破壞性變更，而 D13 的破例窗口已關閉——記為後續項。

## Capabilities

### New Capabilities

- `shop-timezone`: 商家所在時區的儲存、驗證、擁有者與「未設定」的如實表達；以及它作為所有「某一天」語意的單一權威。
- `merchant-reports`: 商家營運報表的**口徑定義**與其強制揭露、期間營業額（摘要／日／月）、商品銷售排行、`report.view` 權限節點。

### Modified Capabilities

- `admin-operations-ui`: 新增報表視圖的行為要求——口徑與時區來源必須在畫面上明示、多幣別期間不得假裝加總出一個總額、期間選擇不入深層連結。

## Impact

- **資料庫**：一支 migration，只新增 `shops.timezone`（nullable varchar(64)）。**零回填、零 CHECK 運算式變動、零索引變動**；既有每一列的每一個欄位值一個位元都不動。
- **API**：三條新路由（2 GET ＋ 1 PUT），全部 additive；`api/openapi/openapi.yaml` ＋ `api/openapi/v1-surface.txt` 只增行。既有端點的請求與回應**逐位元組不變**。
- **Go**：新套件 `internal/report`；`internal/shoptz`（時區解析與驗證，`internal/report` 與 `internal/httpapi` 共用）；`internal/httpapi/reports.go`、`settings.go`；`Deps` 新增兩個 handler 欄位（→ 檢查 ①⑤⑥ 自動涵蓋）；`internal/seed` 新增一個權限節點。**不新增領域事件、不新增訂閱者**（檢查 ⑦ 一行不動）。
- **admin**：新增 `views/reports.js` 與 `lib/format.js`，`index.html` 一個 panel，`app.js` 註冊表一列；`views/{orders,members,points,storefront-clients}.js` 各刪掉一份 `formatTime`。
- **E2E**：`commerce-path.mjs`（口徑數字的端到端斷言）與 `happy-path.mjs`（報表視圖的畫面斷言）各新增一節。
- **設定**：新增 `REPORT_DEFAULT_TIMEZONE`（預設 `Asia/Taipei`）。不設定時行為與預設值相同。
