package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

var loopbackProxies = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.1/32"),
	netip.MustParsePrefix("::1/128"),
}

func TestResolveClientIP(t *testing.T) {
	cases := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		headers map[string][]string
		want    string
	}{
		{"no trusted proxies ignores CF header", nil, "127.0.0.1:5000",
			map[string][]string{"CF-Connecting-IP": {"203.0.113.9"}}, "127.0.0.1"},
		{"trusted loopback peer uses CF header", loopbackProxies, "127.0.0.1:5000",
			map[string][]string{"CF-Connecting-IP": {"203.0.113.9"}}, "203.0.113.9"},
		{"trusted IPv6 loopback peer", loopbackProxies, "[::1]:5000",
			map[string][]string{"CF-Connecting-IP": {"2001:db8::1"}}, "2001:db8::1"},
		{"untrusted peer cannot forge CF header", loopbackProxies, "198.51.100.7:5000",
			map[string][]string{"CF-Connecting-IP": {"203.0.113.9"}}, "198.51.100.7"},
		{"X-Forwarded-For is never trusted", loopbackProxies, "127.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"203.0.113.9"}, "X-Real-Ip": {"203.0.113.10"}}, "127.0.0.1"},
		{"X-Forwarded-For ignored from untrusted peer too", nil, "198.51.100.7:5000",
			map[string][]string{"X-Forwarded-For": {"203.0.113.9"}}, "198.51.100.7"},
		{"trusted peer without header falls back", loopbackProxies, "127.0.0.1:5000", nil, "127.0.0.1"},
		{"garbage header falls back", loopbackProxies, "127.0.0.1:5000",
			map[string][]string{"CF-Connecting-IP": {"not-an-ip"}}, "127.0.0.1"},
		{"list in header falls back", loopbackProxies, "127.0.0.1:5000",
			map[string][]string{"CF-Connecting-IP": {"203.0.113.9, 198.51.100.1"}}, "127.0.0.1"},
		{"repeated header falls back", loopbackProxies, "127.0.0.1:5000",
			map[string][]string{"CF-Connecting-IP": {"203.0.113.9", "198.51.100.1"}}, "127.0.0.1"},
		{"IPv4-mapped header is unmapped", loopbackProxies, "127.0.0.1:5000",
			map[string][]string{"CF-Connecting-IP": {"::ffff:203.0.113.9"}}, "203.0.113.9"},
		{"IPv4-mapped trusted peer", loopbackProxies, "[::ffff:127.0.0.1]:5000",
			map[string][]string{"CF-Connecting-IP": {"203.0.113.9"}}, "203.0.113.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			for k, vs := range tc.headers {
				for _, v := range vs {
					r.Header.Add(k, v)
				}
			}
			if got := resolveClientIP(r, tc.trusted); got != tc.want {
				t.Errorf("resolveClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// The rate limiter's key must follow the middleware's decision, and without
// the middleware (handlers wired directly in tests) it must be the TCP peer —
// never a header.
func TestRateLimitKeyUsesResolvedClientIP(t *testing.T) {
	rule := ipRule("admin_login", 1, time.Minute)

	var gotKey string
	h := NewClientIPMiddleware(loopbackProxies)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotKey, _ = rule.keyFn(r)
	}))

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if gotKey != "admin_login:ip:203.0.113.9" {
		t.Errorf("tunnel request key = %q", gotKey)
	}

	r = httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "198.51.100.7:40000"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if gotKey != "admin_login:ip:198.51.100.7" {
		t.Errorf("direct request with forged header key = %q", gotKey)
	}

	r = httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	if key, _ := rule.keyFn(r); key != "admin_login:ip:127.0.0.1" {
		t.Errorf("no middleware key = %q, want the TCP peer", key)
	}
}
