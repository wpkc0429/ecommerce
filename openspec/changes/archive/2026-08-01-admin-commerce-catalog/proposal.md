## Why

商品／SKU／分類的管理 API 在 change `product-catalog`（2026-07-18）就已交付，並被後續 5 個 proposal 依賴。但後台 `admin/` 到今天為止只有三件事：登入、schema 驅動的 CMS 內容編輯、發佈／預覽——`grep -iE "product|sku|categor|order|cart" admin/app.js` 零命中。**商家要建一個商品、改一次庫存、調一次價，唯一路徑是自己組 curl 與 JSON。** 第一個外部商家（ONDO 溫度花室）即將上線，這是營運阻塞，不是體驗問題。

同時這是 `admin/`——一個刻意選擇「無建置步驟、靜態 ES modules、616 行 vanilla JS」的薄殼——第一次面對真正複雜的表單：巢狀 SKU。下一個 change（`admin-commerce-operations`：訂單／出貨／運送方式／會員等級／publishable key 與 origin）會直接繼承本 change 的結論，因此**本 change 最重要的產出是那個架構決策與它的論證**，UI 本身只是它的第一個證據。

## What Changes

**架構（design D1–D3）**

- `admin/` **維持無建置步驟**，但從單一 `app.js` 改為原生 ES module 的多檔結構（`lib/*.js` + `views/*.js`），並抽出最小可重用詞彙：`el()`／`repeater()`（巢狀列）／`formatMoney()`／權限閘。既有 CMS 程式碼逐字搬移，行為不變。
- 巢狀 SKU 編輯採「**每一列自己持有 `read()` closure**」的模式：狀態住在 closure，DOM 只是輸出。明確**不**沿用 CMS schema 驅動表單那條「用 label 文字回頭找 DOM 節點」的收集路徑，也不把商品塞進 schema 驅動表單——商品的形狀是固定的 Go struct，不是使用者可自訂的 CMS schema。

**功能（新 capability `admin-catalog-ui`）**

- 分類：列表、建立、編輯（含改名／改 slug／改父分類／改排序）、刪除（409 提示先清空）。
- 商品：列表（分頁、關鍵字搜尋、依分類篩選、依狀態篩選）、建立、編輯、刪除。
- 商品編輯器含**巢狀 SKU 的新增／編輯／移除**（SKU 代碼、選項 key/value、價格、幣別、庫存、啟用），以及商品↔分類多對多勾選、草稿／發佈切換。
- 庫存就地在 SKU 列編輯（不另做批次調整介面，見 design D7）。
- **金額一律不縮放**：`3400` 顯示為 `NT$3,400`，輸入框收的就是同一個整數；幣別由同層 `currency` 決定。
- **RBAC 感知**：分頁與每一個按鈕依呼叫者在該商家的實際權限決定可見／可用。

**API（兩處 additive 變更，design D5／D6 論證）**

- 新增 `GET /api/v1/admin/shops/{shopID}/me`：回傳呼叫者在該商家的有效權限節點集合。守衛只有「已認證 ＋ 跨店存取守衛」，不要求任何權限節點——「我在這家店能做什麼」必須是每個進得了這家店的人都答得出來的自我資訊。沒有它，UI 只能靠「按下去拿 403」推測權限。
- 商品列表新增**選填** `q` 查詢參數：對 `title`／`slug`／`sku_code` 做大小寫不敏感子字串比對。不帶時行為與變更前逐位元組相同。

兩者皆為 v1 additive（新端點、新選填參數），不觸及 D13 已關閉的破例窗口。`api/openapi/openapi.yaml` 與 `api/openapi/v1-surface.txt` 同步更新，鎖檔**只增不減**。

**驗證**

- `e2e/happy-path.mjs` 新增瀏覽器路徑：建立分類 → 建立含多個 SKU 的商品 → 改 SKU 庫存 → 發佈 → 前台公開 API 看得到，並**明文斷言金額顯示未被縮放**。

## Capabilities

### New Capabilities
- `admin-catalog-ui`: 後台商品目錄操作介面的行為契約——分類與商品的管理路徑、巢狀 SKU 編輯語意、金額顯示與輸入的不縮放規則、依權限決定可見性、以及「無建置步驟」這個交付形式本身。

### Modified Capabilities
- `product-catalog`: 商品列表新增選填關鍵字搜尋參數（既有的分頁與 `category_id`／`status` 篩選語意不變）。
- `rbac`: 新增「呼叫者可查詢自己在某商家的有效權限集合」這條需求。

## Impact

- 新檔案：`admin/lib/api.js`、`admin/lib/dom.js`、`admin/lib/money.js`、`admin/lib/rbac.js`、`admin/lib/schemaform.js`、`admin/views/pages.js`、`admin/views/content.js`、`admin/views/categories.js`、`admin/views/products.js`。
- 修改：`admin/app.js`（縮為 shell／view 註冊）、`admin/index.html`（新分頁與容器）、`admin/styles.css`、`admin/Dockerfile`（`COPY` 需納入新目錄）。
- 修改（API，additive）：`api/internal/httpapi/authz.go`（新增 access-only 守衛）、`api/internal/httpapi/roles.go`（新增 `/shops/{shopID}/me`）、`api/internal/httpapi/products.go` 與 `api/internal/catalog/service.go`（`q` 參數）、`api/openapi/openapi.yaml`、`api/openapi/v1-surface.txt`。
- 修改：`e2e/happy-path.mjs`（新增商品鏈的瀏覽器斷言）。
- 不動：資料庫 schema、migrations、events 接線、`web/`、任何既有端點的既有行為。
