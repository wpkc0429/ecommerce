# Design: product-catalog

## Context

Phase 1（`phase1-multitenant-cms-foundation`）與 proposal 1–3（`auth-rate-limiting`、`platform-shop-crud`、`cms-editor-field-order`）已交付並歸檔：多租戶資料隔離（`api/internal/tenant`）、shop 範圍化三層 RBAC（`api/internal/rbac`）、schema/payload 解耦的 CMS 引擎（`api/internal/cms`）、雙軌 JWT。技術決策全文見 `openspec/changes/archive/2026-07-18-phase1-multitenant-cms-foundation/design.md`（D1–D13）。

本 change 是 Phase 2 電商核心的**第一個** proposal，建立商品目錄的資料模型與管理 API。後續 5 個 proposal（`shopping-cart`、`order-management`、`payment-integration`、`shipping-logistics`、`member-tiers-and-points`）都會直接讀寫本 change 定義的 `products`/`product_skus` 表，並沿用本 change 定案的金額表示法——因此本文件的 Decisions 特別詳盡，讀者對象除了本 change 的實作者，也包含後續 proposal 的 agent。

讀者對象：後端工程師。資料表欄位一律 snake_case 英文；文件以正體中文撰寫。

## Goals / Non-Goals

**Goals:**

- 商品分類的樹狀資料模型與 shop 內名稱/slug 唯一性。
- 商品的標題/slug/描述/草稿-發佈狀態/SEO meta，語意比照既有 `pages`。
- 商品與分類的關聯方式定案（多對多）。
- 商品 SKU 的資料模型：選項值、價格、庫存、可用旗標，且巢狀掛在商品 CRUD 之下。
- **金額表示法定案**：一個所有後續 Phase 2 proposal 都必須沿用的整數金額慣例。
- 新表全數納入既有租戶隔離機制（`tenant.tenantOwned`）與 RBAC 權限節點命名慣例。
- 商家範圍管理 API（比照 `pages` 的 CRUD 與權限模式）＋ 一個公開唯讀端點（比照 `content-rendering` 的 published-only 語意）。

**Non-Goals（明確留給後續 proposal 或更後期）：**

- 購物車、訂單、金流、物流（`shopping-cart`／`order-management`／`payment-integration`／`shipping-logistics`）。
- 多倉儲、庫存預留鎖定/超賣防護（`order-management` 或更後面處理；本階段 `stock_qty` 僅單一數字）。
- 會員等級／點數（`member-tiers-and-points`）。
- 多幣別「換算」邏輯（本階段每個 SKU 存自己的 `currency`，但不做匯率轉換或跨幣別加總——那是 `payment-integration` 或會計相關 phase 的範圍）。
- 商品在 storefront 的實際渲染頁面（SSR 列表/詳情頁）——非強制，見下方 D8。
- 多語系翻譯管理。
- 商品評論、推薦、搜尋引擎整合。

## Decisions

### D1. 金額表示法：`BIGINT` 整數最小貨幣單位 + `currency` 欄位（本 change 最重要的契約）

**決定**：所有金額欄位（本 change 的 `product_skus.price_amount`，以及後續 cart/order/payment 的金額欄位）一律：

- 資料庫型別 `BIGINT`；Go 型別 `int64`。
- 語意為「該幣別的最小貨幣單位（minor unit）」的整數計數，**絕不使用浮點數**（`float32`/`float64`/`double precision`），也不使用 `numeric`/`decimal`。
- 搭配 `currency CHAR(3)` 欄位（ISO 4217 alpha code，如 `TWD`、`USD`、`JPY`）標示該金額所屬幣別；每個 SKU 自帶 `currency`（非商家全域單一設定），為未來多幣別商家保留擴充性，但**本階段不實作幣別換算，也不驗證 shop 內是否單一幣別**——demo/seed 資料一律使用 `TWD`。
- 最小貨幣單位的定義取決於 ISO 4217 的 exponent：零小數貨幣（`TWD`、`JPY`、`KRW` 等）`price_amount` 直接代表「元」；二小數貨幣（`USD`、`EUR` 等）`price_amount` 代表「分」（cent）。exponent 對照表**本階段不需要程式碼實作**（沒有跨幣別運算需求），只需保證欄位型別與注釋清楚；若後續 phase 真的要接受非零小數貨幣的使用者輸入或做金額換算，屆時再建立一個 `money` package 集中處理 exponent 對照與格式化，不要各 proposal 各自土法煉鋼。

