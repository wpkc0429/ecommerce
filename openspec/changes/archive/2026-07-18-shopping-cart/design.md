## Context

`product-catalog`（已歸檔）建立了 `products`/`product_skus` 與金額表示法慣例（`BIGINT`/`int64` 整數最小貨幣單位 + `currency` varchar(3)，design D1）。本 change 是 Phase 2 電商核心的第五個 proposal，讀者對象除了本 change 實作者，也包含下一個 proposal `order-management`（會直接讀 `carts`/`cart_items` 做「結帳建立訂單」）的 agent——因此本文件的 Decisions 特別詳盡交代資料存取方式與狀態欄位語意。

購物車是已登入會員（member JWT，`aud=shop:{shop_id}`）的自助資源。既有 `Member`（平台級身分，`api/internal/ent/schema/account.go`）與 `ShopMember`（該會員在該店的會籍，`shop_id`+`member_id` 複合唯一）已分離；`auth.MemberFrom(ctx)` 回傳的是 `Member.ID`（平台級），不是 `ShopMember.ID`。

## Goals / Non-Goals

**Goals:**

- 購物車與購物車品項的資料模型，shop-scoped，納入既有租戶隔離機制。
- 會員自助 API：取得目前購物車（含小計/總計）、加入品項、更新數量、移除品項、清空購物車。
- 加入當下的價格快照（不即時查 SKU 現價），並定案讀取時的金額計算方式。
- 幣別一致性規則定案。
- SKU/商品下架、SKU/商品被刪除後購物車品項的呈現與刪除語意定案。
- 為 `order-management` 提供明確的資料存取路徑（package、函式、狀態列舉值）。

**Non-Goals（明確留給後續或更後期）：**

- **訪客購物車**（未登入使用者的購物車）。涉及匿名身分識別（cookie/device id）與登入後「合併購物車」的邏輯，複雜度足以獨立成一個 change；本階段所有購物車操作都要求會員 JWT，未登入使用者沒有購物車可言。
- 庫存預留鎖定/超賣防護（`stock_qty` 檢查僅為「加入當下夠不夠」的提示性驗證，不鎖定、不扣庫存）——結帳當下的最終庫存驗證與扣庫存留給 `order-management`。
- 多幣別換算（購物車幣別固定，混幣別品項直接拒絕加入）。
- 稅金、運費、折扣、優惠券——`total` 在本階段等於 `subtotal`（品項小計加總），差異化定價邏輯留給後續 phase。
- 購物車轉訂單（status 轉為 `converted`）的實際觸發邏輯——本 change 只保留欄位與列舉值，永遠不會自己把 status 從 `active` 改掉；轉換動作是 `order-management` 的職責。
- 購物車「abandoned（棄置）」狀態的自動偵測（例如 N 天無互動的背景 job）——本階段只保留列舉值，不實作偵測邏輯。
- Storefront SSR 購物車頁面（非強制，若時間允許可做但不得犧牲核心 API 品質）。

## Decisions

### D1. 購物車擁有者：直接存 `member_id`（平台級 `Member`），不透過 `ShopMember`

**決定**：`carts.member_id` 直接 FK 到平台級 `Member.ID`，`carts` 表同時擁有 `shop_id`（直接欄位）與 `member_id`（直接欄位），複合唯一鍵 `(shop_id, member_id)`（部分索引，見 D3）。**不**新增一層對 `ShopMember.ID` 的 FK。

**理由**：
1. `api/internal/tenant/enttenancy.go` 的 interceptor/hook 用 `sql.FieldEQ("shop_id", shopID)` 直接對表的 `shop_id` 欄位做斷言，要求被攔截的 entity 直接擁有 `shop_id` 欄位——這與 `product-catalog` design D7 讓 `ProductCategory` 直接存一份 `shop_id`（即使可經 `product_id`/`category_id` 間接推得）是同一個既有慣例。若改成 `carts.shop_member_id` 單一 FK，則失去直接 `shop_id` 欄位，租戶隔離機制無法套用，必須另外繞路——不划算。
2. `ShopMember` 本身的設計就是 `shop_id`+`member_id` 兩個直接欄位（`api/internal/ent/schema/account.go`），不是「先有一個全域 membership id，其他表再 FK 過去」的模式；`carts` 比照同一慣例（`shop_id`+`member_id` 兩欄）與既有資料模型的形狀一致，而不是額外發明一種新的關聯方式。
3. 購物車查詢天生就是「這個 shop、這個會員」兩個維度的複合查詢（`GET /cart` 沒有 cart id 路徑參數，永遠是「呼叫者目前的購物車」），直接查 `(shop_id, member_id)` 比先查 `ShopMember.ID` 再查 `Cart.shop_member_id = ?` 多一趟查詢更直接。

