## 1. ent schema

- [x] 1.1 新增 `api/internal/ent/schema/catalog.go`：`Category`（`shop_id`, `parent_id` optional/nillable 自參考, `name` varchar(100), `slug` varchar(150), `position` int default 0, TimeMixin）；索引 `UNIQUE(shop_id, name)`、`UNIQUE(shop_id, slug)`、`INDEX(parent_id)`。
- [x] 1.2 `Product`（`shop_id`, `title` varchar(200), `slug` varchar(200), `description` text default "", `status` int16 default 0 + CHECK IN (0,1), `meta` jsonb default '{}', TimeMixin）；索引 `UNIQUE(shop_id, slug)`、`INDEX(shop_id, status)`。
- [x] 1.3 `ProductSKU`（`shop_id`, `product_id`, `sku_code` varchar(64), `options` jsonb default '{}', `price_amount` int64/BIGINT + CHECK >= 0, `currency` varchar(3) default 'TWD', `stock_qty` int32 default 0 + CHECK >= 0, `is_active` bool default true, TimeMixin），table 名 `product_skus`；索引 `UNIQUE(shop_id, sku_code)`、`INDEX(product_id)`。
- [x] 1.4 `ProductCategory`（join table，`shop_id`, `product_id`, `category_id`，複合 PK），`entsql.Annotation{Table: "product_category"}`（比照 `role_permission`/`site_shop` 命名慣例）；索引 `INDEX(category_id)`。
- [x] 1.5 Edges：`Category` 自參考 parent/children、`Category.products` through `product_category`；`Product.skus`（一對多）、`Product.categories` through `product_category`；`ProductSKU.product`（多對一）。刪除行為：SKU/product_category 隨 Product 刪除 `entsql.OnDelete(entsql.Cascade)`；Category 的 parent/child 與 product_category 不設 CASCADE（D4 的 RESTRICT 語意由 service 層檢查、不倚賴 DB CASCADE）。**實作筆記**：`entsql.OnDelete` annotation 必須放在 assoc（`.To()`）邊上才會生效——放在 inverse（`.From().Ref()`）邊上會被 ent 的 SQL diff 靜默忽略（`entc/gen/graph.go` 建 FK 時直接跳過 inverse edge），已修正為放在 `Product.Edges` 的 `skus` 邊。
- [x] 1.6 `go generate ./internal/ent` 重新產生 ent 生成碼，確認編譯通過。

## 2. Migration

- [x] 2.1 `make migrate-gen name=add_product_catalog` 產生 versioned migration；檢查產出的 SQL 包含四張新表、正確的 CHECK/UNIQUE/index，且未動到既有表欄位。**發現並修正一個既有 repo 缺陷**：`api/migrations/atlas.sum` 自 Phase 1 的手寫 migration（`20260718063900_rbac_unique_nulls_not_distinct`）加入後就沒有同步更新，導致 `migrate-gen` 的 directory-integrity 檢查一律失敗；用 `sqltool.GolangMigrateDir.Checksum()` + `migrate.WriteSumFile` 重新計算校驗和修復（僅重新計算 checksum，未變動任何 migration 內容）。另外，Atlas diff 引擎照例不知道手寫的 `UNIQUE NULLS NOT DISTINCT` 約束（`role_user`/`user_permission`），產出的 up/down.sql 一度包含 DROP/re-ADD 這兩個約束的陳述，已依 migrategen.go 文件的既有提示手動移除。
- [x] 2.2 本機 `make migrate` 套用 migration 至 dev/主資料庫，確認無誤（四張表、CHECK、UNIQUE index 皆正確建立，`role_user`/`user_permission` 的 NULLS NOT DISTINCT 約束未受影響）；`make migrate-down` 驗證可回滾（表格正確移除），再次 `make migrate` 驗證可重新套用。

## 3. 租戶隔離