**為什麼是整數而非浮點數**：IEEE 754 浮點數無法精確表示大多數十進位小數（如 0.1），金額運算（加總購物車、計算稅金、拆分退款）長期累積誤差會導致對帳不平——這是金融系統的基本常識，不需要進一步論證。

**為什麼是整數最小單位而非 `numeric(p,s)`**：`numeric` 在 PostgreSQL 內部精確、可用，是另一個合理選項。選擇整數最小單位而非 `numeric` 的理由：
1. 本專案的資料層是 Go + ent + `encoding/json`——`numeric` 經 `database/sql` 讀出常是 `string` 或需要額外的 decimal 套件（如 `shopspring/decimal`）才能安全運算，等於引入一個新的第三方相依；`BIGINT` 對應 Go `int64` 是標準函式庫原生型別，序列化為 JSON 也不會有精度陷阱（在 JS/JSON number 的安全整數範圍內，`int64` 的常見金額範圍完全安全）。
2. 支付閘道（Stripe、綠界、Line Pay 等，`payment-integration` 會串接）的 API 本身也是以最小貨幣單位整數表示金額（如 Stripe 的 `amount` 欄位就是 cents），採用同樣的表示法讓串接時不需要再做一次型別轉換與精度驗證，減少一個出錯環節。
3. `BIGINT` 對「元」（TWD 情境）等於直接儲存整數金額，可讀性與 `numeric(10,0)` 相當，但少了 `numeric` 型別在 Go 生態的序列化摩擦。

**為什麼 `BIGINT` 而非 `INT`（int32）**：`INT4` 上限約 21 億，單一商品價格不可能超過，但購物車/訂單金額是**加總**（多商品、多數量、稅金、運費），長期累積或極端情境（B2B 大宗訂單）用 `INT4` 有理論溢位風險；`BIGINT`（int64，上限約 922 京）在可預見的未來沒有溢位疑慮，且與 SKU 價格欄位用同一型別，運算時不需要在加總處另做型別提升，減少一個潛在的隱蔽 bug 來源。

**後續 proposal 的強制契約**（寫給 `shopping-cart`/`order-management`/`payment-integration`/`shipping-logistics` 的 agent）：
- 讀取 SKU 價格：`ent.Client.ProductSKU.Get(ctx, skuID)` 後取 `.PriceAmount`（`int64`）與 `.Currency`（`string`，3 碼）。
- 購物車/訂單的單價、小計、總金額、運費、折扣金額等**所有**金額欄位一律 `BIGINT` + 同一枚 `int64`，不得引入 `float64` 或 `numeric`。
- 若購物車/訂單需要「下單當下的價格快照」（常見電商需求：SKU 價格之後調整不應影響已下單訂單），複製 `price_amount`/`currency` 的**值**到訂單自己的欄位，不要只存 FK 後即時查詢 SKU 現價——但這是 `order-management` 的設計範圍，本 change 只保證來源值的型別正確。

### D2. 商品 ⇄ 分類：多對多

**決定**：`product_category` join table（`product_id`, `category_id`, `shop_id`），複合主鍵 `(product_id, category_id)`。

**理由**：真實電商情境商品經常同時屬於多個分類（例如「新品上市」與「男鞋」同時成立、或同一商品在「特價專區」與其原生分類並存）。若採單一 `category_id` FK，商家在這類常見情境會被迫用「主分類 + 額外標籤系統」繞過限制，等於自己在應用層重新發明多對多。既有慣例已有兩個 join table 前例（`role_permission`、`site_shop`），複雜度增加的成本是可控、已驗證的模式。

**放棄的替代方案**：單一 `category_id`（單一父分類）——實作最簡單，但前述情境需要商家自行建立「交集分類」（如「新品男鞋」）來模擬多對多，長期會讓分類樹退化成標籤爆炸；不採用。

