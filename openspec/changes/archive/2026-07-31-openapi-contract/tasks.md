## 1. 前置查證

- [x] 1.1 以 `chi.Walk` 對零值 `Deps` 組出的 router 取得完整路由清單，確認數量與生產環境一致（design D3 的前提：掛載只看 nil，守衛在掛載時不解參照）——**查證結果推翻了原本的假設，見 design D3a**
- [x] 1.2 確認 `santhosh-tekuri/jsonschema/v6` 能以 `#/components/schemas/X` 片段指標編譯 OpenAPI 3.1 文件中的子 schema（design D4）
- [x] 1.3 確認 `gopkg.in/yaml.v3` 已在 `go.sum`（提為 direct 不會新增下載）
- [x] 1.4 確認 `.github/workflows/ci.yml` 執行 `go test ./...`（四道檢查因此自動進 CI，`ci.yml` 無需修改 — design D2）

## 2. OpenAPI 骨架與共用元件（spec: api-contract）

- [x] 2.1 建立 `api/openapi/openapi.yaml`：`openapi: 3.1.0`、`info`（含租戶解析、錯誤約定、RBAC 敘述為人工維護的聲明 — design D3/D8）、`servers`
- [x] 2.2 `components/securitySchemes`：`adminJWT`／`memberJWT`／`orderAccessToken`／`providerSignature` 四種，各自寫明受眾與失敗行為（design D7）
- [x] 2.3 `components/parameters/PublishableKey`：header 參數，描述寫明「識別碼非憑證、決定租戶不決定授權、無效即 404 不回退」；**不得**出現在 `securitySchemes`（design D7）
- [x] 2.4 `components/schemas/Error` ＋ `ValidationDetail`：統一 envelope，`Error` 的描述列出全部已知 `code` 及其意義（design D8）
- [x] 2.5 `components/responses/*`：`BadRequest`／`Unauthorized`／`Forbidden`／`NotFound`／`Conflict`／`ValidationFailed`／`TooManyRequests`／`ShopDisabled`／`NoContent`，全部 `$ref` 同一個 `Error`
- [x] 2.6 `components/schemas/MinorUnitAmount`：唯一的金額型別，描述寫明整數／最小貨幣單位／非浮點／幣別由同層 `currency` 決定，`examples` 用真實量級（design D9）
- [x] 2.7 `components/parameters/*`：`page`／`page_size`／`shopID` 共用參數

## 3. OpenAPI 路徑：前台與訪客（ONDO 路徑優先，spec: api-contract）

- [x] 3.1 公開目錄：`GET /shop/products`、`GET /shop/products/{slug}`、`GET /shop/shipping-methods`
- [x] 3.2 會員認證：`POST /shop/auth/{register,login,refresh,logout}`、`GET /shop/me`
- [x] 3.3 購物車：`GET /shop/cart`、`POST /shop/cart/items`、`PUT|DELETE /shop/cart/items/{itemID}`、`DELETE /shop/cart/items`
- [x] 3.4 會員訂單與付款：`POST /shop/checkout`、`GET /shop/orders`、`GET /shop/orders/{orderID}`、`POST /shop/orders/{orderID}/cancel`、`POST /shop/orders/{orderID}/payments`、`GET /shop/orders/{orderID}/shipment`
- [x] 3.5 會員點數：`GET /shop/points`、`GET /shop/points/transactions`
- [x] 3.6 訪客：`POST /shop/guest/checkout`（含 `order_access_token` 回應）、`GET /shop/guest/order`、`POST /shop/guest/order/payments`、`GET /shop/guest/order/shipment`
- [x] 3.7 共用回應 schema：`Order`／`OrderItem`／`Cart`／`CartItem`／`Product`／`ProductSKU`／`PublicShippingMethod`／`Payment`／`Shipment`／`MemberPoints`／`PointTransaction`／`TokenResponse` 等 46 個

## 4. OpenAPI 路徑：後台、webhook 與渲染（spec: api-contract）

