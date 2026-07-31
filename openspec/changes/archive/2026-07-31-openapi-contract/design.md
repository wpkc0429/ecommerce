## Context

真實可達的路由是 **111 條**（`chi.Walk` 回報 109，另 2 條落在 Walk 的盲點，見 D3a），分佈如下：

```
/api/v1/admin/**                 82   後台：平台層 28 ＋ shop 層 54（全部 RBAC 判定）
/api/v1/shop/**                  25   前台：會員 JWT 13、公開 3、訪客 1、訂單存取 token 3、會員認證 4、/me 1
/api/v1/webhooks/payments/{p}     1   金流回呼（簽章認證，無 JWT 無租戶）
/api/v1/render/**                 2   渲染 bundle ＋ 預覽
/healthz                          1
```

而 `openspec/specs/*/spec.md` 全文只出現 38 個 `/api/v1` 路徑字串。差距不是疏漏——那些檔案本來就不是 API 參考，它們回答「這個行為對不對」，不回答「這個 body 長什麼樣」。

本變更的消費端是 ONDO（`bouquet`）：

```
零 package.json、零打包、零 TypeScript      → 產 TS 型別對它是空投；它拿不到也吃不到
不同 git repo、不同部署管線 (CF Pages)      → 契約必須能離開本 repo 獨立閱讀與執行
bouquet/CLAUDE.md 已預告「後端 API 契約」    → 這份交付物是被期待的
price: 3400 (元) vs total_amount: 340000   → 差 100 倍不噴錯，只賣錯價
```

既有的紀律與限制，本設計必須全部承接：

- **錯誤 envelope 統一**（design D12，`internal/httpx`）：`{"error":{"code","message","details"}}`，7 個建構子（`BadRequest`/`Unauthorized`/`Forbidden`/`NotFound`/`Conflict`/`Unprocessable`/`Internal`）＋ `ShopDisabled`(503) ＋ `TooManyRequests`(429)。
- **四種互斥的認證機制**：admin JWT（`aud=admin`）、member JWT（`aud=shop:{id}`）、訂單存取 token（`aud=order:{shop_id}`）、金流簽章。加上 **publishable key**——它**是識別碼不是憑證**（change storefront-client-auth design D1 明文），以及租戶解析的 `Host` / `X-Site-Domain`（後者已收斂為僅 `/api/v1/render/page` 可用）。
- **D13：v1 只做 additive 變更**，三次破例窗口已於 change `order-extras` 用盡並明文關閉。
- **e2e 不在 CI 內**（change `commerce-demo-seed` 的實測發現）。這一條直接決定本設計的核心：任何放在 e2e 的防腐機制，其可信度上限就是「有人記得在本機跑」。
- 金額一律 `int64` 最小貨幣單位 ＋ `currency CHAR(3)`，禁用浮點數。

## Goals / Non-Goals

**Goals:**

- `/api/v1` 的**每一條**路由都在一份機讀契約裡，且「每一條」這件事**由測試證明**，不是由撰寫時的細心保證。
- 契約與實作不一致時，**CI 轉紅**——而不是等某個消費端在生產環境發現。
- 契約可直接餵給標準工具產 mock server，讓 ONDO 不必起 Go + PostgreSQL + Redis 就能開發。
- D13 的 additive-only 規則從「一句寫在 CLAUDE.md 的話」變成「一個必須手動刪掉一行檔案才能違反的動作」。
- 金額的「最小貨幣單位」在文件中是**一個地方**說一次，且新欄位不可能忘記聲明。
- ONDO 拿到一份完整、單一、可直接照做的接入文件，而不是三個 archive 目錄的 tasks.md 末尾。

**Non-Goals:**

- **不產 TypeScript／任何語言的 client SDK**（消費端零建置，見 Context）。
- **不做文件驅動開發**：OpenAPI 不生成 Go 程式碼、不參與 request 驗證、不在執行期被伺服器讀取。實作是權威（D11）。
- **不在 API 內建 Swagger UI / Redoc 端點**（D6）。
- **不為 `/api/v2` 預留任何結構**（尚不存在；見 Open Questions）。
- **不寫進 bouquet repo**：那是另一個 repo、另一條部署管線，不在本變更的邊界內。
- 不涵蓋 `/web`（Next.js SSR，它是 API 的消費端不是提供端）與 `/admin`（同上）的內部路由。
- 不做 API 版本協商、deprecation 標記流程、或變更日誌自動產生。

## Decisions

### D1. 手寫 OpenAPI ＋ 契約測試，而不是由程式碼註解產生

三條路都可行，先把它們的真實成本攤開：

