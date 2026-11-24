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
cp .env.example .env     # 首次；把 <generate: …> 佔位值換成自己產生的隨機值（見檔內說明）
make up                  # PostgreSQL 16 + Redis 7
make migrate             # 套用 migrations
make seed-demo           # 權限、角色、super admin、starter 主題 + demo 商家
make dev                 # API      :8080
make web-dev             # 前台 SSR :3000
make admin-dev           # 後台 SPA :3001
```

本機基礎設施只綁 `127.0.0.1`；Postgres 與 Redis 的密碼都來自 `.env`（`POSTGRES_PASSWORD`、`REDIS_PASSWORD`），未設定時 `docker compose` 會直接拒絕啟動。super admin 的密碼是 `.env` 裡的 `SEED_ADMIN_PASSWORD`（只在帳號尚不存在時寫入）。

`make seed-demo` 之後 demo 商家綁在 `demo.localhost`，帳號密碼與固定識別值見 `CLAUDE.md`。

**`APP_ENV` 未設定＝`production`**：除 `development`／`test` 外，API（與 migrate／seed）遇到空的、已知預設的或未填的 `<佔位>` 密鑰會拒絕啟動，`seed -demo` 也會被拒絕。放在反向代理或 Cloudflare Tunnel 後面時，以 `TRUSTED_PROXIES`（例如 `127.0.0.1/32,::1/128`）指定可信任其 `CF-Connecting-IP` 的對端；未設定時一律以 TCP 對端為客戶端 IP，`X-Forwarded-For` 永不採信。

在另一組 port／獨立資料庫上跑瀏覽器 E2E（`E2E_API_BASE_URL`／`E2E_WEB_BASE_URL`／`E2E_ADMIN_BASE_URL` ＋ 不同的 `REDIS_DB`）見 `CLAUDE.md` 的 Commands。

其餘指令（測試、lint、migration 產生、契約檢查、mock server）：`make help`。

## 文件在哪

| 你想知道 | 看這裡 |
| --- | --- |
| **API 有哪些端點、request/response 長什麼樣** | [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml) — OpenAPI 3.1，涵蓋全部 146 條路由，由四道自動化測試守著 |
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