**放棄的替代方案**：`carts.shop_member_id` FK 到 `ShopMember.ID`——語意上更「規範化」（會籍是購物車存在的前提），但增加一次額外查詢/join，且違反 D7 的租戶隔離直接欄位要求；不採用。

### D2. 讀取購物車時金額計算：一律使用加入當下的價格快照

**決定**：`GET /cart` 的小計（`line_total` = `cart_items.price_amount * quantity`）與總計（`subtotal`/`total`）**一律**使用 `cart_items` 表中儲存的價格快照計算，**不**即時查詢 SKU 現價重算。

**理由（取捨）**：
- 穩定性優先：購物車金額若隨 SKU 現價即時浮動，會員看到的總金額可能在瀏覽過程中無預警變動（商家調價），體驗上是驚訝而非透明；且下一步 `order-management` 建立訂單時，若採用即時重算，「使用者最後看到的總金額」與「結帳當下重新計算出的總金額」可能不一致，需要額外處理「金額於送出瞬間被抽換」的邊界情況。用快照則使用者從加入到查看到（未來）結帳的整個流程金額一致、可預期。
- 這與 `product-catalog` design D1 對「下單當下的價格快照」的既有指引一致（「複製值到訂單自己的欄位，不要只存 FK 即時查詢 SKU 現價」），購物車在概念上是「準訂單」，沿用同一慣例。
- 代價：SKU 現價與購物車快照可能不同步（商家調漲或調降後，購物車品項金額未更新）。緩解：每個購物車品項的回應額外附上 `purchasable`/`unavailable_reason`（見 D4）讓前端至少能標示「此品項狀態有變化」；若商家降價，使用者未必馬上受益，但不做「隱性漲價」風險（使用者看到的價格永遠是他加入時同意的價格），這在電商實務上是更保守、更不會引發客訴的選擇。價格是否需要主動提示「現價已變動、是否更新」是 UX 優化，留給前端／後續迭代。

**放棄的替代方案**：即時查 SKU 現價重算——對使用者「更準確」（永遠反映最新價格），但需要處理「快照與現價不同步時如何顯示」「結帳瞬間價格又變了怎麼辦」等一整套邊界情況，且與 `product-catalog` D1 的既有慣例（訂單類資料一律走快照）不一致；不採用。

### D3. 同一 (shop, member) 至多一個 active 購物車；`GET /cart` 不強制先建立

**決定**：`carts` 表對 `(shop_id, member_id)` 建立**部分唯一索引**（`WHERE status = 0`，即僅 active 狀態互斥），比照 `SiteShop` 的 `entsql.IndexWhere` 慣例。購物車列不在會員首次呼叫 `GET /cart` 時建立——只有 `POST /cart/items`（加入第一個品項）才會真正 `INSERT` 一筆 `carts` 列並以該品項的 SKU 幣別設定 `carts.currency`。`GET /cart` 在找不到 active 購物車時回傳一個**未落地（ephemeral）**的空購物車視圖（`id: null`、`items: []`、`subtotal: 0`、`total: 0`、`currency: null`），不建立資料列。

**理由**：多數會員瀏覽商品但不加入購物車，若 `GET /cart` 就建立資料列，會產生大量永遠是空的 `carts` 孤兒列；延後到「真的有品項」才落地，資料庫裡的 `carts` 列都是有意義的。這也連帶解決「購物車幣別要不要允許 NULL」的問題——`currency` 欄位可以宣告為 `NOT NULL`，因為資料列存在的當下就一定知道幣別（來自第一個品項的 SKU）。

### D4. 幣別鎖定：第一個品項決定，之後不符直接拒絕

**決定**：`carts.currency` 由建立當下第一個加入品項的 SKU `currency` 決定並固定；之後任何 `POST /cart/items` 若 SKU 的 `currency` 與購物車現有 `currency` 不同，回 422（不接受、不做任何轉換）。

**理由**：多幣別合計換算需要匯率來源與精確的換算/捨入規則（是一個獨立且有金融合規意涵的子系統），本階段判斷不值得為了「理論上商家可能同時上架多幣別 SKU」這種邊角情境引入複雜度；`product-catalog` design D1 本來就已言明「本階段不做幣別換算」，購物車延續同一立場，不在此另開先例。若商家真的需要多幣別購物車，屆時再開一個獨立 change 處理換算層。

### D5. 加入已存在品項的語意：累加，而非拒絕或覆寫；`PUT` 更新數量是絕對設定值

