package previeworigin

import "testing"

func mustParse(t *testing.T, loopbackHTTP bool, entries ...string) *Set {
	t.Helper()
	s, err := Parse(entries, loopbackHTTP)
	if err != nil {
		t.Fatalf("Parse(%q): %v", entries, err)
	}
	return s
}

func TestEmptyMeansOff(t *testing.T) {
	for _, s := range []*Set{nil, {}, mustParse(t, false)} {
		if s.Enabled() {
			t.Fatal("empty set must be disabled")
		}
		if _, ok := s.Match("https://a.preview.example.test"); ok {
			t.Fatal("empty set must match nothing")
		}
	}
}

func TestParseRejects(t *testing.T) {
	bad := []string{
		"", " https://a.example.test", "https://a.example.test ", // padding
		"HTTPS://a.example.test", "https://A.example.test", // uppercase
		"a.example.test", "ftp://a.example.test", "//a.example.test",
		"https://a.example.test/", "https://a.example.test/x", "https://a.example.test?x=1",
		"https://a.example.test#f", "https://user@a.example.test", "https://user:pw@a.example.test",
		"https://a.example.test:", "https://a.example.test:0", "https://a.example.test:99999",
		"https://a.example.test:08080", "https://a.example.test:80a",
		"https://a.example.test.",    // trailing dot
		"https://localhost",          // single label (https, non-loopback allowed? loopback is fine) - see below
		"https://*",                  // bare wildcard
		"https://*.com",              // base needs 2 labels
		"https://*.*.example.test",   // double wildcard
		"https://pre*.example.test",  // partial label wildcard
		"https://a.*.example.test",   // wildcard not first
		"https://*example.test",      // no dot
		"https://*.example.test.",    // trailing dot
		"http://*.example.test",      // wildcard needs https
		"http://a.example.test",      // http only for loopback
		"https://*.127.0.0.1",        // IP wildcard
		"https://10.0.0.1",           // IP literal (non-loopback)
		"https://xn--é.example.test", // non-ascii
		"https://café.example.test",  // unicode host
		"https://a_b.example.test",   // underscore
		"https://-a.example.test",    // leading hyphen
		"https://a..example.test",    // empty label
		"https://*..example.test",    // empty base label
		"https://[::1",               // malformed IPv6
		"https://a.example.test\\b",  // backslash
		"https://*.example.test:abc", // bad port
	}
	for _, b := range bad {
		if b == "https://localhost" { // single-label loopback host is a legal exact entry
			continue
		}
		if _, err := Parse([]string{b}, true); err == nil {
			t.Errorf("Parse(%q) must fail", b)
		}
	}
	if _, err := Parse([]string{"http://localhost:3002"}, false); err == nil {
		t.Error("http loopback must be refused when not allowed (production)")
	}
	if _, err := Parse([]string{"http://localhost:3002", "http://127.0.0.1:3002", "http://[::1]:3002"}, true); err != nil {
		t.Errorf("http loopback must be accepted in development: %v", err)
	}
}

func TestExactMatch(t *testing.T) {
	s := mustParse(t, false, "https://p-3f9a.preview.example.test", "https://pv.example.test:8443")
	yes := []string{
		"https://p-3f9a.preview.example.test", "HTTPS://P-3F9A.PREVIEW.EXAMPLE.TEST",
		"https://pv.example.test:8443",
	}
	for _, o := range yes {
		if _, ok := s.Match(o); !ok {
			t.Errorf("%q should match", o)
		}
	}
	no := []string{
		"https://p-3f9a.preview.example.test:8443", // port mismatch
		"https://pv.example.test", "https://pv.example.test:443", "https://pv.example.test:8444",
		"http://p-3f9a.preview.example.test", // scheme
		"https://x.p-3f9a.preview.example.test",
		"https://p-3f9a.preview.example.test.", // trailing dot
		"https://p-3f9a.preview.example.test/x", "https://user@p-3f9a.preview.example.test",
		"https://p-3f9a.preview.example.test@evil.test", "https://evil.test#@p-3f9a.preview.example.test",
		"", "null", "https://",
	}
	for _, o := range no {
		if _, ok := s.Match(o); ok {
			t.Errorf("%q must not match", o)
		}
	}
}

func TestWildcardEdgeCases(t *testing.T) {
	s := mustParse(t, false, "https://*.preview.example.test")
	yes := []string{
		"https://a.preview.example.test", "https://p-3f9a.preview.example.test",
		"https://A.Preview.Example.Test", "https://a1-b2.preview.example.test",
	}
	for _, o := range yes {
		if _, ok := s.Match(o); !ok {
			t.Errorf("%q should match", o)
		}
	}
	no := map[string]string{
		"https://preview.example.test":              "the base itself",
		"https://a.b.preview.example.test":          "two labels",
		"https://evil-preview.example.test":         "lookalike (no dot boundary)",
		"https://evilpreview.example.test":          "lookalike",
		"https://a.preview.example.test.evil.test":  "suffix trick",
		"https://a.preview.example.test.":           "trailing dot",
		"https://a.preview.example.test:8443":       "port not in pattern",
		"http://a.preview.example.test":             "scheme",
		"https://user@a.preview.example.test":       "userinfo",
		"https://a.preview.example.test@evil.test":  "userinfo trick",
		"https://a.preview.example.test/":           "path",
		"https://xn--e1afmkfd.preview.example.test": "punycode label",
		"https://xn--a.preview.example.test":        "punycode label",
		"https://é.preview.example.test":            "unicode label",
		"https://-a.preview.example.test":           "hyphen edge",
		"https://.preview.example.test":             "empty label",
		"https://a_b.preview.example.test":          "underscore",
		"https://*.preview.example.test":            "literal wildcard in request",
	}
	for o, why := range no {
		if _, ok := s.Match(o); ok {
			t.Errorf("%q must not match (%s)", o, why)
		}
	}
}

func TestWildcardWithPort(t *testing.T) {
	s := mustParse(t, false, "https://*.preview.example.test:8443")
	if _, ok := s.Match("https://a.preview.example.test:8443"); !ok {
		t.Error("port-bound wildcard must match its port")
	}
	for _, o := range []string{"https://a.preview.example.test", "https://a.preview.example.test:9443"} {
		if _, ok := s.Match(o); ok {
			t.Errorf("%q must not match", o)
		}
	}
}

func TestMatchReturnsCanonicalForm(t *testing.T) {
	s := mustParse(t, false, "https://*.preview.example.test")
	got, ok := s.Match("  HTTPS://AbC.Preview.Example.test ")
	if !ok || got != "https://abc.preview.example.test" {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestLoopbackHTTPExact(t *testing.T) {
	s := mustParse(t, true, "http://localhost:3002")
	if _, ok := s.Match("http://localhost:3002"); !ok {
		t.Error("loopback http entry must match")
	}
	if _, ok := s.Match("https://localhost:3002"); ok {
		t.Error("scheme must be exact")
	}
	if _, ok := s.Match("http://localhost:3003"); ok {
		t.Error("port must be exact")
	}
}
