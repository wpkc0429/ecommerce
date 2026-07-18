## 1. ent schema

- [x] 1.1 新增 `api/internal/ent/schema/cart.go`：`Cart`（`shop_id`, `member_id`, `status` int16 default 0 + CHECK IN (0,1,2), `currency` varchar(3), TimeMixin）；索引 `UNIQUE(shop_id, member_id) WHERE status = 0`（`entsql.IndexWhere`，比照 `SiteShop` 慣例，design D3）、`INDEX(member_id)`。
- [x] 1.2 `CartItem`（`shop_id`, `cart_id`, `sku_id` optional/nillable, `quantity` int32 + CHECK > 0, `price_amount` int64/BIGINT + CHECK >= 0, `currency` varchar(3), TimeMixin），table 名 `cart_items`；索引 `INDEX(cart_id)`、`INDEX(sku_id)`。
- [x] 1.3 Edges：`Cart.shop`/`Cart.member`（FK `Member`，design D1：直接欄位，非透過 `ShopMember`）、`Cart.items`（一對多，`entsql.OnDelete(entsql.Cascade)` — 購物車刪除時品項隨之刪除；本 change 沒有刪除購物車的端點，但欄位設計要為未來留一致性）；`CartItem.shop`、`CartItem.cart`（inverse of `Cart.items`，**不**在此邊放 OnDelete annotation——沿用 product-catalog 的既有教訓，OnDelete 必須放在 assoc `.To()` 邊）、`CartItem.sku`（assoc 邊 `.To(ProductSKU.Type).Field("sku_id")`，`entsql.OnDelete(entsql.SetNull)`，design D7）。
- [x] 1.4 `go generate ./internal/ent` 重新產生 ent 生成碼，確認編譯通過（`cd api && go build ./...`）。

## 2. Migration

- [x] 2.1 `make migrate-gen name=add_shopping_cart` 產生 versioned migration；檢查產出 SQL 含兩張新表、正確的 CHECK/partial UNIQUE INDEX（`WHERE status = 0`）、`sku_id` 的 `ON DELETE SET NULL` FK，且未動到既有表欄位。若 Atlas diff 對 partial index 或 SET NULL FK 產出不理想，比照 product-catalog design D3/9 的既有處理方式人工修正。
- [x] 2.2 本機 `make migrate` 套用 migration，確認兩張表、CHECK、部分唯一索引皆正確建立；`make migrate-down` 驗證可回滾，再次 `make migrate` 驗證可重新套用。

## 3. 租戶隔離

- [x] 3.1 `api/internal/tenant/enttenancy.go` 的 `tenantOwned` map 新增 `"Cart"`, `"CartItem"` 兩筆。

## 4. Service 層（`api/internal/cart`）

- [x] 4.1 建立 `api/internal/cart/service.go`：`Service{Client *ent.Client}`、`ErrNotFound`、`ValidationError{Message, Details []Detail}`（結構比照 `catalog` package；不定義 `ConflictError`，design D9 已說明理由）。
- [x] 4.2 `CartView`/`CartItemView` 唯讀投影型別（design D9 已定型別欄位）。
- [x] 4.3 `GetCartView`：查詢 active（`status=0`）購物車；查無資料回傳 `ID: nil` 的空視圖（design D3），不建立資料列。品項的 `purchasable`/`unavailable_reason` 於此函式內即時計算（design D6/D7：`sku_id == nil` → `sku_deleted`；`sku.IsActive == false` → `sku_inactive`；`sku.Edges.Product.Status != 1` → `product_unpublished`；`sku.StockQty < item.Quantity` → `insufficient_stock`；否則可購買）。
- [x] 4.4 `AddItem`：驗證 SKU 屬於 shop、`is_active`、商品已發佈（design spec Add item validation）；find-or-create active 購物車（新建時以此 SKU 的 `currency` 設定購物車幣別，design D3/D4）；若購物車已有幣別，驗證與此 SKU 的 `currency` 相符（不符回 `ValidationError`，422，design D4）；若購物車內已有相同 `sku_id` 的品項，累加 `quantity`（design D5），否則新建品項並複製 `price_amount`/`currency` 快照（design spec Price snapshot on add）；累加/新增後的總量需 `<= stock_qty`，否則回 `ValidationError`（不修改既有數量）。
- [x] 4.5 `UpdateItemQuantity`：絕對設定值語意（design D5）；`quantity <= 0` 回 `ValidationError`；驗證所屬 cart 的 `member_id` 與呼叫者相符（否則 `ErrNotFound`，不洩漏品項存在性）；重新驗證新數量 `<= stock_qty`。
- [x] 4.6 `RemoveItem`：驗證品項屬於呼叫者的購物車（`ErrNotFound` 否則），刪除該品項。
- [x] 4.7 `ClearCart`：刪除呼叫者 active 購物車的所有品項；無 active 購物車時為 no-op（不回錯誤，design spec 幂等性要求）。

## 5. HTTP handler