| | 手寫 YAML | 註解產生（swaggo 等） | 由型別產生 |
| --- | --- | --- | --- |
| 前置成本 | 一次寫 109 條 | 在 109 個 handler 上補註解 | handler 的請求／回應多為 `map[string]any`，**沒有型別可以反射** |
| 新依賴 | 無（測試用的 yaml.v3 已在 `go.sum`） | 產生器 ＋ 其執行期套件 | — |
| 腐爛方式 | 改了 code 忘了改 YAML | 改了 code 忘了改**旁邊那行註解** | 不腐爛 |
| 可讀性 | 可寫敘述與範例 | 註解語法擠在 handler 上方 | 機械翻譯 |

**第三條路直接出局，而且是硬性的**：本專案的 handler 幾乎全部以 `map[string]any` 組回應（`orderJSON`、`productJSON`、`cartViewJSON`…），沒有 struct 可反射。要走型別產生就得先把 109 個端點的回應改寫成 struct——那是一個比本變更大得多、且**會改動實作**的重構。

**第二條路的關鍵誤判必須點破**：註解式產生器**看起來**貼著 code，但它防的不是腐爛。`// @Success 200 {object} OrderResponse` 是一行註解——編譯器不看它，測試不看它，`golangci-lint` 不看它。改了 `orderJSON` 而沒改註解，產生器會安靜地產出一份錯的文件，**而且它的錯法比手寫更危險**：文件緊貼原始碼會讓人相信它必然同步。它把「文件在別的檔案」這個明顯的風險，換成「文件在同一個檔案但仍然是註解」這個隱形的風險。

**因此真正的問題不是產生方式，是防腐機制是否可信。** 一旦有了 D2 的四道檢查，兩種產生方式的腐爛風險**同時歸零**——差別只剩可讀性與前置成本，而手寫在可讀性上勝出且不引入依賴。所以選手寫。

**手寫的代價，誠實記錄：** 一次性的體力活（本變更最大的單項工作量），而且 D2 的檢查只能證明「路由集合相等」「回應通過 schema」，不能證明「這段敘述寫得對」。敘述的正確性沒有機械保證，只能靠寫的時候讀 code——這是手寫路線唯一無法測試化的部分，也是為什麼 D9 要求敘述集中在少數共用 component 而非散落在 109 處。

### D2. 防腐機制：四道檢查，全部住在既有的 `go test ./...` 裡

**這是本變更的核心，OpenAPI 檔本身只是它的輸入。** 一份與實作不符的契約比沒有契約更危險——消費端會信它。

| # | 檢查 | 層級 | 擋住什麼 | **擋不住什麼** |
| --- | --- | --- | --- | --- |
| 1 | 路由覆蓋 | 單元 | 新增端點忘了寫文件；刪除端點忘了刪文件；路徑參數改名 | 路由存在但 body 形狀寫錯 |
| 2 | 金額單位紀律 | 單元 | 新增金額欄位而未聲明「最小貨幣單位」 | 消費端讀了聲明仍然誤解 |
| 3 | 真實回應結構驗證 | 整合 | 回應新增／刪除／改型別的鍵；狀態碼改變；錯誤 envelope 走樣 | 沒有被測試呼叫到的端點 |
| 4 | v1 表面鎖 | 單元 | 靜默移除一條路由或一個公開回應鍵（＝破壞 v1） | 語意層面的破壞（如 `order-extras` 的 `total_amount`） |

**為何全部放在 Go 測試層，而不是 e2e、也不是獨立的 `make` target。** 這一條有實測支撐：change `commerce-demo-seed` 發現 **e2e 不在 CI 內**，`wire.go` 的事件訂閱因此只由「有人記得跑 `commerce-path.mjs`」守著。一個防腐機制若不在 CI，它擋不住的正是它要擋的那種疏忽（改了東西沒想到要跑）。同理，新增一個 `make openapi-check` 而不接進 CI，等於把檢查降級成一份提醒。

`.github/workflows/ci.yml` 今天跑的是 `INTEGRATION=1 go test -p 1 -count=1 ./...`。把四道檢查寫成 `internal/httpapi` 的普通 Go 測試，它們**立刻**在 CI 內，**且 `ci.yml` 一個字都不用改**。這不是巧合帶來的便利，是選址的理由本身。

**檢查 1／2／4 刻意不需要基礎設施**（不碰 DB、不碰 Redis），所以 `make test` 就會跑到它們——最常被開發者手動執行的那個指令。只有檢查 3 需要真實請求，落在 `make test-int`。

**四道檢查的證明義務。** 一個沒被證明會失敗的測試等於沒有測試。本變更的驗收條件（tasks 8）明文要求：對每一道檢查**刻意製造一次不一致**，記錄它轉紅的實際輸出。

