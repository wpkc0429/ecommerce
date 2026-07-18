# Design: auth-rate-limiting

## Context

`openspec/specs/authentication/spec.md`（Phase 1 歸檔）定義了後台 `users` 登入/refresh 與前台 `members` 註冊/登入/refresh 流程，但這些端點目前沒有任何嘗試次數限制。Phase 1 design.md（`openspec/changes/archive/2026-07-18-phase1-multitenant-cms-foundation/design.md`）的 Risks 已記載：

> [members 全平台唯一 email 有跨店帳號存在性洩漏面] → 註冊/登入回應與時序不區分「新建」與「掛會籍」；**速率限制**。

回應/時序已在 Phase 1 做了（`registerFailed` 統一 422、`auth.EqualizeVerifyTiming()`），速率限制當時沒有對應 task。這條 risk 之外，密碼暴力破解與帳號列舉是所有帳密登入端點的共通威脅，一併處理。

現行程式碼相關位置：
- `api/internal/httpapi/router.go`：`/api/v1/admin/auth/*`、`/api/v1/shop/auth/*` 路由掛載。
- `api/internal/httpapi/adminauth.go`、`memberauth.go`：handler 本體。
- `api/internal/httpapi/tenantmw.go`：會員路由的 `TenantMW` 把 `shop_id` 塞進 `r.Context()`（`tenant.ShopID(ctx)`），在 `sr.Use(d.TenantMW)` 之後才進入 auth handler——本次的會員限流 middleware 必須掛在 `TenantMW` **之後**才讀得到 shop_id。
- `api/internal/render/cache.go`、`api/internal/rbac/engine.go`：既有 Redis 用法慣例（直接持有 `*redis.Client`、nil 或錯誤一律 fail-open/degrade、`log/slog` 記警告不擋路）——本次沿用同一慣例。
- `api/internal/app/wire.go`：唯一組裝 Redis client、handler、middleware 的地方。
- `api/internal/testutil/testutil.go`：`OpenRedis(t)` 回傳 DB15、已 flush 的 Redis client，供 `INTEGRATION=1` 測試使用（`render/cache_integration_test.go`、`rbac/engine_integration_test.go` 皆用此模式）。

## Goals / Non-Goals

**Goals:**

- 後台 `POST /api/v1/admin/auth/login`、`POST /api/v1/admin/auth/refresh` 與會員 `POST /api/v1/shop/auth/register`、`POST /api/v1/shop/auth/login`、`POST /api/v1/shop/auth/refresh` 皆有 Redis-backed 速率限制。
- 限流鍵值正確域界（scope）：後台無租戶概念（一個 user 可能屬於多個 shop），故以 IP（+ IP/email 組合）為界；會員端點必須把 `shop_id` 納入鍵值，避免不同商家共用同一 bucket。
- 超限回 `429`，統一錯誤格式 `{"error":{"code":"rate_limited",...}}`，帶 `Retry-After`（秒）。
- Redis 故障時 fail-open（放行 + 記警告），不得讓速率限制器變成新的單點故障，鎖死所有登入。
- 門檻可由環境變數覆寫（比照現有 `ACCESS_TOKEN_TTL` 等慣例），有安全的預設值。

**Non-Goals:**

- 不做全域 WAF / DDoS 防護、不做 CAPTCHA、不做帳號鎖定（account lockout，那是持久化狀態而非速率限制，且會被拿來當成跨店帳號存在性探測的旁路——故意不做）。
- 不引入 X-Forwarded-For / 反向代理信任鏈解析（見 Open Questions）——目前沒有已知的反向代理拓撲，盲目信任 client 可控 header 會讓限流鍵可被偽造繞過，風險大於現在不支援。
- 不對其他非認證端點（如渲染 API、CMS 編輯 API）加限流——超出本次範圍，且它們已由 JWT/RBAC 保護，威脅模型不同。
- 不引入新的外部服務依賴（沿用專案既有 `go-redis/v9`）。

## Decisions

### D1. 演算法：Redis ZSET 滑動視窗日誌（sliding window log），單一 Lua script 原子操作

每次 `Allow(key, limit, window)` 呼叫執行一支 Lua script：

