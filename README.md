# 多網站電商 SaaS

多租戶電商平台的 monorepo：一套後端服務多個商家，每個商家可以有自己的網域、自己的主題、
以及自己的前端（平台託管的，或商家自帶的）。

```
api/     Go headless API — 唯一的資料權威（chi + ent + PostgreSQL + Redis）
web/     Next.js storefront SSR — 消費渲染 bundle API，主題＝前端元件庫
admin/   無建置步驟的薄殼 SPA — 登入、schema 驅動的內容編輯、發佈／預覽
e2e/     Playwright 端到端（CMS 流程 ＋ 電商鏈）
docs/    交付文件
```

## 跑起來

需要 Docker、Go 1.26、Node 20。

```bash
cp .env.example .env     # 首次
make up                  # PostgreSQL 16 + Redis 7
make migrate             # 套用 migrations
make seed-demo           # 權限、角色、super admin、starter 主題 + demo 商家
make dev                 # API      :8080
make web-dev             # 前台 SSR :3000
make admin-dev           # 後台 SPA :3001
```

`make seed-demo` 之後 demo 商家綁在 `demo.localhost`，帳號密碼與固定識別值見 `CLAUDE.md`。

其餘指令（測試、lint、migration 產生、契約檢查、mock server）：`make help`。

## 文件在哪

| 你想知道 | 看這裡 |
| --- | --- |
| **API 有哪些端點、request/response 長什麼樣** | [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml) — OpenAPI 3.1，涵蓋全部 111 條路由，由四道自動化測試守著 |
| **自帶前端要怎麼接入**（金鑰、CORS、訪客結帳、付款、查單） | [`docs/storefront-integration.md`](docs/storefront-integration.md) |
| **這個系統為什麼長這樣**（架構決策、取捨、已知限制） | [`openspec/`](openspec/) — 每個變更都有 proposal／design／spec／tasks |
| **目前的行為驗收契約**（WHEN/THEN） | [`openspec/specs/<capability>/spec.md`](openspec/specs/) |
| **在這個 repo 工作的規則與陷阱** | [`CLAUDE.md`](CLAUDE.md) |

`openspec/specs/` 是行為契約，`api/openapi/openapi.yaml` 是介面契約——兩者不重疊，都不是可省略的。

## 開發紀律（最短版本）

- **改任何端點，同時改 `api/openapi/openapi.yaml`**，否則 `go test ./...` 轉紅。
- **`/api/v1` 只做 additive 變更**：可以新增，不可以移除或改名。破壞性變更走新端點或 `/api/v2`。
  這條規則由 `api/openapi/v1-surface.txt` 鎖檔強制。
- 金額一律是**最小貨幣單位的整數** ＋ `currency`，禁用浮點數。
- 改動 `internal/app/wire.go` 或領域事件鏈後，**務必**跑 `cd e2e && node commerce-path.mjs`——
  那是唯一覆蓋事件訂閱接線的測試。

完整版見 [`CLAUDE.md`](CLAUDE.md)。