**取捨**：多對多讓「分類刪除時商品去向」的語意變複雜（見 D4），且商品列表依分類篩選需要 join 而非簡單 WHERE；這是可接受的成本。

### D3. 分類樹狀結構：自參考 `parent_id` + 服務層防環

**決定**：`categories.parent_id` 為可空的自參考 FK；根分類 `parent_id IS NULL`。樹狀查詢在 service 層完成：一次查出 shop 內所有分類（資料量在合理範圍內，不需要遞迴 CTE），於記憶體組裝 `map[id][]children`。

**防環**：異動 `parent_id` 時（建立或更新），service 層沿新 parent 的祖先鏈往上走，若途中命中「被更新的分類自己」則拒絕（422）——PostgreSQL 的 FK 約束無法表達「不可成環」，只能在應用層做，這與既有 `PrecheckTheme`（`cms/service.go`）等應用層業務規則校驗是一致的模式（design D3/D6 已有先例：資料庫做結構完整性，業務語意留給 service 層）。

**放棄的替代方案**：PostgreSQL 遞迴 CTE 即時查樹——對商品分類這種資料量（通常數十到數百筆／shop）沒有效能必要性，記憶體組裝更簡單、更容易測試；若未來分類數量暴增，屆時再優化查詢層,不影響 API 契約。

### D4. 刪除語意：非空分類/掛載商品的分類拒絕刪除

**決定**：分類刪除採 **RESTRICT** 語意——若分類仍有子分類（`parent_id` 指向它）或仍有商品透過 `product_category` 掛在它底下，刪除回 409（Conflict，`httpx.Conflict`）並提示先清空。

**理由**：CASCADE 刪除子分類或靜默解除商品分類關聯，容易造成商家「刪錯一個分類、整棵子樹與商品分類關聯消失」的不可逆意外；RESTRICT 強迫商家先手動處理（搬移子分類、移除商品關聯），符合既有 `pages` 對 `home` 頁面「不可刪除」的保守刪除哲學（`cms/service.go` `DeletePage`）。商品刪除本身沒有這個問題（商品沒有「子節點」），商品刪除時其 SKU 與 `product_category` 關聯以 DB `ON DELETE CASCADE` 一併清除。

### D5. SKU 選項值：JSON 物件（非陣列）

**決定**：`product_skus.options` 為 JSONB **物件**（如 `{"size":"M","color":"red"}`），非陣列。

**理由（與已知 jsonb 鍵序地雷的區辨）**：Phase 1 已確認 PostgreSQL jsonb 不保留物件鍵順序（依鍵長+字典序重排）。CMS 的 `page_schema.sections` 因為是「依序渲染的區塊列表」，順序即語意，所以用陣列。SKU 的 `options` 用途不同——它是**透過已知鍵名查找**的屬性集合（前端永遠是 `options.size`、`options.color` 這樣具名存取，不會「依序迭代第一個/第二個 key」來決定意義），鍵的排列順序不承載任何業務語意，只有顯示排序可能受影響，而選項的顯示排序（例如「先顯示顏色、再顯示尺寸」）本就該由前端依商品類型的既定 UI 規則決定，不該依賴後端 JSON 序列化的鍵序這種不可靠的隱含機制。因此物件形式在此場景是安全的，且比陣列形式（`[{"key":"size","value":"M"}]`）更直觀、查詢更簡單（`options->>'size'`）。

**放棄的替代方案**：陣列形式 `[{"key":"size","value":"M"},...]`——語意上更「保序」，但選項值本就不需要保序，徒增查詢與序列化複雜度；不採用。

### D6. 庫存：單一數字，欄位設計預留未來擴充

**決定**：`product_skus.stock_qty INT NOT NULL DEFAULT 0`（`CHECK (stock_qty >= 0)`），代表可售庫存的單一數字，**不做多倉儲、不做預留鎖定**（如「加入購物車即鎖 15 分鐘」這類邏輯）。

