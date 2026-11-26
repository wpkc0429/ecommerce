package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// DesignConfig is the policy of the design history (change design-history,
// slice 1; screenshot storage settings arrive with slice 2).
type DesignConfig struct {
	ApprovalTTL            time.Duration // DESIGN_APPROVAL_TTL: how long an approval stays usable
	MinPreviewRemaining    time.Duration // DESIGN_MIN_PREVIEW_REMAINING: a preview must live this long at submit
	DailyRequestCap        int           // DESIGN_DAILY_REQUEST_CAP: service-created requests per shop per UTC day
	ClaimLease             time.Duration // DESIGN_CLAIM_LEASE
	ApprovalPendingTimeout time.Duration // DESIGN_APPROVAL_PENDING_TIMEOUT: awaiting_approval fails after this

	// Retention (design D21).
	PromptRetention                time.Duration // DESIGN_PROMPT_RETENTION
	ScreenshotRetention            time.Duration // DESIGN_SCREENSHOT_RETENTION
	ScreenshotRetentionUnpublished time.Duration // DESIGN_SCREENSHOT_RETENTION_UNPUBLISHED
	AuditRetention                 time.Duration // DESIGN_AUDIT_RETENTION
	UntrackedRetention             time.Duration // DESIGN_UNTRACKED_RETENTION
	CredentialRetention            time.Duration // DESIGN_CREDENTIAL_RETENTION

	// Version preview (design D25). TTL bounds how long a preview may live
	// (default 30m, at most 2h); the caps are database COUNTs per (shop, user)
	// per hour and per shop per UTC day; retention removes the usage rows.
	VersionPreviewTTL       time.Duration // DESIGN_VERSION_PREVIEW_TTL
	VersionPreviewHourlyCap int           // DESIGN_VERSION_PREVIEW_HOURLY_CAP
	VersionPreviewDailyCap  int           // DESIGN_VERSION_PREVIEW_DAILY_CAP
	VersionPreviewRetention time.Duration // DESIGN_VERSION_PREVIEW_RETENTION

	// Screenshot storage (slice 2, design D5): a SECOND, private bucket. It has
	// its own credentials (the token is scoped to that bucket and must not be the
	// public-image token); the account id / endpoint are shared with media.
	// All-or-nothing: bucket + both keys set = enabled, none = disabled (the
	// routes still mount and answer 503 design_storage_not_configured).
	R2Bucket       string // DESIGN_R2_BUCKET
	R2AccessKeyID  string // DESIGN_R2_ACCESS_KEY_ID
	R2SecretKey    string // DESIGN_R2_SECRET_ACCESS_KEY
	R2Endpoint     string // derived: R2_ENDPOINT, else https://<R2_ACCOUNT_ID>.r2.cloudflarestorage.com
	ScreenshotTTL  time.Duration
	ShopQuotaBytes int64 // DESIGN_SHOP_QUOTA_BYTES: stored screenshot bytes per shop
	// MaxScreenshotAttempts is DESIGN_MAX_SCREENSHOT_ATTEMPTS: every upload attempt per request (default 240 = 3 x 80 slots).
	MaxScreenshotAttempts int

	// SandboxOrigins is DESIGN_SANDBOX_ORIGINS: the exact origins (scheme://host[:port])
	// of the sandbox stack. Admin screenshots must declare one of them as
	// capture_manifest.origin. Empty means admin captures are refused when
	// APP_ENV is production-like (SandboxOriginsRequired) and accepted per the
	// manifest rules alone in development and test.
	SandboxOrigins         []string
	SandboxOriginsRequired bool
}

// Enabled reports whether the private screenshot bucket is configured.
func (d DesignConfig) Enabled() bool { return d.R2Bucket != "" }

