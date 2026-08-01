## Context

`api/internal/app/wire.go` 是全系統唯一的真實接線處。它做三件事，三件都是「不接也能編譯、不接也全綠」的事：

```
deps.X = &httpapi.XHandler{...}          ← 決定 router.go 掛不掛 X 的那一組路由
handler.Dep = someService                ← 決定該 handler 的某個功能是否作用（如 Idempotency）
dispatcher.Subscribe(svc.Handle)         ← 決定跨套件事件鏈成不成立
```

而測試碼裡至少有八處在鏡像它，其中兩處**宣稱完整**：

| 位置 | 鏡像什麼 | 宣稱完整？ | 現況 |
| --- | --- | --- | --- |
| `internal/httpapi/openapi_contract_test.go` 的 `contractRouter()` | `Deps` 的全部 handler 欄位 | **是**——檢查 ① 的路由來源 | **已實測失效** |
| `wire.go` 的 `dispatcher.Subscribe` 四行 | 事件訂閱集合 | **是**——事件鏈成立與否 | **零 Go 覆蓋** |
| `points_integration_test.go:135`、`guest_integration_test.go:98`、`clientauth_integration_test.go:33`、`cms_integration_test.go:72`、`shops_integration_test.go:84`、`tenancy_integration_test.go:72`、`openapi_conformance_integration_test.go:298`、`payment/service_integration_test.go:319` | dispatcher／service 的**子集** | 否 | 正常的 fixture |

**鏡像本身不是罪，宣稱完整才是。** 一支測試自建它需要的那幾個訂閱者，測的是「如果接對了，行為對不對」——那是它的正當工作。問題出在沒有任何東西回答另一半：**「有沒有接」**。

兩項實測（皆為本變更動工前執行）：

```
① 新增 handler + 兩條路由，不碰 contractRouter()：
   --- PASS: TestOpenAPICoversEveryRoute   契約覆蓋 114 條路由（chi.Walk 列出 112 條 + Walk 盲點 2 條）
   --- PASS: TestOpenAPIMoneyFieldsDeclareUnit
   --- PASS: TestOpenAPIV1SurfaceLock
   → 兩條零文件的公開路由，三道檢查全綠。

② 刪掉 dispatcher.Subscribe(pointsService.Handle)（change commerce-demo-seed 已實測並記錄）：
   make test-int 全綠，真實伺服器靜默不發點數。
```

既有約束，本設計必須全部承接：

- **D13 的 v1 破例窗口已關閉**。本變更 MUST NOT 改變任何 API 行為；`openapi.yaml` 與 `v1-surface.txt` 不得變動。
- **既有四道檢查不得被弱化**。重構後它們必須仍然擋得住它們原本擋得住的每一種破壞。
- **e2e 不在 CI 內**（`commerce-demo-seed` 實測、`openapi-contract` design D2 引用）。任何放在 e2e 的防線，其可信度上限是「有人記得跑」。
- **CI 執行 `INTEGRATION=1 go test -p 1 -count=1 ./...`**——普通 Go 測試（含整合測試）自動在 CI 內，`ci.yml` 不需要改。
- `chi.Walk` 的已知盲點（`openapi-contract` design D3a）與其「可達性探測」處置必須原樣保留。

## Goals / Non-Goals

**Goals:**

- **新增一個 handler 卻忘了接線時，一定有東西變紅。** 這是唯一的驗收標準，且必須以實際製造該疏漏來證明。
- 讓檢查 ① 的路由來源**不可能**因為「少寫一行」而缺漏——把那一行本身刪掉，而不是要求人記得寫。
- 讓「wire.go 的哪一個欄位沒接」在 CI 內被指名回報，而不是表現成一條 404 或一個靜默失效的功能。
- 讓「誰訂閱了誰」從一句註解變成一項被機械比對的事實，且**新增訂閱與移除訂閱同樣轉紅**。
- 全程不改變任何執行期行為。

**Non-Goals:**

