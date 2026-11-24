## 1. 資料模型

- [ ] 1.1 `api/internal/ent/schema/catalog.go`：`Product` 新增 `field.String("subtitle").Default("")`，`varchar(200)`；長度以 rune 驗證於 service 層（ent `MaxLen` 算 byte，沿用 order-extras 的慣例）
- [ ] 1.2 `ProductCategory` 新增 `field.Int32("position").Default(0)` ＋ CHECK `product_category_position_check (position >= 0)`；索引 `(category_id, position, product_id)`
- [ ] 1.3 `cd api && go generate ./internal/ent`
- [ ] 1.4 `make migrate-gen name=add_product_subtitle_and_category_position`，檢查產生的 SQL（`NOT NULL DEFAULT`、CHECK 有產生否則手寫）；`make migrate` 並驗證 up→down→up

## 2. Service

- [ ] 2.1 `api/internal/catalog/service.go`：`ProductInput.Subtitle string`、`ProductUpdate.Subtitle *string`；rune 上限 200，超過回 `ValidationError`（pointer `/subtitle`）
- [ ] 2.2 `PublicListParams` 新增 `CategorySlug string`；`ListPublishedProducts` 在非空時：載入本店全部分類 → 求所請求分類＋子孫 id 集合（純函式 `descendantIDs`，單元測試含多層與循環防護）→ `HasCategoriesWith(category.IDIn(...))`；排序以 `LEFT JOIN product_category pc ON pc.product_id = products.id AND pc.category_id = :requested AND pc.shop_id = :shop` 的 `pc.position NULLS LAST, products.id`（design D7）；`total` 依篩選後計算
- [ ] 2.2a `UpdateProduct`：`category_ids` 改為差集更新（只刪被移除者、只新增新加入者），新關聯 `position = max+1`；`CreateProduct` 同樣以 max+1 建立（design D6）
- [ ] 2.2b 新增 `GetCategoryProductOrder`／`SetCategoryProductOrder`（完整排列檢查、單一交易）
- [ ] 2.3 `WithCategories` 的載入改為明訂順序（`position`、`id`）
- [ ] 2.4 新增 `ListCategoriesPublic(ctx, shopID)`（或直接重用 `ListCategories` ＋ `BuildCategoryTree`），確認同層排序為 `position`、`id`

## 3. HTTP

- [ ] 3.1 `api/internal/httpapi/products.go`：`productJSON` 輸出 `subtitle`；公開回應新增 `categories: [{id, slug, name}]`（後台回應 MAY 一併輸出，兩邊同一函式）
- [ ] 3.2 `publicList` 讀 `category` 查詢參數
- [ ] 3.3 `api/internal/httpapi/categories.go`：新增 `MountPublic(r)` → `GET /categories`；`router.go` 在 `/shop` 群組掛上（`if d.Categories != nil`）
- [ ] 3.4 `categories.go` `MountShop`：`GET`（`category.view`）／`PUT`（`category.edit`）`/categories/{categoryID}/product-order`

## 4. Seed

- [ ] 4.1 `api/internal/seed/seed.go`：demo 新增 `new-arrivals`／「本季新品」分類（自帶自然鍵防重，比照 commerce-demo-seed design D4），掛上兩個花藝商品；花藝商品補 `subtitle`
- [ ] 4.2 `api/internal/seed/seed_integration_test.go`：斷言分類存在、關聯數、冪等

## 5. Admin

- [ ] 5.1 `admin/views/products.js`：編輯器新增副標欄位（隨完整酬載送出，不得破壞 design D8 的全量 `skus`）；列表標題下顯示副標。量測 #5（`products.js` 目前 338 行，若逼近 400 先拆檔）
- [ ] 5.2 `admin/views/categories.js`：每個分類的「商品排序」面板（上下移動、送完整排列、子分類說明文字）；若使 `categories.js` 逼近 400 行，拆成 `admin/views/category-product-order.js`

## 6. API 契約

- [ ] 6.1 `api/openapi/openapi.yaml`：`Product.subtitle`、`Product.categories`（新 `CategorySummary` schema）、`ProductInput`/`ProductUpdate.subtitle`、`GET /api/v1/shop/categories`、`/api/v1/shop/products` 的 `category` 查詢參數（`optional`，敘述含子分類與排序規則）、`GET`/`PUT .../categories/{categoryID}/product-order`（`CategoryProductOrder` schema）
- [ ] 6.2 重新產生 `v1-surface.txt`，確認 diff 只有新增
- [ ] 6.3 `openapi_conformance_integration_test.go`：新端點與 `CategorySummary` 納入取樣

## 7. 測試

- [ ] 7.1 `api/internal/httpapi/products_integration_test.go`：副標建立／更新未帶不覆蓋／rune 上限 422；公開回應含 `categories`；`?category=` 篩選、含子分類且不重複、直接成員依 position 在前、未知 slug 空列表、他店 slug 空列表
- [ ] 7.1a 排序：`product-order` GET／PUT、不完整排列 422、權限 403、更新商品（相同 `category_ids`）後 position 不變、新增關聯排最後；既有「`category_ids` 全量取代」測試仍綠
- [ ] 7.2 `api/internal/httpapi/categories_integration_test.go`：公開分類樹、排序、不跨店、未知 Host 404（沿用既有租戶解析行為）

## 8. E2E

- [ ] 8.1 `e2e/happy-path.mjs` 商品目錄段：編輯副標並儲存 → 公開詳情含副標
- [ ] 8.2 `e2e/commerce-path.mjs`：`GET /shop/categories` 含 `new-arrivals`；`GET /shop/products?category=new-arrivals` 只回該分類商品
- [ ] 8.3 `e2e/happy-path.mjs`：在分類視圖調整「本季新品」排序 → 公開列表順序一致（結束時還原原順序，維持可重複執行）

## 9. 文件與驗收

- [ ] 9.1 `docs/storefront-integration.md`：新增公開分類端點與 `category` 篩選說明
- [ ] 9.2 `CLAUDE.md`：記下「本季新品是分類不是旗標」、`subtitle` 語言中立、排序存在關聯上且 PUT 改為差集更新（否則順序會靜默歸零）、子分類篩選的排序規則
- [ ] 9.3 `make test`、`make test-int`、`make lint`、`make openapi`；`node happy-path.mjs`、`node commerce-path.mjs` 全綠