### D3. 路由覆蓋：`chi.Walk` 對真實 router，雙向逐條比對

```go
h := httpapi.New(Deps{ /* 每個 handler 都給零值指標，四個 MW 給 passthrough */ })
mux := h.(chi.Routes)
chi.Walk(mux, func(method, route string, ...) error { ... })
```

**為何這能成立而且很便宜。** `httpapi.New` 的路由掛載只看 `Deps` 的欄位是否為 nil；`RequireShopPermission` 之類的守衛在掛載時只回傳 closure，不解參照 `h.Authz`。所以零值 handler 指標就能組出**與生產環境逐條相同**的路由表，不需要 DB、不需要 Redis、不需要 config。而 chi 的路徑樣板語法（`{shopID}`）與 OpenAPI 的 path template **逐字相同**，比對不需要任何轉換規則。

**雙向，而且是集合相等而非包含。** 只檢查「文件裡的路徑都存在」會放過漏記；只檢查「路由都被記載」會放過幽靈端點（文件寫了但實作已刪——消費端會照著寫然後拿到 404）。兩個方向都必須報錯，錯誤訊息各自列出缺哪幾條。

**已評估並否決：從 `router.go` 原始碼做靜態分析。** 那要重新實作一遍 chi 的路由組合語意（`Route`／`Group`／`With`／`Mount` 的巢狀前綴），而且會漏掉本專案大量使用的 `d.X.MountShop(sh)` 這種跨檔案間接掛載。`chi.Walk` 走的是**組好之後的真實 trie**——但它並非完全無死角，見 D3a。

**已知邊界，明寫。** 覆蓋測試證明的是「路徑 ＋ 方法」的集合相等，**不是**「每個端點的 middleware 鏈與文件宣告的 security 一致」。chi.Walk 的 `middlewares ...func(http.Handler) http.Handler` 參數拿得到 closure，但 closure 無法反射出它守的是哪個權限節點。要機械化驗證授權宣告，唯一誠實的做法是對每個端點實際發一次無憑證請求並斷言 401/403——那屬於檢查 3 的範圍，而檢查 3 只覆蓋被測試呼叫到的端點。因此「文件寫的 RBAC 權限節點」目前是**人工維護的敘述**，這一點必須在 OpenAPI 的 `info.description` 裡對讀者說清楚，而不是假裝它被驗證了。

### D3a. `chi.Walk` 有一個盲點——實測發現的，處置方式是「未被 Walk 到的必須被實際探測證明可達」

**實測結果（本變更在實作前的第一項查證）：** `chi.Walk` 對真實 router 回報 **109** 條，但實際可達的是 **111** 條。少掉的兩條是：

```
GET  /api/v1/admin/shops/{shopID}
PUT  /api/v1/admin/shops/{shopID}
```

原因在 `router.go`：`pr.Route("/shops/{shopID}", …)` 在該樣板上掛了一個子 router，而 `Shops.MountPlatform(pr)` **在同一個樣板上**又註冊了 `Get`／`Put`。chi 兩者都收下，執行期兩者都正常運作（已以真實請求驗證：`GET /admin/shops/7` 回到 platform handler，`GET /admin/shops/7/products` 回到子 router），**但 `Walk` 走到那個節點時會下潛進子 router，不回報該節點自身的方法處理器**。

這件事若不處理，覆蓋測試會把這兩條回報成「文件有而實作沒有的幽靈端點」——一個**假警報**，而假警報是防腐機制最快的死法（人會學會忽略它）。

**處置：把「文件有但 Walk 沒有」從例外清單改成一個被證明的斷言。** 比對規則因此不是單純的集合相等，而是：

1. **Walk 到的每一條都必須在文件中**（漏記端點 → 失敗）。這一半是嚴格的集合包含，沒有任何例外。
2. **文件有而 Walk 沒有的每一條，必須通過「可達性探測」**：把路徑參數代入合法值，對同一個 router 實際發一次請求，斷言狀態碼**不是 404、也不是 405**（`ServeHTTP` 走到了某個處理器）。通不過的就是真正的幽靈端點 → 失敗。

於是 chi 的盲點不需要任何人工維護的白名單：它被一個**每次執行都重新驗證**的機制吸收掉了。

**殘留的洞，誠實記錄。** 探測比精確比對弱：若文件把 `{orderID}` 誤寫成 `{id}`，代入後的 URL 仍然可達，探測會通過。但這個弱點只作用在「文件有而 Walk 沒有」這個集合上——目前恰好兩條，且兩條都在同一個已知的 chi 行為底下。真正的殘留風險是**日後有人在同一種樣板衝突下新增端點**：它既不會被 Walk 到，也不在文件中，於是兩道檢查都沉默。這一條寫進測試檔頭當作警語；要完全封死它需要繞過 `chi.Walk` 自己走 trie 的私有結構，代價與收益不成比例。

