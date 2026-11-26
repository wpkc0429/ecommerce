package httpapi_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Audit M4: the baseline per-IP limit covers /api/v1, while payment webhooks
// live in their own, larger bucket so neither starves the other.
func TestBaselineRateLimit(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	limiter := ratelimit.New(rdb, log)
	router := httpapi.New(httpapi.Deps{
		Cfg: &config.Config{},
		Log: log,
		// general 3 per window, webhooks 6 per window.
		BaselineRateLimit: httpapi.NewBaselineRateLimit(limiter, 3, time.Minute, 6, time.Minute),
	})
	do := func(method, path, remote string) int {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = remote
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec.Code
	}
	const a, b = "198.51.100.1:1000", "198.51.100.2:1000"

	for i := 0; i < 3; i++ {
		if c := do(http.MethodGet, "/api/v1/shop/products", a); c == http.StatusTooManyRequests {
			t.Fatalf("request %d limited too early", i+1)
		}
	}
	if c := do(http.MethodGet, "/api/v1/render/page", a); c != http.StatusTooManyRequests {
		t.Fatalf("4th general request from the same IP = %d, want 429", c)
	}
	if c := do(http.MethodGet, "/api/v1/shop/products", b); c == http.StatusTooManyRequests {
		t.Fatalf("a different IP must have its own bucket")
	}
	// /healthz is outside /api/v1 and is never limited.
	if c := do(http.MethodGet, "/healthz", a); c == http.StatusTooManyRequests {
		t.Fatalf("/healthz must not be limited")
	}

	// Webhooks: unaffected by the exhausted general bucket, bounded by their own.
	for i := 0; i < 6; i++ {
		if c := do(http.MethodPost, "/api/v1/webhooks/payments/ecpay/1", a); c == http.StatusTooManyRequests {
			t.Fatalf("webhook %d limited while under its own limit", i+1)
		}
	}
	if c := do(http.MethodPost, "/api/v1/webhooks/payments/ecpay/1", a); c != http.StatusTooManyRequests {
		t.Fatalf("7th webhook from one IP = %d, want 429", c)
	}
}

// A zero limit disables the bucket (the dev/test default).
func TestBaselineRateLimitDisabledWhenZero(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	router := httpapi.New(httpapi.Deps{
		Cfg:               &config.Config{},
		Log:               log,
		BaselineRateLimit: httpapi.NewBaselineRateLimit(ratelimit.New(rdb, log), 0, time.Minute, 0, time.Minute),
	})
	for i := 0; i < 20; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/shop/products", nil)
		r.RemoteAddr = "198.51.100.9:1"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("limit 0 must disable the bucket (request %d)", i+1)
		}
	}
}