- **不把 e2e 納入 CI**（`commerce-demo-seed` Open Questions 已界定那是獨立的一個 change：需要 CI 起三個服務 ＋ 決定資料庫生命週期）。本變更做的是把**事件鏈是否接上**這一項從 e2e 移進 CI，不是搬移 e2e。
- **不驗證事件處理器的行為正確性**。「付款後發幾點」是 `points_integration_test.go` 與 `commerce-path.mjs` 的工作，本變更只回答「有沒有接」。
- **不重寫既有的子集 fixture**（那八處鏡像中的六處）。它們是正當的測試裝配，把它們改成共用一份完整接線會讓每支測試背上與它無關的相依。
- **不機械驗證授權宣告**（`openapi-contract` design D3 已界定的邊界，觸發條件未變）。
- **不動 `openapi.yaml`、不動任何 handler／service、不新增 migration、不改 `ci.yml`。**
- 不為 `wire.go` 引入依賴注入框架或程式碼產生器。

## Decisions

### D1. 修法是**把那份清單刪掉**，不是把它記得更牢

面對「有人忘了往 `contractRouter()` 加一行」，三條路：

| | 做法 | 「忘了」還會發生嗎 |
| --- | --- | --- |
| A | 在 `Deps` 與 `contractRouter()` 上補註解／checklist／PR 模板 | **會**——這正是 `openapi-contract` design D1 已經論證過的失敗模式（註解不被編譯器、測試、linter 看見） |
| B | 寫一道測試斷言「`contractRouter()` 提到了每一個 `Deps` 欄位」 | 不會，但它要求那份清單繼續存在，只是多一道守衛 |
| C | **讓 `contractRouter()` 由 `Deps` 的結構自己長出來**（反射逐欄位填滿） | **問題消失**——沒有清單可以忘記 |

選 C。B 與 C 的紅燈時機相同，但 C 之後「新增 handler」這個動作在契約檢查這一側**不需要任何人做任何事**：欄位加進 `Deps`、路由掛進 `router.go`，檢查 ① 立刻看見它並要求文件。B 則保留了一份必須跟著改的清單，只是改錯時會被抓到——那仍然是每一次新增都要付的稅。

**這一節的一般化教訓，明寫下來：** 當防腐機制的失效模式是「有人忘了同步一份清單」時，正確的修法優先序是「刪掉清單 > 守衛清單 > 提醒人」。本專案已經有一個同型的正確答案可以對照——`v1-surface.txt` 是**產生**的（`-openapi.update-surface`），不是手抄的。

### D2. 反射填充的規則表，以及「填不出來就硬失敗」

`httpapi.New` 只對 `Deps` 的欄位做 nil 判定（見 D3），所以零值指標就能組出與生產環境逐條相同的路由表——這是 `openapi-contract` design D3 已經論證並實測過的性質，本變更沿用它，只是不再逐欄位手寫。

填充規則，依序套用：

| 順序 | 欄位型別 | 填法 | 理由 |
| --- | --- | --- | --- |
| 1 | 登記在**明示覆寫表**中的欄位 | 用登記的值 | 見下 |
| 2 | 指標（`*T`） | `reflect.New(T)` | 零值 handler 指標足以掛路由 |
| 3 | `func(T) T` | **恆等函式** | middleware 欄位；回傳零值會讓 `chi.Use` 掛上 nil handler，請求時 panic |
| 4 | 其他 func | 回傳各回傳值的零值 | `Health func() error` → nil error |
| 5 | **其餘一切（介面、map、slice…）** | **`t.Fatalf`，指名該欄位與其型別** | ← 本節的重點 |

**規則 5 是這個機制不會重蹈覆轍的原因。** 反射能填的型別，填了就對；填不了的型別若「靜默留 nil」，就等於把今天修掉的洞在明天以另一種型別重新打開——某人新增一個介面型別的 gate 欄位，路由再一次無聲消失。所以填不出來 MUST 失敗，並要求開發者到覆寫表登記一個值與**一句理由**。

