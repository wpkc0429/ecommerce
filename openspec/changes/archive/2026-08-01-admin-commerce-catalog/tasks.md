## 1. API additive（design D5／D6）

- [x] 1.1 `api/internal/httpapi/authz.go`：新增 `AuthzMW.RequireShopAccess()`——已認證 + `Snapshot` + `CanAccessShop()`，不檢查任何權限節點
- [x] 1.2 `api/internal/httpapi/roles.go`：新增 `GET /shops/{shopID}/me` handler，讀 `permissions` 表的節點目錄逐一 `snap.Allows()`，回 `{shop_id, permissions[]}`（排序），掛在 `MountShop` 上以 `RequireShopAccess()` 守衛
- [x] 1.3 `api/internal/catalog/service.go`：`ProductListParams` 新增 `Q *string`，`ListProducts` 於 `Q` 非空時以 `TitleContainsFold` / `SlugContainsFold` / `HasSkusWith(SkuCodeContainsFold)` 的 OR 條件過濾；不帶時查詢一行不動
- [x] 1.4 `api/internal/httpapi/products.go`：`list` 讀取 `q` query 參數並帶入 `ProductListParams`
- [x] 1.5 整合測試：`/shops/{shopID}/me` 的權限集合正確（角色、個人覆蓋授予、個人覆蓋剝奪、無權限者回 200 空集合、非成員 403、未認證 401）
- [x] 1.6 整合測試：`q` 命中 title／slug／sku_code、與 `status` 併用、未帶時結果不變

## 2. 契約與表面鎖（design D11）

- [x] 2.1 `api/openapi/openapi.yaml`：新增 `/api/v1/admin/shops/{shopID}/me` 路徑與 `ShopPermissions` 具名 schema（`additionalProperties: false`）
- [x] 2.2 `api/openapi/openapi.yaml`：`listProductsAdmin` 新增選填 `q` 參數
- [x] 2.3 以 `-update-surface` 重生 `api/openapi/v1-surface.txt`，逐行確認 diff **只有新增、零刪除**
- [x] 2.4 `make openapi` 四道全綠

## 3. admin 拆檔（design D1）

- [x] 3.1 建立 `admin/lib/api.js`：`tokens`、`refreshTokens`、`api()`、`ApiError`（自 `app.js` 逐字搬移）
- [x] 3.2 建立 `admin/lib/dom.js`：`el()`、`$`、`clear()`（`el()` 逐字搬移）
- [x] 3.3 建立 `admin/lib/schemaform.js`：`resolveRef`／`orderedKeys`／`sectionBranches`／`buildForm`／`buildField`／`buildListField`／`buildSectionsField`／`readInput`／`collectNode`／`keyOf`／`collectForm`／`showValidationErrors`（逐字搬移，僅 CMS 使用）
- [x] 3.4 建立 `admin/views/pages.js`、`admin/views/content.js`：既有 CMS 視圖邏輯搬移，改為匯出 `init(ctx)`／`load()`
- [x] 3.5 `admin/app.js` 縮為 shell：登入／登出、商家選擇、視圖註冊表、通用 `showView()`（`.tab-panel` 迴圈取代硬編碼三行）
- [x] 3.6 `admin/Dockerfile` 改為 `COPY . /usr/share/nginx/html/`，新增檔案不再需要逐檔登記
- [x] 3.7 跑既有 E2E 確認 CMS 流程（sections 編輯器、422 定位、發佈、全域內容、換版）未因搬移而壞

## 4. admin 共用詞彙（design D2／D4／D5）

- [x] 4.1 `admin/lib/dom.js` 新增 `repeater(host, renderRow)`：`add`／`reset`／`read`，列自持 `{ node, read }`，不做任何 DOM 回查
- [x] 4.2 `admin/lib/money.js`：`formatMoney(amount, currency)`（`Intl.NumberFormat` 的 decimal 樣式 + 幣別前綴，**無任何縮放**）與 `parseAmount(input)`（整數）
- [x] 4.3 `admin/lib/rbac.js`：`loadPermissions(shopID)` 打 `/admin/shops/{id}/me`、`can(node)`、`gate(node, el)`；登入與切換商家時重載

## 5. 分類視圖（design D9）

- [x] 5.1 `admin/views/categories.js`：以 `?flat=1` 取平坦清單，表格顯示名稱／slug／父分類／排序
- [x] 5.2 建立／編輯 dialog：名稱、slug、父分類 `<select>`（排除自己）、`position`；422 錯誤顯示
- [x] 5.3 刪除：409 時顯示伺服器訊息（仍有子分類／仍有商品）
- [x] 5.4 依 `category.view/create/edit/delete` 決定分頁與各按鈕可見性

## 6. 商品視圖（design D2／D4／D6／D8）

- [x] 6.1 `admin/views/products.js` 列表：分頁（`page`／`page_size`／`total`）、搜尋框（`q`）、分類 `<select>`（`category_id`）、狀態 `<select>`（`status`）
- [x] 6.2 列表欄位：ID、標題、slug、分類、SKU 數、價格區間（`formatMoney`）、庫存合計、狀態徽章、動作
- [x] 6.3 商品編輯器基本區：標題、slug、描述、狀態（草稿／已發佈）
- [x] 6.4 分類關聯：以 checkbox 清單呈現，收集為 `category_ids`（全量取代語意）
- [x] 6.5 SKU 表格：以 `repeater` 呈現，每列 sku_code／options（巢狀 `repeater` 的 key-value）／price_amount／currency／stock_qty／is_active／移除
- [x] 6.6 儲存：一顆儲存鍵，一律送出完整 `skus`（既有列帶回 `id`）與完整 `category_ids`；422 details 定位到欄位
- [x] 6.7 建立商品：dialog 或同一編輯器的新建模式，成功後進入編輯
- [x] 6.8 發佈／取消發佈：以 `status` 欄位切換並儲存；刪除商品（確認後）
- [x] 6.9 依 `product.view/create/edit/delete` 決定分頁與各按鈕可見性；無 `product.edit` 時表單唯讀
- [x] 6.10 `admin/index.html` 新增「分類」「商品」分頁與容器；`admin/styles.css` 補上表格內表單、SKU 列、工具列的樣式

## 7. E2E（design D10）

- [x] 7.1 `e2e/happy-path.mjs` 瀏覽器區段新增商品鏈：登入 → 建立分類 → 建立含 2 個 SKU 的商品
- [x] 7.2 **金額不縮放斷言**：`price_amount = 3400` 的 SKU 在畫面上顯示 `NT$3,400`，且不出現 `NT$34` 或 `NT$340,000`
- [x] 7.3 編輯其中一個 SKU 的庫存並儲存 → 重新載入後該 SKU 為新庫存，**另一個 SKU 仍在且未變動**（守著 design D8）
- [x] 7.4 發佈商品 → 打 `/api/v1/shop/products` 確認公開端點看得到，`stock_qty` 為新值
- [x] 7.5 識別值帶 `runID` 確保可重複執行

## 8. 驗證

- [x] 8.1 `make test` 全綠
- [x] 8.2 `make test-int` 全綠（`-p 1`）
- [x] 8.3 `make lint` 無新增問題
- [x] 8.4 `make openapi` 四道全綠、`v1-surface.txt` diff 零刪除
- [x] 8.5 重置 DB → `make migrate && make seed-demo` → 三服務啟動 → `cd e2e && node happy-path.mjs` 全綠
- [x] 8.6 更新根 `CLAUDE.md` 的 Architecture notes，記入 admin 前端架構決策、金額不縮放規則與 D8 的全量 SKU 陷阱