**為什麼欄位設計不會擋到未來擴充**：`stock_qty` 是一個獨立欄位，不與其他欄位耦合語意；未來若要加「多倉儲」，自然的路徑是新增 `warehouse_stocks(sku_id, warehouse_id, qty)` 子表，`product_skus.stock_qty` 可以改為衍生欄位（各倉加總的快取值）或保留作為「總覽用途」欄位而不破壞現有讀取路徑；若要加「預留鎖定」，自然路徑是新增 `stock_reservations(sku_id, qty, expires_at, order_id)` 子表，可售庫存 = `stock_qty - SUM(未過期 reservations)`，同樣不需要改動 `stock_qty` 本身的型別或既有的讀取端。因此本階段不需要為了「預留擴充性」而過度設計本表，只要不把 `stock_qty` 和其他語意（如價格）綁在同一個結構、保持它是一個單純的整數欄位即可。

**為什麼 `INT`（int32）而非 `BIGINT`**：庫存數量不是金額，沒有「加總跨商品」的天然需求，實體商品庫存數量落在 int32 範圍（21 億）內是絕對安全的假設；與 D1 的金額欄位刻意選 `BIGINT` 的理由不同，這裡沒有必要跟隨同一個型別選擇。

### D7. 商品/分類的租戶隔離與 RBAC 落地

- `categories`、`products`、`product_skus`、`product_category` 四張表全數在 `api/internal/tenant/enttenancy.go` 的 `tenantOwned` map 註冊，每張表都有直接的 `shop_id` 欄位（`product_category` 雖然可經由 `product_id`/`category_id` 間接推得 shop，但既有 interceptor/hook 的實作方式要求被攔截的 entity 直接擁有 `shop_id` 欄位才能套用 `sql.FieldEQ("shop_id", shopID)` 與建立時的 `m.SetField("shop_id", shopID)`——因此比照既有 `ShopMember`/`MemberRefreshToken` 的模式，`product_category` 也直接存一份 `shop_id`，屬 defense-in-depth：即使應用層 join 邏輯有 bug，租戶隔離仍在資料層兜底）。
- 新權限節點沿用 `page.*`/`shop.*` 的命名慣例：`category.view`、`category.create`、`category.edit`、`category.delete`、`product.view`、`product.create`、`product.edit`、`product.delete`。SKU 的建立/修改/刪除巢狀在 `product.create`/`product.edit` 之下，不另立 `sku.*` 權限節點——SKU 離開商品沒有獨立業務意義，這與「頁面發佈」需要獨立 `page.publish` 權限的情境不同（SKU 沒有類似「發佈」的獨立動作）。
- `roleDefs`：`merchant_owner` 取得全部 8 個新節點；`editor` 取得除 `category.delete`/`product.delete` 外的 6 個節點——比照 `editor` 缺 `page.delete`/`page.publish` 的既有模式（editor 可以建立/編輯內容，但刪除性操作保留給 owner）。

### D8. 公開唯讀端點：掛在既有 `/shop` tenant-scoped 路由群組

**決定**：新增 `Deps.Catalog *httpapi.CatalogHandler`，掛載在 router.go 既有的 `v1.Route("/shop", func(sr chi.Router){ sr.Use(d.TenantMW); ... })` 群組內（該群組目前掛 member 認證端點）。`GET /products`、`GET /products/{slug}` 為公開路由（群組內、`TenantMW` 之後、`MemberMW` 之前），比照 `sr.Post("/auth/register", ...)` 同樣不需要登入即可呼叫、但需要 tenant 上下文（Host 或 `X-Site-Domain` 解析）。

**published-only 語意**：僅回傳 `status = 1`（已發佈）的商品；`status = 0`（草稿）商品在此端點一律 404，比照 `content-rendering` spec 的既有語意（頁面草稿不可公開存取）。此端點**不做 Redis 快取**（design D8 的 render bundle 快取是給頁面渲染設計的、有明確的版本化失效事件；商品目錄快取涉及庫存等易變欄位，快取策略留給後續評估，本階段先以直接查 DB 的正確性為優先，效能問題等有實際流量數據再處理）。

**是否做 storefront 實際渲染頁面（SSR）**：非強制。若時間允許可以做一個最小的 `web/app` 路由讀取此公開端點、渲染商品列表/詳情，但這不影響 API 契約，且**不得**因為做這個加分項而犧牲核心 API 的測試覆蓋——proposal 範圍界線已載明。