今天覆寫表只有兩項：

- `Log *slog.Logger` — 零值 `slog.Logger` 的 handler 是 nil，而檢查 ① 的可達性探測**會真的發請求**，第一次 `Log.Info` 就 panic。填一個丟棄輸出的真 logger。
- `CORSOrigins OriginRegistry` — 介面（規則 5 命中）。Go 無法在執行期生出一個實作，填一個永遠回 false 的 stub。它不影響路由表（只決定 CORS middleware 掛不掛），但**留 nil 就會讓「所有欄位皆非 nil」這句話出現例外**，而 D3 的整個論證正建立在那句話上。

**已評估並否決：讓填充器跳過它填不了的型別。** 那會讓失敗模式從「編譯期／測試期硬錯」退化成「靜默少填」，也就是本變更要修的那個東西的精確複製品。

### D3. 為什麼「每個欄位皆非 nil」就等於「最大路由表」——以及這個論證需要一道機械守衛

`httpapi.New` 的每一個掛載判定都是**且只是**一次 nil 判定（`if d.Orders != nil`、`if d.AdminMW != nil`、…）。`.With(orPassthrough(d.XRateLimit))` 不改變路徑。因此：

> 兩個 `Deps` 若每個欄位皆非 nil，`httpapi.New` 對它們產生的路由表**逐條相同**。

這句話讓檢查 ① 與生產環境的等價性成為**結構性事實**而不是巧合：檢查 ⑤ 證明生產側全非 nil，檢查 ① 的填充器保證測試側全非 nil，於是兩張路由表相同。

**但這個論證有一個前提會過期**：只要有人寫下第一個非 nil 判定的掛載條件（`if d.Cfg.Env != "production"`、`if len(d.Providers) > 0`…），它就不再成立，而且**不成立的那一刻沒有任何東西會說話**。所以本設計不接受純論證，另立**檢查 ⑥**：以 `app.New` 組出真正的生產 router，逐條比對它與檢查 ① 所走的 router。論證負責解釋為什麼它們該相同，檢查 ⑥ 負責在它們不同的那一天轉紅。

**檢查 ⑥ 走的是 `app.New` 而不是 `wire`**，因為 `New` 才是 `cmd/server` 實際呼叫的東西（`deps.Cfg`／`deps.Log` 由它設定，`DatabaseURL` 為空時它整段跳過 `wire`）。比對的參照物則由同一支反射填充器產生——**兩側都沒有手寫清單**。

### D4. 生產側：跑真正的 `wire`，遞迴稽核 nil；零豁免（已實測）

檢查 ⑤ 在 `package app` 的整合測試內直接呼叫 `a.wire(ctx, &deps)`——**不是一個代表 wire 的東西，就是 wire 本身**——然後遞迴走訪結果：

- `Deps` 的每一個匯出欄位（指標／介面／func／map／slice）MUST 非 nil；
- 每一個 handler 的每一個匯出相依欄位 MUST 非 nil，再往下一層服務同理。

遞迴邊界：只進入型別位於 `ksdevworks/ecommerce/api/internal/` 之下的指標欄位，且跳過 `internal/ent`（產生碼，數十個 client 欄位）與 `internal/config`（設定值，空字串／空 slice 是合法狀態）。其餘套件的建構子（`auth.TokenIssuer`、`rbac.Engine`、`tenant.Resolver`、`render.Cache`）欄位全為未匯出，遞迴自然停止——這不是巧合，是本專案「建構子 + 未匯出狀態」慣例的副產品。

**實測結果：今天的 `wire` 通過此稽核，nil 欄位 0 個，豁免表為空。** 這件事讓稽核可以用最強的形式陳述（「不得有任何 nil」）而不必背一份例外清單。日後若真的出現一個生產環境合法為 nil 的相依，加入豁免 MUST 附理由——與 `moneyExemptions` 同一個紀律。

