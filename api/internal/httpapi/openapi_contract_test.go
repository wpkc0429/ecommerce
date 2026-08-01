package httpapi

// 契約防腐機制 1／2／4（change openapi-contract, design D2）。
//
// 這三道檢查刻意**不需要任何基礎設施**（不碰資料庫、不碰 Redis、不讀 config），
// 所以 `make test` 就會跑到它們，而 CI 既有的 `go test ./...` 自動涵蓋——
// `.github/workflows/ci.yml` 一個字都不必改。這是選址的理由本身：姊妹 change
// commerce-demo-seed 實測發現 e2e 不在 CI 內，一個不在 CI 的防腐機制擋不住它要擋的疏忽。
//
//	檢查 1  TestOpenAPICoversEveryRoute       chi.Walk 的真實路由表 vs 文件的 paths
//	檢查 2  TestOpenAPIMoneyFieldsDeclareUnit 金額欄位必須 $ref MinorUnitAmount
//	檢查 4  TestOpenAPIV1SurfaceLock          v1 已承諾的表面不得消失
//
// 重新產生表面鎖檔（**刪行代表破壞 v1，請確認你真的要這麼做**）：
//
//	cd api && go test ./internal/httpapi/ -run TestOpenAPIV1SurfaceLock -openapi.update-surface
//
// ── 檢查 4 的涵蓋範圍與其邊界（change api-contract-parameter-lock）─────────
//
// 鎖檔收錄五種行：路由、具名 schema 的屬性、端點接受的請求參數、該參數可否省略、
// 端點承諾的回應標頭。參數與標頭一律**以端點為單位**記錄而非以 components 元件為
// 單位——消費端依賴的是「這條端點接受 X-Publishable-Key」，把某條端點的
// `- $ref: PublishableKey` 刪掉而保留元件定義是一次真實的破壞，元件層級的鎖會沉默。
//
// **required 的方向性住在產生器，不住在檢查器。** 選填參數產生兩行（「接受」＋
// 「可省略」），必填參數只產生一行。於是 false→true（收緊）讓 optional 那一行消失
// 而轉紅，true→false（放寬）只是新增一行而放行——檢查器那句唯一的規則（「鎖檔的
// 每一行今天仍存在於契約中」）一個字都不必改。這句話短到可以在 code review 時原地
// 驗證，那正是鎖檔可信的原因。
//
// **明寫的邊界：本鎖不記錄參數的型別、pattern、長度、值域、敘述與範例。** 因此把
// Idempotency-Key 的 maxLength 從 255 改成 32（收緊 ＝ 破壞）本鎖**沉默**。這是取捨
// 不是疏漏：約束的破壞方向是「收緊」，而收緊在鎖檔裡表現為新增，要擋住它得讓檢查器
// 變成 JSON Schema 相容性判定器；同時放寬 pattern、補 example、修一句敘述全部會轉紅
// ——**假警報是防腐機制最快的死法**。可機械化又不誤報的部分本鎖百分之百擋住，其餘
// 不宣稱擋得住。
//
// ── chi.Walk 的已知盲點（design D3a）─────────────────────────────────────
//
// `router.go` 在 `/shops/{shopID}` 這個樣板上同時掛了一個子 router 與兩個方法處理器
// （`Shops.MountPlatform` 的 GET/PUT）。chi 執行期兩者都正常，但 `Walk` 走到該節點時
// 會下潛進子 router，不回報節點自身的方法處理器。因此比對規則不是集合相等，而是：
//
//	(a) Walk 到的每一條都必須在文件中——嚴格，無例外；
//	(b) 文件有而 Walk 沒有的每一條，必須通過「實際發請求」的可達性探測。
//
// **殘留風險，明寫：** 若日後有人在同一種樣板衝突下新增端點，它既不會被 Walk 到、
// 也不會在文件中，兩道檢查都會沉默。要封死它得繞過 chi.Walk 自己走 trie 的私有結構，
// 代價與收益不成比例。新增端點時若發現它沒出現在 Walk 清單裡，那就是踩到了這個坑。

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// updateSurface regenerates api/openapi/v1-surface.txt instead of checking it.
var updateSurface = flag.Bool("openapi.update-surface", false,
	"rewrite api/openapi/v1-surface.txt from openapi.yaml (removing a line breaks v1)")

