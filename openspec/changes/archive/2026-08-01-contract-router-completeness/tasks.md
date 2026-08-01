## 1. 先證明洞是真的（修好之前）

- [x] 1.1 於 `httpapi.Deps` 新增一個假 handler 欄位、在 `router.go` 掛上兩條假路由、**不碰** `contractRouter()`，執行檢查 ①②④ 並記錄「全綠」的實際輸出（本變更存在的理由）
- [x] 1.2 還原 1.1 的改動，確認工作區乾淨

## 2. 共用的反射工具（`internal/testutil`）

- [x] 2.1 新增 `internal/testutil/wiring.go`：`FillNonNil(t, structPtr, overrides)` 依 design D2 的規則表逐欄位填滿，填不出來的型別 MUST `t.Fatalf` 並指名欄位與型別
- [x] 2.2 同檔新增 `NilDependencyPaths(v, rootName, exemptions)`：遞迴稽核匯出欄位是否為 nil，遞迴邊界為「型別位於 `ksdevworks/ecommerce/api/internal/` 之下、且不在 `internal/ent`／`internal/config`」，並以指標位址防環
- [x] 2.3 同檔新增 `RouteTable(t, h)`：`chi.Walk` 列舉「METHOD /path」並排序（供檢查 ⑥ 與既有檢查 ① 共用同一份列舉邏輯）

## 3. 修好檢查 ①：路由來源由結構推導

- [x] 3.1 `contractRouter()` 改為以 `testutil.FillNonNil` 建立 `Deps`；覆寫表只保留 `Log`（丟棄輸出的真 logger）與 `CORSOrigins`（永遠回 false 的 stub），各附一句理由
- [x] 3.2 新增 `TestContractRouterFillsEveryDep`：斷言填出來的 `Deps` 頂層無任何 nil 欄位（給出比「路由少了」更直接的失敗訊息）
- [x] 3.3 更新 `openapi_contract_test.go` 檔頭：說明路由來源已改為機械推導、規則 5 的硬失敗語意、以及檢查 ⑤⑥⑦ 各自守著什麼
- [x] 3.4 `make test` 全綠；確認檢查 ① 回報的路由條數與變更前相同（112 walked + 2 盲點 = 114）

## 4. 事件分派器的可列舉性

- [x] 4.1 `internal/events/events.go` 新增 `Subscribers() []Handler`（回傳複本、唯讀），doc comment 說明它存在的唯一理由是讓「誰訂閱了誰」成為可檢查的事實
- [x] 4.2 確認沒有任何生產路徑呼叫它（`go build ./...` ＋ grep）

## 5. 生產接線的三道檢查（`internal/app`，整合層）

- [x] 5.1 新增 `internal/app/wiring_integration_test.go` 檔頭：說明這三道檢查為何必須在整合層、為何不是「代表 wire 的東西」而是 wire 本身、以及它們與既有四道檢查的分工
- [x] 5.2 檢查 ⑤ `TestWireFillsEveryDependency`：以 `TEST_DATABASE_URL` 呼叫真實的 `a.wire`，遞迴稽核 nil，豁免表為空
- [x] 5.3 檢查 ⑥ `TestWiredRouteTableMatchesContractSource`：以真實的 `app.New` 組出生產 router，與 `testutil.FillNonNil` 填出的參照 router 逐條比對路由表
- [x] 5.4 檢查 ⑦ `TestWiredDispatcherSubscribers`：斷言單一 dispatcher 實例（sites／clientauth／cms／payment／shipping 持有同一個），並以正規化後的函式符號名多重集合與預期清單做相等比對
- [x] 5.5 `make test-int` 全綠

## 6. 可信度證明（每一項都要貼出實際輸出）

- [x] 6.1 重放 1.1 的假 handler（同一份 patch）→ 檢查 ① 必須**轉紅**，與 1.1 的「全綠」對照
- [x] 6.2 假 handler 只加進 `Deps` 而 `wire.go` 不接 → 檢查 ⑤ 轉紅並指名該欄位
- [x] 6.3 把 `deps.Orders.Idempotency` 拿掉（巢狀漏接，路由數不變）→ 檢查 ⑤ 轉紅
- [x] 6.4 移除 `dispatcher.Subscribe(pointsService.Handle)` → 檢查 ⑦ 轉紅（**CI 內**，不只 e2e）
- [x] 6.5 新增一個訂閱者 → 檢查 ⑦ 轉紅（證明它對「新增」不盲）
- [x] 6.6 既有四道檢查各挑一種代表性破壞重跑，確認重構後仍然轉紅：①刪一條 `openapi.yaml` 路由、②新增一個未 `$ref` `MinorUnitAmount` 的金額欄位、③改動某個共用 schema 的回應鍵、④刪除鎖檔中已存在的一行對應的契約內容
- [x] 6.7 每一項證明後還原改動，`git status` 乾淨

## 7. 全套驗收與文件

- [x] 7.1 `make test`、`make test-int`、`make lint`、`make openapi` 四道全綠
- [x] 7.2 確認 `api/openapi/openapi.yaml` 與 `api/openapi/v1-surface.txt` 逐位元組未變（本變更 MUST NOT 改變 API 行為）
- [x] 7.3 DB 重置後 `make migrate && make seed-demo`，`cd e2e && node happy-path.mjs` 全綠（187 項斷言）
- [x] 7.4 更新 `CLAUDE.md`：四道檢查 → 七道；改寫「事件接線只由 e2e 覆蓋」為新的 CI／e2e 分工；記錄「新增 handler 不需要改契約測試」這個新事實