```lua
ZREMRANGEBYSCORE key -inf (now-window)   -- 修剪視窗外的舊嘗試
ZCARD key                                 -- 目前視窗內嘗試數
if count < limit:
  ZADD key now member; PEXPIRE key window; return {allowed=1, retryAfterMs=0}
else:
  return {allowed=0, retryAfterMs = 最舊嘗試時間 + window - now}
```

**取捨（vs. 固定視窗 INCR+EXPIRE、vs. token bucket）：**

- **固定視窗計數器**實作最簡單（`INCR` + `EXPIRE`），但有邊界突刺問題：攻擊者可在視窗邊界前後各打滿一次額度，短時間內達到 2× 限制——這正是要防的暴力破解場景，選它等於防護打對折。
- **Token bucket** 精確度相當，但需要自行維護「上次補充時間 + 剩餘 token 數」兩個欄位並在每次讀取時計算補充量，狀態管理比 ZSET 日誌更繁瑣；在認證端點這種低流量場景（每分鐘至多數十次請求）沒有明顯效能優勢。
- **ZSET 滑動視窗日誌**精確（無邊界突刺）、狀態自我描述（每個 member 本身就是一次嘗試的時間戳）、`PEXPIRE` 讓閒置 key 自然過期（不需額外清理 cron/SCAN），且 `Retry-After` 可直接從「最舊嘗試 + window」算出，語意明確。代價是 `ZADD`/`ZREMRANGEBYSCORE` 略重於單一 `INCR`，但單支 Lua script 仍是一次 Redis round-trip，且認證端點請求量遠低於渲染/CMS 熱路徑，這個代價可忽略。
- 全部包在一支 Lua script 內以確保「讀視窗內數量 → 判斷 → 寫入」的原子性；若拆成多次 Redis 呼叫（`ZREMRANGEBYSCORE` → `ZCARD` → 條件式 `ZADD`），並發請求會有 race window 讓限流失準（多個 goroutine/instance 同時通過 `ZCARD` 檢查）。

### D2. 限流維度與門檻：IP-scope broad + IP/shop+email-scope tight 雙層

每個「登入類」端點（admin login、member login、member register）套用**兩條規則**，任一超限即拒絕（回應 `Retry-After` 取兩者中較大值）：

| 規則 | 目的 | 鍵值 |
|---|---|---|
| broad | 擋單一來源對大量帳號的地毯式嘗試（enumeration / credential stuffing） | admin: `ip`；member: `shop_id + ip` |
| tight | 擋針對單一帳號的暴力破解（brute force） | admin: `ip + email`；member: `shop_id + ip + email` |

`refresh` 端點只套用單一 `ip`（admin）或 `shop_id + ip`（member）規則——refresh token 本身是 30 天效期、雜湊儲存的高熵不透明亂數（`design.md` D9），不像密碼一樣可被字典攻擊，速率限制在此的目的是限制濫用/DoS 而非擋暴力猜測，故不需要 email 維度（refresh 請求本來就不帶 email）。

**為何 member 端點必須含 `shop_id`，admin 端點刻意不含**：會員路由掛在 `TenantMW` 之後，每個請求已解析出唯一 `shop_id`（design D5）；商家 A 的高流量（正常或惡意）如果與商家 B 共用同一把 bucket，會造成互相影響的降級（跨租戶隔離失效，違反 multi-tenancy 的核心保證）。後台 `users` 沒有「當前 shop」的請求態概念——一個 user 可能同時屬於多個 shop（`sids` 陣列），登入當下還不知道要操作哪個 shop，因此 admin 端點以 IP 為界是唯一合理的選擇，並非遺漏。

**門檻預設值**（環境變數可覆寫，見 D4）：

| 端點類 | broad limit/window | tight limit/window |
|---|---|---|
| admin login | 20 / 5m（IP） | 5 / 5m（IP+email） |
| member login | 20 / 5m（shop+IP） | 5 / 5m（shop+IP+email） |
| member register | 10 / 10m（shop+IP） | 3 / 10m（shop+IP+email） |
| admin/member refresh | 30 / 5m（IP 或 shop+IP，單一規則） | — |

