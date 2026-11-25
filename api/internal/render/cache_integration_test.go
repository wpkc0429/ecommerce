package render_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/testutil"
)

func silentLog() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

func TestCacheReadThroughAndVersioning(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	c := render.NewCache(rdb, silentLog())
	ctx := context.Background()

	calls := 0
	assemble := func(context.Context) ([]byte, error) {
		calls++
		return []byte(fmt.Sprintf(`{"v":%d}`, calls)), nil
	}

	// Miss → assemble + fill.
	out, hit, err := c.GetPage(ctx, 1, "home", assemble)
	if err != nil || hit || string(out) != `{"v":1}` {
		t.Fatalf("first get: out=%s hit=%v err=%v", out, hit, err)
	}
	// Hit → no assembly.
	out, hit, err = c.GetPage(ctx, 1, "home", assemble)
	if err != nil || !hit || string(out) != `{"v":1}` || calls != 1 {
		t.Fatalf("second get: out=%s hit=%v calls=%d err=%v", out, hit, calls, err)
	}

	// Version bump → old key irrelevant, fresh assembly.
	c.BumpShops(ctx, 1)
	out, hit, _ = c.GetPage(ctx, 1, "home", assemble)
	if hit || string(out) != `{"v":2}` {
		t.Fatalf("post-bump get: out=%s hit=%v", out, hit)
	}

	// Single-page delete under current version.
	c.DeletePage(ctx, 1, "home")
	out, hit, _ = c.GetPage(ctx, 1, "home", assemble)
	if hit || string(out) != `{"v":3}` {
		t.Fatalf("post-delete get: out=%s hit=%v", out, hit)
	}

	// Other slugs/shops unaffected by DeletePage.
	_, _, _ = c.GetPage(ctx, 2, "home", assemble) // calls=4
	c.DeletePage(ctx, 1, "home")
	_, hit, _ = c.GetPage(ctx, 2, "home", assemble)
	if !hit {
		t.Fatal("DeletePage must not touch other shops")
	}
}

// Scenario (content-rendering/Redis read-through cache): Redis 故障降級 —
// a dead Redis never blocks rendering.
func TestCacheDegradesWithoutRedis(t *testing.T) {
	testutil.RequireIntegration(t)
	dead := redis.NewClient(&redis.Options{ // closed port, fail fast
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	c := render.NewCache(dead, silentLog())

	called := false
	out, hit, err := c.GetPage(context.Background(), 1, "home", func(context.Context) ([]byte, error) {
		called = true
		return []byte(`{"ok":true}`), nil
	})
	if err != nil || hit || !called || string(out) != `{"ok":true}` {
		t.Fatalf("degrade failed: out=%s hit=%v err=%v", out, hit, err)
	}

	// nil Redis (cache disabled) behaves the same.
	c2 := render.NewCache(nil, silentLog())
	out, _, err = c2.GetPage(context.Background(), 1, "home", func(context.Context) ([]byte, error) {
		return []byte(`{"ok":1}`), nil
	})
	if err != nil || string(out) != `{"ok":1}` {
		t.Fatal("nil-redis cache must pass through")
	}
}

// Task 6.3 (headless-content-store): the content namespace degrades the same
// way as the page namespace - a dead or absent Redis never blocks a content
// read, it just goes to the database every time, and the read-through still
// works (and is invalidated) once Redis is there.
func TestContentCacheDegradesWithoutRedis(t *testing.T) {
	testutil.RequireIntegration(t)
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	c := render.NewCache(dead, silentLog())
	calls := 0
	assemble := func(context.Context) ([]byte, error) { calls++; return []byte(`{"content":1}`), nil }
	for i := 0; i < 2; i++ {
		out, hit, err := c.GetContent(context.Background(), 1, "banner", assemble)
		if err != nil || hit || string(out) != `{"content":1}` {
			t.Fatalf("dead redis, call %d: out=%s hit=%v err=%v", i, out, hit, err)
		}
	}
	if calls != 2 {
		t.Fatalf("with Redis down every read must hit the database: %d calls", calls)
	}
	// A failing assembler is an error, not a cached value.
	if _, _, err := c.GetContent(context.Background(), 1, "banner", func(context.Context) ([]byte, error) { return nil, context.DeadlineExceeded }); err == nil {
		t.Fatal("assembler error must surface")
	}
	// nil Redis (cache disabled) is the same.
	c2 := render.NewCache(nil, silentLog())
	if out, hit, err := c2.GetContent(context.Background(), 1, "banner", assemble); err != nil || hit || string(out) != `{"content":1}` {
		t.Fatalf("nil redis: %s %v %v", out, hit, err)
	}

	// With a live Redis: first read assembles, second is a hit, deleting the
	// key makes the next read assemble again.
	rdb := testutil.OpenRedis(t)
	live := render.NewCache(rdb, silentLog())
	calls = 0
	if _, hit, _ := live.GetContent(context.Background(), 1, "banner", assemble); hit || calls != 1 {
		t.Fatalf("first read: hit=%v calls=%d", hit, calls)
	}
	if _, hit, _ := live.GetContent(context.Background(), 1, "banner", assemble); !hit || calls != 1 {
		t.Fatalf("second read must be a hit: hit=%v calls=%d", hit, calls)
	}
	live.DeleteContent(context.Background(), 1, "banner")
	if _, hit, _ := live.GetContent(context.Background(), 1, "banner", assemble); hit || calls != 2 {
		t.Fatalf("after DeleteContent: hit=%v calls=%d", hit, calls)
	}
}

// Task 10.2 core behavior: concurrent misses on the same key are merged by
// singleflight — the assembler runs far fewer times than the request count.
func TestCacheSingleflightMergesConcurrentMisses(t *testing.T) {
	rdb := testutil.OpenRedis(t)
	c := render.NewCache(rdb, silentLog())
	ctx := context.Background()

	var assembles int32
	slowAssemble := func(context.Context) ([]byte, error) {
		atomic.AddInt32(&assembles, 1)
		return []byte(`{"heavy":true}`), nil
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, err := c.GetPage(ctx, 9, "home", slowAssemble)
			if err != nil {
				errs <- err
				return
			}
			if string(out) != `{"heavy":true}` {
				errs <- fmt.Errorf("wrong payload: %s", out)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&assembles); got > 3 {
		t.Fatalf("singleflight ineffective: %d assemblies for %d concurrent requests", got, n)
	}
}
