## 1. `internal/seed`：新增權限節點（design D2）

- [x] 1.1 `api/internal/seed/seed.go` 的 `PermissionCatalog` 新增 `shop.create`（「建立商家（平台層）」）、`shop.list`（「列出所有商家（平台層）」）兩筆。
- [x] 1.2 確認 `roleDefs` 中 `super_admin`（`Perms: []string{"*"}`）自動涵蓋新節點，不需額外改動；`merchant_owner`/`editor` 的 `Perms` 清單維持不變（新節點僅平台路由使用）。

## 2. `internal/cms`：`CreateShop`/`ListShops`/`GetShop`/`UpdateShop` 服務函式（design D3/D4/D5/D7）

- [x] 2.1 `api/internal/cms/service.go` 新增 `ShopInput{Name string; ThemeID *int; Status *int16}`、`validShopStatus(int16) bool`（0/1/2）。
- [x] 2.2 新增私有 helper `createHomePage(ctx context.Context, tx *ent.Tx, shopID int) error`：idempotent（已存在 `slug=home` 則略過），建立 `type_key=home, slug=home, status=0, content_json={"sections":[]}, meta={}`（design D4——不依賴主題/schema 校驗）。
- [x] 2.3 新增 `Service.CreateShop(ctx, ShopInput) (*ent.Shop, error)`：驗證 `name` 非空、`status`（若提供）合法、`theme_id`（若提供）存在；開 `ent.Tx` 建立 shop → 呼叫 `createHomePage` → commit；建立失敗（含 `ent.IsConstraintError`）正確 rollback 並回傳可分類錯誤（`*ValidationError` 供 handler 轉 422）。
- [x] 2.4 新增 `ShopListParams{Page, PageSize int}`、`ShopPage{Shops []*ent.Shop; Total, Page, PageSize int}`、`Service.ListShops(ctx, ShopListParams) (*ShopPage, error)`：`page<1→1`、`page_size<=0→20`、`page_size>100→100`；`Count()` + `Order(shop.ByID()).Limit().Offset()`。
- [x] 2.5 新增 `Service.GetShop(ctx, shopID int) (*ent.Shop, error)`：不存在回 `ErrNotFound`。
- [x] 2.6 新增 `ShopUpdate{Name *string; Status *int16; Meta json.RawMessage}`、`Service.UpdateShop(ctx, shopID int, in ShopUpdate) (*ent.Shop, error)`：partial update；`status` 非法回 `*ValidationError`；`name` 若提供不得為空字串。
- [x] 2.7 `UpdateShop` 內：若 `status` 實際變更（新值 ≠ 更新前值），儲存成功後查 `shop.QuerySites().Select(site.FieldDomain).Strings(ctx)` 取得綁定網域，透過既有 `s.Dispatcher.Publish(ctx, events.SiteMappingChanged{Hosts: domains, ShopIDs: []int{shopID}})` 觸發路由快取失效（design D5，重用既有事件類型與訂閱者，不新增事件）。

## 3. `internal/httpapi`：`ShopsHandler`（design D1/D3）

- [x] 3.1 新增 `api/internal/httpapi/shops.go`：`ShopsHandler{Client *ent.Client; Service *cms.Service; Authz *AuthzMW; Log *slog.Logger}`，`MountPlatform(r chi.Router)` 比照 `SitesHandler.MountPlatform` 掛載：
  - `r.With(h.Authz.RequirePlatformPermission("shop.create")).Post("/shops", h.create)`
  - `r.With(h.Authz.RequirePlatformPermission("shop.list")).Get("/shops", h.list)`
  - `r.With(h.Authz.RequirePlatformPermission("shop.view")).Get("/shops/{shopID}", h.get)`
  - `r.With(h.Authz.RequirePlatformPermission("shop.update")).Put("/shops/{shopID}", h.update)`