數字選擇理由：正常使用者打錯密碼 2-3 次是常態，5 次/5 分鐘足夠寬容真人重試、同時讓自動化暴力破解的有效速率降到接近無用（字典攻擊需要數千到數百萬次嘗試才有意義）；20 次/5 分鐘的 broad 規則只在單一 IP 對大量不同帳號嘗試時觸發，不影響單一帳號重試多次的真人；register 門檻更緊是因為正常使用者的註冊行為天生比登入稀疏（一個人不會一分鐘內連續註冊好幾次）。這些數字非一次到位的科學結論，是可調參數（見 D4），上線後可依實際濫用模式微調。

### D3. 鍵值抽取實作細節：peek email 不消耗 request body

Login/Register 的 email 在 JSON body 而非 header/query，中介層需要「偷看」`email` 欄位但不能讓後續 handler 的 `decodeJSON` 讀到空 body。做法：`peekEmail` 以 `io.LimitReader(r.Body, 1<<20)`（與 `decodeJSON` 相同的 1 MiB 上限）讀出整個 body，解析出 `email` 後用 `io.NopCloser(bytes.NewReader(body))` 換回 `r.Body`，handler 端完全無感知。JSON 解析失敗或欄位缺漏時 `peekEmail` 回空字串，tight 規則的 `keyFn` 回傳 `ok=false` 直接跳過該規則（僅套用 broad 規則）——畸形請求不會讓中介層自己噴 500，交給 handler 自己的 `decodeJSON` 去回 400。

### D4. 組態：`config.RateLimitConfig`，環境變數覆寫、有安全預設值

比照 `AccessTokenTTL`/`RefreshTokenTTL` 的既有慣例，`config.Config` 新增 `RateLimit RateLimitConfig` 欄位，10 個環境變數（`RATE_LIMIT_LOGIN_BROAD_LIMIT`、`RATE_LIMIT_LOGIN_BROAD_WINDOW`、`RATE_LIMIT_LOGIN_TIGHT_LIMIT`、`RATE_LIMIT_LOGIN_TIGHT_WINDOW`、`RATE_LIMIT_REGISTER_BROAD_LIMIT`、`RATE_LIMIT_REGISTER_BROAD_WINDOW`、`RATE_LIMIT_REGISTER_TIGHT_LIMIT`、`RATE_LIMIT_REGISTER_TIGHT_WINDOW`、`RATE_LIMIT_REFRESH_LIMIT`、`RATE_LIMIT_REFRESH_WINDOW`），未設定時採 D2 表列預設值，不需要在生產環境強制設定（不像 `ADMIN_JWT_SECRET` 那樣是安全關鍵到必須要求）。

### D5. 套件邊界：`internal/ratelimit`（通用原語）＋ `internal/httpapi` 內的路由專屬 wiring

- `internal/ratelimit`：只提供 `Limiter.Allow(ctx, key string, limit int, window time.Duration) Result`，不知道 IP/shop/email/HTTP 是什麼——單純的 Redis 滑動視窗原語，可被未來任何端點重用（例如 tasks 2-9 之後的其他 write API 若也要限流）。
- `internal/httpapi/ratelimitmw.go`：知道怎麼從 `*http.Request` 抽取 IP/shop/email、怎麼組鍵、怎麼掛 chi middleware、怎麼寫 429 回應——這層才是「認證端點」的業務決策（D2 的門檻/維度）。
- 這個切法讓 `ratelimit` package 的單元測試不必知道任何 HTTP/租戶語意，`httpapi` 層的測試則直接對 HTTP 路由斷言行為（比照現有 `authflow_integration_test.go` 的測法）。

### D6. Fail-open，不是新的可用性風險點

`ratelimit.Limiter.Allow` 在 Redis 為 nil、Lua script 執行錯誤、或回傳格式異常時一律回傳 `Result{Allowed: true}`（放行）並記 `slog.Warn`——與 `render.Cache`、`rbac.Engine` 遇 Redis 故障時的 degrade 行為一致（Phase 1 design.md Risk：「單 Redis 故障時快取與權限快取同時失效」→「全部快取路徑皆有 DB fallback」）。速率限制是縱深防禦的一層，不是認證正確性的來源；犧牲一次 Redis 故障期間的暴力破解防護，好過讓 Redis 故障變成「所有人都無法登入」的全站中斷。

