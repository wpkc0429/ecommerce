package ratelimit_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/testutil"
)

func silentLog() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

// Scenario: 正常流量放行 — requests under the limit are always allowed.
func TestLimiterAllowsUnderLimit(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	l := ratelimit.New(rdb, silentLog())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		res := l.Allow(ctx, "under-limit", 3, time.Minute)
		if !res.Allowed {
			t.Fatalf("attempt %d: want allowed, got blocked", i)
		}
	}
}

// Scenario: 超限回 429 等價行為 — the (limit+1)th attempt in the window is blocked
// with a positive Retry-After.
func TestLimiterBlocksOverLimit(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	l := ratelimit.New(rdb, silentLog())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if res := l.Allow(ctx, "over-limit", 3, time.Minute); !res.Allowed {
			t.Fatalf("attempt %d: want allowed, got blocked", i)
		}
	}
	res := l.Allow(ctx, "over-limit", 3, time.Minute)
	if res.Allowed {
		t.Fatal("4th attempt: want blocked, got allowed")
	}
	if res.RetryAfter <= 0 {
		t.Fatalf("blocked result must report a positive Retry-After, got %v", res.RetryAfter)
	}
}

// Scenario: 視窗過期後恢復 — once the window elapses, the key's count has
// decayed and new attempts are allowed again.
func TestLimiterRecoversAfterWindow(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	l := ratelimit.New(rdb, silentLog())
	ctx := context.Background()

	const window = 200 * time.Millisecond
	for i := 0; i < 2; i++ {
		if res := l.Allow(ctx, "recovers", 2, window); !res.Allowed {
			t.Fatalf("attempt %d: want allowed, got blocked", i)
		}
	}
	if res := l.Allow(ctx, "recovers", 2, window); res.Allowed {
		t.Fatal("3rd attempt inside window: want blocked, got allowed")
	}

	time.Sleep(window + 100*time.Millisecond)

	if res := l.Allow(ctx, "recovers", 2, window); !res.Allowed {
		t.Fatal("attempt after window elapsed: want allowed, got blocked")
	}
}

// Scenario: 不同 key 互不干擾 — independent keys never share a bucket.
func TestLimiterKeysAreIndependent(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	l := ratelimit.New(rdb, silentLog())
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if res := l.Allow(ctx, "shop-a", 2, time.Minute); !res.Allowed {
			t.Fatalf("shop-a attempt %d: want allowed, got blocked", i)
		}
	}
	if res := l.Allow(ctx, "shop-a", 2, time.Minute); res.Allowed {
		t.Fatal("shop-a 3rd attempt: want blocked, got allowed")
	}

	// A different key (e.g. a different shop/IP combination) starts fresh.
	if res := l.Allow(ctx, "shop-b", 2, time.Minute); !res.Allowed {
		t.Fatal("shop-b must not be affected by shop-a's exhausted quota")
	}
}

// Scenario: Redis 故障降級 — a dead Redis never blocks the caller (fail-open).
func TestLimiterFailsOpenWithoutRedis(t *testing.T) {
	testutil.RequireIntegration(t)
	dead := redis.NewClient(&redis.Options{ // closed port, fail fast
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	l := ratelimit.New(dead, silentLog())

	res := l.Allow(context.Background(), "any-key", 1, time.Minute)
	if !res.Allowed {
		t.Fatal("dead Redis must fail open (allowed)")
	}

	// nil Redis (rate limiting disabled) behaves the same.
	l2 := ratelimit.New(nil, silentLog())
	if res := l2.Allow(context.Background(), "any-key", 1, time.Minute); !res.Allowed {
		t.Fatal("nil-redis limiter must always allow")
	}
}
