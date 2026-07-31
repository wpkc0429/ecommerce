## Context

本專案的電商鏈跨越六個互不 import 的套件，接縫全部落在領域事件層：

```
order.Checkout ──► orders(subtotal, shipping_fee, total)
payment.HandleWebhook ──► events.OrderPaymentSucceeded{SubtotalAmount} ──┐
                                                                        ├──► points.HandleOrderPaid  → point_transactions(kind=0) + members.points + recomputeLevel
shipping.AdvanceShipment(returned) ──► events.OrderReturned ─────────────┘──► points.HandleOrderReturned → point_transactions(kind=1) + members.points + recomputeLevel
```

`points` 套件的檔頭明文寫著它「deliberately never imports order/payment/shipping」。這是正確的設計，但它有一個直接後果：**「誰訂閱了誰」不是編譯期事實，而是 `api/internal/app/wire.go` 裡的兩行 `dispatcher.Subscribe`**。而這個檔案的測試覆蓋率是零——已查證每一支 integration test 都自己重建一份 dispatcher，並在註解裡寫著「mirrors app.wire.go」：

```
internal/httpapi/points_integration_test.go:135  // Dispatcher wiring mirrors api/internal/app/wire.go exactly
internal/httpapi/guest_integration_test.go:96    dispatcher.Subscribe(pointsSvc.Handle)
internal/payment/service_integration_test.go:319 // ... matching how api/internal/app/wire.go constructs payment.Service
```

「mirrors」是註解，不是約束。刪掉 `wire.go` 的 `dispatcher.Subscribe(pointsService.Handle)`，`make test` 與 `make test-int` 全綠，而真實伺服器從此靜默地不再發點數——這正是 `member-tiers-and-points` 的 design Risks 已經預言過的那顆雷（「`Dispatcher` 忘了接線，points 就靜默不核發」），只是當時沒有任何東西守著它。

另一半的現況是 seed。實測（乾淨資料庫 → `make migrate` → `make seed-demo` → 以 super admin 讀後台端點）：

| 表 | 內容 |
| --- | --- |
| `categories` / `products` / `product_skus` | 有（鞋類／包款，單價 890–1290，含一筆草稿商品） |
| `shipping_methods` | **空** |
| `member_tiers` | **空** |
| `publishable_keys` | **空** |
| `shop_allowed_origins` | **空** |
| `members` | **空** |

後四項為空的後果不只是「少了資料」：

- `order-extras` 新增的「結帳選運送方式、運費併入 `total_amount`」在 demo 上**無法觸發**——沒有任何運送方式可選。
- `storefront-client-auth` 新增的 publishable key 解析在 demo 上**無法觸發**——只能走 `Host` 回退。
- `points.recomputeLevel` 在 demo 上**永遠只走 `IsNotFound` 分支**（無等級符合 → `ClearLevelID`）。等級評估的另一半從未在真實資料上執行過。

約束：本變更**不得**新增／修改任何 API 端點的形狀（D13 破例窗口已於 `order-extras` 關閉並明文封閉）；`make seed-demo` 必須維持冪等；`cd e2e && node happy-path.mjs` 必須維持可運作且維持其 CMS 涵蓋範圍。

## Goals / Non-Goals

**Goals:**

- 讓 `make seed-demo` 之後的資料狀態**足以走完整條電商鏈**，且每一段跨套件接縫都有實際資料能觸發。
- 讓 `wire.go` 的事件接線第一次被任何自動化檢查守住。
- 讓點數等級的升級與降級在 demo 資料上**可觀察**——這需要 seed 的等級門檻與商品定價互相對得上，而不是各自看起來合理。
- 電商 e2e 可獨立執行、可重複執行、失敗時指得出是哪一段接縫斷了。
- 維持 seed 的冪等性紀律與既有 demo 資料不變。

**Non-Goals:**

