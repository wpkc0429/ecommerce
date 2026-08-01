## 1. API additive：訂單列表的選填篩選（design D3／D4）

- [x] 1.1 `api/internal/order/service.go`：`AdminListParams` 新增 `PaymentStatus *int16`、`FulfillmentStatus *int16`、`DeliveryFrom *time.Time`、`DeliveryTo *time.Time`、`Q string`
- [x] 1.2 `api/internal/order/service.go`：`ListOrdersAdmin` 依上述欄位條件式加 `Where`；`Q` 以 `sql.P` + `Builder.Arg`（**方言感知的 placeholder 與參數繫結**——`sql.ExprP` 的 `?` 不會被改寫成 Postgres 的 `$N`，實測 500）對 `shipping_address->>'recipient_name'` / `->>'phone'` 做 ILIKE，可解析為正整數時併入訂單編號比對；**全部欄位皆未提供時查詢與變更前逐行相同**
- [x] 1.3 `api/internal/httpapi/order.go`：`listAdmin` 讀取 `payment_status`／`fulfillment_status`／`delivery_from`／`delivery_to`／`q`，日期以 `YYYY-MM-DD` 解析（無法解析＝視同未提供，沿用既有 tolerant-input 慣例）
- [x] 1.4 整合測試：`payment_status`／`fulfillment_status` 各自與併用；不帶時筆數不變
- [x] 1.5 整合測試：`delivery_from`／`delivery_to` 單邊與閉區間；**未指定配送日的訂單被排除**
- [x] 1.6 整合測試：`q` 命中收件人姓名、電話（大小寫不敏感）、訂單編號；跨店不外洩

## 2. 契約與表面鎖（design D12）

- [x] 2.1 `api/openapi/openapi.yaml`：`listOrdersAdmin` 新增五個選填參數（含格式與說明）
- [x] 2.2 以 `-openapi.update-surface` 重生 `api/openapi/v1-surface.txt`，逐行確認 diff **只有新增（預期 10 行）、零刪除**
- [x] 2.3 `make openapi` 四道全綠

## 3. admin 共用詞彙：第三次重複的處置（design D2）

- [x] 3.1 `admin/lib/dom.js` 新增 `rows(host, list, render, { empty, colspan })`：清空、空狀態列、逐列 append
- [x] 3.2 `admin/lib/dom.js` 新增 `renderPager(prefix, page, total, pageSize)`：頁碼文字與上下頁 disabled
- [x] 3.3 回頭改寫 `admin/views/categories.js` 與 `admin/views/products.js` 使用兩個 helper（否則重複只是被搬家）
- [x] 3.4 `admin/lib/money.js` 新增 `formatPoints(n)`／`formatPercent(n)`：**不經過 `formatMoney`**，避免點數被顯示成金額（design D10）

## 4. 前端路由與 shell 改造（design D5）

- [x] 4.1 `admin/lib/router.js`：解析 `#/<view>[/<param>]`、`navigate(name, ...params)`、`onRoute(handler)`、`current()`；約 40 行，無框架
- [x] 4.2 `admin/app.js`：`showTab(name, ...params)` 改為寫 hash；單一 `hashchange` 處理器負責切 panel、標 active、呼叫 `view.load(...params)`；hash 未變時直接渲染一次
- [x] 4.3 `admin/app.js`：開機與切換商家後依權限決定落點——hash 指向的視圖存在且有權限則路由過去，否則落到第一個有權限的視圖；未知 hash 同樣回落且不顯示錯誤
- [x] 4.4 跑既有 E2E 確認 CMS 與商品流程未因路由改造而壞

## 5. 運送方式視圖

- [x] 5.1 `admin/views/shipping-methods.js`：列表（名稱／carrier／固定運費 `formatMoney`／啟用）、建立／編輯 dialog、刪除
- [x] 5.2 依 `shipping_method.view/create/edit/delete` 決定分頁與各按鈕可見性；停用中的方式在視覺上可分辨

## 6. 訂單視圖（design D3／D8／D9／D10）