**不修 `router.go` 讓它變得可 Walk。** 唯一的修法是把這兩條移進子 router 成為 `sh.Get("/")`——那會讓路徑變成 `/admin/shops/{shopID}/`（多一個斜線），是一個 v1 的破壞性變更。為了讓一個測試工具看得見而破壞公開路徑，方向完全是錯的。

### D4. OpenAPI **3.1**，因為它的 schema 就是 JSON Schema 2020-12——所以驗證器已經在專案裡

OpenAPI 3.0 的 schema 是 JSON Schema draft-4 的一個**方言**（`nullable: true`、不支援 `examples` 陣列、`exclusiveMinimum` 是布林…）。OpenAPI **3.1 的 schema 是完整的 JSON Schema 2020-12**，沒有方言差異。

這件事的直接後果是：`github.com/santhosh-tekuri/jsonschema/v6` ——**本專案已經在用的直接相依**（`internal/cms/schemaval.go`，主題 schema 驗證）——可以原封不動地編譯 `openapi.yaml` 裡的任何 `components/schemas/*` 並驗證真實回應。**檢查 3 因此零新依賴。**

```go
c := jsonschema.NewCompiler()
c.DefaultDraft(jsonschema.Draft2020)
c.AddResource("openapi.json", doc)                     // 整份文件
sch, _ := c.Compile("openapi.json#/components/schemas/Order")
sch.Validate(actualResponseBody)
```

**YAML → JSON 的轉換不是雜訊，是必要步驟。** `yaml.v3` 解出來的數字是 Go 的 `int`/`float64`，而 `jsonschema` 期望 `json.Number`。中間過一次 `json.Marshal` → `jsonschema.UnmarshalJSON` 讓型別正規化，順帶讓「YAML 寫錯導致非字串鍵」在測試裡以明確錯誤爆炸而不是靜默行為。

**`gopkg.in/yaml.v3` 由 indirect 提為 direct。** 它已經在 `go.sum`（atlas 的間接相依），提升只是 `go.mod` 的一行歸類變動，**零新增下載、零新增供應鏈面**。這是「選 YAML 而非 JSON 當手寫格式」唯一的成本，而 YAML 換到的是多行敘述、註解、與可讀的縮排——對一份要給跨 repo 的人閱讀的文件，這個交換明顯划算。

**已評估並否決：改用 3.0 以擴大工具相容性。** 3.1 已是 2021 年的標準，Prism、Redocly、Postman、Stoplight 全部支援。用 3.0 換到的相容性是假的（要付出 `nullable` 方言的代價），而失去的是「驗證器已經在專案裡」這個決定性優勢。

### D5. v1 表面鎖：把 additive-only 從一句話變成一個必須手動執行的刪除動作

`api/openapi/v1-surface.txt` 是一份排序過的純文字清單，每行一個 v1 的公開承諾：

```
GET /api/v1/shop/products
GET /api/v1/shop/shipping-methods
Order.total_amount
Order.subtotal_amount
PublicShippingMethod.flat_rate
...
```

測試只做一件事：**鎖檔的每一行今天仍存在於 `openapi.yaml` 中**。新增端點或欄位不會轉紅（additive 自由）；移除任何一條，測試立刻轉紅，且修復它的唯一方式是**手動刪掉鎖檔裡的那一行**——那一行的旁邊，檔頭寫著「刪除此檔任一行即為破壞 v1；D13 的破例窗口已於 change order-extras 關閉」。

**為何是 append-only 鎖檔而不是 `git diff` 比對。** `git show HEAD:api/openapi/openapi.yaml` 這類做法在 CI 的 shallow clone、在 rebase、在首次提交都會出各種狀況，而且它比對的是「上一個 commit」，不是「v1 承諾過什麼」——把一個永久性的承諾綁在一個滑動的參考點上是錯的。鎖檔是承諾本身的物化，與 git 歷史無關。

**這個機制的既有先例。** `.github/workflows/ci.yml` 的 web job 已經在用同一招：`npm run gen:types` 之後 `git diff --exit-code themes/starter/schema-types.ts`——讓一個「應該同步」的檔案在不同步時轉紅。鎖檔是它的變體，差別在方向：那個要求完全相等，這個只要求不減少。