- 不為 `wire.go` 補 Go 測試（D1）。
- 不把 demo shop 改造成 ONDO 花店（D10）。
- 不新增第二家 demo 商家（跨租戶隔離已有 integration test 覆蓋）。
- 不動 `.env` / `.env.example`（D6）。
- 不做 seed 的資料量參數化（`-demo-orders=50` 之類）：沒有消費端。
- 不在 e2e 覆蓋已被 integration test 以 `-race` 守住的並發情境（超賣防護、雙重出貨），那些需要的是精確的併發控制，不是端到端資料。

## Decisions

### D1. 這一段必須是 e2e，不能是第七支 Go integration test

**問題的形狀決定了測試的形狀。** 現存所有 integration test 都在自己的 `TestMain` 裡組裝服務與 dispatcher，正因如此它們**結構上不可能**發現 `wire.go` 接錯線——它們測的是「如果接對了，行為對不對」，而缺的那個問題是「有沒有接」。再寫一支同樣自建 fixture 的 Go 測試，只是把同一個盲點複製第七次。

唯一能回答「有沒有接」的執行體是**真的被 `make dev` 啟動起來的那個 process**：同一份 `app.wire`、同一份 `config.Load()`、同一個 PostgreSQL、同一個 Redis、同一份 `make seed-demo` 產出的資料。這就是 e2e 的定義。

**已評估並否決：把 `wire.go` 拆出可測的接線函式。** 例如 `func Subscriptions(...) []events.Handler` 再對它斷言。這確實能在 Go 裡驗證，但它把「接線」這件事變成一個新的間接層，而**新的間接層自己也可能沒被 `wire` 呼叫**——問題只是往上移了一格。而且它需要真實 PostgreSQL + Redis + 完整設定才有意義，那時它與 e2e 的差別只剩下用哪個語言寫斷言。

**代價，誠實記錄：** e2e 需要三個服務在跑（電商路徑只需要 API 一個），因此不會進 `make test` / `make test-int`，也就不會在每次改動時自動執行。這是真實的覆蓋缺口，不是被解決的問題——本變更能做的是讓它**跑起來夠便宜**（無瀏覽器、可重複執行、單一指令），使「跑一下 e2e」成為合理的收尾動作，而不是一件要準備半天的事。

### D2. 電商路徑不開瀏覽器；`happy-path.mjs` 維持總入口

電商鏈的每一步都是 API 呼叫：`/shop/products`、`/shop/guest/checkout`、`/webhooks/payments/mock`、`/admin/shops/{id}/orders/{id}/shipments`。這些沒有一個有 UI——`/web` 全樹沒有任何商品頁（Phase 1 只有 CMS 渲染），`/admin` 也沒有訂單或出貨畫面。把它們塞進 Playwright 只會付出啟動 Chromium 的成本、承擔瀏覽器層的不穩定，然後在 `page.evaluate` 裡寫一模一樣的 `fetch`。

**檔案切分：**

| 檔案 | 內容 | 需要 |
| --- | --- | --- |
| `e2e/lib.mjs` | `check()`／HTTP 輔助／webhook 簽章／設定解析 | — |
| `e2e/commerce-path.mjs` | 電商鏈，可 `node commerce-path.mjs` 單獨跑 | API |
| `e2e/happy-path.mjs` | 既有瀏覽器 CMS 流程 **＋ 接著跑電商路徑** | API + web + admin |

**為何 `happy-path.mjs` 要把電商路徑也跑掉，而不是各跑各的。** CLAUDE.md 記載的收尾指令只有 `cd e2e && node happy-path.mjs` 一行。如果電商路徑只存在於另一個檔案，實際會發生的事是：所有人照著 CLAUDE.md 跑 happy-path，而電商路徑永遠沒人跑——那就等於沒寫。反過來把 CMS 流程也搬進一個新的總入口，則會讓既有指令失效。讓總入口涵蓋全部、子集可獨立執行，是唯一同時滿足「既有指令不失效」與「新覆蓋真的會被執行」的形狀。