- [x] 4.1 後台認證：`POST /admin/auth/{login,refresh,logout}`、`GET /admin/me`
- [x] 4.2 平台層：`sites`（6）、`shops`（4）、`themes`（7）、`permissions`／`roles`／`users` 角色指派（7）
- [x] 4.3 商家層 CMS：`pages`（8）、`content`（2）、`page-types`（1）、`themes`／`theme`／`theme/precheck`（3）、`roles`／`users`（4）
- [x] 4.4 商家層電商：`categories`（5）、`products`（5）、`orders`（3）、`orders/{id}/payments`（1）、`shipping-methods`（5）、`shipments`（3）、`member-tiers`（5）、`members/{id}/points*`（3）
- [x] 4.5 商家層前端接入：`publishable-keys`（3）、`allowed-origins`（3）
- [x] 4.6 `POST /webhooks/payments/{provider}`（簽章認證、`X-Site-Domain` 不適用、回應 `{status: ok|ignored}`）
- [x] 4.7 `GET /render/page`（唯一仍接受 `X-Site-Domain` 的端點、`X-Cache` 回應標頭）、`GET /render/preview`
- [x] 4.8 `GET /healthz`（唯一不在 `/api/v1` 下的路由）

## 5. 檢查 1／2／4：單元層契約測試（spec: api-contract）

- [x] 5.1 `api/internal/httpapi/openapi_contract_test.go`：載入 YAML → `json.Marshal` → `jsonschema.UnmarshalJSON` 的正規化流程（design D4）
- [x] 5.2 檢查 1（路由覆蓋）：`chi.Walk` 對零值 `Deps` router；Walk 到的必須全部有文件，文件有而 Walk 沒有的必須通過**可達性探測**（design D3a）
- [x] 5.3 檢查 2（金額單位紀律）：走訪全部 schema，金額語意屬性 MUST 為 `$ref`（或單元素 `allOf` 包裝）到 `MinorUnitAmount`；4 個分頁 `total` 為已登記豁免
- [x] 5.4 檢查 4（v1 表面鎖）：讀 `api/openapi/v1-surface.txt`，逐行驗證仍存在於文件中；提供 `-openapi.update-surface` flag 重新產生
- [x] 5.5 產生並提交 `api/openapi/v1-surface.txt`（371 項），檔頭寫明「刪除任一行即為破壞 v1；D13 破例窗口已於 change order-extras 關閉」
- [x] 5.6 `go.mod`：`gopkg.in/yaml.v3` 由 indirect 提為 direct（**手動移行，未跑 `go mod tidy`**，見補充決定）

## 6. 檢查 3：整合層回應驗證（spec: api-contract）

- [x] 6.1 `api/internal/httpapi/openapi_conformance_integration_test.go`：測試檔頭寫明 design D10 的取樣原則與已知缺口
- [x] 6.2 建立最小前置資料（shop、site 綁定、publishable key、已發佈商品含 SKU、兩筆運送方式其一停用、有密碼的會員），組出真實 router
- [x] 6.3 ONDO 路徑逐條驗證：`GET /shop/products` → `GET /shop/products/{slug}` → `GET /shop/shipping-methods` → `POST /shop/guest/checkout` → `GET /shop/guest/order` → `POST /shop/guest/order/payments` → **簽章 webhook** → `GET /shop/guest/order/shipment`
- [x] 6.4 會員路徑補齊共用 schema 覆蓋：`TokenResponse`、`Cart`、`Order`（會員版）、`OrderPage`、`MemberPoints`、`PointTransactionPage`
- [x] 6.5 錯誤 envelope 驗證：401（無 token）、404（不存在的訂單）、422（含 `details[].pointer`）各至少一次
- [x] 6.6 斷言每個共用 schema 至少被驗證過一次（`requiredSchemas` 21 項，以「被驗證過的回應之 schema 圖」計算，避免取樣清單日後被悄悄砍短）
- [x] 6.7 額外斷言真實金額：`subtotal_amount = 6800`（2 × 3400 最小貨幣單位）、`shipping_fee = 200`、`total = subtotal + fee`、**請款金額＝`total_amount`**