**誠實的邊界（重要）。** 鎖檔擋的是**形狀**的破壞，擋不住**語意**的破壞。change `order-extras` 把 `orders.total_amount` 從「品項小計」改成「應付總額」時，鍵名與型別逐字未動——鎖檔會全綠。這不是可以修的缺陷，是「語意」本來就無法從一份結構描述裡機械推導。真正的防線是 D13 的規則本身與 code review；鎖檔負責的是那些**確實可以機械化**的部分，並且把它們百分之百擋住。宣稱它擋得住語意破壞會是一個危險的誇大。

### D6. 單檔、不內建 UI、mock 與 UI 走 `npx`

**單檔 `api/openapi/openapi.yaml`。** 拆多檔要付出的代價是 `$ref` 的跨檔解析——測試端要餵多個 resource 給 compiler、mock server 要能解、下載文件的人要拿到一整包而不是一個檔。換到的只是「單檔比較大」這個純美學問題，而本文件的絕大部分是 `$ref` 到 components，不是重複的字面內容。**ONDO 需要的是一個可以貼進 Prism 的檔案路徑，不是一個目錄樹。**

**不在 API 內建 Swagger UI / Redoc 端點。** 三個理由，由弱到強：(1) 這個 API 是 headless 的，內嵌 HTML 資產與它的定位不合；(2) 內建 UI 要嘛從 CDN 載入（多一個執行期外部相依，離線即壞）要嘛把數 MB 的 JS 提進 repo；(3) **決定性的**——一個 `/api/v1/docs` 端點本身就是一條路由，它會被 D3 的覆蓋測試抓到，於是必須寫進 OpenAPI 裡，於是文件開始描述文件自己。這個遞迴不是不能解，但它是憑空發明的問題。

**改為兩個 `make` target，用 `npx` 執行，不進任何相依清單：**

```
make openapi-mock   npx @stoplight/prism-cli mock api/openapi/openapi.yaml    → :4010
make openapi-ui     npx @redocly/cli preview-docs api/openapi/openapi.yaml    → :8081
```

兩者都是**開發時選用**，不在 CI、不在建置、不在 `go.mod`、不在任何 `package.json`。ONDO 的離線開發需求由 `openapi-mock` 完整滿足：Prism 依 schema 與 `example` 產回應，且會驗證送進去的 request。

**`make openapi` 則是四道檢查的單一入口**（`go test -run 'OpenAPI|Contract|V1Surface' ./internal/httpapi/`），純粹是給人手動用的捷徑；CI 的保證來自 `go test ./...`，不來自這個 target。

### D7. publishable key **不進 `securitySchemes`**——它是租戶宣告，不是憑證

OpenAPI 的 `securityScheme: apiKey` 在語法上完全裝得下 `X-Publishable-Key`，而這正是問題所在。姊妹 change `storefront-client-auth` 的 design D1 明文：**publishable key 是公開識別碼不是憑證**——明文儲存、可完整顯示、MUST NOT 參與任何授權判定。把它列進 `securitySchemes` 會產生兩個具體傷害：

1. 每個讀文件的人（以及每個 codegen、每個 Postman 匯入）都會看到「Security」區塊裡有它，於是把它當成要保密的東西——然後開始問「這個 key 洩漏了怎麼辦」，而正確答案是「它本來就是公開的」。
2. 它會被誤解成「帶了這個 key 就有權限」。實際上它只回答「你代表哪一家商家」；帶著它去打會員端點仍然是 401。

**因此的表達方式：** `X-Publishable-Key` 是一個 header **parameter**（透過 `components/parameters/PublishableKey` 共用），描述寫死「識別碼，非憑證；決定租戶，不決定授權；無效即 404 且不回退」。四個真正的 `securitySchemes` 是：

| scheme | 型別 | 誰用 | 失敗 |
| --- | --- | --- | --- |
| `adminJWT` | http bearer JWT，`aud=admin` | `/api/v1/admin/**` | 401；權限不足 403 |
| `memberJWT` | http bearer JWT，`aud=shop:{id}` | `/api/v1/shop/**` 的會員區 | 401（含跨店重用） |
| `orderAccessToken` | http bearer JWT，`aud=order:{shop_id}` | `/api/v1/shop/guest/order*` | 401 |
| `providerSignature` | apiKey / header（provider 自訂） | `/api/v1/webhooks/payments/{provider}` | 401 |

`orderAccessToken` 的描述必須明講它**不是 session**：只能存取它指名的那一筆訂單、路由不接受路徑上的訂單 id、不能列訂單歷史、不能取消、不能刷新。這是 ONDO 最容易誤用的一項。

