## Why

`openapi-contract` 建立的第 ① 道檢查（路由覆蓋）走的不是伺服器真正組裝出來的 router，而是**測試碼裡手寫的 `contractRouter()`**——一份逐欄位鏡像 `api/internal/app/wire.go` 的清單。`member-directory` 的實作者在完成後自報了它的後果：忘了往那份清單加一行，新端點就完全不會被列舉，四道檢查全綠而該端點沒有任何文件。

**已實測證實**（本變更動工前）：在 `httpapi.Deps` 上新增一個 handler、在 `router.go` 掛上 `GET/POST /api/v1/admin/shops/{shopID}/fake-widgets`、但不碰 `contractRouter()`——

```
--- PASS: TestOpenAPICoversEveryRoute (0.02s)   契約覆蓋 114 條路由（chi.Walk 列出 112 條 + Walk 盲點 2 條）
--- PASS: TestOpenAPIMoneyFieldsDeclareUnit
--- PASS: TestOpenAPIV1SurfaceLock
```

兩條真實存在、對外可呼叫、零文件的路由，三道檢查一句話都沒說。**防腐機制對「新增」是盲的，而新增正是最常發生的事。**

這不是單一疏漏，是一個**類別**：`wire.go` 是唯一的真實接線處，而測試碼至少有兩處在鏡像它，第二處已經被姊妹 change 抓到過——`commerce-demo-seed` 實測刪掉 `dispatcher.Subscribe(pointsService.Handle)`，`make test-int` 依然全綠而真實伺服器靜默不發點數，當時的處置是「靠 e2e 覆蓋」，而 **e2e 不在 CI 內**（同一份 design 自己記錄的事實）。註解寫著 `mirrors app.wire.go` 的地方共有 6 處，沒有一處是約束。

**鏡像必然漂移。本變更要讓「漏接」在 CI 內變紅，而不是變成一份沒有文件的端點或一條靜默斷掉的事件鏈。**

## What Changes

- **檢查 ① 的路由來源不再是手寫清單**：`contractRouter()` 的 `Deps` 改由**反射 `httpapi.Deps` 的結構**逐欄位填滿，而不是逐欄位列舉。新增一個 handler 欄位，它**自動**被填、路由**自動**被列舉、沒寫進 `openapi.yaml` 就**自動**轉紅。填不出來的欄位型別（介面）MUST 讓測試明確失敗並要求登記，MUST NOT 靜默留 nil。
- **新增檢查 ⑤：生產接線完整性**（整合層，CI 內）。執行**真正的** `app.(*App).wire`，遞迴斷言 `Deps` 的每一個欄位、以及每個 handler 的每一個匯出相依欄位皆非 nil。這道檢查涵蓋 `checkout-idempotency` 已明文記載的同型陷阱：`Idempotency` 忘了掛，整套 Go 測試仍全綠而伺服器靜默忽略 `Idempotency-Key`。
- **新增檢查 ⑥：真實路由表等價**（整合層，CI 內）。以 `app.New` 組出**真正的生產 router**，斷言其路由表與檢查 ① 所走的 router 逐條相同。檢查 ① 因此從「一份被相信的鏡像」變成「一份被證明的鏡像」。
- **新增檢查 ⑦：事件訂閱鎖**（整合層，CI 內）。斷言真實 `wire` 產出的 dispatcher 其訂閱者集合恰好等於一份寫在測試裡的清單。移除任一訂閱轉紅（斷鏈）；新增任一訂閱也轉紅（強迫補一條斷言與 e2e 覆蓋），並斷言全系統只有**一個** dispatcher 實例。
- `events.Dispatcher` 新增一個唯讀存取子 `Subscribers()`（純 additive，不改變任何執行期行為），讓「誰訂閱了誰」從註解變成可檢查的事實。
- 既有四道檢查的判定邏輯**一行不改**，涵蓋範圍不縮減。
- **無任何 API 行為變更**：不新增／不修改／不移除任何端點、參數、回應鍵。`openapi.yaml` 與 `v1-surface.txt` 皆不變動。

## Capabilities

### New Capabilities

- `wiring-integrity`: 組裝根（composition root）本身的行為契約——「每一個宣告的相依都必須真的被接上」與「領域事件的訂閱集合必須被機械比對」。這一塊今天在 `openspec/specs/` 完全沒有主人：`demo-seed` 的需求只規範 seed 產出的資料，事件接線的覆蓋缺口只存在於 `commerce-demo-seed` 的 design 與 CLAUDE.md 的敘述中，從未成為任何能力的驗收條件——這正是它可以長年缺席而沒有人被提醒的原因。它也不屬於 `api-contract`（那份契約回答「文件憑什麼可信」，不回答「事件鏈有沒有接」）。

### Modified Capabilities

- `api-contract`: 「路由覆蓋由自動化檢查雙向強制」這條需求目前只約束比對的**兩個方向**，未約束「伺服器實際註冊的路由集合」這個**來源**如何取得——於是它可以合法地由一份人工維護的清單提供，而該清單少掉整組路由時檢查全綠。修訂為：來源 MUST 由相依結構機械推導而非人工列舉，且 MUST 有一道檢查證明該來源與生產環境組裝出的路由表逐條相同。

## Impact

- `api/internal/httpapi/openapi_contract_test.go`——`contractRouter()` 改為反射填充，檔頭補上新的邊界說明。
- `api/internal/app/`——新增 `wiring_integration_test.go`（檢查 ⑤⑥⑦），`package app` 內部測試（`wire` 未匯出）。
- `api/internal/testutil/`——新增反射填充與 nil 稽核的共用實作，讓「填滿一個 `Deps`」全專案只有一份程式碼。
- `api/internal/events/events.go`——新增 `Subscribers()`（唯讀、additive）。
- `CLAUDE.md`——更新「事件接線只由 e2e 覆蓋」與「四道檢查」兩條記述（前者在本變更後不再成立）。
- **不動**：`openapi.yaml`、`v1-surface.txt`、任何 handler／service／migration、`.github/workflows/ci.yml`（新檢查是普通 Go 測試，既有的 `INTEGRATION=1 go test ./...` 自動涵蓋）。
