# 多網站電商 SaaS

多租戶電商平台的 monorepo：一套後端服務多個商家。

> **方向（2026-11-24 決定，見 [`docs/decisions/0001-headless-platform-and-merchant-template.md`](docs/decisions/0001-headless-platform-and-merchant-template.md)
> 與 [`docs/PLATFORM-ROADMAP.md`](docs/PLATFORM-ROADMAP.md)）**：平台是 headless 商務 API ＋ 平台後台 ＋ 平台官網。
> 每家商家的店面與後台都由 merchant template repo 建立、並由 AI agent 只改設計層來重新設計，**不使用本 repo 託管的店面**。
> 因此本 repo 自己的**託管渲染**——`web/` 的 Next.js 租戶渲染店面、**主題與主題系統**、site／網域對應（Host 解析）——
> **作為產品已棄用**：目前仍保留且可運作，但不再演進、新商家不使用，之後會在另一個經明確核准的 change（`retire-hosted-storefront-and-themes`）中移除。
> **CMS 的內容功能不在棄用之列**：頁面與全域內容、schema 驗證、草稿／發佈、預覽、rich text 消毒、`/api/v1/render/*` 公開讀取與快取會被**保留並改造成
> headless 內容儲存**（商家用後台表單編輯的文字、banner、FAQ 等，由商家站台以 publishable key 讀取；見 OpenSpec change `headless-content-store`，提案、未實作）。

```
api/     Go headless API — 唯一的資料權威（chi + ent + PostgreSQL + Redis）
web/     Next.js storefront SSR — 消費渲染 bundle API，主題＝前端元件庫（託管渲染與主題**已棄用**；`web/` 將由平台官網取代或退役）
admin/   無建置步驟的薄殼 SPA — 登入、商品／訂單等營運、平台層開店與帳號（含 schema 驅動的內容編輯與發佈／預覽，屬保留並改造的內容儲存；開店精靈的主題／網域欄位已棄用）
e2e/     Playwright 端到端（CMS 內容流程＋依賴 `web/` 的租戶渲染斷言〔已棄用〕＋ 電商鏈）
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
make web-dev             # 前台 SSR :3000（已棄用的租戶渲染店面）
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
| **API 有哪些端點、request/response 長什麼樣** | [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml) — OpenAPI 3.1，涵蓋全部 156 條路由，由四道自動化測試守著 |
| **自帶前端要怎麼接入**（金鑰、CORS、訪客結帳、付款、查單） | [`docs/storefront-integration.md`](docs/storefront-integration.md) |
| **這個系統為什麼長這樣**（架構決策、取捨、已知限制） | [`openspec/`](openspec/) — 每個變更都有 proposal／design／spec／tasks |
| **平台往哪裡走、哪些功能已棄用** | [`docs/PLATFORM-ROADMAP.md`](docs/PLATFORM-ROADMAP.md)、[`docs/decisions/`](docs/decisions/) |
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