const (
	openAPIRelPath  = "../../openapi/openapi.yaml"
	surfaceRelPath  = "../../openapi/v1-surface.txt"
	minorUnitRef    = "#/components/schemas/MinorUnitAmount"
	surfaceFileHead = `# v1 表面鎖 — 每一行是 /api/v1 已對外做出的一個承諾。
#
# 產生方式：cd api && go test ./internal/httpapi/ -run TestOpenAPIV1SurfaceLock -openapi.update-surface
#
# 檢查規則（internal/httpapi/openapi_contract_test.go）：本檔的每一行都必須仍存在於
# api/openapi/openapi.yaml 中。**新增自由，刪除必須手動刪掉這裡的那一行。**
#
# 刪除任一行 ＝ 破壞 v1。design D13 的破例窗口已於 change order-extras 用盡並關閉：
# 後續的破壞性變更 MUST 改走新端點或 /api/v2，MUST NOT 再引用「尚無外部消費者」。
#
# 行的五種形式：
#   METHOD /path                                一條公開路由
#   SchemaName.property                         一個具名 component schema 的屬性
#   METHOD /path <in> <name>                    該端點接受此請求參數（<in> ＝ header|query|path）
#   METHOD /path <in> <name> optional           且此參數可以省略
#   METHOD /path response <code> <Header-Name>  該端點承諾在此狀態碼回應此標頭
#
# 選填參數刻意產生**兩行**：「接受它」與「可以不帶它」是兩個獨立的承諾。把選填改成
# 必填會讓 optional 那一行消失（＝破壞，轉紅）；把必填改成選填只是新增一行（＝放寬，
# 放行）。方向性因此由事實本身表達，檢查規則維持「每一行都必須還在」這一句話。
#
# 參數與標頭以**端點**為單位。刪掉某條端點的 ` + "`- $ref: PublishableKey`" + ` 而保留元件定義，
# 仍然是破壞——本檔會有對應的行消失。
#
# 本鎖**不**記錄參數的型別／pattern／長度／值域／敘述／範例：改敘述、補範例、放寬
# 值域都不該轉紅（假警報是防腐機制最快的死法）。代價是「把約束改嚴」本鎖沉默，
# 那一段的防線是 code review，見 openapi_contract_test.go 的檔頭。
`
)

var httpMethods = map[string]string{
	"get": http.MethodGet, "post": http.MethodPost, "put": http.MethodPut,
	"delete": http.MethodDelete, "patch": http.MethodPatch,
	"head": http.MethodHead, "options": http.MethodOptions,
}

// ── 文件載入 ──────────────────────────────────────────────────────────

// loadOpenAPI reads the YAML contract and normalizes it through JSON so that
// numbers become json.Number and every map key is a string — the shape both the
// checks below and jsonschema/v6 expect (design D4).
func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(openAPIRelPath)
	if err != nil {
		t.Fatalf("read %s: %v", openAPIRelPath, err)
	}
	var viaYAML any
	if err := yaml.Unmarshal(raw, &viaYAML); err != nil {
		t.Fatalf("parse %s as YAML: %v", openAPIRelPath, err)
	}
	asJSON, err := json.Marshal(viaYAML)
	if err != nil {
		t.Fatalf("re-encode %s as JSON: %v", openAPIRelPath, err)
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(asJSON))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", openAPIRelPath, err)
	}
	return doc
}

// documentedRoutes returns the "METHOD /path" set declared by the contract.
func documentedRoutes(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("openapi.yaml has no paths object")
	}
	out := map[string]bool{}
	for p, item := range paths {
		ops, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("paths%s is not an object", p)
		}
		for key := range ops {
			if m, isMethod := httpMethods[key]; isMethod {
				out[m+" "+p] = true
			}
		}
	}
	return out
}

// ── 檢查 1：路由覆蓋 ───────────────────────────────────────────────────

// contractRouter builds the production router with zero-value handlers.
//
// This works because httpapi.New only ever nil-checks Deps fields, and the RBAC
// guards return closures without dereferencing their receiver at mount time —
// so the route table is identical to production's without a database, a Redis,
// or a config file anywhere in sight.
func contractRouter() http.Handler {
	passthrough := func(next http.Handler) http.Handler { return next }
	return New(Deps{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuth:     &AdminAuthHandler{},
		MemberAuth:    &MemberAuthHandler{},
		Roles:         &RolesHandler{},
		Sites:         &SitesHandler{},
		Shops:         &ShopsHandler{},
		Themes:        &ThemesHandler{},
		Pages:         &PagesHandler{},
		Render:        &RenderHandler{},
		Categories:    &CategoriesHandler{},
		Products:      &ProductsHandler{},
		Cart:          &CartHandler{},
		Orders:        &OrderHandler{},
		Payments:      &PaymentHandler{},
		Shipping:      &ShippingHandler{},
		Points:        &PointsHandler{},
		ClientAuth:    &ClientAuthHandler{},
		Guest:         &GuestHandler{},
		AdminMW:       passthrough,
		TenantMW:      passthrough,
		MemberMW:      passthrough,
		OrderAccessMW: passthrough,
	})
}