func loadDesign(cfg *Config) error {
	c := &cfg.Design
	day := 24 * time.Hour
	durs := []struct {
		dst      *time.Duration
		key      string
		def, min time.Duration
		max      time.Duration
	}{
		{&c.ApprovalTTL, "DESIGN_APPROVAL_TTL", time.Hour, 5 * time.Minute, 24 * time.Hour},
		{&c.MinPreviewRemaining, "DESIGN_MIN_PREVIEW_REMAINING", 30 * time.Minute, 0, 24 * time.Hour},
		{&c.ClaimLease, "DESIGN_CLAIM_LEASE", 15 * time.Minute, time.Minute, time.Hour},
		{&c.ApprovalPendingTimeout, "DESIGN_APPROVAL_PENDING_TIMEOUT", 7 * day, time.Hour, 90 * day},
		{&c.PromptRetention, "DESIGN_PROMPT_RETENTION", 365 * day, day, 10 * 366 * day},
		{&c.ScreenshotRetention, "DESIGN_SCREENSHOT_RETENTION", 365 * day, day, 10 * 366 * day},
		{&c.ScreenshotRetentionUnpublished, "DESIGN_SCREENSHOT_RETENTION_UNPUBLISHED", 30 * day, day, 10 * 366 * day},
		{&c.AuditRetention, "DESIGN_AUDIT_RETENTION", 730 * day, day, 10 * 366 * day},
		{&c.UntrackedRetention, "DESIGN_UNTRACKED_RETENTION", 730 * day, day, 10 * 366 * day},
		{&c.CredentialRetention, "DESIGN_CREDENTIAL_RETENTION", 90 * day, day, 10 * 366 * day},
		{&c.VersionPreviewTTL, "DESIGN_VERSION_PREVIEW_TTL", 30 * time.Minute, time.Minute, 2 * time.Hour},
		{&c.VersionPreviewRetention, "DESIGN_VERSION_PREVIEW_RETENTION", 90 * day, day, 10 * 366 * day},
	}
	for _, d := range durs {
		v, err := parseDuration(d.key, d.def)
		if err != nil {
			return err
		}
		if v < d.min || v > d.max {
			return fmt.Errorf("config: %s must be between %s and %s", d.key, d.min, d.max)
		}
		*d.dst = v
	}
	n, err := parseInt("DESIGN_DAILY_REQUEST_CAP", 20)
	if err != nil {
		return err
	}
	if n < 1 || n > 1000 {
		return fmt.Errorf("config: DESIGN_DAILY_REQUEST_CAP must be between 1 and 1000")
	}
	c.DailyRequestCap = n
	if c.VersionPreviewHourlyCap, err = parseInt("DESIGN_VERSION_PREVIEW_HOURLY_CAP", 6); err != nil {
		return err
	}
	if c.VersionPreviewHourlyCap < 1 || c.VersionPreviewHourlyCap > 1000 {
		return fmt.Errorf("config: DESIGN_VERSION_PREVIEW_HOURLY_CAP must be between 1 and 1000")
	}
	if c.VersionPreviewDailyCap, err = parseInt("DESIGN_VERSION_PREVIEW_DAILY_CAP", 30); err != nil {
		return err
	}
	if c.VersionPreviewDailyCap < 1 || c.VersionPreviewDailyCap > 10000 {
		return fmt.Errorf("config: DESIGN_VERSION_PREVIEW_DAILY_CAP must be between 1 and 10000")
	}
	if c.MaxScreenshotAttempts, err = parseInt("DESIGN_MAX_SCREENSHOT_ATTEMPTS", 240); err != nil {
		return err
	}
	if c.MaxScreenshotAttempts < 80 || c.MaxScreenshotAttempts > 2000 {
		return fmt.Errorf("config: DESIGN_MAX_SCREENSHOT_ATTEMPTS must be between 80 and 2000")
	}
	if c.SandboxOrigins, err = ParseSandboxOrigins(os.Getenv("DESIGN_SANDBOX_ORIGINS")); err != nil {
		return err
	}
	c.SandboxOriginsRequired = !cfg.IsDevOrTest()
	return loadDesignStorage(cfg)
}