**決定**：
- `POST /cart/items`：若購物車內已有相同 `sku_id` 的品項，新的 `quantity` **累加**到既有數量上（重新驗證累加後總量是否超過目前庫存），不是拒絕重複、也不是直接覆寫成新值。這是主流電商「按加入購物車按鈕」的預期行為。
- `PUT /cart/items/{itemID}`：把品項數量**設定**為請求的絕對值（不是累加），用於使用者在購物車頁面直接調整數量。`quantity <= 0` 一律拒絕（422）——移除品項要呼叫 `DELETE /cart/items/{itemID}`，語意上更明確（避免「PUT quantity=0」這種隱式刪除、讓兩個端點各自的職責單純)。

**理由**：把「加入」與「調整」拆成兩種不同的數量語意，對應兩種不同的使用者操作意圖（前者是「我還想要更多」，後者是「我現在要恰好這個數量」），避免用同一個端點模擬兩種行為造成前端呼叫方猜測語意的負擔。

### D6. SKU/商品下架後購物車品項的呈現：保留 + 標記不可購買，不自動移除

**決定**：SKU 被下架（`is_active = false`）或其商品變成非 published（`status != 1`）後，已在購物車內的品項**保留**（不自動刪除、不自動調整數量），但 `GET /cart` 的回應為該品項附加：
- `purchasable: bool`
- `unavailable_reason`: 其中之一 `""`（可購買）、`"sku_inactive"`（SKU 已下架）、`"product_unpublished"`（商品非已發佈）、`"insufficient_stock"`（目前庫存低於購物車內數量）、`"sku_deleted"`（SKU 已被刪除，見 D7）。

這個欄位是**唯讀、每次讀取即時計算**（查 SKU/Product 當下狀態與庫存），不落地儲存——沒有「快取的可購買性」這種需要額外失效邏輯的東西。`price_amount`/`currency`（快照）不受這個判斷影響，繼續依 D2 顯示原價。

**理由**：直接把品項從購物車移除是「使用者驚訝」的來源之一（商家下架商品，會員回頭看購物車東西不見了、無法理解發生什麼事）；保留 + 標記讓使用者清楚知道「這個品項現在不能結帳」而不是資料悄悄消失。結帳時的最終擋下仍是 `order-management` 的職責（本 change 只提供這個唯讀信號，不在購物車層面阻擋任何操作——例如仍然允許移除或清空這類不受影響的操作）。

### D7. SKU 被刪除時購物車品項的刪除語意：`sku_id` 設為 `SET NULL`，品項本身不被刪除

**決定**：`cart_items.sku_id` 為**可空**欄位（`Optional().Nillable()`），FK 邊 `entsql.OnDelete(entsql.SetNull)`（放在 assoc `.To()` 邊，比照 `product-catalog` design D7/`ProductSKU.product` 邊的既有教訓：inverse 邊上的 annotation 會被 ent 的 SQL diff 靜默忽略）。當商品被刪除（`product-catalog` design：`Product.skus` 邊已是 `OnDelete(Cascade)`，商品刪除會連帶刪除其 SKU）或 SKU 被單獨刪除時，資料庫層級自動把引用它的 `cart_items.sku_id` 設為 `NULL`；**購物車品項本身不會被刪除**，其 `price_amount`/`currency`/`quantity` 快照原封不動保留，`GET /cart` 回應該品項 `sku_id: null`、`purchasable: false`、`unavailable_reason: "sku_deleted"`。使用者仍可手動 `DELETE` 移除該品項，或清空購物車。

**理由**：
1. **CASCADE（品項隨 SKU 一起刪除）**——最簡單，但會讓使用者的購物車內容在他不知情的情況下悄悄變短，且如果剛好在他準備結帳的當下發生，體驗更差；也讓 `order-management` 失去「這個品項曾經存在、金額是多少」的軌跡（例如商家在會員結帳的瞬間下架又刪除商品，這是需要被看見而非被抹除的異常情況）。
2. **RESTRICT（SKU 被購物車引用就不能刪）**——會讓商家的刪除操作被完全不相關的「某個素未謀面的會員購物車裡還躺著這個 SKU」卡住，商家甚至無從得知是誰的購物車擋住了他，是很糟的耦合，明確不採用。
3. **SET NULL（本決定）**——保留品項與價格快照的歷史軌跡（`order-management` 若要顯示「你購物車裡有品項已下架」可以直接讀到快照金額，不需要額外的稽核表），同時不阻擋商家刪除商品/SKU 的正常操作。代價是 `sku_id` 必須可空，讀取品項詳情（如 `sku_code`、目前庫存）時要先判斷 `sku_id != nil` 才能安全 join；已在 `CartItemView`（見 D9）的 `unavailable_reason = "sku_deleted"` 分支處理。