**失敗數合併，而非各自 `process.exit`。** `lib.mjs` 持有唯一的 `failures` 計數器，兩支腳本共用；`commerce-path.mjs` 以 `import.meta.url === process.argv[1]` 判斷自己是否為進入點來決定是否自行結束行程。被 `happy-path.mjs` import 時它只累加失敗數，由總入口統一結束——否則第一段的失敗會被第二段的 `process.exit(0)` 蓋掉。

### D3. 等級門檻與商品定價必須一起決定，這是 seed 的內部一致性要求

**這是本變更最容易被寫錯的一節，所以正面寫下來。**

`points.recomputeLevel` 的規則是「取 `min_points <= 餘額` 之中 `min_points` 最大的那一級」，而核發規則是 `subtotal_amount / 100` 無條件捨去。把這兩條和現行 demo 目錄放在一起：最貴的品項 1290，一筆「看起來正常」的訂單（兩件）小計 2580 → **25 點**。

於是任何「看起來正常」的等級階梯（銅 0／銀 100／金 500）在 demo 資料上都**永遠跨不過去**。等級評估會執行，但每次都得到同一個答案，等於沒有被驗證。要跨過門檻就得下一筆 40 件的訂單——那不是 demo 資料，那是為了配合測試而扭曲的資料。

**決定：seed 同時新增高單價品項與與之相稱的等級階梯，兩者一起決定。**

| | 值 | 理由 |
| --- | --- | --- |
| 花藝品項單價 | 3200 / 3400 / 3600 / 3800 / 4200 / 4400 | 直接取自 ONDO 的 `P[].price` 與 `SIZES[].d`（S −400、M 0、L +600），是真實的花藝客單價 |
| 等級門檻 | 0 / 30 / 100 | 單筆單品訂單（3200–4400 → 32–44 點）**跨過 30**；三筆同級訂單跨過 100 |

由此得到的可觀察行為：

- 新顧客 `level_id = NULL`（從未有過任何點數異動）→ `recomputeLevel` 的 `IsNotFound` 分支仍然被 demo 資料涵蓋。
- 第一筆訂單付款 → 餘額 32–88 → **升級**（`一般會員` → `銀卡會員`）。
- 該筆訂單退貨 → 餘額歸零 → **降級**（`銀卡會員` → `一般會員`）。

兩個分支都被走到，而且用的是一筆「一位顧客真的會下的訂單」。

**為何最低一級是 `min_points = 0` 而不是 1。** 有一級 0 分意味著只要發生過任何點數異動，`level_id` 就不會是 NULL——這是商家實際會設定的形狀（人人都是「一般會員」）。它並沒有讓 NULL 分支變得不可觀察：`level_id` 的初始值本來就是 NULL，任何尚未消費過的顧客都停在那裡。

**為何不改既有的鞋類／包款定價來達成同樣效果。** `seedDemoCatalog` 以 `classic-runner` 是否存在作為整段的冪等性守衛，改動既有列會讓「已經 seed 過的資料庫」與「新 seed 的資料庫」產生兩種不同狀態，而 seed 沒有 migration 機制去調和它們（D4）。新增則沒有這個問題。

### D4. 冪等性：每個 seed 區塊自己的自然鍵守衛，不共用一個全域旗標

既有紀律是「以自然鍵查存在，存在就整段跳過」（`seedDemoCatalog` 查 `classic-runner`、`seedStarterTheme` 查 `code = "starter"`）。本變更新增四個區塊，各自沿用同一形狀：

| 區塊 | 守衛 |
| --- | --- |
| 花藝品項 | `slug = "anniversary-bouquet"` 是否存在 |
| 運送方式 | 逐筆以 `(shop_id, name)` 查，缺哪筆補哪筆 |
| 會員等級 | 逐筆以 `(shop_id, name)` 查，缺哪筆補哪筆 |
| publishable key / allowed origin / demo 顧客 | 各以其自然唯一鍵（`key` / `origin` / `(shop_id, email)`）查 |

**為何運送方式與等級是逐筆守衛，而目錄是整段守衛。** 目錄的多筆資料之間有引用關係（product → sku → product_category），半套狀態是有害的，整段跳過才安全。運送方式與等級則彼此獨立，逐筆補齊讓「日後在 seed 裡多加一級」能對既有開發資料庫生效——這正是 `seedStarterTheme` 對 theme page 採取的做法（既有的就更新，缺的就建立），是本專案已有的慣例。