func walkedRoutes(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("router is not a chi.Routes; the coverage check cannot enumerate it")
	}
	out := map[string]bool{}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return out
}

var pathParamRe = regexp.MustCompile(`\{[^/}]+\}`)

// probeReachable reports whether METHOD+path resolves to some handler. Anything
// other than 404/405 proves the route exists (401 from an auth guard, 500 from a
// zero-value handler dereferencing nil — both mean ServeHTTP found a handler).
func probeReachable(h http.Handler, method, pathTemplate string) (bool, int) {
	concrete := pathParamRe.ReplaceAllString(pathTemplate, "1")
	req := httptest.NewRequest(method, concrete, http.NoBody)
	req.Host = "probe.invalid"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	code := rec.Code
	return code != http.StatusNotFound && code != http.StatusMethodNotAllowed, code
}

func TestOpenAPICoversEveryRoute(t *testing.T) {
	doc := loadOpenAPI(t)
	documented := documentedRoutes(t, doc)

	router := contractRouter()
	walked := walkedRoutes(t, router)

	// (a) Every route the server actually registers MUST be documented.
	var undocumented []string
	for route := range walked {
		if !documented[route] {
			undocumented = append(undocumented, route)
		}
	}
	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("%d 條已註冊的路由不在 api/openapi/openapi.yaml 中——新增端點時必須同步更新契約：\n  %s",
			len(undocumented), strings.Join(undocumented, "\n  "))
	}

	// (b) Everything documented but not walked MUST be proven reachable, which
	// absorbs chi.Walk's blind spot (design D3a) without a hand-kept whitelist.
	var ghosts []string
	var blindSpots []string
	for route := range documented {
		if walked[route] {
			continue
		}
		method, pathTemplate, ok := strings.Cut(route, " ")
		if !ok {
			t.Fatalf("malformed documented route %q", route)
		}
		reachable, code := probeReachable(router, method, pathTemplate)
		if reachable {
			blindSpots = append(blindSpots, fmt.Sprintf("%s (probe %d)", route, code))
			continue
		}
		ghosts = append(ghosts, fmt.Sprintf("%s (probe %d)", route, code))
	}
	sort.Strings(ghosts)
	sort.Strings(blindSpots)
	if len(ghosts) > 0 {
		t.Errorf("%d 條路由寫在契約中但實作沒有——消費端照著寫會拿到 404：\n  %s",
			len(ghosts), strings.Join(ghosts, "\n  "))
	}
	if len(blindSpots) > 0 {
		t.Logf("chi.Walk 未列出但實測可達的路由（design D3a）：\n  %s", strings.Join(blindSpots, "\n  "))
	}
	t.Logf("契約覆蓋 %d 條路由（chi.Walk 列出 %d 條 + Walk 盲點 %d 條）",
		len(documented), len(walked), len(blindSpots))
}

// ── 檢查 2：金額單位紀律 ────────────────────────────────────────────────

// moneyProperty reports whether a property name carries monetary semantics.
// Deliberately name-based: the whole point is to catch a *new* money field
// someone adds without thinking about units.
func moneyProperty(name string) bool {
	switch name {
	case "amount", "subtotal", "total", "line_total", "flat_rate":
		return true
	}
	return strings.HasSuffix(name, "_amount") ||
		strings.HasSuffix(name, "_fee") ||
		strings.HasPrefix(name, "price")
}

// moneyExemptions are properties whose name matches moneyProperty but which are
// not money. Each entry needs a reason; the list is meant to stay tiny.
var moneyExemptions = map[string]string{
	// Pagination counts, not amounts. `total` is unfortunately the natural name
	// for both "how many rows match" and "how much does this cost"; the two are
	// told apart by which schema they live in, not by the key.
	"/components/schemas/ProductPage/properties/total":          "pagination count",
	"/components/schemas/OrderPage/properties/total":            "pagination count",
	"/components/schemas/ShopPage/properties/total":             "pagination count",
	"/components/schemas/PointTransactionPage/properties/total": "pagination count",
}