這道檢查同時涵蓋 `checkout-idempotency` 已明文記載的陷阱：`deps.Orders.Idempotency` / `deps.Guest.Idempotency` 忘了掛，路由一條不少、整套 Go 測試全綠，而伺服器靜默忽略 `Idempotency-Key`（重送就是第二筆訂單）。它是「巢狀一層」的漏接，檢查 ⑥ 的路由表比對看不見它，**只有遞迴稽核看得見**。

**誠實邊界：** nil 稽核證明「有東西被接上去」，不證明「接上去的是對的東西」。把 `deps.Orders.Service` 接成另一個 shop 的 service，稽核會通過。那個層級的錯誤由既有的整合測試（行為層）負責，而它們有能力負責——它們唯一沒能力回答的就是「有沒有接」。

### D5. 推翻 `commerce-demo-seed` design D1 的「不為 wire.go 補 Go 測試」——它否決的是另一件事

那份 design 明文寫著 Non-Goal「不為 `wire.go` 補 Go 測試（D1）」，而 D1 的論證是：

> **已評估並否決：把 `wire.go` 拆出可測的接線函式。** …它把「接線」這件事變成一個新的間接層，而**新的間接層自己也可能沒被 `wire` 呼叫**——問題只是往上移了一格。

**這條反對意見完全正確，而且它不適用於本變更。** 它針對的是「抽出 `func Subscriptions(...) []events.Handler` 再對它斷言」——一個**代表** wire 的新東西。本變更沒有抽出任何東西：檢查 ⑤／⑦ 呼叫的是 `a.wire` 本身，檢查 ⑥ 呼叫的是 `app.New` 本身。沒有新的間接層，也就沒有「間接層自己沒被呼叫」的可能。

D1 的另一半論證是「它需要真實 PostgreSQL + Redis + 完整設定才有意義，那時它與 e2e 的差別只剩下用哪個語言寫斷言」。**這一半在事實上不成立，而差別恰恰是決定性的那一項：`INTEGRATION=1 go test ./...` 在 CI 裡，`node commerce-path.mjs` 不在。** 一個需要真實基礎設施的 Go 整合測試，與一支 e2e 腳本，在「是否會自動執行」上是兩種東西——這正是同一份 design 自己記錄的事實（e2e 不在 CI 內），只是當時沒有被套用回它自己的結論上。

因此：**推翻該 Non-Goal，理由是原論證的適用範圍被誤估，不是原判斷錯誤。** `commerce-demo-seed` 選擇 e2e 是在「重寫第七份自建 fixture」與「e2e」之間選，那個二選一裡它選對了；本變更提供的是當時沒有被放上桌的第三個選項。

### D6. 事件訂閱鎖：鎖的是**訂閱者集合**，不是行為探針

要在 CI 內守住 `dispatcher.Subscribe` 的四行，兩條路：

| | 行為探針 | 訂閱者集合鎖 |
| --- | --- | --- |
| 做法 | 對真實 dispatcher 發真實事件，斷言副作用（點數入帳、快取被刪） | 列舉真實 dispatcher 的訂閱者，與一份清單做**集合相等**比對 |
| 移除訂閱 | 紅 | 紅 |
| **新增訂閱** | **綠**——沒有人為它寫探針 | **紅**——集合多了一項 |
| 需要的 fixture | 每個訂閱者一套（點數探針要有真實 shop／member／order 列） | 無 |
| 會不會弄髒環境 | 會（真實 Redis DB 0、真實資料列） | 不會（只讀） |
| 證明的事 | 「接了而且行為對」 | 「接了」 |

**選集合鎖，而且理由就是那一列粗體字。** 行為探針對「新增」是盲的——那正是本變更要修的那個病，用它來治這個病等於把病複製到第二個器官。而「行為對不對」本來就已經有主人：`points_integration_test.go`、`cms_integration_test.go` 等六支測試各自驗它，`commerce-path.mjs` 端到端再驗一次。**唯一沒有主人的問題是「有沒有接」，集合鎖恰好只回答它。**