**明確不做：不 update 既有列。** 若 seed 的門檻值日後改了，已存在的資料庫**不會**被改寫。理由是這些是商家可在後台編輯的營運資料，seed 不該把開發者手動調過的值蓋回去（starter theme 的 schema 會被覆寫是因為那是平台資產，不是商家資料）。記為刻意的不對稱。

### D5. demo 的 publishable key 用固定值，不是隨機值

`tenant.NewPublishableKey()` 產生 `pk_` + 32 個隨機 base32 字元。demo seed 改用固定值 `pk_demoseedstorefrontkeynotsecret`（僅 a–z，落在 `ValidKeyFormat` 允許的字元集內）。

理由，由弱到強：

1. **冪等性變成精確的。** 自然鍵就是 key 值本身，「已存在就跳過」是一次主鍵查詢。若用隨機值，冪等性只能掛在 `label` 上——而 label 明文寫著「purely descriptive — no code path makes any decision from it」，拿它當識別鍵就是在推翻自己的 schema 註解。
2. **可寫進文件。** 手動 curl 與 ONDO 接入範例可以直接貼一個能用的 header，不必先登入後台撈金鑰。
3. **（決定性）它不增加任何暴露面。** publishable key 是**識別碼不是憑證**（`storefront-client-auth` design D1：「its disclosure is not a security incident. It grants nothing」）。而 demo shop **本來就能以 `Host: demo.localhost` 無金鑰解析**——一個可猜到的 key 給出的東西，`Host` 早就免費給了。這裡的隨機性原本是為了防止列舉平台上有哪些商家，而「demo.localhost 上有一家 demo 商家」不是秘密。

**為何值裡不用底線分段（`pk_demo_storefront`）。** `storefront-client-auth` design D8 刻意讓隨機段不含 `_`，以保留 `pk_test_` 這類**環境前綴**的文法空間。用 `pk_demo_` 會提前佔用那個命名空間，而且 `demo` 在那個文法裡會被讀成環境名。固定值放在隨機段的位置、只用該段合法的字元，就不碰到這個問題。

### D6. e2e 以與伺服器完全相同的順序解析 mock webhook 金鑰

**這是實測踩到的坑，不是假想。** 第一次跑通鏈時 webhook 回 401：本機 `.env` 產生於 payment-integration 之前，不含 `PAYMENT_MOCK_WEBHOOK_SECRET`，於是伺服器落到 `config.Load()` 的內建預設值 `dev-only-mock-payment-webhook-secret`；而 `.env.example` 裡寫的是 `dev-only-mock-payment-webhook-secret-change-me`。照著 `.env.example` 簽的簽章，永遠驗不過。

**決定：e2e 的解析順序 MUST 逐項對應伺服器的來源，且 MUST NOT 讀 `.env.example`。**

```
process.env.PAYMENT_MOCK_WEBHOOK_SECRET   ← Makefile 的 `include .env; export` 或呼叫者自行設定
  → 專案根 .env 的該鍵                     ← make dev 的實際來源
  → "dev-only-mock-payment-webhook-secret" ← config.Load() 的內建預設，逐字相同
```

`.env.example` **不在**伺服器的解析鏈上（它是一份範本，不是設定），所以它也不該在 e2e 的解析鏈上。把它加進去只會製造第四種可能的答案。

**401 時 MUST 印出診斷而非只記一次失敗。** 這是唯一一個「所有斷言都正確、環境卻錯了」的失敗模式，而它的表徵（webhook 401 → 訂單未付款 → 點數 0 → 後面十幾項全紅）會把人引導到完全錯誤的方向。訊息直接說明：伺服器與 e2e 解析到不同的金鑰，並列出 e2e 用了哪一個來源。

