## 1. `internal/ratelimit`：通用滑動視窗限流原語（design D1）

- [x] 1.1 新增 `api/internal/ratelimit/limiter.go`：`Limiter` 型別（持有 `*redis.Client` + `*slog.Logger`）、`New(rdb, log) *Limiter`、內嵌的 sliding-window-log Lua script（`redis.NewScript`）、`Allow(ctx, key string, limit int, window time.Duration) Result`（`Result{Allowed bool; RetryAfter time.Duration}`）。
- [x] 1.2 `Allow` 對 `l == nil`、`l.redis == nil`、Lua 執行錯誤、回傳格式異常一律 fail-open（`Result{Allowed: true}`）並在有 logger 時記 `slog.Warn`（design D6）。
- [x] 1.3 單元測試 `api/internal/ratelimit/limiter_integration_test.go`（`INTEGRATION=1`，比照 `render/cache_integration_test.go` 用 `testutil.OpenRedis(t)`）：
  - 正常流量放行（視窗內請求數 < limit 皆 `Allowed=true`）
  - 超限回 `Allowed=false` 且 `RetryAfter > 0`
  - 視窗過期後恢復（用短視窗如 200ms，sleep 過視窗後再次 `Allow` 應放行）
  - 不同 key 互不干擾（同時對兩個不同 key 打滿額度，互不影響對方計數）
  - Redis 不可用（連到已關閉的埠）時 fail-open

## 2. `internal/httpx`：429 回應 helper（design D7）

- [x] 2.1 `api/internal/httpx/httpx.go` 新增 `TooManyRequests(w http.ResponseWriter, retryAfter time.Duration)`：無條件進位到整秒（最少 1 秒）寫入 `Retry-After` header，body 走既有 `WriteError(w, 429, "rate_limited", ..., nil)`。

## 3. `internal/config`：可覆寫的限流門檻（design D4）

- [x] 3.1 `api/internal/config/config.go` 新增 `RateLimitConfig` struct（`LoginBroadLimit/Window`、`LoginTightLimit/Window`、`RegisterBroadLimit/Window`、`RegisterTightLimit/Window`、`RefreshLimit/Window`）與 `Config.RateLimit` 欄位。
- [x] 3.2 `Load()` 解析對應 10 個環境變數（`RATE_LIMIT_LOGIN_BROAD_LIMIT` 等），未設定時採 design D2 表列預設值；新增 `parseInt` helper（比照既有 `parseDuration`）。
- [x] 3.3 `.env.example` 補上這 10 個變數的預設值註記（沿用既有分區風格）。

## 4. `internal/httpapi`：路由專屬限流 middleware（design D2/D3/D5）

- [x] 4.1 新增 `api/internal/httpapi/ratelimitmw.go`：`rateLimitRule` 型別、`newAuthRateLimitMW`（跑完所有規則、任一超限即 429、`Retry-After` 取最大值）、`passthroughMW`/`orPassthrough`（nil-safe）。
- [x] 4.2 `clientIP(r *http.Request) string`：從 `r.RemoteAddr` 解析出 IP（`net.SplitHostPort`，失敗則回原字串）。
- [x] 4.3 `peekEmail(r *http.Request) string`：以 1 MiB 上限讀出 body 解析 `email` 欄位後用 `io.NopCloser` 換回 `r.Body`（design D3），不影響後續 `decodeJSON`。
- [x] 4.4 `ipRule`、`ipEmailRule`、`shopIPRule`、`shopIPEmailRule` 四個規則建構函式；`shopIP*` 系列從 `tenant.ShopID(r.Context())` 取值，取不到（`ok=false`）時該規則整條跳過。
- [x] 4.5 `NewAdminLoginRateLimit`、`NewAdminRefreshRateLimit`、`NewMemberLoginRateLimit`、`NewMemberRegisterRateLimit`、`NewMemberRefreshRateLimit` 五個建構函式，分別組裝對應規則（admin 用 `ip*`，member 用 `shopIP*`，refresh 只掛單一 broad 規則）。
- [x] 4.6 `router.go` 的 `Deps` 新增 5 個 `func(http.Handler) http.Handler` 欄位；5 個路由（`admin/auth/login`、`admin/auth/refresh`、`shop/auth/register`、`shop/auth/login`、`shop/auth/refresh`）改用 `.With(orPassthrough(d.XxxRateLimit))` 掛載，缺省 nil 時行為與現在完全一致（不影響既有測試）。

## 5. `internal/app/wire.go`：組裝

- [x] 5.1 建立 `ratelimit.New(rdb, a.log)`，用既有 `rdb`（不新增 Redis client/連線）。
- [x] 5.2 把 5 個 `httpapi.NewXxxRateLimit(limiter, cfg.RateLimit)` 結果指派到對應 `Deps` 欄位。

## 6. 整合測試：`internal/httpapi` 端到端行為

- [x] 6.1 新增 `api/internal/httpapi/ratelimit_integration_test.go`：`newAuthEnv` 的變體（或擴充既有 `authEnv`）額外注入真實 `ratelimit.Limiter`（`testutil.OpenRedis(t)`）與小門檻（如 limit=2, window=200ms，測試才不用等太久)。
- [x] 6.2 測試案例：
  - 正常流量放行（未達門檻的請求皆非 429）
  - 超限回 429，body 為 `{"error":{"code":"rate_limited",...}}`，帶 `Retry-After` header
  - 視窗過期後恢復（sleep 過視窗後重試應放行）
  - shop A 與 shop B 使用相同 IP + email 各自打滿門檻，互不干擾（shop A 超限不影響 shop B 的配額）——對應 spec「跨商家流量互不干擾」情境
  - 不同來源 IP（`req.RemoteAddr` 各自設定）對 admin login 互不干擾
  - refresh 端點超限回 429 且不進入 token 輪替（呼叫後 refresh token 仍可用於下一次成功的 rotate）

## 7. 驗證與收尾

- [x] 7.1 `cd api && go build ./...` 通過
- [x] 7.2 `cd api && golangci-lint run` 0 issues
- [x] 7.3 `cd api && go test ./...`（無 INTEGRATION）與 `cd api && INTEGRATION=1 go test -p 1 -count=1 ./...` 皆綠燈
- [x] 7.4 覆核 `router.go` 既有測試（`authflow_integration_test.go` 等）未受影響（未注入 RateLimit 欄位的既有 `httpapi.Deps` 用例行為不變）
