// Package ratelimit implements a Redis-backed sliding-window request
// limiter (design auth-rate-limiting D1). It is a generic primitive: callers
// own key construction (IP, shop, email, endpoint scoping — see
// httpapi/ratelimitmw.go for the auth-endpoint wiring) and choose their own
// limit/window per call.
package ratelimit

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// slidingWindowScript implements an accurate sliding-window-log limiter as a
// single atomic Redis round-trip:
//  1. drop entries older than the window from the per-key sorted set;
//  2. if the remaining count is under the limit, record this attempt (as a
//     new sorted-set member scored by "now") and allow;
//  3. otherwise reject and report how long until the oldest entry in the
//     window falls out of it (Retry-After).
//
// A sorted-set log (vs. a fixed-window INCR+EXPIRE counter) avoids the
// classic boundary-burst where two windows' worth of attempts land back to
// back around a window edge — exactly the case rate limiting a brute-force
// login is meant to close. A token bucket would be equally accurate but
// needs its own clock-drift bookkeeping (last-refill timestamp, fractional
// token accounting) for no benefit at the request volumes auth endpoints
// see; the ZSET log keeps state self-describing (each member IS an attempt
// timestamp) and self-expiring via PEXPIRE, so idle keys never need a
// sweep/cron (design D1).
var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)
if count < limit then
  redis.call('ZADD', key, now, member)
  redis.call('PEXPIRE', key, window)
  return {1, 0}
end
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local retryAfter = window
if oldest[2] ~= nil then
  retryAfter = (tonumber(oldest[2]) + window) - now
  if retryAfter < 0 then
    retryAfter = 0
  end
end
return {0, retryAfter}
`)

// Limiter evaluates sliding-window quotas against Redis. A nil *redis.Client
// (or any Redis error) fails OPEN — rate limiting is a defense-in-depth
// layer, not the source of truth for auth correctness; an outage must
// degrade like every other Redis-backed path in this codebase (design D6:
// consistent with render.Cache / rbac.Engine), not lock every user out of
// login.
type Limiter struct {
	redis *redis.Client
	log   *slog.Logger
}

// New builds a Limiter. rdb may be nil (rate limiting disabled, always
// allow) for callers that don't wire Redis (e.g. most existing tests).
func New(rdb *redis.Client, log *slog.Logger) *Limiter {
	return &Limiter{redis: rdb, log: log}
}

// Result is the outcome of one Allow call.
type Result struct {
	Allowed    bool
	RetryAfter time.Duration // meaningful when !Allowed
}

// Allow records one attempt under key and reports whether it is within
// (limit) attempts per (window). key is caller-scoped (should already
// encode IP/shop/email/endpoint as needed — see httpapi/ratelimitmw.go);
// this method only adds the "ratelimit:" namespace prefix.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) Result {
	if l == nil || l.redis == nil || limit <= 0 || window <= 0 {
		return Result{Allowed: true}
	}
	member := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(rand.Uint64(), 36)
	now := time.Now().UnixMilli()
	res, err := slidingWindowScript.Run(ctx, l.redis, []string{"ratelimit:" + key}, now, window.Milliseconds(), limit, member).Result()
	if err != nil {
		l.warn("eval failed, failing open", key, err)
		return Result{Allowed: true}
	}
	vals, ok := res.([]interface{})
	if !ok || len(vals) != 2 {
		l.warn("unexpected script result, failing open", key, nil)
		return Result{Allowed: true}
	}
	allowed, _ := vals[0].(int64)
	retryMS, _ := vals[1].(int64)
	return Result{Allowed: allowed == 1, RetryAfter: time.Duration(retryMS) * time.Millisecond}
}

func (l *Limiter) warn(msg, key string, err error) {
	if l.log == nil {
		return
	}
	if err != nil {
		l.log.Warn("ratelimit: "+msg, "key", key, "err", err)
		return
	}
	l.log.Warn("ratelimit: "+msg, "key", key)
}
