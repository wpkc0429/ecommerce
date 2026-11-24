package app

// 接線防腐機制 5／6／7（change contract-router-completeness）。
//
// `wire` 是全系統唯一的真實接線處，而它做的三件事全部是「不接也能編譯、不接也全綠」的事：
//
//	deps.X = &httpapi.XHandler{...}   決定 router.go 掛不掛 X 的那一組路由
//	handler.Dep = someService         決定該 handler 的某個功能是否作用（如 Idempotency）
//	dispatcher.Subscribe(svc.Handle)  決定跨套件事件鏈成不成立
//
// 這三件事的共同性質是**漏掉它們不會壞掉任何測試**：既有的每一支整合測試都自建 fixture，
// 因此結構上只能回答「如果接對了，行為對不對」，永遠回答不了「有沒有接」。已實測的兩個
// 後果：新增 handler 不接進契約測試 → 兩條零文件路由全綠通過；刪掉
// `dispatcher.Subscribe(pointsService.Handle)` → `make test-int` 全綠而真實伺服器不發點數。
//
//	檢查 5  TestWireFillsEveryDependency             每一項宣告的相依都被賦值（遞迴）
//	檢查 6  TestWiredRouteTableMatchesContractSource 真實 app.New 的路由表 == 契約檢查走的路由表
//	檢查 7  TestWiredDispatcherSubscribers           真實 dispatcher 的訂閱者集合（相等，非包含）
//
// ── 為什麼是這裡，不是 e2e ────────────────────────────────────────────────
//
// change commerce-demo-seed 的 design D1 曾把這一段判給 e2e，理由有二。第一條——「抽出
// 一個可測的接線函式，那個新間接層自己也可能沒被 wire 呼叫」——完全正確，而且**本檔沒有
// 抽出任何東西**：檢查 5／7 呼叫的是 `a.wire` 本身，檢查 6 呼叫的是 `app.New` 本身。
// 第二條——「需要真實基礎設施的話，它與 e2e 的差別只剩下用哪個語言寫斷言」——在事實上
// 不成立，而那個差別恰恰是決定性的：`INTEGRATION=1 go test ./...` 在 CI 裡，
// `node commerce-path.mjs` 不在（那份 design 自己記錄的事實）。
//
// ── 分工，明寫 ───────────────────────────────────────────────────────────
//
// 本檔只回答「有沒有接」。「接上去之後行為對不對」是既有六支整合測試與
// e2e/commerce-path.mjs 的工作，本檔不宣稱涵蓋它們，它們也不宣稱涵蓋本檔——
// 這條縫正是本 change 存在的理由。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// wiredDepsExemptions lists dependencies that may legitimately be nil after wire.
//
// **It is empty, and that is a measured fact, not an aspiration** — today's wire
// fills every reachable exported dependency. Adding an entry costs a written
// reason, the same discipline as the contract check's money exemptions: the
// failure this audit exists to catch (checkout-idempotency's Idempotency field —
// unset, all routes still mounted, entire Go suite green, server silently ignores
// Idempotency-Key) looks exactly like an optional dependency until it bites.
var wiredDepsExemptions = map[string]string{}

// bootWire runs the real wiring against the integration database and returns the
// Deps it produced. Cfg/Log mirror the two lines App.New sets before calling wire;
// everything else in the returned value is wire's own work.
func bootWire(t *testing.T) (*App, httpapi.Deps) {
	t.Helper()
	testutil.OpenDB(t) // migrations + truncation; also asserts INTEGRATION=1

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.DatabaseURL = os.Getenv("TEST_DATABASE_URL")
	if cfg.DatabaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	a := &App{cfg: cfg, log: log}
	t.Cleanup(a.Close)
	deps := httpapi.Deps{Cfg: cfg, Log: log}
	if err := a.wire(context.Background(), &deps); err != nil {
		t.Fatalf("wire: %v", err)
	}
	return a, deps
}