- [x] 6.1 `admin/views/orders.js` 列表：分頁 + 篩選列（訂單狀態／付款狀態／出貨狀態／指定配送日起訖／關鍵字），全部作為 query 參數送出
- [x] 6.2 套用任一日期界限時，畫面明示「僅顯示有指定配送日的訂單」
- [x] 6.3 列表欄位：編號、建立時間、收件人、指定配送日、金額（應付總額）、訂單／付款／出貨三個狀態徽章、動作
- [x] 6.4 訂單詳情：**金額三件組以組合式呈現**（小計 ＋ 運費 ＝ 應付總額）、運送方式名稱、指定配送日、收件資訊
- [x] 6.5 訂單詳情：品項表（商品／SKU／單價／數量／小計／**卡片訊息隨列顯示**）
- [x] 6.6 訂單詳情：付款紀錄（`GET …/orders/{id}/payments`，需 `payment.view`），**無任何手動改付款狀態的操作**
- [x] 6.7 訂單詳情：出貨區塊三態（未出貨→建立表單／已出貨→送達・退貨兩顆按鈕／終態→只讀），出貨表單只有 carrier 與追蹤號，按鈕文字明示「整筆訂單」
- [x] 6.8 標記退貨的確認訊息說明「已核發的點數會被扣回」；已取消訂單不顯示建立出貨表單
- [x] 6.9 取消訂單：確認後呼叫既有端點，409／422 原樣顯示伺服器訊息
- [x] 6.10 「檢視此顧客點數」按鈕導向 `#/points/{memberID}`
- [x] 6.11 依 `order.view/order.cancel/payment.view/shipment.view/shipment.create/shipment.update` 決定各區塊與按鈕可見性

## 7. 會員等級與點數視圖

- [x] 7.1 `admin/views/member-tiers.js`：列表（名稱／`min_points` 以點數呈現／`discount_percent` 以百分比呈現）、建立／編輯 dialog、刪除
- [x] 7.2 `admin/views/points.js`：以 member id 定址（深層連結 `#/points/{memberID}` 或手動輸入），顯示餘額、目前等級、流水帳（分頁：異動量／種類／訂單／時間）
- [x] 7.3 `admin/views/points.js`：`point.adjust` 時提供手動調整（非零異動量 + 非空理由），成功後重載餘額與流水帳
- [x] 7.4 依 `member_tier.*`／`point.view`／`point.adjust` 決定可見性；點數與百分比一律不經 `formatMoney`

## 8. 前端接入設定視圖（design D6）

- [x] 8.1 `admin/views/storefront-clients.js`：publishable key 區塊（列出／產生／撤銷），已撤銷者仍列出並標示撤銷時間，key 值完整顯示不遮蔽
- [x] 8.2 同檔 allowed origin 區塊（列出／登記／移除），依 `can("shop.manage_client_origins")` 決定整塊可見性，並標明「平台層設定」
- [x] 8.3 分頁本身在缺 `publishable_key.view` 且缺 `shop.manage_client_origins` 時不出現

## 9. 版面

- [x] 9.1 `admin/index.html`：新增訂單、訂單詳情、運送方式、會員等級、會員點數、前端接入六個 `.tab-panel` 容器
- [x] 9.2 `admin/styles.css`：篩選列、狀態徽章、金額組合式呈現、詳情區塊、流水帳表格的樣式

## 10. E2E（design D11）

- [x] 10.1 `e2e/happy-path.mjs`：後台 UI 建立運送方式（固定運費），斷言列表顯示 `NT$150` 且不出現被縮放的數值
- [x] 10.2 以商品鏈剛發佈的商品與該運送方式，經公開 API 訪客下單（帶卡片訊息與指定配送日）
- [x] 10.3 以深層連結 `#/orders/{id}` 開啟詳情，斷言三件組金額組合、卡片訊息、指定配送日、收件人
- [x] 10.4 付款（API 發起 + 已簽章 webhook），回訂單詳情斷言已付款
- [x] 10.5 後台 UI 建立出貨（carrier + 追蹤號）→ 斷言出貨狀態；再標記已送達 → 斷言終態且不再有狀態按鈕
- [x] 10.6 以深層連結 `#/points/{memberID}` 斷言餘額 = `floor(subtotal/100)`、流水帳含該訂單的核發列，且**畫面未以貨幣格式呈現點數**
- [x] 10.7 產生 publishable key，斷言列表出現 `pk_` 開頭的完整值
- [x] 10.8 會員等級 CRUD（高門檻建立→編輯→刪除，淨零變動）；allowed origin 以 `runID` 登記後移除
- [x] 10.9 `e2e/commerce-path.mjs`：以商家 owner 斷言 `/shops/{id}/me` 含 `publishable_key.create` 且不含 `shop.manage_client_origins`，且 `GET /allowed-origins` 回 403
- [x] 10.10 全部識別值帶 `runID`，確保可重複執行

## 11. 驗證

- [x] 11.1 `make test` 全綠
- [x] 11.2 `make test-int` 全綠（`-p 1`）
- [x] 11.3 `make lint` 無新增問題
- [x] 11.4 `make openapi` 四道全綠、`v1-surface.txt` diff 零刪除
- [x] 11.5 重置 DB → `make migrate && make seed-demo` → 三服務啟動 → `cd e2e && node happy-path.mjs` 全綠
- [x] 11.6 更新根 `CLAUDE.md` 的 Architecture notes：tripwire #1 的查證結論、`rows()`／`renderPager()`、hash 路由、點數 vs 金額的單位紀律、origin 可見性來源