### D7. 錯誤回應：新增 `httpx.TooManyRequests`，`Retry-After` 以整秒回報

延伸既有 `internal/httpx` 的錯誤 helper 慣例（design D12 的 401/403/404/422/503 之外新增 429）：

```go
func TooManyRequests(w http.ResponseWriter, retryAfter time.Duration)
```

內部把 `retryAfter` 無條件進位到整秒（Lua script 算出的是毫秒），最少回 1 秒（避免 0 秒的 `Retry-After` 造成 client 端立即重試迴圈），寫入 `Retry-After` header 再走 `WriteError(w, 429, "rate_limited", ..., nil)`，維持統一錯誤格式。

## Risks / Trade-offs

- [ZSET 滑動視窗比固定視窗略重（每次請求都要 ZREMRANGEBYSCORE + ZCARD，超限時還要 ZRANGE）] → 認證端點請求量遠低於渲染熱路徑，且全部包在單一 Lua script 原子執行（一次 round-trip），可忽略；未來若真的成為瓶頸，可換成近似滑動視窗計數器（two fixed windows weighted）而不改對外介面。
- [Redis 故障時速率限制形同虛設（fail-open）] → 刻意的權衡（D6）：可用性優先於「這一層」防護；密碼本身仍是 Argon2id 雜湊、refresh token 仍會輪替偵測重放，暴力破解防線不是只靠這一層。
- [IP-scope 對 NAT/公司網路後的多使用者不公平（一人超限，其他人被誤傷）] → tight 規則已把 email 納入鍵值降低誤傷（除非攻擊者剛好打中同一 email），broad 規則的門檻（20/5m）刻意設得比單一真人正常重試量寬鬆很多；長期若發現誤傷需求，可加大 broad 門檻或改用其他訊號，本次不過度設計。
- [不支援 X-Forwarded-For，若未來部署在反向代理/LB 後方，所有請求會共用同一個「代理 IP」，broad 規則形同對全站生效] → 記錄為 Open Question；等實際部署拓撲定案（是否有固定可信代理）再實作 trusted-proxy 鏈解析，避免現在猜錯設計成可被偽造繞過的洞。
- [`peekEmail` 需要把整個 request body 讀進記憶體再放回去] → 已用 `io.LimitReader` 蓋 1 MiB 上限（與 `decodeJSON` 一致），且僅套用於本來就該讀 body 的 POST 端點，記憶體/延遲成本可忽略。

## Migration Plan

Greenfield 新增（無資料遷移、無既有資料格式變更）：

1. `internal/ratelimit`：`Limiter` + Lua script + 單元測試（`INTEGRATION=1` 用真 Redis，比照 `render`/`rbac` 慣例）。
2. `internal/httpx`：新增 `TooManyRequests`。
3. `internal/config`：新增 `RateLimitConfig` + 10 個環境變數解析，`.env.example` 補上預設值註記。
4. `internal/httpapi`：新增 `ratelimitmw.go`（規則組裝、chi middleware、`orPassthrough` nil-safe 包裝），`router.go` 的 5 個路由（admin login/refresh、member register/login/refresh）掛上對應中介層，`Deps` 新增對應欄位（缺省 nil → 不限流，既有測試不受影響）。
5. `internal/app/wire.go`：組裝 `ratelimit.Limiter`（沿用既有 `rdb`）與 5 個 middleware 塞進 `Deps`。
6. 整合測試：新增 `httpapi` 測試檔覆蓋 429、視窗過期恢復、shop/IP 互不干擾（沿用 `authflow_integration_test.go` 的 `newAuthEnv` 測法，額外注入 rate limit middleware）。

不需要 rollback 特殊處理（新增程式碼路徑，關閉方式是不設定/移除 middleware 或調大門檻）。

## Open Questions

- 若未來部署於反向代理/LB 之後，需要定義可信代理鏈與 `X-Forwarded-For`/`X-Real-IP` 解析規則（見 Risks）——留待實際部署拓撲確定後另立 change。
- 是否需要「連續超限自動延長封鎖時間」（exponential backoff on repeated violations）——本次維持固定視窗門檻，觀察實際濫用模式再決定是否加碼。