// TestWireFillsEveryDependency — 檢查 5.
//
// Walks everything reachable from the Deps the real wire produced and fails on any
// unset dependency. The recursion matters: a nil handler removes routes (which
// check 6 would also catch), but a nil dependency *inside* a handler removes a
// feature while the route table stays byte-for-byte identical. Only this audit
// sees that one.
func TestWireFillsEveryDependency(t *testing.T) {
	testutil.RequireIntegration(t)
	_, deps := bootWire(t)

	unwired := testutil.NilDependencyPaths(deps, "Deps", wiredDepsExemptions)
	if len(unwired) > 0 {
		t.Errorf("%d 項相依在真實的 app/wire.go 中沒有被接上——"+
			"漏接 handler 會讓它的路由在生產環境回 404，漏接 handler 底下的相依則會讓某個功能"+
			"靜默失效（路由數不變、所有既有測試照樣全綠）：\n  %s",
			len(unwired), strings.Join(unwired, "\n  "))
		return
	}
	t.Logf("真實接線稽核通過；已登記豁免 %d 項", len(wiredDepsExemptions))
}

// TestWiredRouteTableMatchesContractSource — 檢查 6.
//
// The contract's route-coverage check (internal/httpapi) walks a router the test
// builds itself, because it must run without infrastructure. That router is only
// worth anything if it is the same router a real server boots with.
//
// Why they are the same is an argument: every mount decision in httpapi.New is a
// nil check on a Deps field, so two Deps values with no nil fields must produce
// identical route tables. **This test is what keeps the argument true.** The day
// someone writes a mount condition that is not a nil check (`if d.Cfg.Env != …`),
// the argument silently stops holding — and this comparison turns red instead.
func TestWiredRouteTableMatchesContractSource(t *testing.T) {
	testutil.RequireIntegration(t)
	testutil.OpenDB(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.DatabaseURL = os.Getenv("TEST_DATABASE_URL")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The real entry point cmd/server uses — not wire, because New is what sets
	// Cfg/Log and what decides whether wire runs at all.
	a, err := New(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(a.Close)
	production := testutil.RouteTable(t, a.Router())

	// The reference side is built the same way the contract check builds its
	// router: filled from the Deps struct by reflection, never from a list.
	var reference httpapi.Deps
	testutil.FillNonNil(t, &reference, map[string]any{
		"Log":         log,
		"CORSOrigins": nullOriginRegistry{}, // interface — FillNonNil rule 5 requires an entry
	})
	contract := testutil.RouteTable(t, httpapi.New(reference))

	failed := false
	if missing := difference(contract, production); len(missing) > 0 {
		failed = true
		t.Errorf("%d 條路由存在於契約檢查所走的 router，但真實伺服器沒有註冊——"+
			"契約檢查正在對一個不存在的伺服器做保證：\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	if extra := difference(production, contract); len(extra) > 0 {
		failed = true
		t.Errorf("%d 條路由存在於真實伺服器，但契約檢查所走的 router 沒有——"+
			"這些路由不會被路由覆蓋檢查列舉，因此可以完全沒有文件而全綠：\n  %s",
			len(extra), strings.Join(extra, "\n  "))
	}
	if !failed {
		t.Logf("真實伺服器與契約檢查的路由表逐條相同（%d 條）", len(production))
	}
}

// nullOriginRegistry mirrors the contract check's override: an OriginRegistry that
// answers "no". It changes no route — it only decides whether CORS is mounted —
// but leaving the field nil would break the "every field is set" premise the route
// tables' equality rests on.
type nullOriginRegistry struct{}

func (nullOriginRegistry) LookupOrigin(context.Context, string) (int, string, bool) {
	return 0, "", false
}

func difference(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// expectedSubscribers is the locked subscriber set of the one domain-event
// dispatcher, as function symbols (see normalizeSubscriber for the anonymous-
// closure rule).
//
// **Comparison is equality, not containment, and that is deliberate.** Removing a
// subscription breaks a cross-package chain silently; adding one *creates* a
// cross-package chain that nobody has been asked to cover. Both must stop the
// build and make someone edit this list on purpose — which is also the moment to
// answer "does e2e need an assertion for this?", the question
// commerce-demo-seed left open.
var expectedSubscribers = []string{
	"ksdevworks/ecommerce/api/internal/app.(*App).wire.<closure>",   // SiteMappingChanged → host/key cache
	"ksdevworks/ecommerce/api/internal/app.(*App).wire.<closure>",   // StorefrontClientChanged → key/origin cache
	"ksdevworks/ecommerce/api/internal/points.(*Service).Handle-fm", // 付款核發點數 / 退貨扣回
	"ksdevworks/ecommerce/api/internal/render.(*Invalidator).Handle-fm",
	// change order-notification-email: 訂單成立信 / 付款成功信。
	//
	// 這一行是本 change 被迫明示的那個決定，順便回答了它逼出的問題——「e2e 要不要補
	// 一條斷言？」答案是不要，理由是可觀測性：本 change 刻意沒有新增任何 API（寄送
	// 狀態不長出端點），而 demo 環境沒有 SMTP 憑證所以根本不寄信，因此 e2e 唯一能斷
	// 言的是「寄信沒把結帳弄壞」——那已經被既有的每一條結帳斷言涵蓋。取而代之的覆蓋
	// 是本檢查（有沒有接）＋ internal/notify 的整合測試（接上之後寄了什麼、重複投遞
	// 只寄一封、跨店不外洩、金額組成正確）。完整論證見 design D11。
	"ksdevworks/ecommerce/api/internal/notify.(*Service).Handle-fm",
}

// closureSuffix matches the compiler's numbering of anonymous functions.
//
// The numbers are position-dependent: `deps.Health`'s closure is func1, so the two
// cache-invalidation subscribers are func2/func3, and inserting any unrelated
// closure earlier in wire renumbers them. Locking the raw names would then produce
// a **false alarm** for a change that touched no subscription at all, and false
// alarms are the fastest way to kill an anti-rot check (the same rule the v1
// surface lock is built on). Normalizing keeps every real signal — a closure
// subscription removed (2→1), added (2→3), or a named subscriber gone — and drops
// only the ability to tell two closures apart, which code review covers.
var closureSuffix = regexp.MustCompile(`\.func\d+(\.\d+)*$`)

func normalizeSubscriber(name string) string {
	return closureSuffix.ReplaceAllString(name, ".<closure>")
}

// TestWiredDispatcherSubscribers — 檢查 7.
func TestWiredDispatcherSubscribers(t *testing.T) {
	testutil.RequireIntegration(t)
	_, deps := bootWire(t)

	// One dispatcher, or the lock guards the wrong object: a publisher holding a
	// second dispatcher produces exactly the same symptom as no subscription at
	// all, and the set below would still be perfectly correct.
	dispatcher := deps.Sites.Dispatcher
	for name, held := range map[string]*events.Dispatcher{
		"ClientAuth": deps.ClientAuth.Dispatcher,
		"Shops.cms":  deps.Shops.Service.Dispatcher,
		"Themes.cms": deps.Themes.Service.Dispatcher,
		"Pages.cms":  deps.Pages.Service.Dispatcher,
		"Payments":   deps.Payments.Service.Dispatcher,
		"Shipping":   deps.Shipping.Service.Dispatcher,
		// change order-notification-email: order.Service became a publisher
		// (events.OrderPlaced from inside Checkout). A second dispatcher here
		// would mean every order confirmation email silently stops — with the
		// subscriber set below still perfectly correct.
		"Orders": deps.Orders.Service.Dispatcher,
	} {
		if held != dispatcher {
			t.Errorf("%s 持有的 dispatcher 不是同一個實例——"+
				"發佈端與訂閱端分屬兩個 dispatcher，效果與沒有訂閱完全相同", name)
		}
	}

	var got []string
	for _, h := range dispatcher.Subscribers() {
		got = append(got, normalizeSubscriber(runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()))
	}
	sort.Strings(got)
	want := append([]string(nil), expectedSubscribers...)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("領域事件的訂閱者集合與鎖定的清單不符。\n"+
			"少了訂閱＝該條跨套件事件鏈靜默斷裂（事件照發、所有測試照綠、效果不再發生）；\n"+
			"多了訂閱＝新增了一條沒有人被要求覆蓋的事件鏈。兩者都必須手動更新 expectedSubscribers。\n"+
			"  預期：\n    %s\n  實際：\n    %s",
			strings.Join(want, "\n    "), strings.Join(got, "\n    "))
		return
	}
	t.Logf("事件訂閱鎖：%d 個訂閱者全部相符", len(got))
}

// Compile-time proof that the override above really satisfies the interface the
// Deps field declares (FillNonNil would fail at run time otherwise).
var _ httpapi.OriginRegistry = nullOriginRegistry{}