// refsMinorUnit accepts either a bare $ref to MinorUnitAmount or a single-element
// allOf wrapper (the OpenAPI-idiomatic way to attach a per-field description to
// a $ref without redefining the type).
func refsMinorUnit(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if ref, ok := m["$ref"].(string); ok {
		return ref == minorUnitRef
	}
	all, ok := m["allOf"].([]any)
	if !ok || len(all) != 1 {
		return false
	}
	inner, ok := all[0].(map[string]any)
	if !ok {
		return false
	}
	ref, _ := inner["$ref"].(string)
	return ref == minorUnitRef
}

// walkProperties visits every "properties" object anywhere in the document and
// reports each (jsonPointer, name, schema).
func walkProperties(node any, pointer string, visit func(pointer, name string, schema any)) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			child := pointer + "/" + escapePointer(k)
			if k == "properties" {
				if props, ok := v.(map[string]any); ok {
					for name, sub := range props {
						visit(child+"/"+escapePointer(name), name, sub)
					}
				}
			}
			walkProperties(v, child, visit)
		}
	case []any:
		for i, v := range n {
			walkProperties(v, fmt.Sprintf("%s/%d", pointer, i), visit)
		}
	}
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func unescapePointer(s string) string {
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
}

// derefLocal follows a local `$ref` (`#/components/...`) and returns the object
// it points at; objects without a `$ref` are returned as-is.
//
// **An unresolvable $ref is a hard failure, never a silent skip** (design D7).
// Skipping would be safe while *checking* — the missing facts make locked lines
// go missing, which turns the test red. It is not safe while *generating*: the
// person running -openapi.update-surface would see deletions in the diff with no
// way to tell a typo'd $ref from a deliberate removal, and would commit a
// silently incomplete lock as the new baseline.
func derefLocal(t *testing.T, doc map[string]any, node any) map[string]any {
	t.Helper()
	obj, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("openapi.yaml: expected an object, got %T", node)
	}
	ref, isRef := obj["$ref"].(string)
	if !isRef {
		return obj
	}
	if !strings.HasPrefix(ref, "#/") {
		t.Fatalf("openapi.yaml: only local $ref is supported, got %q", ref)
	}
	var cur any = doc
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		seg = unescapePointer(seg)
		m, isObj := cur.(map[string]any)
		if !isObj {
			t.Fatalf("openapi.yaml: $ref %q does not resolve (%q is not inside an object)", ref, seg)
		}
		next, found := m[seg]
		if !found {
			t.Fatalf("openapi.yaml: $ref %q does not resolve (no %q)", ref, seg)
		}
		cur = next
	}
	return derefLocal(t, doc, cur)
}

func TestOpenAPIMoneyFieldsDeclareUnit(t *testing.T) {
	doc := loadOpenAPI(t)

	// The shared definition must exist and must say what the unit is — the whole
	// mechanism collapses to a tautology otherwise.
	schemas, _ := doc["components"].(map[string]any)
	defs, _ := schemas["schemas"].(map[string]any)
	minor, ok := defs["MinorUnitAmount"].(map[string]any)
	if !ok {
		t.Fatal("components.schemas.MinorUnitAmount is missing — every money field refs it")
	}
	if desc, _ := minor["description"].(string); !strings.Contains(desc, "最小貨幣單位") {
		t.Error("MinorUnitAmount.description 必須聲明「最小貨幣單位」——那是這個型別存在的唯一理由")
	}
	if typ, _ := minor["type"].(string); typ != "integer" {
		t.Errorf("MinorUnitAmount.type = %q, 必須是 integer（金額永不為浮點數）", typ)
	}

	var offenders []string
	var checked int
	walkProperties(doc, "", func(pointer, name string, schema any) {
		if !moneyProperty(name) {
			return
		}
		if _, exempt := moneyExemptions[pointer]; exempt {
			return
		}
		checked++
		if !refsMinorUnit(schema) {
			offenders = append(offenders, pointer)
		}
	})
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("%d 個金額欄位沒有 $ref '%s'——單位必須只在一個地方聲明，"+
			"就地展開會讓「差 100 倍」這種錯誤靜默通過：\n  %s",
			len(offenders), minorUnitRef, strings.Join(offenders, "\n  "))
	}
	t.Logf("檢查了 %d 個金額欄位，%d 個已登記的豁免", checked, len(moneyExemptions))
}

// ── 檢查 4：v1 表面鎖 ──────────────────────────────────────────────────

