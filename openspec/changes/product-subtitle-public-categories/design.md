## Context

- `Product`（`api/internal/ent/schema/catalog.go`）：`title`、`slug`、`description`、`status`、`meta`；分類為 M2M（`product_category`，含 `shop_id`）。
- 公開端點 `ProductsHandler.MountPublic` 掛在 `/shop` 租戶群組；`ListPublishedProducts` 已 `WithCategories()`，`productJSON` 只輸出 `category_ids`。
- `catalog.BuildCategoryTree` 已存在（後台用），分類有 `position`。
- 商品 PUT 是全量語意（admin-commerce-catalog design D8），但 `ProductUpdate` 的欄位是指標——未送 = 不變；`subtitle` 沿用指標慣例。

## Goals / Non-Goals

**Goals:** 副標欄位；前台可取得分類樹、可依分類（含子分類）列商品、商品帶分類名稱與 slug；以分類表達「本季新品」；商家可手動排序分類內的商品。

**Non-Goals:**
- 多語系（i18n）欄位模型——見 D1。
- 公開分類端點的分類圖片、描述。

## Decisions

### D1：欄位叫 `subtitle`，不叫 `subtitle_en`
欄位的語意是「商品卡片上的第二行」，英文是**這家店**的內容選擇；另一家店可能放日文或產地。把語言寫進欄位名，等到真的做 i18n 時它會是一個錯誤的半套方案（只有副標有語言、標題沒有）。後台表單標籤寫「副標（例如英文品名）」。（已定案：`subtitle`。）

### D2：`categories` 摘要是 additive，`category_ids` 不動
D13 窗口已關閉，`category_ids` 不能移除。摘要只帶 `id`／`slug`／`name`——前台連結與顯示需要的最小集合；不帶 `parent_id`／`children`（那是分類端點的事，避免兩份樹的序列化漂移）。排序依 `position`、`id`（專案規則：回傳集合必須明訂順序）。

### D3：「本季新品」是分類，不是旗標
從程式碼看，分類已經具備旗標需要的一切且更多：
- **篩選**：本 change 本來就要新增「依分類列商品」，新品區塊直接重用，不需要第二個篩選參數；旗標則需要 `is_new` 欄位 ＋ 新查詢參數 ＋ 新後台控件 ＋ migration。
- **命名與數量由商家決定**：「本季新品」「母親節限定」「聖誕檔期」都是同一個機制；旗標只能表達一種。
- **換季**：把商品從分類移除即可；旗標同樣要逐一取消，沒有優勢。
- **CMS 綁定**：後續 content-field schema 可以有一個「分類選擇」欄位讓區塊顯示某分類的商品——通用；旗標需要專屬區塊型別。

旗標唯一的優勢是「有自動到期日」（`new_until`）——但那是排程需求，分類加 `visible_from/until` 也做得到，且今天沒人要求。代價：分類刪除有 RESTRICT（design D4 of product-catalog），換季刪分類前要先解除關聯——這是既有行為，不是新問題。

### D4：公開分類端點回傳整棵樹，不分頁
分類數量在一家店是數十級，不是千級；前台導覽需要整棵。重用 `BuildCategoryTree`，與後台同一個序列化函式（避免兩份）。

### D6：分類內的商品手動排序存在關聯上
`product_category` 新增 `position`（int，DEFAULT 0，CHECK `>= 0`）。排序是「商品在**某個分類**裡的位置」，同一商品在「本季新品」排第一、在「玫瑰」排第五，所以只能放在關聯列上，不能放在 `products`。
- 新端點 `GET /admin/shops/{shopID}/categories/{categoryID}/product-order`（`category.view`，回傳直接關聯商品 `[{product_id, title, status}]` 依 `position`、`product_id`）與 `PUT` 同路徑（`category.edit`，`{product_ids: [...]}` 必須**恰好是**目前直接關聯商品的排列，否則 422——與商品圖片重排同一條規則）。
- 新關聯（建立商品或 PUT 時新增的 `category_ids`）的 `position` = 該分類目前最大值 + 1（排最後）。
- **既有的商品 PUT 會刪光再重建關聯**（`catalog.UpdateProduct` 對 `category_ids` 做 Delete＋Create），這會把手排的順序歸零而且不會噴錯。本 change 把它改成**差集更新**：只刪除被移除的、只新增新加入的，保留下來的關聯列（與其 `position`）一個字不動。商品 PUT 的對外語意（全量取代 `category_ids`）不變。
- 不帶 `category` 篩選的公開列表仍依 `id` 排序（行為不變）。

### D7：分類篩選包含子分類
`?category=flowers` 回傳直接關聯於 `flowers` **或其任一子孫分類**的已發佈商品，每個商品只出現一次。子孫集合在 Go 端由該店全部分類（一次查詢，數十筆）走樹求得，再以 `HasCategoriesWith(category.IDIn(...))` 查詢——不寫遞迴 CTE（ent 表達不了，且原生 SQL 會繞過租戶 interceptor）。

**排序**：先列**直接**屬於所請求分類的商品（依該關聯的 `position`、再依 `id`），再列只經由子孫分類到達的商品（依 `id`）。理由：手動排序是商家在「這個分類頁」上的意圖，只有直接成員有這個位置；子分類的商品沒有在父分類上的位置，依 id 穩定排在後面。排序在 SQL 以 `LEFT JOIN product_category ON category_id = :requested` 的 `position NULLS LAST, id` 完成（以 ent 的 `Modify`／`sql.Selector` 表達，`shop_id` 條件明寫，不倚賴 interceptor）。

### D5：未知分類 slug 回 200 ＋ 空列表
`/shop/products?category=x` 是集合查詢；以 404 回應會讓「分類存在但目前沒有已發佈商品」與「分類不存在」變得可區分只在一邊，且靜態建置時拼錯 slug 應該得到空頁而不是建置失敗。與 `q` 篩選的既有容錯慣例一致。

## Risks / Trade-offs

- [前台把 `category_ids` 與 `categories` 混用造成不一致] → 兩者由同一個 `Edges.Categories` 產生，契約敘述標明 `categories` 是 `category_ids` 的展開。
- [差集更新改寫了 `UpdateProduct` 的關聯路徑] → 既有「全量取代」整合測試保留，另加「保留下來的關聯 position 不變」的斷言。
- [公開分類端點暴露草稿分類] → 分類沒有草稿狀態；所有分類本來就是公開導覽用。若某分類只有草稿商品，公開列表回空，不洩漏草稿商品。

## Migration Plan

`make migrate-gen name=add_product_subtitle_and_category_position`：`products.subtitle varchar(200) NOT NULL DEFAULT ''`、`product_category.position integer NOT NULL DEFAULT 0` ＋ CHECK。既有關聯全部為 0，同 position 依 `product_id` 排序，等同今天的 id 順序——零回填，可安全回滾。

## Decided（原 Open Questions，已由使用者定案）

- 欄位名為 `subtitle`（語言中立）。
- 支援分類內的商品手動排序（D6）。
- 分類篩選包含子分類（D7）。
