package testutil

// 接線稽核工具（change contract-router-completeness）。
//
// 這裡的三個函式服務同一個目的：**讓「有沒有接」成為一件被機械回答的事**，而不是
// 一份寫在測試碼裡、必須有人記得同步的清單。
//
// 起因是一次實測。契約檢查 ①（路由覆蓋）走的是測試碼自己組的 router，而那個 router
// 的 Deps 是逐欄位手寫的——在 Deps 上新增一個 handler、掛上兩條路由、不碰那份清單，
// 三道單元檢查全部通過，兩條零文件的公開路由沒有任何人提起。清單型的鏡像必然漂移，
// 而漂移的方向恰好是最常發生的那個：新增。
//
// 處置是把清單刪掉（design D1）：
//
//	FillNonNil           由結構本身長出一個「每個欄位都有值」的 Deps——新增欄位自動被填
//	NilDependencyPaths   對真實接線的產物遞迴稽核，指名哪一條相依沒被接上
//	RouteTable           單一份路由列舉邏輯，供「檢查來源」與「生產 router」逐條比對
//
// 這三個函式本身不需要基礎設施；只有呼叫它們去稽核**真實** wire 的那些測試需要
// （見 internal/app/wiring_integration_test.go）。

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// modulePrefix bounds how deep the audits recurse: only types this codebase owns.
const modulePrefix = "ksdevworks/ecommerce/api/internal/"

// auditSkippedPackages are in-module packages the recursion deliberately stops at.
//
//	internal/ent    — generated code; *ent.Client exposes dozens of per-entity
//	                  client fields that say nothing about wiring.
//	internal/config — configuration values, where an unset string or an empty
//	                  slice is a legitimate state, not a missing dependency.
var auditSkippedPackages = []string{
	modulePrefix + "ent",
	modulePrefix + "config",
}

// FillNonNil populates every exported field of the struct behind structPtr with a
// non-nil value derived from the field's own type, so the resulting value is the
// "everything is wired" case without anyone enumerating what everything is.
//
// Rules, applied in order (design D2):
//
//  1. a name registered in overrides            → that value
//  2. pointer (*T)                              → reflect.New(T)
//  3. func(T) T                                 → the identity function
//  4. any other func                            → returns each result's zero value
//  5. anything else (interface, map, slice, …)  → t.Fatalf naming field and type
//
// **Rule 5 is why this helper does not re-open the hole it closes.** Silently
// leaving a field nil because its type is awkward would put the same failure mode
// back, wearing a different type: a whole route group could vanish from the
// enumeration and nothing would say so. Filling is either mechanical and correct,
// or it is a decision a human registers — with a reason — in overrides.
//
// Rule 3 is not cosmetic either: middleware fields are func(http.Handler)
// http.Handler, and a generated func returning the zero value would hand chi a nil
// handler that panics on the first request the coverage probe makes.
//
// Fields that cannot be nil (ints, strings, structs) are left at their zero value:
// they are never "unset" in a way that changes which routes get mounted.
func FillNonNil(t *testing.T, structPtr any, overrides map[string]any) {
	t.Helper()
	pv := reflect.ValueOf(structPtr)
	if pv.Kind() != reflect.Pointer || pv.Elem().Kind() != reflect.Struct {
		t.Fatalf("FillNonNil: want a pointer to struct, got %T", structPtr)
	}
	v := pv.Elem()
	st := v.Type()

	used := map[string]bool{}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if ov, ok := overrides[f.Name]; ok {
			used[f.Name] = true
			rv := reflect.ValueOf(ov)
			if !rv.IsValid() || !rv.Type().AssignableTo(f.Type) {
				t.Fatalf("FillNonNil: override for %s.%s is %T, not assignable to %s",
					st.Name(), f.Name, ov, f.Type)
			}
			fv.Set(rv)
			continue
		}
		switch f.Type.Kind() {
		case reflect.Pointer:
			if fv.IsNil() {
				fv.Set(reflect.New(f.Type.Elem()))
			}
		case reflect.Func:
			if fv.IsNil() {
				fv.Set(makeFunc(f.Type))
			}
		default:
			if isNilable(f.Type.Kind()) {
				t.Fatalf(
					"FillNonNil: 無法機械填充 %s.%s（型別 %s）。\n"+
						"這個欄位若留 nil，它所控制的路由會從契約覆蓋檢查中整組消失而不會有人知道——"+
						"那正是本機制存在的理由。請在 overrides 登記一個值，並寫下一句理由。",
					st.Name(), f.Name, f.Type)
			}
		}
	}
	for name := range overrides {
		if !used[name] {
			t.Fatalf("FillNonNil: override %q 對不上 %s 的任何匯出欄位（欄位被改名或刪除了？）",
				name, st.Name())
		}
	}
}

