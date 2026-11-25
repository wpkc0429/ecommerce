package config

import (
	"fmt"
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
	return loadDesignStorage(cfg)
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