**`Host` 與 `X-Site-Domain`。** `Host` 是 HTTP 保留頭，OpenAPI 的 parameter 不得描述它（規範明文排除）——改以 `servers` 的變數與 `info.description` 的租戶解析章節說明：`/api/v1/shop/**` 的規則是「`X-Publishable-Key` 優先，否則 `Host`；金鑰無效即 404 不回退」。`X-Site-Domain` 只掛在 `/api/v1/render/page` 一個端點上，並在該處註明它是第一方 SSR 中繼訪客 Host 用的，回傳為已發佈公開內容。

### D8. 錯誤 envelope 表為共用 responses，且**只列呼叫端需要分辨的狀態碼**

`components/responses/` 定義一組共用回應（`BadRequest` / `Unauthorized` / `Forbidden` / `NotFound` / `Conflict` / `ValidationFailed` / `TooManyRequests` / `ShopDisabled` / `InternalError`），全部 `$ref` 同一個 `Error` schema。每個端點以 `$ref` 引用，**不重複展開**。

**刻意不做的事：在每個端點機械地列出 500 與 429。** 每一條路由理論上都可能 500，把它寫 109 次不會讓任何人多知道一件事，只會把真正有意義的錯誤（例如「422 且 details 指向 `/shipping_method_id`」）淹沒。作法是：`info.description` 統一聲明「任何端點都可能回 500（`internal`）；掛了速率限制的端點可能回 429（`rate_limited`，附 `Retry-After`）」，端點層只列**呼叫端需要分辨並採取不同動作**的狀態碼。

`ValidationFailed`(422) 的 `details` 是 `[{pointer, message}]` 的 JSON Pointer 陣列——這是 ONDO 最需要的東西（「哪個欄位錯了」），所以它的 schema 要有具體 example，且 `docs/storefront-integration.md` 有一張 pointer → 意義的對照表。

**已評估並否決：為每個端點列舉它可能產生的 `error.code` 字面值。** 誘人但不可維護：`code` 是 handler 裡的字串常數，散落在 9 個 `write*Error` 函式與各處 `httpx.WriteError` 呼叫，沒有集中列舉，也沒有任何機械方式驗證清單完整。列一份**必然會漏且無法被檢查 3 證偽**的清單，比不列更糟。改為在 `Error` schema 的描述裡列出**全部**已知 code 及其意義（一處、可維護），端點層只承諾狀態碼。

### D9. 金額：一個 `MinorUnitAmount` component，＋ 一道機械化的紀律檢查

這一節直接回應 Context 裡的 100 倍地雷。

```yaml
MinorUnitAmount:
  type: integer
  format: int64
  minimum: 0
  description: |
    最小貨幣單位的整數金額，永不為浮點數。幣別見同層的 currency（ISO 4217）。
    TWD 的最小貨幣單位是「元」→ 3400 表示 NT$3,400。
    JPY 同理。USD 的最小貨幣單位是「分」→ 340000 表示 US$3,400.00。
  examples: [340000]
```

**檢查 2（機械化的部分）：** 測試走遍整份文件的 schema 樹，凡是屬性名匹配金額樣式（`*_amount`、`*_fee`、`*_rate`、`price*`、`line_total`、`subtotal`、`total`、`amount`）者，**MUST 是 `$ref: '#/components/schemas/MinorUnitAmount'`**，不得就地展開成 `type: integer`。於是「新增一個金額欄位而忘了聲明單位」在單元測試層就轉紅，而單位的敘述只需要維護一份。

**誠實：契約測試擋得住什麼、擋不住什麼。** 它擋住的是**文件端**的漂移——金額欄位漏聲明單位、或某處偷偷變成字串／浮點數。它**擋不住**消費端讀了正確的聲明仍然把 340000 當成 3400 元——那發生在 bouquet 的 JS 裡，本 repo 的測試到不了那裡。對那一段，本變更能做的是兩件具體的事，兩件都做：

1. **mock server 供應真實量級的值**（`example` 一律用 demo seed 的真實數量級，例如 `3200`–`4400` 的花藝定價與 `340000` 的說明用例）。ONDO 對著 Prism 開發時看到的就是真實數字，量級錯誤會在畫面上一眼看穿——這比任何文字警告都有效。
2. **`docs/storefront-integration.md` 開篇第一段就是這件事**，不是附錄的一行小字。

### D10. 檢查 3 的取樣策略：**覆蓋 ONDO 的整條路徑，其餘取代表**

檢查 3 要對真實 router 發真實請求並驗證回應。全部 109 條都做一次是**不切實際且不誠實**的：多數後台端點需要先建出一整套前置資料（角色、權限、主題、頁型…），寫出來的會是一組脆弱且與既有整合測試高度重複的測試。

**取樣原則，明寫在測試檔頭：**