- [x] 3.1 `api/internal/tenant/enttenancy.go` 的 `tenantOwned` map 新增 `"Category"`, `"Product"`, `"ProductSKU"`, `"ProductCategory"` 四筆。
- [x] 3.2 擴充 `enttenancy_test.go`：新增 `TestTenantScopeOnCatalogEntities`，驗證四個型別的 interceptor（跨 shop 查詢被過濾）與 hook（建立時 shop_id 被強制覆蓋）行為；`INTEGRATION=1` 測試通過。

## 4. RBAC 權限節點

- [x] 4.1 `api/internal/seed/seed.go` 的 `PermissionCatalog` 新增 `category.view/create/edit/delete`、`product.view/create/edit/delete`（8 筆，附中文說明）。
- [x] 4.2 `roleDefs`：`merchant_owner` 取得全部 8 個新節點；`editor` 取得除 `category.delete`/`product.delete` 外的 6 個節點。
- [x] 4.3 確認 `super_admin` 的 `"*"` 展開機制自動涵蓋新節點（程式碼機制不變，經整合測試間接驗證）。

## 5. Service 層（`api/internal/catalog`）

- [x] 5.1 建立 `api/internal/catalog/service.go`：`Service{Client *ent.Client}`、`ErrNotFound`、`ValidationError{Message, Details []Detail}`、`ConflictError{Message}`（結構比照 `cms` package，獨立定義不 import cms）。**與原計畫的偏差**：不含 `events.Dispatcher` 欄位——本 change 沒有任何快取需要失效（design D8：公開端點直接查 DB、不快取），保留未使用的 Dispatcher 欄位會是死程式碼；design.md 已在 D8 說明此點，日後若替 catalog 加上快取層再補上。
- [x] 5.2 `normalizeSlug`（`^[a-z0-9-]+$`，獨立實作於 catalog package）。
- [x] 5.3 分類 CRUD：`CreateCategory`/`UpdateCategory`/`DeleteCategory`/`GetCategory`/`ListCategories` + `BuildCategoryTree`（service 層純函式，Go 記憶體組樹）。
- [x] 5.4 分類防環邏輯：`wouldCreateCycle` 沿祖先鏈檢查，`UpdateCategory` 命中則回 `ValidationError`（422）。
- [x] 5.5 分類刪除守衛（D4 RESTRICT）：`DeleteCategory` 檢查子分類/`product_category` 掛載，命中回 `ConflictError`（handler 轉 409）。
- [x] 5.6 商品 CRUD：`CreateProduct`/`UpdateProduct`（皆在單一 Tx 內處理 product 欄位 + category_ids 全量取代 + SKU upsert）、`DeleteProduct`（cascade 交給 DB FK）、`GetProduct`、`ListProducts`（分頁 + `category_id`/`status` 篩選）。
- [x] 5.7 SKU 驗證：`price_amount >= 0`、`stock_qty >= 0`、`sku_code` 非空；unique constraint 錯誤轉譯 422（`ent.IsConstraintError`）。
- [x] 5.8 公開查詢函式：`ListPublishedProducts`、`GetPublishedProductBySlug`（皆僅 `status=1`，未命中回 `ErrNotFound`）。

## 6. HTTP handler（merchant CRUD）

- [x] 6.1 `api/internal/httpapi/categories.go`：`CategoriesHandler`，`MountShop` 掛 `/categories`（含 `?flat=1` 平鋪列表選項）。
- [x] 6.2 `api/internal/httpapi/products.go`：`ProductsHandler`，`MountShop` 掛 `/products`（分頁/`category_id`/`status` 篩選 query）。
- [x] 6.3 錯誤轉譯：新增 `writeCatalogError`（`ValidationError`→422、`ConflictError`→409、`ErrNotFound`→404）。
- [x] 6.4 `router.go`：`Deps` 新增 `Categories`/`Products`，掛於 `/admin/shops/{shopID}` group。
- [x] 6.5 `app/wire.go`：組裝 `catalog.Service`、`CategoriesHandler`、`ProductsHandler`。

