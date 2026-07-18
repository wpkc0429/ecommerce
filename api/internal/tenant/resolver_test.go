package tenant

import "testing"

func strp(s string) *string { return &s }

// Pure matching-logic tests for spec multi-tenancy/Path-prefix disambiguation.
func TestMatchMapping(t *testing.T) {
	mappings := []Mapping{
		{ShopID: 1, PathPrefix: nil, ShopStatus: 1},                  // default shop
		{ShopID: 2, PathPrefix: strp("/brand"), ShopStatus: 1},       //
		{ShopID: 3, PathPrefix: strp("/brand-a"), ShopStatus: 1},     //
		{ShopID: 4, PathPrefix: strp("/brand-a/sub"), ShopStatus: 1}, //
	}

	cases := []struct {
		name      string
		path      string
		wantShop  int
		wantPath  string
		wantFound bool
	}{
		// Scenario: 前綴命中 + 剝離.
		{"prefix hit strips", "/brand-a/about", 3, "/about", true},
		// Scenario: 最長前綴優先 — /brand-a beats /brand for /brand-a/x.
		{"longest prefix wins", "/brand-a/x", 3, "/x", true},
		{"even longer prefix", "/brand-a/sub/page", 4, "/page", true},
		{"shorter sibling prefix", "/brand/x", 2, "/x", true},
		// /brand-ab must NOT match /brand-a (segment-aware).
		{"no partial segment match", "/brand-ab/x", 1, "/brand-ab/x", true},
		// Scenario: 無前綴命中落到預設商家.
		{"default fallback", "/about", 1, "/about", true},
		{"root to default", "/", 1, "/", true},
		// Prefix root resolves to "/" after stripping.
		{"prefix root", "/brand-a", 3, "/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, stripped, ok := MatchMapping(mappings, tc.path)
			if ok != tc.wantFound {
				t.Fatalf("found=%v want %v", ok, tc.wantFound)
			}
			if m.ShopID != tc.wantShop || stripped != tc.wantPath {
				t.Fatalf("got shop=%d path=%q, want shop=%d path=%q", m.ShopID, stripped, tc.wantShop, tc.wantPath)
			}
		})
	}
}

// Scenario: 無預設商家 — no prefix hit and no NULL-prefix mapping → not found.
func TestMatchMappingNoDefault(t *testing.T) {
	mappings := []Mapping{
		{ShopID: 2, PathPrefix: strp("/brand"), ShopStatus: 1},
	}
	if _, _, ok := MatchMapping(mappings, "/other"); ok {
		t.Fatal("must not match without default shop")
	}
	if m, stripped, ok := MatchMapping(mappings, "/brand/x"); !ok || m.ShopID != 2 || stripped != "/x" {
		t.Fatal("prefix match must still work")
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"Shop1.COM":          "shop1.com",
		"shop1.com:8080":     "shop1.com",
		" demo.localhost ":   "demo.localhost",
		"demo.localhost:443": "demo.localhost",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizePathPrefix(t *testing.T) {
	cases := map[string]string{
		"brand-a":    "/brand-a",
		"/brand-a/":  "/brand-a",
		"/Brand-A":   "/brand-a",
		"/":          "",
		"":           "",
		"/a/b/":      "/a/b",
	}
	for in, want := range cases {
		if got := NormalizePathPrefix(in); got != want {
			t.Errorf("NormalizePathPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