「新增訂閱也轉紅」是刻意的：一個新訂閱者代表一條新的跨套件事件鏈，它應該迫使作者停下來補一條斷言、並回答「e2e 要不要也覆蓋它」。這正是 `commerce-demo-seed` 的 Open Question（「新增一個訂閱者不會有任何檢查提醒你去 e2e 補一條斷言」）所要的東西。

**另外斷言 dispatcher 是單一實例**（`deps.Sites.Dispatcher == deps.ClientAuth.Dispatcher == cms/payment/shipping 服務持有的那一個`）。集合鎖鎖的是「那一個 dispatcher」的訂閱者；若有人為某個子系統另建第二個 dispatcher，鎖會對著錯的物件全綠——而發佈端與訂閱端分屬兩個 dispatcher 正是事件鏈斷裂的另一種形態。

### D7. 訂閱者的識別方式：函式符號名，匿名 closure 正規化

`events.Handler` 是 `func(context.Context, Event)`——沒有名字、沒有型別可以比對。可用的識別是函式符號：`runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()`。實測今天的四個訂閱者：

```
ksdevworks/ecommerce/api/internal/app.(*App).wire.func2          ← SiteMappingChanged 快取失效
ksdevworks/ecommerce/api/internal/app.(*App).wire.func3          ← StorefrontClientChanged 快取失效
ksdevworks/ecommerce/api/internal/render.(*Invalidator).Handle-fm
ksdevworks/ecommerce/api/internal/points.(*Service).Handle-fm
```

具名方法值（`-fm`）穩定且自我描述。**匿名 closure 的編號則不是**：`func2`／`func3` 是 `wire` 內第 2、3 個 closure（第 1 個是 `deps.Health`），在 `wire` 裡任何位置插入一個與事件無關的 closure 都會讓編號位移，鎖檔轉紅——一個**假警報**，而假警報是防腐機制最快的死法（`openapi-contract` 已就此立過規矩）。

**處置：把 `…wire.funcN` 一律正規化成 `…wire.<closure>`，以多重集合比對。** 保留的訊號：移除一個 closure 訂閱（2 → 1，紅）、新增一個 closure 訂閱（2 → 3，紅）、移除任一具名訂閱（紅）。消掉的雜訊：與訂閱無關的 closure 造成的重新編號。失去的能力：無法分辨「刪掉 A closure 又加了 B closure」——一個必須同時做兩件事才能製造、且兩件都在同一個檔案的同一段裡的情況，留給 code review。

**已評估並否決：改寫 `wire.go` 讓那兩個 closure 變成具名方法**（例如 `tenant.(*Resolver).HandleSiteMappingChanged`）。它確實會讓鎖更精確，而且本身是個不錯的重構——但它改動生產碼、改動的是本變更之外的關注點，而換到的精確度剛好落在正規化已經涵蓋的範圍內側。記為後續可做的獨立整理，不夾帶。

### D8. `events.Dispatcher` 新增 `Subscribers()`：一行生產碼，換掉一個 unsafe 讀私有欄位的測試

`d.handlers` 未匯出。測試要列舉它有兩條路：

1. `reflect.ValueOf(d).Elem().FieldByName("handlers")` 讀私有欄位（`Value.Pointer()` 對唯讀 Value 可用）——**零生產碼變更**，但把一個機制的可信度綁在「那個欄位叫 handlers」這個字串上，而且讀者看不出為什麼可以這樣做。
2. 在 `events` 加一個唯讀存取子。

選 2。它是純 additive、無執行期行為變更（沒有任何生產路徑呼叫它）、回傳的是複本（呼叫端不能改動訂閱），而且它讓「誰訂閱了誰是一項可檢查的事實」這件事**寫在領域套件自己身上**，而不是藏在一個測試的反射技巧裡。硬約束允許為可測性改動生產碼，前提是論證且不改變執行期行為——這兩項都成立。