### D9. Package 結構：`api/internal/catalog`

比照 `api/internal/cms` 的檔案切分：`service.go`（Category/Product/SKU 的領域操作，`ValidationError`/`ErrNotFound` 沿用同構但獨立定義在 catalog package，不 import cms package 製造跨 package 耦合——兩者結構相似是刻意的一致性，不是共用程式碼的理由）。若後續發現 slug 正規化、分頁參數等邏輯與 cms package 高度重複，可在後續 proposal 評估抽出共用 helper，本階段先各自獨立以降低模組間耦合。

## Risks / Trade-offs

- [金額欄位型別選擇一旦定案、後續 5 個 proposal 都會依賴，若日後發現不合適，遷移成本高] → D1 已充分論證且對齊業界慣例（支付閘道原生用最小貨幣單位整數），改變機率低；若真的需要改，屆時所有依賴此慣例的欄位型別一致，可用同一套遷移腳本批次處理。
- [多對多分類關聯增加查詢複雜度，商品列表依分類篩選需要 join] → 影響範圍侷限在 catalog 內部查詢實作，不影響 API 契約；資料量級（單一商家的商品目錄）不構成效能疑慮。
- [分類刪除 RESTRICT 語意可能造成商家困惑（為什麼不能刪）] → 錯誤訊息需明確說明「請先移除子分類/商品關聯」，前台/後台 UI 未來可在刪除按鈕旁提示。
- [SKU options 物件形式若未來真的出現「順序敏感」的選項情境（目前判斷不會發生）] → 因為是獨立 JSONB 欄位，若真有需要，遷移路徑是新增一個排序陣列欄位或改變寫入格式，不影響 price_amount/stock_qty 等其他欄位；影響面可控。
- [product_category 為了 tenant 隔離重複存一份 shop_id，理論上可能與 product/category 的 shop_id 不一致（資料異常）] → service 層建立關聯時一律從已載入的 product/category entity 取 shop_id 寫入（不接受 client 傳入的 shop_id），且三者的 shop_id 皆由同一個 tenant hook 強制附加，實務上不會產生跨租戶不一致的關聯列。

## Migration Plan

Greenfield 新增功能，不涉及既有資料遷移。實作順序（對應 tasks.md）：

1. ent schema（`catalog.go`：Category、Product、ProductSKU、ProductCategory）。
2. `make migrate-gen` 產生 versioned migration。
3. 租戶隔離註冊（`tenant.tenantOwned` 新增四表）。
4. RBAC 權限節點（`seed.go` 的 `PermissionCatalog`/`roleDefs`）。
5. `catalog` service 層（分類 CRUD + 防環、商品 CRUD + SKU 巢狀 upsert）。
6. HTTP handler（merchant CRUD，掛 `/admin/shops/{shopID}/...`）。
7. 公開唯讀端點（掛 `/shop`）。
8. `app/wire.go` 組裝。
9. 整合測試（涵蓋 proposal.md 列出的所有場景）。
10.（選做，不得拖累前述品質）storefront 最小 SSR 列表/詳情頁。

回滾策略：Atlas versioned migration down；本 change 新增的表彼此獨立、未修改既有表結構，回滾風險低。

## Open Questions

- 分類是否需要「排序」欄位（`position`）以控制前台顯示順序——本階段先加一個簡單的 `position INT DEFAULT 0` 欄位（由商家手動設定序號），細緻的拖拉排序 UX 留給 admin 前端後續迭代。
- 商品是否需要「主圖/圖片集」欄位——本階段判斷屬於 storefront 呈現需求而非目錄核心資料模型，`meta` JSONB 可暫時承載（如 `meta.images`），若後續證明需要獨立表（多圖 + 排序 + alt text）再拆分，不影響本階段的 API 消費者。
- SKU 是否需要獨立於商品的 SEO slug（讓單一 SKU 有自己的 URL）——目前判斷商品層級的 slug 已足夠 Phase 2 需求，SKU 由 `sku_code` 或 `options` 在商品詳情頁內選擇，不需要獨立路由；留待 storefront 實際開發時再驗證此假設。