- **ONDO 會走的每一條路徑都必須有**：`GET /shop/products`、`GET /shop/products/{slug}`、`GET /shop/shipping-methods`、`POST /shop/guest/checkout`、`GET /shop/guest/order`、`POST /shop/guest/order/payments`、`GET /shop/guest/order/shipment`。這是本檢查的第一責任。
- **每一個共用 schema 至少被驗證一次**：`Order`（含 `items[]`）、`Cart`、`Product`、`PublicShippingMethod`、`Payment`、`Shipment`、`MemberPoints`、`TokenResponse`、`Error`。共用 schema 是 109 條路由的實際交集，驗證它們的槓桿遠高於逐條驗證端點。
- **錯誤 envelope 至少驗證 401／404／422 各一次**（422 必須連 `details[].pointer` 一起驗）。

**已評估並否決：把驗證掛進既有的每一支整合測試（例如包一層 assert helper）。** 那會讓 20 幾個既有測試檔全部改動，且把「契約驗證」這個關注點灑進與它無關的測試裡。獨立一支測試檔、自己建最小前置資料，關注點清楚，壞掉時看得懂是什麼壞了。

### D11. 不一致時**實作是權威**，文件是被測試守著的鏡子

必須明確表態，否則第一次紅燈時就會有人問「該改哪邊」。

**答案：改文件——除非測試揭露的是實作的 bug。** OpenAPI 不參與執行期任何判定；它不驗證 request、不生成 handler、伺服器不讀它。它的角色是一面**被測試守著的鏡子**。

**為何不做反向（文件驅動 / spec-first）。** 讓 OpenAPI 成為 request 驗證的來源需要在每個 handler 前插入一層 schema 驗證，那會與 `internal/httpx` 的 422 details 契約打架（誰產生 pointer？），並在熱路徑上加一次 JSON Schema 驗證。用執行期成本與一個第二權威，換不到本變更要的東西——本變更要的是「不一致會被發現」，而不是「不一致不可能發生」。

**唯一的例外，記為紀律：** 若檢查 3 揭露的是實作違反了 `openspec/specs/` 的行為契約（不是文件寫錯），那要修的是實作。判斷順序是 `openspec/specs/` > 實作 > `openapi.yaml`。

### D12. ONDO 接入說明放 `docs/storefront-integration.md`，內容以路徑組織而非欄位窮舉

**為何要搬。** 目前資訊在三個 archive 目錄的 `tasks.md` 末尾（`storefront-client-auth`、`guest-checkout`、`order-extras`），每份只講自己那塊。archive 目錄的性質是**歷史紀錄**——它記載「當時決定了什麼」，不該被當成活文件維護。把接入說明留在那裡，等於要求 ONDO 的人讀三份歷史文件並自行拼接。

**為何不叫 `ondo-integration.md`。** 這份文件描述的是「自帶前端如何接入本平台」，ONDO 只是第一個讀者。用消費者的名字命名會讓第二個商家以為它不適用，或讓人為第二個商家再抄一份。

**組織方式：依 ONDO 的實際流程**（取得金鑰與 origin → 列商品 → 取運送方式 → 訪客結帳 → 付款 → 查單 → 之後註冊認領身分），每一步給可貼上就跑的 `fetch` 片段。**開篇第一段是金額單位**（D9）。末尾一張錯誤對照表（狀態碼 ＋ `error.code` ＋ `details[].pointer` → 意義）。

**刻意不寫進去的東西：** 完整欄位表。那是 `openapi.yaml` 的工作，重複一遍只會製造第二個會腐爛的地方——而這份 markdown **不在任何測試的覆蓋範圍內**。這個限制要在文件開頭對讀者說明：欄位以 `openapi.yaml` 為準，本文件負責「怎麼串起來」。

### D13. README 的範圍：只寫「這是什麼、怎麼跑、文件在哪」

整個 repo 目前零 README。要避免的失敗模式是寫一份與 `CLAUDE.md`（10 KB，架構註記）和 `openspec/project.md`（專案脈絡）三份重複的東西——那保證會出現三份互相矛盾的敘述。

**README 只負責 `CLAUDE.md` 不負責的事**：一個外部的人第一次打開這個 repo 需要的最短路徑（monorepo 有哪四個東西、`make up` → `make migrate` → `make seed-demo` → `make dev`、以及「API 契約在 `api/openapi/openapi.yaml`、接入說明在 `docs/`、架構決策在 `openspec/`」三個指路）。**其餘一律連結過去，不複製。**

### D14. 回應 schema 一律 `additionalProperties: false`——它是檢查 3 的力量來源，代價要對消費端說清楚

檢查 3 之所以能抓到「實作多回了一個沒記載的鍵」，唯一的原因就是回應 schema 標了 `additionalProperties: false`。拿掉它，檢查 3 就只剩「必要鍵存在且型別對」——**新增回應鍵而不更新文件的漂移會完全靜默**，而那正是最常見的漂移形態（新增比刪除頻繁得多）。