- [x] 5.1 `api/internal/httpapi/cart.go`：`CartHandler{Client, Service, Log}`，方法從 `auth.MemberFrom(ctx)` 與 `tenant.ShopID(ctx)` 取得呼叫者身分與 shop（比照 `memberauth.go` 的 `shopFrom` 慣例），不依賴任何 URL 路徑參數識別購物車/會員（design Risks：規避 IDOR 的路由設計）。
- [x] 5.2 `writeCartError`：`ValidationError` → 422、`ErrNotFound` → 404（比照 `writeCatalogError`）。
- [x] 5.3 `MountShop(r chi.Router)`（掛於既有 `/shop` tenant-scoped 群組內，`d.MemberMW` 保護）：`GET /cart`、`POST /cart/items`、`PUT /cart/items/{itemID}`、`DELETE /cart/items/{itemID}`、`DELETE /cart/items`。
- [x] 5.4 JSON 序列化：`cartViewJSON`/`cartItemJSON`（`price_amount`/`quantity` 等金額/數量欄位直接輸出 `int64`/`int32`，`purchasable`/`unavailable_reason` 逐項輸出）。
- [x] 5.5 `router.go`：`Deps` 新增 `Cart *CartHandler`；在既有 `v1.Route("/shop", ...)` 群組內、`d.MemberMW` 保護的 `mr.Group` 中掛載（比照現有 `mr.Get("/me", d.MemberAuth.Me)` 的掛法，新增一個 `if d.Cart != nil { d.Cart.MountShop(mr) }`）。
- [x] 5.6 `app/wire.go`：組裝 `cart.Service`、`httpapi.CartHandler`。

## 6. 整合測試

- [x] 6.1 新建 `api/internal/httpapi/cart_integration_test.go`：共用測試環境 `newCartEnv`（比照 `categories_integration_test.go` 的 `newCatalogEnv` 寫法，但改為簽發**會員** JWT 而非 admin JWT；建立 shop A/B、商品/SKU 種子資料、至少兩個會員）。
- [x] 6.2 `TestCartEmptyByDefault`：尚未加入品項時 `GET /cart` 回空視圖、不建立資料列（可直接查 `client.Cart.Query().CountX(ctx) == 0` 驗證）。
- [x] 6.3 `TestCartAddItemValidation`：加入跨店/不存在 SKU、`is_active=false` SKU、未發佈商品的 SKU、超過庫存的數量，皆回 422 非 500；合法加入回 200/201 且購物車列被建立（`status=0`）。
- [x] 6.4 `TestCartAddItemAccumulates`：重複加入同一 SKU 累加數量（單一品項列）；累加後超過庫存被拒且既有數量不變。
- [x] 6.5 `TestCartUpdateItemQuantity`：`PUT` 絕對設定值語意、超過庫存被拒、`<=0` 被拒。
- [x] 6.6 `TestCartRemoveAndClear`：移除單一品項不影響其他品項；清空購物車移除所有品項；對沒有品項的購物車清空是幂等成功（非錯誤）。
- [x] 6.7 `TestCartMixedCurrencyRejected`：購物車幣別由第一品項決定，加入不同 `currency` 的 SKU 回 422，且原購物車內容不受影響。
- [x] 6.8 `TestCartPriceSnapshotStable`：加入品項後修改該 SKU 的 `price_amount`（透過 catalog service 或直接 ent update），重新 `GET /cart` 驗證該品項金額與購物車總計仍為加入當下的快照值，不隨現價變動。
- [x] 6.9 `TestCartDeactivatedSKUFlagged`：SKU 下架（`is_active=false`）或商品轉為草稿後，品項仍保留在購物車回應中且 `purchasable=false`／對應 `unavailable_reason`；原數量與價格快照不變。
- [x] 6.10 `TestCartSKUDeletionSetsNull`：刪除品項對應的商品（連帶刪除 SKU）後，`GET /cart` 該品項仍存在、`sku_id` 為 null、`purchasable=false`、`unavailable_reason="sku_deleted"`，價格快照與數量不變。
- [x] 6.11 `TestCartCrossMemberAndCrossShopIsolation`：會員 A 無法透過 `itemID` 操作會員 B 的品項（404，不洩漏存在性）；shop A 會員的購物車與 shop B 完全隔離；未帶 token 或帶錯 shop 的 member token 存取購物車 API 一律 401（涵蓋 spec Member-owned cart access control 的所有 Scenario）。
- [x] 6.12 `go build ./...`、`go vet ./...`、`golangci-lint run` 全數 0 issues；`go test ./...`（單元）與 `INTEGRATION=1 go test -p 1 -count=1 ./...`（整合，含既有 Phase 1/product-catalog 測試）全數通過，無既有測試回歸。

## 7. Seed（可選，供本機/E2E 使用）

- [x] 7.1 **決定跳過**。核心任務群組（1–6：資料模型、migration、租戶隔離、service 層、HTTP handler、整合測試）已完整交付並通過驗證（`go build`/`go vet`/`golangci-lint` 0 issues；全部單元+整合測試通過，無既有測試回歸）。示範購物車資料需要先有示範會員（`members`/`shop_member`）——目前 `seed.go` 完全沒有種過任何 demo member（會員身分只透過 `/api/v1/shop/auth/register` API 建立，非種子資料），要幫這個 change 補一個「先種會員、再種購物車品項」的路徑，會引入與本 change 核心範圍無關的新增種子邏輯，不是三言兩語能做完又不犧牲品質；且購物車是易變的執行期資料（會被 demo/E2E 操作改動），種一筆固定的購物車列意義不大（不像 catalog 的商品目錄是穩定的展示用資料）。留給有實際 demo/E2E 腳本需求時再補；不影響本 change 的完成判定。