**已評估並否決：改 `.env.example` 讓它與內建預設一致。** 那會讓「範本值是個明顯要被換掉的佔位字串」這個有用的性質消失，而且它修不了真正的問題——問題是 `.env` 可以早於任何一個新設定鍵而存在，這對日後每一個新設定都成立。

**已評估並否決：本變更順手補上本機 `.env` 的缺鍵。** `.env` 在 `.gitignore` 內，是開發者自己的檔案；一個 change 去改它既不會被 review 也不會被同步到其他人的機器。

### D7. e2e 可重複執行：相對斷言 + 每次唯一的身分

一份只能在剛 seed 完的資料庫上跑一次的 e2e，實際上會變成「沒人跑的 e2e」——因為跑它的前置成本是重建資料庫。既有的 `happy-path.mjs` 已經懂這件事（結尾把主題切回 starter 以維持可重複性），電商路徑沿用同一紀律：

- **身分每次唯一**：訪客 email 帶時間戳後綴（`e2e-guest-{ts}@example.com`）。每次執行都是一位新顧客，餘額必然從 0 起算。
- **會員路徑用 seed 的固定顧客**（`demo-customer@example.com`）：不註冊，因此不消耗註冊速率限制的配額；其餘額跨執行累積，正好證明流水帳是累加的。
- **斷言一律相對**：`點數(後) - 點數(前) === floor(subtotal/100)`、`庫存(後) - 庫存(前) === -數量`。沒有任何斷言寫死絕對值。
- **等級斷言由 API 回傳的階梯推導**：先 `GET /member-tiers` 取得階梯，再以「`min_points <= 餘額` 中最大者」算出期望值。門檻值不寫死在 e2e 裡——這也順帶讓 e2e 在 seed 門檻日後被調整時不會誤報。

**庫存必須撐得住重複執行。** 已付款後退貨的訂單**不歸還庫存**（只有取消才歸還），所以每執行一次就永久消耗數件。seed 的花藝 SKU 庫存訂在 24–36，電商路徑每次消耗 3 件（會員兩件同款不同卡片 + 訪客一件），約可連續執行 8 次以上；再多就重跑 `make seed-demo`（新資料庫）即可。這是刻意的取捨：庫存設成 999 會讓 demo 資料失真，而「跑八次」對一個收尾檢查綽綽有餘。**e2e 在庫存不足時 MUST 明確報出是庫存耗盡**，而不是讓結帳的 422 看起來像功能壞了。

**後台登入改用 demo 商家 owner，不用平台 super admin。** 這一項是實測踩到的：連續跑第二次 `happy-path.mjs` 時後台登入回 429。登入的**嚴格**規則是每 `(shop, IP, email)` 每 5 分鐘 5 次，而 `happy-path.mjs` 本來就會用掉兩次 `admin@example.com`（瀏覽器登入 + API 建主題），電商路徑再用第三次，兩輪就爆。

改用 `demo-owner@example.com` 同時解決兩件事，而第二件比第一件重要：

1. 兩個帳號各有各的配額，電商路徑單獨執行可連跑五次。
2. **它才是真實情境**——出貨、標記退貨、看顧客點數都是商家操作。用 super admin 會直接跳過 RBAC，於是「seed 出來的 `merchant_owner` 角色到底有沒有 `shipment.create` / `shipment.update` / `point.view`」這件事從頭到尾沒被驗證過。改用 owner 之後，每一個後台呼叫都同時是一次 RBAC 授權的驗證。

已查證 `merchant_owner` 的權限集合涵蓋電商路徑用到的全部後台端點（`shipping_method.view`、`member_tier.view`、`shipment.create`、`shipment.update`、`point.view`）。

**429 與 401 同級，必須明確報出。** 兩者都是「所有斷言都正確、環境卻不對」，都會把讀者引去錯誤的方向。登入回 429 時直接說明是速率限制與其窗口，而不是「登入失敗」。

**訪客結帳的速率限制是已知邊界。** 寬鬆規則為每 IP 每 10 分鐘 20 次，電商路徑每次執行用掉 1 次，實務上不會撞到；記為已知邊界而非風險。