- [x] 3.2 `create` handler：解析 `cms.ShopInput`（JSON body：`name`、`theme_id`、`status`）→ `h.Service.CreateShop` → 錯誤依 `*cms.ValidationError` 422／其餘 500（比照 `writeCMSError` 既有模式，若 `writeCMSError` 已涵蓋則直接複用，不重新發明）→ 成功回 201，body 含 `id`、`name`、`theme_id`、`status`。
- [x] 3.3 `list` handler：解析 `page`/`page_size` query（`strconv.Atoi`，解析失敗當作未提供，不回 400——design D3 寬容輸入原則）→ `h.Service.ListShops` → 200，body `{"shops": [...], "page", "page_size", "total"}`。
- [x] 3.4 `get` handler：`intParam(r, "shopID")` → `h.Service.GetShop` → 404（`cms.ErrNotFound`）或 200。
- [x] 3.5 `update` handler：`intParam` + 解析 `cms.ShopUpdate` → `h.Service.UpdateShop` → 404/422/200（含更新後的 `status`）。
- [x] 3.6 共用 `shopJSON(*ent.Shop) map[string]any` helper（`id`、`name`、`theme_id`、`status`、`meta`、`content_json`、`updated_at`），供 create/get/update/list 共用，避免重複欄位組裝程式碼。

## 4. `router.go` + `app/wire.go`：掛載（design D1）

- [x] 4.1 `api/internal/httpapi/router.go` 的 `Deps` 新增 `Shops *ShopsHandler` 欄位；在 `AdminMW` group 內、與 `d.Roles.MountPlatform(pr)`/`d.Sites.MountPlatform(pr)` 同級新增 `if d.Shops != nil { d.Shops.MountPlatform(pr) }`。
- [x] 4.2 `api/internal/app/wire.go` 組裝 `deps.Shops = &httpapi.ShopsHandler{Client: client, Service: cmsService, Authz: authz, Log: a.log}`（重用既有 `cmsService`/`authz` 變數，不新建）。

## 5. 整合測試

- [x] 5.1 新增 `api/internal/httpapi/shops_integration_test.go`：比照 `roles_integration_test.go` 的 `newRBACEnv`/`call` 模式，建立含平台角色（`shop.create`/`shop.list`/`shop.view`/`shop.update`）與商家角色（僅 shop-scoped）的測試環境，路由器注入 `Shops` handler。
- [x] 5.2 案例：平台角色 `POST /admin/shops` 成功建店（201），並直接查 `Page` 表驗證存在 `shop_id=新商家, slug="home", status=0` 的頁面。
- [x] 5.3 案例：僅持有商家 A `merchant_owner`（shop-scoped，非平台角色）者呼叫 `POST /admin/shops`／`GET /admin/shops` 均回 403。
- [x] 5.4 案例：`PUT /admin/shops/{id}` 合法 status 轉換（1→0）回 200 且 DB 值更新；非法值（如 9）回 422 且 DB 值不變。
- [x] 5.5 案例：建立 30 筆商家後 `GET /admin/shops?page=1&page_size=10` 與 `page=2&page_size=10` 回傳不重複的 10 筆、`total=30`（含既有 seed 帶入的商家需一併納入 total 計算或於測試中以乾淨 DB 假設處理，比照 `testutil.OpenDB` 的 truncate-per-test 慣例）。
- [x] 5.6 案例（design D5）：對已綁定網域的商家做 status 轉換後，直接檢查 Redis `route:{host}` key 已被刪除（比照 `tenancy_integration_test.go`/`ratelimit_integration_test.go` 的 `testutil.OpenRedis(t)` 用法）。
- [x] 5.7 單元測試（若不需 INTEGRATION）：`cms` package 對 `validShopStatus`、分頁參數正規化（`page<1`、`page_size` 上下限）等純函式邏輯視情況補充，非必須（純邏輯簡單，整合測試已覆蓋端到端行為即足夠，故省略——`ListShops`/`CreateShop`/`UpdateShop` 的邊界行為已由 `TestCreateShopValidation`/`TestUpdateShopStatusTransition`/`TestListShopsPagination` 端到端覆蓋）。

## 6. 驗證與收尾

- [x] 6.1 `cd api && go build ./...` 通過
- [x] 6.2 `cd api && golangci-lint run` 0 issues
- [x] 6.3 `cd api && go test ./...`（無 INTEGRATION）與 `cd api && INTEGRATION=1 go test -p 1 -count=1 ./...` 皆綠燈
- [x] 6.4 覆核既有測試（`roles_integration_test.go`、`tenancy_integration_test.go`、`cms_integration_test.go` 等）未受影響——未注入 `Shops` 欄位的既有 `httpapi.Deps` 用例行為不變（全套 `INTEGRATION=1` 測試綠燈已涵蓋此項）
- [x] 6.5 手動確認 `make seed` 可重跑不出錯（idempotent 權限新增）——`make seed` 與 `make seed-demo` 各自連續執行兩次皆成功