**代價，誠實記錄：** 這是一個「只有測試會呼叫」的匯出方法。判準是它有沒有讓套件的公開語意變糟——`Dispatcher` 本來就是一個「持有一組訂閱者」的物件，讓它願意說出自己持有哪些，不是洩漏實作細節，是陳述它是什麼。

### D9. 檢查落在哪一層：① 留在單元層，⑤⑥⑦ 在整合層

| 檢查 | 層級 | 指令 | 需要基礎設施 |
| --- | --- | --- | --- |
| ① 路由覆蓋（來源改為反射填充） | 單元 | `make test` | 否 |
| ② 金額單位、④ 表面鎖 | 單元 | `make test` | 否 |
| ③ 回應結構 | 整合 | `make test-int` | 是 |
| **⑤ 生產接線完整性** | 整合 | `make test-int` | 是 |
| **⑥ 真實路由表等價** | 整合 | `make test-int` | 是 |
| **⑦ 事件訂閱鎖** | 整合 | `make test-int` | 是 |

**① 必須留在單元層**：`api-contract` 的既有需求明文要求路由覆蓋檢查不得需要資料庫或快取（`#### Scenario: 檢查不需要基礎設施`）。把它改成需要真實 `wire` 會違反那條需求，也會讓最常被手動執行的 `make test` 失去這道最有價值的檢查。

**⑤⑥⑦ 必須在整合層**：`wire` 會 `database.Open` 並 ping，沒有真資料庫就沒有真接線。它們在 CI 內（`INTEGRATION=1 go test ./...`），這是它們相對於 e2e 的全部價值。

`make openapi` 的定義維持不變（`-run TestOpenAPI`）——新檢查不叫 `TestOpenAPI*`，因為它們守的不是契約文件，是接線本身。

### D10. e2e 的角色重新定位，而不是被取代

`commerce-path.mjs` 對事件鏈的覆蓋一條都不刪。分工變成：

- **CI（檢查 ⑦）**：有沒有接。斷了立刻紅，每一次 push。
- **e2e**：接上去之後整條鏈的**行為**對不對——金額、點數數量、等級升降、冪等重送。這是集合鎖永遠不會回答的事。

CLAUDE.md 裡「事件接線只由 e2e 覆蓋」這句話在本變更後不再成立，必須改寫；但「改動 `wire.go` 或事件鏈後請務必跑 `commerce-path.mjs`」這句仍然成立，理由變成上面那條分工。

## Risks / Trade-offs

- **[反射填充器對未來的欄位型別可能填不出來，導致新增欄位時測試直接 fatal]** → 這是**設計的行為而非風險**（D2 規則 5）：它把「靜默少填」換成「明確要求登記一個值與理由」。代價是新增介面型別 gate 欄位的人要多寫兩行；換到的是同類漏洞不會以新型別重新出現。

- **[遞迴 nil 稽核日後可能誤報：某個 handler 新增一個生產環境合法為 nil 的匯出欄位]** → 真實風險，且今天的實測（0 nil、豁免表為空）不保證明天。緩解是豁免機制與 `moneyExemptions` 同型——可以豁免，但必須寫下理由，而理由會出現在 diff 裡。**注意這個誤報方向是安全的**：它逼人看一眼「這個相依真的可以不接嗎」，而 `Idempotency` 的教訓正是「看起來可以不接的相依，不接就是靜默失效」。

- **[訂閱者集合鎖以函式符號名比對，Go 工具鏈若改變符號命名慣例會整組轉紅]** → `-fm` 與 `funcN` 是長期穩定的既有行為；真的改變時，症狀是一次性的、指名的、修法顯而易見（更新清單）。相較於它擋住的東西（整條事件鏈靜默斷裂），這個代價可以接受。已以正規化消掉最可能的假警報來源（closure 重新編號）。