**已知：完整的 `happy-path.mjs` 本來就不可重複執行。** 它的 CMS 段會建立 `starter-v2` 主題，第二次執行必然因 code 重複而失敗——這是**本變更之前就存在**的性質，不由本變更引入也不由本變更修復（修它要嘛得刪主題、要嘛得讓 code 每次唯一，兩者都會改動一段與本變更無關的既有測試）。因此「可重複執行」這個保證的精確範圍是**電商路徑**：`node commerce-path.mjs` 可連續跑；`node happy-path.mjs` 一次 seed 跑一次。

### D8. 修正公開商品端點的 SKU 排序未定義

實測：一筆有兩個 SKU（id 1、id 2）的商品，`GET /api/v1/shop/products` 回傳的 `skus[]` 順序是 `[id=2, id=1]`。`catalog.Service` 的四處 `WithSkus()` 都沒有指定排序，於是順序由 Postgres 決定，可能隨計畫或列的物理位置改變。

**為何這是缺陷而不是「未定義即自由」。** 對消費端而言 `skus[]` 的順序**就是 UI 上尺寸選單的順序**。ONDO 的尺寸是 S/M/L，一個會隨重新整理換位置的尺寸選單是使用者看得見的 bug，而 API 這一側沒有任何東西告訴它「這個順序不可靠」。

**決定：四處 `WithSkus()` 一律 `Order(ByID())`**（建立順序＝商家輸入順序），後台與公開端點一致。這與同專案既有慣例相同——`ListShippingMethods`、`ListActiveShippingMethods` 皆為 `Order(ByID())`，`ListMemberTiers` 為 `Order(ByMinPoints())`；沒有一個清單端點把順序留給資料庫決定。

**為何不是依 `sku_code` 或依價格排序。** `sku_code` 是商家自訂字串，字典序不等於尺寸序（`L` < `M` < `S`）；依價格排序則會在同價位的 SKU 之間又回到未定義。id 遞增＝商家在後台輸入的順序，是唯一一個「商家能自己控制、且不需要新欄位」的答案。日後若需要顯式排序欄位（`sort_order`），那是純 additive 的後續變更，且會以本次的 id 序作為預設。

**e2e 仍然以 `sku_code` 選取 SKU，不用陣列索引。** 這不是重複防護：測試不該倚賴它正在驗證的那個性質。

### D9. seed 刻意包含一筆停用的運送方式

既有 seed 已有這個紀律的先例，註解明寫著理由：

```go
// A draft product (spec Published-only public catalog endpoint — must
// not appear on the public endpoint) so E2E/manual QA can verify the
// draft/published boundary against real seeded data.
```

同一條理由完整適用於運送方式：`order-extras` 規定停用的運送方式 MUST NOT 出現在公開清單，且結帳選用它 MUST 回 422。沒有一筆停用的資料，這兩條規則就只能在單元測試裡被驗證，而不能在 demo 資料上被驗證——**而 demo 資料正是商家與接入方會看到的東西**。

因此 seed 佈建四筆運送方式，三筆啟用（200／150／0）＋ 一筆停用（離島加價 350）。e2e 據此斷言：公開端點只回三筆、且以停用那筆結帳回 422 指向 `/shipping_method_id`。

### D10. 新增 ONDO 形狀的品項，但不把 demo shop 改造成 ONDO

**新增而非取代。** 既有的鞋類／包款目錄一列不動：它是 `seedDemoCatalog` 的冪等性錨點，也是 `product-catalog` spec 的示例資料，而且對一個**多租戶 SaaS 的 demo** 而言，多分類、多價位帶的目錄本來就比單一垂直的擬真店更能撐起示範。

**取自 ONDO 的是形狀，不是內容：** 高客單價（3200–4400）、同商品多尺寸 SKU（S/M/L，價差 −400／0／+600，逐字取自 ONDO 的 `SIZES`）、會附卡片訊息的品類。這三項各自對應一段本變更要驗的邏輯——等級階梯要跨得過去（D3）、SKU 排序要穩定（D8）、購物車合併鍵是 `(sku_id, gift_message)`（`order-extras` design D1）。