## 7. 交付文件（spec: api-contract）

- [x] 7.1 `docs/storefront-integration.md`：**第 0 節就是金額單位聲明**；依接入流程組織；每步可貼上就跑的 `fetch` 片段
- [x] 7.2 同檔：彙整三個已 archive change 的接入資訊（`storefront-client-auth` 的金鑰／origin／CORS、`guest-checkout` 的訪客流程與訂單存取 token、`order-extras` 的卡片訊息／配送日／運費與點數基準）
- [x] 7.3 同檔：錯誤對照表（依狀態碼 ＋ 依 `details[].pointer` ＋ 其他症狀共三張表）
- [x] 7.4 同檔：明寫分工——欄位以 `openapi.yaml` 為準；並註明本文件不在測試覆蓋範圍內，以及「請忽略未知的回應鍵」（design D12/D14）
- [x] 7.5 根目錄 `README.md`：只寫「這是什麼／怎麼跑／文件在哪」，其餘連結至 `CLAUDE.md`／`openspec/`／`api/openapi/`（design D13）
- [x] 7.6 `Makefile`：`openapi`（四道檢查入口）、`openapi-mock`（Prism）、`openapi-ui`（Redocly），後兩者以 `npx --yes` 執行

## 8. 防腐機制的失敗證明（**本變更唯一真正重要的驗收**）

- [x] 8.1 檢查 1 證明（兩個方向皆實測）
- [x] 8.2 檢查 2 證明
- [x] 8.3 檢查 3 證明（**文件側與實作側各一次**）
- [x] 8.4 檢查 4 證明
- [x] 8.5 每次證明後完整還原，確認四道檢查全部回綠

## 9. 驗證

- [x] 9.1 `make test` 全綠
- [x] 9.2 `make test-int` 全綠
- [x] 9.3 `make lint` 無新增問題（`0 issues.`）
- [x] 9.4 `make seed-demo` 後 `cd e2e && node happy-path.mjs` 通過（**76 項斷言**，乾淨狀態）
- [x] 9.5 `make openapi-mock` 起 Prism，實際對 mock 發請求（含 request 驗證），確認 ONDO 可離線開發
- [x] 9.6 契約文件通過 `npx @redocly/cli lint`（**valid**，2 個已評估後保留的 warning）

## 10. 收尾

- [x] 10.1 更新 `CLAUDE.md`：契約位置、四道檢查、chi.Walk 盲點、「改端點必須同時改 `openapi.yaml`」的紀律
- [x] 10.2 更新 `openspec/project.md`：Testing Strategy 加入契約測試；Important Constraints 加入 v1 表面鎖與權威順序；修正已過時的「尚未初始化 git repo」
- [x] 10.3 `openspec validate openapi-contract` 通過

---

### 實作過程中的補充決定（與 design 的差異）