func isNilable(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return true
	default:
		return false
	}
}

// makeFunc builds a non-nil func of type ft: the identity function when the
// signature is func(T) T (middleware), otherwise a func returning zero values.
func makeFunc(ft reflect.Type) reflect.Value {
	if ft.NumIn() == 1 && ft.NumOut() == 1 && ft.In(0) == ft.Out(0) && !ft.IsVariadic() {
		return reflect.MakeFunc(ft, func(args []reflect.Value) []reflect.Value { return args })
	}
	return reflect.MakeFunc(ft, func([]reflect.Value) []reflect.Value {
		out := make([]reflect.Value, ft.NumOut())
		for i := range out {
			out[i] = reflect.Zero(ft.Out(i))
		}
		return out
	})
}

// NilDependencyPaths reports every exported, nil-able field reachable from root
// that is nil — i.e. every declared dependency nobody wired.
//
// Recursion follows pointers to structs this codebase owns (modulePrefix) except
// auditSkippedPackages, and stops naturally everywhere else: this project's
// constructors keep their state unexported (auth.TokenIssuer, rbac.Engine,
// tenant.Resolver, render.Cache), so there is nothing further to walk.
//
// exemptions maps a path ("Deps.Orders.Idempotency") to the reason it is allowed
// to be nil in production. It is meant to stay empty — it was empty when this was
// written — and every entry costs a written justification, the same discipline as
// the money-unit check's exemption list.
func NilDependencyPaths(root any, rootName string, exemptions map[string]string) []string {
	v := reflect.ValueOf(root)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return []string{rootName}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	visited := map[uintptr]bool{}
	walkDependencies(v, rootName, exemptions, visited, &out)
	sort.Strings(out)
	return out
}

func walkDependencies(v reflect.Value, path string, exemptions map[string]string, visited map[uintptr]bool, out *[]string) {
	st := v.Type()
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		p := path + "." + f.Name

		if isNilable(fv.Kind()) && fv.IsNil() {
			if _, ok := exemptions[p]; !ok {
				*out = append(*out, p+" ("+fv.Type().String()+")")
			}
			continue
		}
		switch {
		case fv.Kind() == reflect.Pointer && fv.Elem().Kind() == reflect.Struct:
			if !auditable(fv.Type().Elem()) {
				continue
			}
			if visited[fv.Pointer()] {
				continue
			}
			visited[fv.Pointer()] = true
			walkDependencies(fv.Elem(), p, exemptions, visited, out)
		case fv.Kind() == reflect.Struct:
			if !auditable(fv.Type()) {
				continue
			}
			walkDependencies(fv, p, exemptions, visited, out)
		}
	}
}

func auditable(t reflect.Type) bool {
	pkg := t.PkgPath()
	if !strings.HasPrefix(pkg, modulePrefix) {
		return false
	}
	for _, skip := range auditSkippedPackages {
		if pkg == skip || strings.HasPrefix(pkg, skip+"/") {
			return false
		}
	}
	return true
}

// RouteTable enumerates a chi router as sorted "METHOD /path" lines.
//
// One implementation, two callers: the contract coverage check and the check that
// proves the contract check's router matches the one a real server boots with. Two
// implementations of "list the routes" would eventually disagree about something
// (trailing slashes, method casing) and the comparison would quietly lose meaning.
func RouteTable(t *testing.T, h http.Handler) []string {
	t.Helper()
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("RouteTable: %T is not a chi.Routes; it cannot be enumerated", h)
	}
	var out []string
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("RouteTable: chi.Walk: %v", err)
	}
	sort.Strings(out)
	return out
}