// ParseSandboxOrigins parses the DESIGN_SANDBOX_ORIGINS comma list. Each entry
// must be a bare http(s) origin (no userinfo, path, query or fragment); entries
// are lower-cased and de-duplicated, so a later comparison is exact.
func ParseSandboxOrigins(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		o, ok := NormalizeOrigin(part)
		if !ok {
			return nil, fmt.Errorf("config: DESIGN_SANDBOX_ORIGINS entries must be bare http(s) origins such as http://127.0.0.1:13001")
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

// NormalizeOrigin returns scheme://host[:port] in lower case, or false when s
// is not exactly an http(s) origin.
func NormalizeOrigin(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.Contains(s, "#") || strings.Contains(s, "?") {
		return "", false
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), true
}

// Bounds of the per-shop screenshot quota.
const (
	DefaultDesignShopQuotaBytes = 2 << 30 // 2 GiB (design D5)
	minDesignShopQuotaBytes     = 64 << 20
)

func loadDesignStorage(cfg *Config) error {
	c := &cfg.Design
	var err error
	if c.ScreenshotTTL, err = parseDuration("DESIGN_SCREENSHOT_URL_TTL", 5*time.Minute); err != nil {
		return err
	}
	if c.ScreenshotTTL < 30*time.Second || c.ScreenshotTTL > time.Hour {
		return fmt.Errorf("config: DESIGN_SCREENSHOT_URL_TTL must be between 30s and 1h")
	}
	if c.ShopQuotaBytes, err = parseInt64("DESIGN_SHOP_QUOTA_BYTES", DefaultDesignShopQuotaBytes); err != nil {
		return err
	}
	if c.ShopQuotaBytes < minDesignShopQuotaBytes || c.ShopQuotaBytes > 1<<40 {
		return fmt.Errorf("config: DESIGN_SHOP_QUOTA_BYTES must be between %d and %d", minDesignShopQuotaBytes, int64(1)<<40)
	}

	c.R2Bucket = strings.TrimSpace(os.Getenv("DESIGN_R2_BUCKET"))
	c.R2AccessKeyID = strings.TrimSpace(os.Getenv("DESIGN_R2_ACCESS_KEY_ID"))
	c.R2SecretKey = strings.TrimSpace(os.Getenv("DESIGN_R2_SECRET_ACCESS_KEY"))
	required := []struct{ key, val string }{
		{"DESIGN_R2_BUCKET", c.R2Bucket},
		{"DESIGN_R2_ACCESS_KEY_ID", c.R2AccessKeyID},
		{"DESIGN_R2_SECRET_ACCESS_KEY", c.R2SecretKey},
	}
	var missing []string
	for _, r := range required {
		if r.val == "" {
			missing = append(missing, r.key)
		}
	}
	if len(missing) == len(required) {
		return nil // screenshot storage disabled
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: design screenshot storage is partially configured; missing %s (set all of DESIGN_R2_BUCKET, DESIGN_R2_ACCESS_KEY_ID, DESIGN_R2_SECRET_ACCESS_KEY, or none)",
			strings.Join(missing, ", "))
	}
	// The endpoint and account are shared with media.
	c.R2Endpoint = cfg.Media.Endpoint
	if c.R2Endpoint == "" {
		account := strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID"))
		if account == "" {
			return fmt.Errorf("config: DESIGN_R2_BUCKET needs R2_ACCOUNT_ID (or R2_ENDPOINT for MinIO) to locate the object store")
		}
		c.R2Endpoint = "https://" + account + ".r2.cloudflarestorage.com"
	}
	// The bucket is private and separate by design: never the public image bucket.
	if c.R2Bucket == strings.TrimSpace(os.Getenv("R2_BUCKET")) {
		return fmt.Errorf("config: DESIGN_R2_BUCKET must not be the public image bucket (R2_BUCKET)")
	}
	if !cfg.IsDevOrTest() {
		// The bucket token must be its own: sharing the public-image token would
		// give a leaked image token read access to every screenshot.
		if c.R2AccessKeyID == strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID")) {
			return fmt.Errorf("config: DESIGN_R2_ACCESS_KEY_ID must differ from R2_ACCESS_KEY_ID (use a token scoped to the design bucket)")
		}
	}
	return nil
}