| 項目 | 決定 | 理由 |
| --- | --- | --- |
| **`chi.Walk` 的盲點**（design D3a，**實作前查證時發現，回頭改了 design**） | 比對規則由「集合相等」改為「Walk 到的必須有文件 ＋ 文件多出來的必須通過可達性探測」 | `chi.Walk` 對真實 router 只回報 109 條，實際可達 111 條。`router.go` 在 `/shops/{shopID}` 同一樣板上既掛了子 router 又掛了 `Shops.MountPlatform` 的 GET/PUT，Walk 走到該節點會下潛進子 router，不回報節點自身的方法處理器（已以真實請求驗證兩者執行期皆正常）。若不處理，覆蓋測試會把這兩條報成幽靈端點——**假警報是防腐機制最快的死法**。改成探測後，盲點被一個每次執行都重新驗證的斷言吸收，不需要人工白名單。 |
| `go.mod` 的 yaml.v3 提升 | **手動移行，不跑 `go mod tidy`** | `go mod tidy` 順帶從 `go.sum` 移除了 28 行與本變更無關的陳舊間接相依。那是合法的清理，但它讓一個「零行為變更」的 diff 多出一段與主題無關的噪音。手動把 require 行移到 direct 區塊達成同樣效果，`go.sum` 逐位元組不變（`go build ./...` 與全部測試已驗證）。 |
| 金額欄位允許 `allOf` 單元素包裝 | 檢查 2 接受 `{$ref: MinorUnitAmount}` 與 `{allOf: [{$ref: …}], description: …}` 兩種 | OpenAPI 的 `$ref` 兄弟鍵在 3.1 下語意曖昧，附加 per-field 描述的慣用寫法就是單元素 `allOf`。不接受它會逼所有金額欄位放棄自己的描述——而描述正是這份文件的價值所在。 |
| `ShippingAddress` 拆成兩個 schema | 新增 `ShippingAddressInput`（請求，必填 3 個）與保留 `ShippingAddress`（回應，全鍵必存在） | 同一個 schema 沒辦法同時正確描述兩端：請求端 `city`/`postal_code`/`country` 選填，回應端它們一律存在（空字串）。**由 `npx @redocly/cli lint` 抓到**——本變更自己的四道檢查只驗回應，驗不到請求 schema 的缺陷。 |
| `MemberCheckoutRequest` 由 `allOf` 改為平鋪 | 把地址欄位寫開 | `allOf` ＋ `additionalProperties: false` 是**永遠驗不過**的組合：每個子 schema 只看得見自己的 `properties`，於是任何一個鍵都會被另一個子 schema 判為多餘。同樣由 redocly lint 抓到。這個缺陷會讓 Prism mock 拒絕每一個合法的會員結帳請求——直接打到 ONDO 的離線開發。 |
| 移除 `components/responses/InternalError` | 刪除 | design D8 明訂 500 不逐端點列出，於是這個 component 永遠不會被引用。redocly 的 `no-unused-components` 抓到；500 的形狀由 `Error` schema 的 code 全集表描述，資訊沒有遺失。 |
| `make openapi` 的範圍 | 一併跑整合層的檢查 3（`INTEGRATION=1`） | 若只跑單元層三道，這個 target 會給人「契約已檢查完畢」的錯覺，而漏掉唯一能抓形狀漂移的那一道。 |

### 防腐機制的失敗證明（task 8 的實際輸出）

四道檢查各刻意破壞一次，記錄實際輸出，之後完整還原。

**① 檢查 1 — 文件漏記一條實作有的路由**（從 `openapi.yaml` 移除 `/api/v1/shop/shipping-methods`）

```
--- FAIL: TestOpenAPICoversEveryRoute (0.02s)
    1 條已註冊的路由不在 api/openapi/openapi.yaml 中——新增端點時必須同步更新契約：
      GET /api/v1/shop/shipping-methods
```

**② 檢查 1 — 文件寫了實作沒有的幽靈端點**（在契約中加 `GET /api/v1/shop/wishlist`）

```
--- FAIL: TestOpenAPICoversEveryRoute (0.02s)
    1 條路由寫在契約中但實作沒有——消費端照著寫會拿到 404：
      GET /api/v1/shop/wishlist (probe 404)
```

同一次執行中，兩條真正落在 Walk 盲點的路由**沒有**被誤報，而是照設計走了探測並通過：

```
    chi.Walk 未列出但實測可達的路由（design D3a）：
      GET /api/v1/admin/shops/{shopID} (probe 401)
      PUT /api/v1/admin/shops/{shopID} (probe 401)
```

**③ 檢查 2 — 金額欄位就地展開，單位聲明消失**（`PublicShippingMethod.flat_rate` 改為 `type: integer`）

```
--- FAIL: TestOpenAPIMoneyFieldsDeclareUnit (0.04s)
    1 個金額欄位沒有 $ref '#/components/schemas/MinorUnitAmount'——單位必須只在一個地方聲明，
    就地展開會讓「差 100 倍」這種錯誤靜默通過：
      /components/schemas/PublicShippingMethod/properties/flat_rate
```