**不搬的東西：** ONDO 的八款商品全集、圖片、文案、FAQ、`CONFIG.showPrices` 之類的前端旗標。那些對驗證接縫沒有貢獻，只會讓 seed 變成一份需要維護的仿製品。

### D11. e2e 覆蓋範圍的取捨

**涵蓋（每一項都對應一段跨套件接縫或一條 seed 保證）：**

| 步驟 | 驗的接縫 |
| --- | --- |
| publishable key 解析 `/shop/*` | `storefront-client-auth` × seed |
| 公開運送方式清單（只回 active） | `order-extras` × `shipping` × seed |
| 商品清單／詳情，SKU 依 id 排序 | D8 |
| 訪客結帳（卡片訊息、運送方式、配送日） | `guest-checkout` × `order` × `cart` |
| 請款金額 `=== total_amount`（含運費） | `payment` × `order-extras` |
| webhook → `payment_status=1` | `payment` |
| 點數核發 `=== floor(subtotal/100)`，**不含運費** | `payment` → **events** → `points`（**含 `wire.go` 接線**） |
| 流水帳出現 `kind=0` 且 `points_delta` 相符 | `points` 帳本 |
| 等級升級至階梯推導出的那一級 | `points.recomputeLevel` × seed 門檻 |
| webhook 重送 → 點數與流水帳皆不變 | `payment` 重送分支 × `points` 冪等 |
| 出貨 → 訪客可查出貨 | `shipping` × 訂單存取 token |
| 退貨 → 扣回 `kind=1`、餘額回到起點、等級降級 | `shipping` → **events** → `points`（**含 `wire.go` 接線**） |
| 停用的運送方式結帳 → 422 | D9 |
| 會員路徑：購物車、同 SKU 不同卡片＝兩列 | `cart` 合併鍵 |
| 取消訂單 → 庫存歸還 | `order.CancelOrder` |

**不涵蓋（明確記錄）：** 並發超賣（`-race` integration test 的領域，e2e 無法可靠製造）、跨租戶隔離（integration test 已密集覆蓋，且需要第二家 demo 商家）、速率限制（刻意觸發它會讓 e2e 自己不可重複執行）、RBAC 拒絕矩陣（`roles_integration_test.go` 的領域）。

## Risks / Trade-offs

- **[e2e 不在 `make test` / `make test-int` 內，因此不會自動執行；`wire.go` 的接線仍可能在 CI 綠燈下被改壞]** → 這是 D1 誠實承認的殘留缺口，本變更**沒有消除它**，只是把執行成本壓到最低（電商路徑無瀏覽器、可獨立、可重複、單一指令）並在 CLAUDE.md 明文記載這條覆蓋只由 e2e 提供。真正的解法是把 e2e 納入 CI，那需要 CI 起三個服務＋資料庫，是獨立的一個 change。

- **[seed 的等級門檻與商品定價互相耦合，日後改了其中一個會讓另一個失去意義]** → 已在 D3 明文寫下這個耦合，並讓 e2e **由 API 回傳的階梯推導期望值**而非寫死門檻：改了門檻 e2e 仍然通過（因為它算得出新的期望），改到「單筆訂單再也跨不過任何門檻」時，e2e 的「升級後等級 MUST 不同於升級前」那條斷言會紅——耦合被破壞時會有人知道。

- **[固定的 publishable key 若被誤複製到正式環境的 seed]** → `-demo` 旗標是唯一的觸發條件，正式 seed 不佈建任何 demo 資料；且該值字面上寫著 `notsecret`。更根本地，publishable key 不是憑證（D5 理由 3），它洩漏不構成事件。

- **[e2e 可重複執行的次數受庫存限制（約 8 次）]** → 刻意的取捨（D7）：庫存設成不真實的大數會讓 demo 資料失真。e2e 在庫存不足時明確報「庫存耗盡，請重跑 `make seed-demo`」，不讓它偽裝成功能故障。