**對 `order-management` 的含意**：結帳建立訂單前，`order-management` 必須自行判斷每個 `cart_item` 的 `purchasable`（或等價邏輯），`sku_id == nil` 或其他 `unavailable_reason != ""` 的品項不得被結入訂單（應提示使用者移除或商家已下架，無法購買）。

### D8. `status` 欄位：`int16`，比照既有 `Product`/`Shop` 慣例，三個列舉值

**決定**：`carts.status int16`，`CHECK (status IN (0, 1, 2))`，預設 `0`。**列舉值（`order-management` 必須知道的確切值）**：

| 值 | 名稱 | 語意 |
|---|---|---|
| `0` | active | 使用中的購物車，本 change 唯一會建立/操作的狀態；`GET /cart` 只會找 `status = 0` 的列 |
| `1` | converted | 已結帳轉為訂單——本 change **不會**把任何購物車轉成這個狀態，是 `order-management` 結帳成功後的職責（把 `carts.status` 更新為 `1`）。轉換後，該會員下次呼叫 `GET /cart`／`POST /cart/items` 會因為找不到 active 購物車（D3 的部分唯一索引只約束 `status=0`，允許同一會員之後開新的 active 購物車）而重新建立一個新的空購物車 |
| `2` | abandoned | 保留給未來「N 天無互動自動標記棄置」的背景 job（本 change 不實作偵測邏輯），純粹預留列舉值與欄位語意，不影響本階段任何行為 |

**理由**：與 `Product.status`（`int16` + `CHECK IN (0,1)`）、`Shop.status`（`int16` + `CHECK IN (0,1,2)`）用同一套慣例，序列化到 JSON 也是裸數字（不是字串列舉），維持 API 回應風格一致。

### D9. Package 結構與 `order-management` 存取指南

**決定**：新 package `api/internal/cart`（比照 `api/internal/catalog` 的結構：`Service{Client *ent.Client}`、`ErrNotFound`、`ValidationError{Message, Details []Detail}`；本 change 沒有出現需要 409 的情境——沒有「刪不掉」這種 RESTRICT 語意（D7 用 SET NULL 迴避了這個問題）——因此不定義 `ConflictError`，避免死程式碼）。

**關鍵函式（`order-management` 的 agent 會需要）**：

```go
package cart // api/internal/cart

type Service struct { Client *ent.Client }

// GetCartView returns the member's current cart (ephemeral empty view if no
// active cart row exists — design D3). Always looks up status=0 (active)
// only.
func (s *Service) GetCartView(ctx context.Context, shopID, memberID int) (*CartView, error)

// AddItem finds-or-creates the active cart (setting its currency from this
// SKU if newly created — design D3/D4), validates the SKU (shop match,
// is_active, product published, currency match, stock >= resulting
// quantity), and either creates a new cart_item or increments an existing
// one's quantity (design D5).
func (s *Service) AddItem(ctx context.Context, shopID, memberID, skuID int, quantity int32) (*CartView, error)

// UpdateItemQuantity sets an absolute quantity (design D5); quantity <= 0 is
// rejected (422) — use RemoveItem instead.
func (s *Service) UpdateItemQuantity(ctx context.Context, shopID, memberID, itemID int, quantity int32) (*CartView, error)

func (s *Service) RemoveItem(ctx context.Context, shopID, memberID, itemID int) error

// ClearCart deletes all items of the active cart, if one exists; a no-op
// (not an error) when the member has no active cart.
func (s *Service) ClearCart(ctx context.Context, shopID, memberID int) error
```

```go
// CartView / CartItemView are read-only projections (not ent entities) —
// order-management can either reuse this package's GetCartView for display
// purposes, or query ent directly for checkout (recommended: re-validate
// price/stock independently at checkout time rather than trusting a
// display-layer struct — see below).
type CartView struct {
	ID        *int // nil when no active cart row exists (design D3)
	Status    int16
	Currency  string
	Items     []CartItemView
	Subtotal  int64 // sum of LineTotal across items (design D2: snapshot-based)
	Total     int64 // == Subtotal in this phase (no tax/shipping/discount yet)
	ItemCount int
}

type CartItemView struct {
	ID                int
	SKUID             *int   // nil if the SKU was deleted (design D7)
	SKUCode           string // "" if SKUID is nil
	Quantity          int32
	PriceAmount       int64  // snapshot (design D2)
	Currency          string // snapshot
	LineTotal         int64  // PriceAmount * int64(Quantity)
	Purchasable       bool
	UnavailableReason string // "", "sku_inactive", "product_unpublished", "insufficient_stock", "sku_deleted"
}
```

