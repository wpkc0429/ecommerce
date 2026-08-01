## Why

change `admin-commerce-catalog` 讓商家能在後台建商品、改庫存、調價。但**商家的日常營運仍然沒有任何介面**：訂單看不到、出貨標不了、運送方式建不了、publishable key 產不出來、點數餘額查不到。`grep -iE "order|shipment|shipping-method|member-tier|point|publishable" admin/views/*.js` 今天零命中——這些能力的 API 早在 `order-management`（2026-07-18）就交付了，缺的只有畫面。

第一個外部商家 ONDO 溫度花室即將上線。一個商家可以接受「商品要工程師幫忙建」，不可能接受「每一筆訂單都要工程師下 curl 才看得到、每一次出貨都要工程師改狀態」。這是營運阻塞，而且是每天都會撞到的那種。

## What Changes

**新增六個後台視圖（純前端，繼承 `admin-commerce-catalog` design D1 的原生 ES module 架構）**

- **訂單**：列表（分頁／訂單狀態／付款狀態／出貨狀態／指定配送日期間／關鍵字）、詳情（品項與卡片訊息、金額三件組 `subtotal_amount` + `shipping_fee` = `total_amount`、收件資訊、指定配送日、付款紀錄、出貨紀錄）、取消。
- **出貨**：在訂單詳情內建立 shipment（carrier + 追蹤號）、推進到「已送達」或「已退貨」（終態，狀態機由伺服器判定）。
- **運送方式**：CRUD（名稱、carrier、固定運費、啟用）。
- **會員等級**：CRUD（`min_points` 階梯、`discount_percent` 本階段僅儲存）。
- **點數**：以會員為單位檢視餘額、等級與流水帳（分頁），並在具 `point.adjust` 時手動調整。入口是訂單詳情的「檢視此顧客點數」與可分享的深層連結。
- **前端接入**：publishable key 的產生／列出／撤銷（商家層權限），以及 allowed origin 的登記／移除（**平台層**權限 `shop.manage_client_origins`）。

**後台 shell 新增雜湊路由（約 40 行，無框架）**

`#/orders/1234`、`#/points/57` 這類深層連結是真實的商家需求——「這筆訂單有問題，你看一下」必須貼得出去。路由同時解決另一個問題：訂單詳情與點數頁都是「帶參數的視圖」，沒有路由就得靠模組間互相持有狀態。

**API：四處 additive 變更（design D3／D4 論證）**

商家訂單列表新增選填參數 `payment_status`、`fulfillment_status`、`delivery_from`／`delivery_to`（依 `requested_delivery_date` 篩選）、`q`（訂單編號精確比對，或收件人姓名／電話的大小寫不敏感子字串）。**不帶任何新參數時，查詢與回應與變更前逐位元組相同。**

這四項不是「UI 方便」而是「分頁列表無法在前端過濾」：前端只拿得到當頁，對當頁過濾會產生一個**主動說謊**的畫面（「今天沒有要出的貨」而其實在第 3 頁）——`admin-commerce-catalog` design D6 對商品搜尋的論證在此逐字成立，而且訂單的後果更嚴重（漏出一筆貨 vs. 重建一個商品）。

皆為 v1 additive（新選填 query 參數），不觸及 D13 已用盡並關閉的破例窗口。`api/openapi/openapi.yaml` 與 `api/openapi/v1-surface.txt` 同步更新，鎖檔只增不減。

**明確不做（留給後續 change，見 design Open Questions）**

- **顧客名冊**：平台今天沒有任何「列出會員」的端點，點數視圖因此只能從訂單或已知 member id 進入。一份顧客清單需要搜尋、分頁與一份 PII 揭露決策，那是一個獨立 change 的體量，不是這裡順手加一個端點。
- 訂單的 `created_at` 日期區間：平台沒有商家時區欄位，「某一天」在伺服器上無法無歧義地定義（design D4）。
- 退款、部分出貨、訂單匯出、批次操作——皆無對應 API，且各自是獨立領域。

## Capabilities

### New Capabilities
- `admin-operations-ui`: 後台日常營運介面的行為契約——訂單的檢視與取消、出貨的建立與狀態推進、運送方式與會員等級的維護、會員點數的檢視與調整、前端接入憑證（publishable key）與 origin 的維護，以及這些畫面依權限決定可見性的規則。

### Modified Capabilities
- `order-management`: 商家訂單列表新增選填篩選（付款狀態、出貨狀態、指定配送日區間、關鍵字），既有的分頁與 `status` 篩選語意不變。

`admin-catalog-ui` **不列為 modified**：深層連結是 shell 的新行為，寫在新 capability 裡即可，重複描述會讓兩份 spec 日後各自漂移；而該 capability 既有的「無建置步驟」「金額不縮放」「依權限決定可見性」等需求在本 change 全部原樣成立（`views/categories.js`、`views/products.js` 改用新的共用 helper 是實作細節，不是需求變更）。

## Impact

- 新檔案：`admin/lib/router.js`、`admin/views/orders.js`、`admin/views/shipping-methods.js`、`admin/views/member-tiers.js`、`admin/views/points.js`、`admin/views/storefront-clients.js`。
- 修改：`admin/app.js`（視圖註冊表支援參數與路由）、`admin/index.html`（六個新 panel）、`admin/styles.css`、`admin/lib/dom.js`（若且唯若出現第三次重複才新增 helper）。
- 修改（API，additive）：`api/internal/order/service.go`（`AdminListParams` 新增四類選填欄位）、`api/internal/httpapi/order.go`（讀取新 query 參數）、`api/openapi/openapi.yaml`、`api/openapi/v1-surface.txt`。
- 修改：`e2e/happy-path.mjs`（後台營運鏈的瀏覽器斷言）、`e2e/commerce-path.mjs`（權限可見性來源的 API 斷言）。
- 新增測試：`api/internal/httpapi/order_integration_test.go` 的新篩選案例、`api/internal/httpapi/shopme_integration_test.go` 的平台層節點可見性案例。
- 不動：資料庫 schema、migrations、events 接線、任何既有端點的既有行為、`web/`。