- **[seed 新增的 demo 顧客帶明文預設密碼]** → 與既有的 `demo-owner@example.com` / `demo-owner-change-me` 完全同型（同一個 `-demo` 旗標、同一個用途）。命名同樣以 `-change-me` 結尾表達意圖。

- **[SKU 排序改動觸及後台端點，可能有既有消費端依賴目前的順序]** → 目前的順序是**未定義**的，任何依賴它的消費端本來就已經壞了（只是還沒被發現）。`/admin` SPA 不渲染 SKU 清單，`/web` 不渲染商品，e2e 改為以 `sku_code` 選取；實際受影響的消費端為零。

- **[trade-off：`happy-path.mjs` 同時涵蓋兩段，單次執行時間變長]** → 電商路徑無瀏覽器，實測增量遠小於既有的 Chromium 啟動與頁面往返；而拆開的代價是新覆蓋不會被執行（D2）。

- **[認證端點的速率限制會讓密集重跑 e2e 看起來像功能壞掉]** → 已在 D7 處理：電商路徑改用 demo 商家 owner（與平台 admin 各有配額），且登入回 429 時明確報出是速率限制與其窗口。殘留邊界照實記錄——完整 happy path 每輪用掉兩次平台 admin 登入，五分鐘內連跑三輪仍會撞到；但它本來就不可重複執行（D7），所以這不是新增的限制。

- **[trade-off：seed 檔案變長]** → `seed.go` 由 613 行增至 984 行。已切為五個各自獨立、各自帶冪等守衛的函式，而不是繼續堆進 `seedDemoCatalog`；每個函式的守衛就是它的自然鍵，讀者不需要理解全域狀態。

## Migration Plan

1. **無 migration。** 本變更不動 ent schema，`api/migrations/` 與 `atlas.sum` 皆不變。
2. `make seed-demo` 對**既有**開發資料庫執行：既有的鞋類／包款／草稿商品與 demo owner 全部原樣保留（各自的守衛命中），新增花藝品項、四筆運送方式、三級等級、一把 key、一筆 origin、一位 demo 顧客。連續執行第二次 MUST 完全無變更。
3. 對**乾淨**資料庫：`make migrate && make seed-demo` 之後即可直接跑完整電商 e2e。這是本變更的驗收條件。
4. 已存在的資料庫若手動建過同名的運送方式或等級，seed 會視為已存在而跳過——不覆寫（D4 的刻意不對稱）。
5. **Rollback**：seed 只新增資料列，回退程式碼即可；已佈建的 demo 資料不會使任何舊版程式碼失效（它們都是商家後台本來就能建立的普通資料）。

## Open Questions

- **e2e 要不要進 CI？** 本變更把它變得夠便宜，但沒有做這件事——CI 需要起 PostgreSQL + Redis + API（電商路徑）或再加 web + admin（完整 happy path），還要決定 seed 與資料庫的生命週期。那是一個獨立的 change，而本變更是它的前提（在此之前，電商鏈根本沒有可執行的 e2e 可放進 CI）。
- **`wire.go` 是否該有一份「所有領域事件訂閱者」的顯式清單？** 目前訂閱散在 `wire` 的四處 `dispatcher.Subscribe`，新增一個訂閱者不會有任何檢查提醒你去 e2e 補一條斷言。把訂閱集中成一份具名清單至少能讓遺漏在 code review 時看得見，但那是 `internal/events` 自己的形狀問題，不該由一個 seed/e2e 變更順手決定。
- **demo 資料要不要包含已完成的歷史訂單？** 後台的訂單列表、點數流水帳目前在剛 seed 完的資料庫上都是空的，商家第一次看到的是空畫面。但 seed 直接寫入 `orders` / `point_transactions` 會繞過 `order.Service.Checkout`（唯一一份並發安全的扣庫存路徑）與整條事件鏈——那正是本變更要求所有下單路徑都不得繞過的東西。若要做，正確做法是讓 seed 呼叫服務層而非直接寫表，那是一個獨立的決定。
