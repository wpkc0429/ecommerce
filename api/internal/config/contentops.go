package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ContentOpsConfig is the operational policy of the content store's publish
// hook, scheduled publishing and audit retention (change headless-content-store
// slice 3, design D7/D11/D12).
type ContentOpsConfig struct {
	// HookExtraHosts are operator-approved hosts ADDED to the built-in
	// Cloudflare deploy-hook allowlist (CONTENT_HOOK_ALLOWED_HOSTS). The env
	// variable can only add; it cannot remove or narrow the built-ins, and it
	// can never widen the address checks (private, loopback, link-local ...).
	HookExtraHosts []string
	// HookDebounce merges bursts of publishes of one shop into one call.
	HookDebounce time.Duration
	// HookMaxWait bounds how long a continuous burst can postpone the call.
	HookMaxWait time.Duration
	// HookMinInterval is the shortest gap between two calls of one shop.
	HookMinInterval time.Duration
	HookDailyCap    int
	// HookQueueSize bounds the in-memory queue between the event handler and
	// the writer goroutines.
	HookQueueSize int
	// HookFlushInterval is how often the flusher looks for due hooks.
	HookFlushInterval time.Duration
	// HookDevInsecure lets the hook sender use http, any port and loopback
	// addresses, for local e2e against a stub receiver ONLY. Refused in
	// production.
	HookDevInsecure bool

	// Scheduled publishing.
	ScheduleEnabled   bool          // in-process ticker of the API server (default off)
	ScheduleInterval  time.Duration // ticker period (default 1m)
	ScheduleMinLead   time.Duration // earliest allowed publish_at from now
	ScheduleMaxAhead  time.Duration // furthest allowed publish_at from now
	ScheduleGrace     time.Duration // later than this past publish_at = missed
	ScheduleRetention time.Duration // finished schedule rows kept this long

	// AuditRetention is the content_audit_log retention (default 180 days).
	AuditRetention time.Duration
}

func loadContentOps(cfg *Config) error {
	c := &cfg.ContentOps
	var err error
	for _, h := range strings.Split(getenv("CONTENT_HOOK_ALLOWED_HOSTS", ""), ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if strings.ContainsAny(strings.TrimPrefix(h, "*."), "/ ?#@:*") {
			return fmt.Errorf("config: CONTENT_HOOK_ALLOWED_HOSTS: %q must be a bare host name", h)
		}
		c.HookExtraHosts = append(c.HookExtraHosts, h)
	}
	durs := []struct {
		dst  *time.Duration
		key  string
		def  time.Duration
		min  time.Duration
		max  time.Duration
		what string
	}{
		{&c.HookDebounce, "CONTENT_HOOK_DEBOUNCE", 30 * time.Second, time.Second, 10 * time.Minute, ""},
		{&c.HookMaxWait, "CONTENT_HOOK_MAX_WAIT", 5 * time.Minute, time.Second, time.Hour, ""},
		{&c.HookMinInterval, "CONTENT_HOOK_MIN_INTERVAL", 60 * time.Second, 0, time.Hour, ""},
		{&c.HookFlushInterval, "CONTENT_HOOK_FLUSH_INTERVAL", 5 * time.Second, 100 * time.Millisecond, time.Minute, ""},
		{&c.ScheduleInterval, "CONTENT_SCHEDULER_INTERVAL", time.Minute, time.Second, time.Hour, ""},
		{&c.ScheduleMinLead, "CONTENT_SCHEDULE_MIN_LEAD", 2 * time.Minute, 0, 24 * time.Hour, ""},
		{&c.ScheduleMaxAhead, "CONTENT_SCHEDULE_MAX_AHEAD", 90 * 24 * time.Hour, time.Hour, 366 * 24 * time.Hour, ""},
		{&c.ScheduleGrace, "CONTENT_SCHEDULE_GRACE", time.Hour, time.Minute, 7 * 24 * time.Hour, ""},
		{&c.ScheduleRetention, "CONTENT_SCHEDULE_RETENTION", 90 * 24 * time.Hour, 24 * time.Hour, 5 * 366 * 24 * time.Hour, ""},
		{&c.AuditRetention, "CONTENT_AUDIT_RETENTION", 180 * 24 * time.Hour, 24 * time.Hour, 10 * 366 * 24 * time.Hour, ""},
	}
	for _, d := range durs {
		if *d.dst, err = parseDuration(d.key, d.def); err != nil {
			return err
		}
		if *d.dst < d.min || *d.dst > d.max {
			return fmt.Errorf("config: %s must be between %s and %s", d.key, d.min, d.max)
		}
	}
	if c.HookDailyCap, err = parseInt("CONTENT_HOOK_DAILY_CAP", 30); err != nil {
		return err
	}
	if c.HookDailyCap < 1 || c.HookDailyCap > 1000 {
		return errors.New("config: CONTENT_HOOK_DAILY_CAP must be between 1 and 1000")
	}
	if c.HookQueueSize, err = parseInt("CONTENT_HOOK_QUEUE_SIZE", 256); err != nil {
		return err
	}
	if c.HookQueueSize < 1 || c.HookQueueSize > 100000 {
		return errors.New("config: CONTENT_HOOK_QUEUE_SIZE must be between 1 and 100000")
	}
	c.HookDevInsecure = getenv("CONTENT_HOOK_DEV_INSECURE", "") == "1"
	if c.HookDevInsecure && cfg.Env == EnvProduction {
		return errors.New("config: CONTENT_HOOK_DEV_INSECURE is refused when APP_ENV=production")
	}
	c.ScheduleEnabled = getenv("CONTENT_SCHEDULER_ENABLED", "") == "1" || strings.EqualFold(getenv("CONTENT_SCHEDULER_ENABLED", ""), "true")
	return nil
}