**④ 檢查 4 ＋ 檢查 3 — 從 `Order` 移除 `shipping_fee`**（模擬有人靜默拿掉一個 v1 公開鍵）

檢查 4（單元層，免基礎設施）：

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.02s)
    1 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。
    若確實要移除，必須同時從 ../../openapi/v1-surface.txt 手動刪掉對應的行
    （該刪除會出現在 code review 的 diff 中）：
      Order.shipping_fee
```

檢查 3（整合層）同時在**四條**回傳訂單的端點上轉紅，且指出了確切位置：

```
--- FAIL: TestOpenAPIResponsesMatchImplementation (1.08s)
    POST /api/v1/shop/guest/checkout -> 201 的回應不符合契約：
      - at '/order': additional properties 'shipping_fee' not allowed
    GET /api/v1/shop/guest/order -> 200 …  - at '': additional properties 'shipping_fee' not allowed
    POST /api/v1/shop/checkout -> 201 …    - at '': additional properties 'shipping_fee' not allowed
    GET /api/v1/shop/orders -> 200 …       - at '/orders/0': additional properties 'shipping_fee' not allowed
    5 個共用 schema 沒有被任何一條被驗證過的回應涵蓋——取樣清單被砍短了（design D10 規則 2）：
      GuestCheckoutResult / Order / OrderItem / OrderPage / ShippingAddress
```

**⑤ 檢查 3 — 這一次改的是實作（最真實的漂移方向）**

前四項都是「動文件」，但真實世界的漂移是**改了 code 忘了改文件**。所以再證一次：在
`internal/httpapi/order.go` 的 `orderJSON` 加一個 `discount_amount` 鍵，**完全不動 `openapi.yaml`**：

```
--- FAIL: TestOpenAPIResponsesMatchImplementation (1.03s)
    - at '/order':    additional properties 'discount_amount' not allowed
    - at '':          additional properties 'discount_amount' not allowed
    - at '':          additional properties 'discount_amount' not allowed
    - at '/orders/0': additional properties 'discount_amount' not allowed
```

**⑥ 全部還原後**

```
--- PASS: TestOpenAPIResponsesMatchImplementation (1.13s)
--- PASS: TestOpenAPICoversEveryRoute (0.01s)
--- PASS: TestOpenAPIMoneyFieldsDeclareUnit (0.03s)
--- PASS: TestOpenAPIV1SurfaceLock (0.02s)
```

### 覆蓋數字（實測）

| 項目 | 數字 |
| --- | --- |
| 實際可達的路由 | **111**（`/api/v1` 110 ＋ `/healthz` 1） |
| `chi.Walk` 回報 | 109（差額 2 條為 design D3a 的盲點，已由可達性探測證明可達） |
| 契約記載的 operation | **111**（逐條相等） |
| 具名 component schema | 46 |
| 受檢的金額欄位 | 18（＋4 個已登記的分頁 `total` 豁免） |
| v1 表面鎖項目 | 371 |
| 以真實回應驗證的共用 schema | 21 |
| e2e 斷言 | 76（17 CMS ＋ 59 commerce） |

### Prism mock 實測（`make openapi-mock`，:4010，無 DB 無 Redis）

| 請求 | 結果 |
| --- | --- |
| `GET /api/v1/shop/shipping-methods` | 200，回契約的範例：`[{id:1, name:"台北市當日配送", carrier:"自有車隊", flat_rate:200}, {id:2, …flat_rate:0}]` |
| `GET /api/v1/shop/products` | 200，`skus[0].price_amount = 3400`（**真實量級**，量級錯誤會在畫面上一眼看穿） |
| `POST /api/v1/shop/guest/checkout`（合法） | 201，回完整訂單（`total_amount`／`subtotal_amount`／`shipping_fee` 齊備） |
| `POST /api/v1/shop/guest/checkout`（**缺 `email`**） | Prism **擋下**並回契約的錯誤 envelope（`validation_failed` ＋ `details[].pointer`）——mock 會驗證送進去的 request，不只是回假資料 |