代價是它會對讀者說一句過強的話：「回應只會有這些鍵」。而 v1 的規則明確允許**新增**回應鍵，所以一個把本文件釘住並嚴格驗證的消費端，會在下一次 additive 變更時炸掉。

**處置：兩件事同時做，不擇一。**

1. 保留 `additionalProperties: false`（檢查 3 的力量不能放棄）。
2. 在 `info.description` 的「版本策略」段落**明文**告訴消費端：本文件描述的是「截至本版本，回應有這些鍵」，**不是**「未來永遠只會有這些鍵」；消費端 MUST 忽略未知的回應鍵。

**已評估並否決：只在測試裡動態加上 `additionalProperties: false`，發佈的文件不帶。** 那會讓「被測試驗證的東西」與「消費端讀到的東西」變成兩份不同的 schema——本變更整個立足點就是這兩者是同一份。用一句可以寫清楚的話（作法 2）換掉這個分裂，划算得多。

## Risks / Trade-offs

- **[手寫 109 條，敘述可能寫錯而測試不會發現]** → 這是手寫路線無法測試化的部分（D1 誠實記錄）。緩解有三：敘述集中在少數共用 component（改一處而非 109 處）；檢查 3 讓每個共用 schema 的**結構**被真實回應驗證；ONDO 是真實讀者，讀錯會回報。**不宣稱敘述正確性有機械保證。**

- **[檢查 3 只覆蓋取樣到的端點，未取樣的端點形狀漂移不會被發現]** → 已明寫取樣原則（D10）並保證 ONDO 路徑 100% 覆蓋、每個共用 schema 至少一次。真正的緩解是共用 schema 的槓桿：`Order` 被 6 條路由回傳，驗一次就守住六處。**未取樣的後台端點確實是缺口，記為已知限制而非假裝覆蓋。**

- **[鎖檔擋不住語意破壞（`total_amount` 型的變更）]** → D5 已正面記錄。防線是 D13 規則本身 ＋ code review；鎖檔負責可機械化的部分並百分之百擋住。**明確不宣稱它能擋語意。**

- **[新增端點的 change 現在多一道必做的功課]** → 這是本變更**刻意加上**的成本，也是它的全部意義。緩解：路由覆蓋測試的失敗訊息直接列出「缺哪幾條路徑」，把它從「猜哪裡不對」變成「照著補」。

- **[`npx` 的 mock/UI 需要網路與 Node]** → 兩者都是**選用**，不在 CI、不在建置、不影響任何測試。`openapi.yaml` 本身是純文字，離線可讀；離線又要 mock 的人可以 `npm i -g @stoplight/prism-cli` 一次。相較之下把 Prism 或 Redoc 提進 repo 會讓一個 headless Go API 長出 node_modules，代價大得多。

- **[OpenAPI 的 RBAC 權限節點敘述是人工維護的，沒有機械驗證]** → D3 已明寫此邊界，並要求在 `info.description` 對讀者聲明，而不是讓讀者以為它被驗證過。要驗證它需要對 109 個端點各發一次無憑證／低權限請求，成本與收益不成比例（後台消費者是第一方 admin SPA，它壞了會立刻被 e2e 的 CMS 流程抓到）。

- **[單檔 YAML 會長到數千行]** → 已知且接受（D6）。緩解是結構紀律：`paths` 幾乎全是 `$ref`，實體內容集中在 `components`。若日後真的難以維護，拆檔是純機械的重構，且檢查 1／2／4 會在拆檔過程中守住等價性——**現在拆是為想像中的問題付現金**。

## Open Questions

- **`/api/v2` 出現時，是同一份文件加 `servers`／`paths` 前綴，還是第二份 `openapi-v2.yaml`？** 現在沒有 v2，也沒有任何要做 v2 的計畫；兩種做法對今天的檔案結構沒有任何影響（v1 的內容一個字都不會因為選哪個而不同）。此刻決定等於憑空猜測 v2 的形狀，故明確留白。唯一先行的約束：**v1 的鎖檔（`v1-surface.txt`）不因 v2 出現而失效**——v1 只要還在服務，它的承諾就還在。

- **後台端點的授權宣告要不要機械化驗證？** D3 已論證目前不做並記錄了邊界。真正會改變這個判斷的觸發條件是「出現第一個第一方以外的後台消費端」（例如商家自建的營運後台）——屆時 RBAC 敘述從「內部備忘」變成「對外承諾」，值得為它付出 109 次無憑證請求的測試成本。在那之前不做。
