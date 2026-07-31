## Why

ONDO 溫度花室（`bouquet`）即將接入本 API，而**本 API 至今沒有任何形式的 API 文件**：無 OpenAPI/Swagger、無 Postman collection、根目錄與 `api/` `web/` `admin/` 皆無 README、`go.mod` 無任何文件產生器。最接近文件的 `openspec/specs/*/spec.md` 是行為驗收契約（WHEN/THEN），不是 API 參考——實測：spec 全文只順帶提到 38 個 `/api/v1` 路徑字串，而實測真實可達的是 **111 條路由**（`chi.Walk` 回報 109，另 2 條落在 Walk 的盲點，見 design D3a）。今天要知道「建立商品的 request body 長什麼樣」「哪些錯誤碼回什麼」，唯一的辦法是讀 Go handler 原始碼。

ONDO 的性質決定了文件的價值形態：**零建置流程**（純 HTML/CSS/vanilla JS，無 `package.json`、無打包、無 TypeScript）、**跨 repo**、**跨部署管線**（Cloudflare Pages）。所以「產 TS 型別」對它毫無用處——它拿不到也吃不到型別檢查。真正有價值的是三件事：跨 repo 的人可讀契約、可產 mock server（讓 ONDO 離線開發不必起整套 Go + PostgreSQL + Redis）、以及**契約測試**——在一個沒有型別檢查的消費端，這是唯一能自動擋住契約漂移的機制。

而且這件事現在有一個具體的靜默地雷可以作為標的：ONDO 的商品價格是 `price: 3400`（新台幣元），本 API 的金額一律是**最小貨幣單位整數**（`total_amount: 340000`）＋ `currency CHAR(3)`。**差 100 倍不會噴任何錯，只會賣錯價。**

## What Changes

- **新增 `api/openapi/openapi.yaml`**：手寫的 OpenAPI 3.1 規格，覆蓋 `/api/v1` 的**全部 110 條路由**加 `/healthz`（共 111 條，逐條與實作對齊）。統一錯誤 envelope（design D12）表達為單一共用 response component，四種互斥的認證機制表達為可區分的 `securitySchemes`。

- **新增四道防腐機制**，全部落在**既有的 `go test ./...`** 之內，因此**自動進 CI**（`.github/workflows/ci.yml` 已跑 `INTEGRATION=1 go test -p 1 -count=1 ./...`）——這是本變更最重要的部分，不是 OpenAPI 檔本身：
  1. **路由覆蓋測試**（單元，免基礎設施）：`chi.Walk` 走真實路由表，與 OpenAPI 的 `paths` 比對——Walk 到的每一條都必須有文件；文件有而 Walk 沒有的每一條，都必須通過**實際發請求的可達性探測**（design D3a：`chi.Walk` 實測有盲點，這個設計把盲點吸收成一個每次都重新驗證的斷言，而不是一份人工白名單）。漏記或多記一條都轉紅。
  2. **金額單位紀律測試**（單元）：文件中每一個金額欄位 MUST `$ref` 同一個 `MinorUnitAmount` component。新增金額欄位而忘了聲明單位，測試轉紅。
  3. **真實回應結構驗證**（整合）：對真實 router 發真實請求，回應以 `santhosh-tekuri/jsonschema/v6`（**既有直接相依**）驗證 OpenAPI 的 schema。實作改了形狀而文件沒跟上，測試轉紅。
  4. **v1 表面鎖**（單元）：`api/openapi/v1-surface.txt` 記錄每一條路由與每一個公開回應鍵；測試只檢查「鎖檔裡的每一行今天仍存在於文件中」。新增自由，**刪除必須手動刪掉鎖檔的那一行**——D13 的 additive-only 規則因此成為一個機械化、可審查的動作，而不是一句口號。

- **新增 `docs/storefront-integration.md`**：ONDO 接入說明的單一權威。目前這份資訊散落在三個已 archive 的 change（`storefront-client-auth`、`guest-checkout`、`order-extras`）的 `tasks.md` 末尾，各自只講自己那塊、且會隨 archive 目錄一起沉底。彙整為一份可直接照做的文件，以「列商品 → 結帳 → 付款 → 查單」這條路徑組織，而非欄位窮舉。

- **新增根目錄 `README.md`**：整個 repo 目前沒有任何 README。最小可用：這是什麼、怎麼跑起來、文件在哪。

- **新增 `make openapi`／`make openapi-mock`／`make openapi-ui`**：分別是四道檢查的單一入口、Prism mock server、Redocly 可瀏覽文件。後兩者以 `npx` 執行，**不進 `go.mod` 也不進任何 `package.json`**。

- **零行為變更**：本變更不新增、不移除、不修改任何端點、欄位或狀態碼。`api/internal/` 下唯一的非測試改動是 `go.mod` 把 `gopkg.in/yaml.v3` 由 indirect 提為 direct（該模組已在 `go.sum` 中，無新增下載）。

## Capabilities

### New Capabilities

- `api-contract`: `/api/v1` 的機讀契約本身成為一份被測試守護的交付物——覆蓋範圍完整性、金額單位聲明、與實作的一致性、以及 v1 additive-only 的機械化強制。

### Modified Capabilities

無。本變更不改變任何端點的行為，因此沒有任何既有 spec 的 requirement 需要修改。（文件與實作不符時該以何者為準，是 `api-contract` 自己要回答的問題，不是既有能力的 requirement 變更。）

## Impact

**新增檔案**

- `api/openapi/openapi.yaml` — OpenAPI 3.1 規格（單檔）
- `api/openapi/v1-surface.txt` — v1 表面鎖（產生後提交，刪行需人工）
- `api/internal/httpapi/openapi_contract_test.go` — 檢查 1／2／4（單元，免基礎設施）
- `api/internal/httpapi/openapi_conformance_integration_test.go` — 檢查 3（整合）
- `docs/storefront-integration.md` — ONDO／自帶前端接入說明
- `README.md` — 專案入口

**修改檔案**

- `Makefile` — `openapi` / `openapi-mock` / `openapi-ui` 三個 target
- `api/go.mod` — `gopkg.in/yaml.v3` indirect → direct（測試解析 YAML 用；`go.sum` 不變）
- `CLAUDE.md`、`openspec/project.md` — 記載契約位置與「改端點必須同時改 OpenAPI」這條紀律

**不變**

- 任何 `api/internal/**` 的非測試程式碼（除 `go.mod` 外零改動）
- `.github/workflows/ci.yml`（四道檢查靠既有的 `go test ./...` 自動涵蓋，這正是選擇把它們放在 Go 測試層的理由）
- 資料庫 schema、migrations

**下游相依**

- ONDO（`bouquet`）：本變更交付 `openapi.yaml`（可餵 Prism 產 mock server）與 `docs/storefront-integration.md`。**接入動作發生在 bouquet repo，不在本變更範圍。**
- 未來任何新增端點的 change：MUST 同步更新 `openapi.yaml`，否則路由覆蓋測試轉紅——這是本變更留給後續變更的唯一負擔，也是它的全部意義。