**`order-management` 建立訂單時直接查 ent 的建議寫法**（結帳應該重新驗證，不是照搬購物車顯示層的資料）：

```go
c, err := client.Cart.Query().
	Where(cart.ShopIDEQ(shopID), cart.MemberIDEQ(memberID), cart.StatusEQ(0)).
	WithItems(func(q *ent.CartItemQuery) { q.WithSku(func(sq *ent.ProductSKUQuery) { sq.WithProduct() }) }).
	Only(ctx)
// c.Edges.Items[i].PriceAmount / .Currency / .Quantity 是快照（design D2）；
// c.Edges.Items[i].Edges.Sku 可能是 nil（design D7，SKU 已刪除）——結帳必須擋下。
// 結帳成功後：client.Cart.UpdateOne(c).SetStatus(1).Save(ctx) （design D8，本 change 不會自己做這一步）。
```

金額欄位型別延續 `product-catalog` design D1：一律 `int64`/`BIGINT`，不得引入 `float64`/`numeric`。

## Risks / Trade-offs

- [D2 快照計算可能與 SKU 現價不同步，商家降價後使用者看不到優惠] → 可接受的保守選擇（見 D2 理由）；若未來要做「價格變動提示」，資料庫已有原始 SKU 可查，屬於純前端/API 呈現層的增量功能，不需要改資料模型。
- [D7 的 `sku_id` 可空增加讀取品項時的 nil 檢查負擔] → 影響侷限在 `cart` package 內部與 `order-management` 的結帳驗證邏輯，是資料完整性與商家操作自由度之間刻意的取捨；已在 `CartItemView.SKUID`/`UnavailableReason` 明確建模，不是隱藏的陷阱。
- [D3 的部分唯一索引（`WHERE status = 0`）如果 Atlas diff 引擎對 partial index 語法生成不理解，可能需要手動修正 migration] → `product-catalog`/Phase 1 已有 `SiteShop`（`entsql.IndexWhere`）與 `role_user`/`user_permission`（`UNIQUE NULLS NOT DISTINCT`）的先例，`atlas.sum` 已同步，`make migrate-gen` 產出後照既有慣例人工檢查一次即可。
- [沒有 RBAC 保護，僅靠「member_id 相等」做存取控制，若 handler 忘記做 ownership 檢查會有 IDOR 風險] → API 設計本身規避了大部分風險（路由不接受 cart id/member id 路徑參數，全部從 JWT 取得呼叫者身分；唯一接受外部 id 的是 `itemID`，service 層對每個 `itemID` 操作都必須先驗證該品項所屬的 cart 屬於呼叫者，見 tasks.md 測試要求），並有整合測試明確覆蓋跨會員/跨店存取。

## Migration Plan

Greenfield 新增功能，不涉及既有資料遷移。實作順序（對應 tasks.md）：

1. ent schema（`cart.go`：`Cart`、`CartItem`）。
2. `make migrate-gen` 產生 versioned migration；檢查部分唯一索引與 `SET NULL` FK 語法正確產出。
3. 租戶隔離註冊（`tenant.tenantOwned` 新增 `Cart`/`CartItem`）。
4. `cart` service 層。
5. HTTP handler + router 掛載（`/shop/cart/...`，`d.MemberMW` 認證，不套 RBAC）。
6. `app/wire.go` 組裝。
7. 整合測試（涵蓋 proposal.md 與本文件列出的所有場景）。

回滾策略：Atlas versioned migration down；本 change 新增的表彼此獨立、僅新增一條指向既有 `product_skus`/`shops`/`members` 表的 FK（`SET NULL`，不會在 down 時破壞既有資料），回滾風險低。

## Open Questions

- `abandoned`（棄置）狀態的自動偵測 job 何時做——留給偵測到有實際需求（例如需要對棄置購物車做行銷再行銷/召回）時再開一個獨立 change，不阻塞本 change。
- 是否要在 `GET /cart` 品項回應中额外附上「目前 SKU 現價」供前端顯示「價格已變動」的提示——本階段判斷是純呈現層的增量功能，需要時再加一個唯讀欄位，不影響現有欄位/API 契約。
- Storefront 端（`web/`）購物車 UI——非本 change 強制範圍，且需要與既有主題 schema/元件系統的整合方式一併設計，留給有實際 storefront 開發排期時再處理。