// parameterFacts renders one route's request parameters as lock lines.
//
// OpenAPI lets an operation override a path-item parameter that has the same
// (name, in), so the lists are merged in order with the operation winning.
//
// Each parameter yields ONE fact — "this endpoint accepts it here" — and, when
// it is optional, a SECOND independent fact — "you may omit it". That split is
// the whole of the required-direction handling: turning optional into required
// deletes the second line (breaking → red), while turning required into optional
// merely adds one (widening → green), and the checker never learns what
// `optional` means (design D3).
func parameterFacts(t *testing.T, doc map[string]any, route string, lists ...[]any) []string {
	t.Helper()
	merged := map[string]map[string]any{}
	var order []string
	for _, list := range lists {
		for _, raw := range list {
			p := derefLocal(t, doc, raw)
			name, _ := p["name"].(string)
			in, _ := p["in"].(string)
			if name == "" || in == "" {
				t.Fatalf("%s: a parameter has no name/in — the lock cannot address it", route)
			}
			key := in + " " + name
			if _, dup := merged[key]; !dup {
				order = append(order, key)
			}
			merged[key] = p
		}
	}
	out := make([]string, 0, len(order)*2)
	for _, key := range order {
		out = append(out, route+" "+key)
		if required, _ := merged[key]["required"].(bool); !required {
			out = append(out, route+" "+key+" optional")
		}
	}
	return out
}

// responseHeaderFacts renders the headers a route promises to send.
//
// The response objects are dereferenced because `Retry-After` lives on the
// shared components/responses/TooManyRequests — reading only inline responses
// would see 3 of the 9 headers and silently under-lock the other 6.
func responseHeaderFacts(t *testing.T, doc map[string]any, route string, op map[string]any) []string {
	t.Helper()
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for code, raw := range responses {
		headers, ok := derefLocal(t, doc, raw)["headers"].(map[string]any)
		if !ok {
			continue
		}
		for name := range headers {
			out = append(out, route+" response "+code+" "+name)
		}
	}
	return out
}

// currentSurface renders the contract's v1 promises as sorted lines.
//
// Sorting is a single flat sort.Strings over all five line forms. That is not
// laziness: a space (0x20) sorts before '/' (0x2F), so a route's parameter and
// header lines land directly beneath the route line and above its sub-paths —
// one endpoint's complete promise clusters into one readable block for free.
func currentSurface(t *testing.T, doc map[string]any) []string {
	t.Helper()
	seen := map[string]bool{}
	for route := range documentedRoutes(t, doc) {
		seen[route] = true
	}
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for name, raw := range schemas {
		s, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			continue
		}
		for prop := range props {
			seen[name+"."+prop] = true
		}
	}

	// Request parameters and response headers, per operation (change
	// api-contract-parameter-lock). Everything above this point is untouched:
	// extending the lock must not weaken what it already held.
	paths, _ := doc["paths"].(map[string]any)
	for p, raw := range paths {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		shared, _ := item["parameters"].([]any)
		for key, rawOp := range item {
			method, isMethod := httpMethods[key]
			if !isMethod {
				continue
			}
			op, ok := rawOp.(map[string]any)
			if !ok {
				t.Fatalf("paths%s.%s is not an object", p, key)
			}
			own, _ := op["parameters"].([]any)
			route := method + " " + p
			for _, line := range parameterFacts(t, doc, route, shared, own) {
				seen[line] = true
			}
			for _, line := range responseHeaderFacts(t, doc, route, op) {
				seen[line] = true
			}
		}
	}

	out := make([]string, 0, len(seen))
	for line := range seen {
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func TestOpenAPIV1SurfaceLock(t *testing.T) {
	doc := loadOpenAPI(t)
	current := currentSurface(t, doc)

	if *updateSurface {
		var b strings.Builder
		b.WriteString(surfaceFileHead)
		for _, line := range current {
			b.WriteString(line)
			b.WriteString("\n")
		}
		if err := os.WriteFile(surfaceRelPath, []byte(b.String()), 0o600); err != nil {
			t.Fatalf("write %s: %v", surfaceRelPath, err)
		}
		t.Logf("已重寫 %s（%d 行）——請確認 diff 中沒有非預期的刪除",
			filepath.Base(surfaceRelPath), len(current))
		return
	}

	raw, err := os.ReadFile(surfaceRelPath)
	if err != nil {
		t.Fatalf("read %s: %v", surfaceRelPath, err)
	}
	present := map[string]bool{}
	for _, line := range current {
		present[line] = true
	}
	var missing []string
	var locked int
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		locked++
		if !present[line] {
			missing = append(missing, line)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。\n"+
			"若確實要移除，必須同時從 %s 手動刪掉對應的行（該刪除會出現在 code review 的 diff 中）：\n  %s",
			len(missing), surfaceRelPath, strings.Join(missing, "\n  "))
		return
	}
	t.Logf("表面鎖：%d 項承諾全部仍在（文件目前共 %d 項，新增不受限）", locked, len(current))
}