## 7. 公開唯讀端點

- [x] 7.1 `ProductsHandler.MountPublic`：`GET /products`（僅已發佈）、`GET /products/{slug}`（含 SKU、僅已發佈，未命中 404）。
- [x] 7.2 `router.go`：掛於既有 `v1.Route("/shop", ...)` 群組。**與原計畫的偏差**：該群組原本的掛載條件是 `d.MemberAuth != nil && d.TenantMW != nil`，改為只需 `d.TenantMW != nil`（MemberAuth 相關路由改成群組內部再判斷 nil）——否則公開商品端點會被迫依賴一個它不需要的模組（MemberAuth）才能掛載，且會限制未來獨立測試/獨立部署 catalog 公開端點的彈性；正式環境的 wire.go 一律同時設定兩者，行為不受影響（純粹放寬掛載條件，非窄化）。
- [x] 7.3 handler 透過 `tenant.ShopID(ctx)` 讀取 `TenantMW` 已解析好的 shopID（與 render.go 的 tenant ctx 用法一致，未額外接線 app/wire.go）。

## 8. Seed（可選，供本機/E2E 使用）

- [x] 8.1 `seedDemoShop` 新增 `seedDemoCatalog`：2 個分類（含 1 層子分類）、2 個已發佈商品（各 1-2 個 SKU，`currency='TWD'`）、1 個草稿商品（供驗證公開端點的 published-only 語意），idempotent（以 slug 存在性判斷是否已種過）。已於本機 `go run ./cmd/seed -demo` 執行兩次驗證冪等，並以 curl 驗證公開端點回傳正確資料、草稿商品 404。

## 9. 整合測試

- [x] 9.1 `categories_integration_test.go`：`TestCategoryTreeQuery`、`TestCategoryUniqueness`、`TestCategoryCyclePrevention`、`TestCategoryDeletionGuard`、`TestCategoryRBACDeleteRestriction`、`TestCategoryCrossShopIsolation`。
- [x] 9.2 `products_integration_test.go`：`TestProductListPaginationAndFilters`（分頁/分類/狀態篩選）、`TestProductRBACDeleteRestriction`、`TestProductCrossShopIsolation`。
- [x] 9.3 `TestProductCreateWithNestedSKUs`、`TestProductUpdateSKUUpsert`（新增/更新/移除）、`TestSKUMoneyAndStockPrecision`（int64 邊界值 9,223,372,036 往返精確、負數價格/庫存 422）。
- [x] 9.4 RBAC/隔離測試涵蓋於 9.1/9.2 的 RBAC 與 CrossShopIsolation 測試案例。
- [x] 9.5 `TestPublicCatalogPublishedOnly`（草稿 404、已發佈含 SKU）、`TestPublicCatalogCrossShopIsolation`。
- [x] 9.6 `go build ./...`、`go vet ./...`、`golangci-lint run` 全數 0 issues；`go test ./...`（單元）與 `INTEGRATION=1 go test -p 1 -count=1 ./...`（整合，含既有 Phase 1 測試）全數通過，無既有測試回歸。

## 10. Storefront 最小 SSR（選做，不得拖累前述任務品質）

- [ ] 10.1 **決定跳過**。前 9 個任務群組（核心資料模型、API、RBAC、租戶隔離、整合測試）已完整交付並通過驗證；`web/` 現有的渲染管線（`app/[[...path]]/page.tsx`）與 CMS 的 `theme`/`component_key`/`page_schema` 深度耦合，商品目前是純資料 API、尚無對應的主題 schema 概念，要做到「不是臨時拼湊」的最小商品頁需要另外設計 storefront 端的資料串接方式，非三言兩語能做完又不犧牲品質。留給下一個 proposal（或本 change 的後續加強）處理；不影響本 change 的完成判定。