- **[檢查 ⑦ 證明「有接」不證明「接對」]** → 明寫的邊界（D6）。行為由既有的六支整合測試與 `commerce-path.mjs` 負責，本檢查不宣稱涵蓋它們。反過來也成立：那些測試不宣稱涵蓋「有沒有接」，這正是本變更填補的縫。

- **[檢查 ⑤／⑥ 會在測試中開啟真實的 Redis 連線（`cfg.RedisAddr`，DB 0，非測試用的 DB 15）]** → 兩道檢查皆**只讀不寫**（走訪路由表、稽核欄位），不發佈任何事件、不觸碰任何快取鍵。`wire` 對 Redis 只在啟動時 ping 一次，ping 失敗只 warn 不阻擋，所以即使沒有 Redis 也不會讓檢查誤紅。測試結束時 `a.Close()` 關閉連線。

- **[`app` 套件現在有整合測試，`make test`（無基礎設施）會跳過它們]** → 與既有整合測試同一形狀（`testutil.RequireIntegration` skip），CI 一律以 `INTEGRATION=1` 執行。殘留的真實結果是：接線類的疏漏在 `make test` 上不會被抓到，只在 `make test-int` 與 CI 上——已在 D9 表格中明列，不假裝它們在單元層。

- **[本變更為每一次「新增 handler」加上一道必答題（要嘛寫文件，要嘛看見紅燈）]** → 這是它**刻意加上**的成本，也是它的全部意義（與 `openapi-contract` 對此的表述一致）。緩解是失敗訊息直接指名缺哪一條路由／哪一個欄位／哪一個訂閱者。

- **[六處子集 fixture 仍然在鏡像 wire.go，本變更沒有動它們]** → 刻意的（Non-Goals）。它們現在有了一個真正的參照物：若某支 fixture 的接線與生產不同而導致誤導，檢查 ⑤／⑦ 提供了可以對照的事實來源。把它們統一成一份完整接線會讓每支測試背上與它無關的相依，那是比鏡像更貴的代價。

## Migration Plan

1. **無 migration、無資料變更、無 API 變更。** `api/migrations/`、`atlas.sum`、`openapi.yaml`、`v1-surface.txt` 皆不動。
2. 部署面零影響：新增的全是測試碼，唯一的生產碼變更是 `events.Dispatcher.Subscribers()`，沒有任何生產路徑呼叫它。
3. **Rollback**：回退程式碼即可，沒有任何持久化副作用。
4. 驗收（每一項都必須實際執行並記錄輸出）：`make test`、`make test-int`、`make lint`、`make openapi` 四道全綠；乾淨資料庫 `make migrate && make seed-demo` 後 `node e2e/happy-path.mjs` 全綠；**並且**：新增假 handler 不接線 → 紅（修好前為綠，兩者對照）、移除任一 `dispatcher.Subscribe` → CI 內的檢查紅、既有四道檢查各挑一種代表性破壞重跑確認仍然轉紅。

## Open Questions

- **`wire.go` 的兩個匿名 closure 要不要改成具名方法？** D7 已論證本變更不做（改生產碼、關注點不同、換到的精確度落在正規化的內側）。真正會改變這個判斷的觸發條件是「出現第三個 closure 訂閱者」——屆時「哪一個 closure 不見了」開始有實質差異，值得為它付出重構成本。
- **`Deps` 之下的第三層以外要不要繼續稽核？** 目前遞迴會一路走到未匯出欄位為止，實測自然停在建構子邊界。若日後某個服務長出匯出的子服務欄位，稽核會自動跟進——這是想要的行為，但它也意味著稽核的涵蓋面會隨著別人的重構而變動。記為需要觀察的性質，不預先設限。
- **e2e 進 CI 仍是未決問題**（`commerce-demo-seed` 留下）。本變更把「事件鏈有沒有接」從 e2e 移進 CI，因此降低了它的急迫性，但沒有回答它——e2e 仍是唯一覆蓋「整條鏈的行為」與「瀏覽器層」的東西。
