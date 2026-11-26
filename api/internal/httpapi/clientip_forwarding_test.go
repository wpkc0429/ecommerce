package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// Audit H1: forwarding headers from a peer that is NOT a trusted proxy must
// never change the rate-limit key, however they are spelled; from a trusted
// proxy only CF-Connecting-IP is honored.
func TestForwardedHeadersHonoredOnlyFromTrustedProxy(t *testing.T) {
	rule := ipRule("baseline", 1, time.Minute)
	keyFor := func(trusted []netip.Prefix, remote string, hdr map[string]string) string {
		var got string
		h := NewClientIPMiddleware(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, _ = rule.keyFn(r)
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	spoof := map[string]string{"CF-Connecting-IP": "203.0.113.9", "X-Forwarded-For": "203.0.113.9", "X-Real-IP": "203.0.113.9"}

	if got := keyFor(loopbackProxies, "198.51.100.7:1", spoof); got != "baseline:ip:198.51.100.7" {
		t.Errorf("spoofed headers from untrusted peer changed the key: %q", got)
	}
	if got := keyFor(nil, "127.0.0.1:1", spoof); got != "baseline:ip:127.0.0.1" {
		t.Errorf("empty TRUSTED_PROXIES must key by peer: %q", got)
	}
	if got := keyFor(loopbackProxies, "127.0.0.1:1", map[string]string{"CF-Connecting-IP": "203.0.113.9"}); got != "baseline:ip:203.0.113.9" {
		t.Errorf("trusted proxy CF-Connecting-IP not honored: %q", got)
	}
	if got := keyFor(loopbackProxies, "127.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.9"}); got != "baseline:ip:127.0.0.1" {
		t.Errorf("X-Forwarded-For must never be honored: %q", got)
	}
}

func TestUntrustedForwardingWarning(t *testing.T) {
	serve := func(enabled bool, setHeader func(*http.Request), n int) string {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		h := NewClientIPMiddleware(nil, WithUntrustedForwardingWarning(enabled, log))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		for i := 0; i < n; i++ {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = "127.0.0.1:5000"
			setHeader(r)
			h.ServeHTTP(httptest.NewRecorder(), r)
		}
		return buf.String()
	}
	cf := func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.9") }
	xff := func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.9") }
	none := func(*http.Request) {}

	out := serve(true, cf, 3)
	if strings.Count(out, "level=WARN") != 1 {
		t.Errorf("want exactly one WARN, got %q", out)
	}
	if !strings.Contains(serve(true, xff, 1), "level=WARN") {
		t.Error("X-Forwarded-For must trigger the warning")
	}
	if out := serve(true, none, 3); out != "" {
		t.Errorf("no forwarding headers, no warning; got %q", out)
	}
	if out := serve(false, cf, 3); out != "" {
		t.Errorf("disabled (dev or proxies configured) must stay quiet; got %q", out)
	}
}

// Audit M1: the anonymous health endpoint never echoes the error.
func TestHealthzDoesNotLeakError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	router := New(Deps{Log: log, Health: func() error {
		return errors.New("dial tcp db.internal.example:5432: user ecommerce_prod")
	}})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db.internal") || strings.Contains(rec.Body.String(), "ecommerce_prod") {
		t.Errorf("response leaks the error: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "db.internal.example") {
		t.Errorf("error must be logged server-side, log = %q", buf.String())
	}

	ok := New(Deps{Log: log, Health: func() error { return nil }})
	rec = httptest.NewRecorder()
	ok.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthy status = %d", rec.Code)
	}
}
