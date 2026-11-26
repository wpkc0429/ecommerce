package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDesignDefaultsAndBounds(t *testing.T) {
	cfg := &Config{}
	if err := loadDesign(cfg); err != nil {
		t.Fatal(err)
	}
	d := cfg.Design
	day := 24 * time.Hour
	if d.ApprovalTTL != time.Hour || d.MinPreviewRemaining != 30*time.Minute || d.DailyRequestCap != 20 ||
		d.ClaimLease != 15*time.Minute || d.ApprovalPendingTimeout != 7*day ||
		d.PromptRetention != 365*day || d.ScreenshotRetention != 365*day ||
		d.ScreenshotRetentionUnpublished != 30*day || d.AuditRetention != 730*day ||
		d.UntrackedRetention != 730*day || d.CredentialRetention != 90*day ||
		d.VersionPreviewTTL != 30*time.Minute || d.VersionPreviewHourlyCap != 6 ||
		d.VersionPreviewDailyCap != 30 || d.VersionPreviewRetention != 90*day {
		t.Fatalf("defaults: %+v", d)
	}
	for key, bad := range map[string]string{
		"DESIGN_APPROVAL_TTL": "1s", "DESIGN_CLAIM_LEASE": "48h", "DESIGN_DAILY_REQUEST_CAP": "0",
		"DESIGN_VERSION_PREVIEW_TTL": "3h", "DESIGN_VERSION_PREVIEW_HOURLY_CAP": "0", "DESIGN_VERSION_PREVIEW_DAILY_CAP": "0",
		"DESIGN_VERSION_PREVIEW_RETENTION": "1h",
		"DESIGN_PROMPT_RETENTION":          "1h", "DESIGN_AUDIT_RETENTION": "999999h", "DESIGN_APPROVAL_PENDING_TIMEOUT": "nope",
	} {
		t.Setenv(key, bad)
		if err := loadDesign(&Config{}); err == nil {
			t.Errorf("%s=%s was accepted", key, bad)
		}
		t.Setenv(key, "")
	}
}

func TestLoadDesignStorageAllOrNothing(t *testing.T) {
	// Pin the media env so the host's own values cannot leak into the test.
	for _, k := range []string{"R2_ACCOUNT_ID", "R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY_ID", "DESIGN_R2_BUCKET", "DESIGN_R2_ACCESS_KEY_ID", "DESIGN_R2_SECRET_ACCESS_KEY"} {
		t.Setenv(k, "")
	}
	cfg := &Config{Env: EnvTest}
	if err := loadDesign(cfg); err != nil || cfg.Design.Enabled() {
		t.Fatalf("unset must be disabled: %v %+v", err, cfg.Design.Enabled())
	}
	if cfg.Design.ScreenshotTTL != 5*time.Minute || cfg.Design.ShopQuotaBytes != 2<<30 || cfg.Design.MaxScreenshotAttempts != 240 {
		t.Fatalf("defaults: %v %d", cfg.Design.ScreenshotTTL, cfg.Design.ShopQuotaBytes)
	}

	t.Setenv("DESIGN_R2_BUCKET", "design-bucket")
	err := loadDesign(&Config{Env: EnvTest})
	if err == nil || !strings.Contains(err.Error(), "DESIGN_R2_ACCESS_KEY_ID") || !strings.Contains(err.Error(), "DESIGN_R2_SECRET_ACCESS_KEY") {
		t.Fatalf("partial config must name the missing keys: %v", err)
	}

	t.Setenv("DESIGN_R2_ACCESS_KEY_ID", "design-key")
	t.Setenv("DESIGN_R2_SECRET_ACCESS_KEY", "design-secret-value")
	if err := loadDesign(&Config{Env: EnvTest}); err == nil || !strings.Contains(err.Error(), "R2_ACCOUNT_ID") {
		t.Fatalf("no endpoint source: %v", err)
	}
	t.Setenv("R2_ACCOUNT_ID", "acct")
	cfg = &Config{Env: EnvTest}
	if err := loadDesign(cfg); err != nil || !cfg.Design.Enabled() || cfg.Design.R2Endpoint != "https://acct.r2.cloudflarestorage.com" {
		t.Fatalf("full config: %v %+v", err, cfg.Design.R2Endpoint)
	}

	// Never the public image bucket; in production never the public image token.
	t.Setenv("R2_BUCKET", "design-bucket")
	if err := loadDesign(&Config{Env: EnvTest}); err == nil {
		t.Fatal("the design bucket must differ from R2_BUCKET")
	}
	t.Setenv("R2_BUCKET", "images")
	t.Setenv("R2_ACCESS_KEY_ID", "design-key")
	if err := loadDesign(&Config{Env: EnvTest}); err != nil {
		t.Fatalf("shared key id is tolerated in test (MinIO root): %v", err)
	}
	if err := loadDesign(&Config{Env: EnvProduction}); err == nil {
		t.Fatal("production must refuse the public image token for the design bucket")
	}

	// A known default secret is refused outside dev/test.
	t.Setenv("R2_ACCESS_KEY_ID", "other")
	t.Setenv("DESIGN_R2_SECRET_ACCESS_KEY", "minioadmin")
	prod := &Config{Env: EnvProduction}
	if err := loadDesign(prod); err != nil {
		t.Fatal(err)
	}
	if err := prod.validateSecrets(); err == nil || !strings.Contains(err.Error(), "DESIGN_R2_SECRET_ACCESS_KEY") || strings.Contains(err.Error(), "minioadmin") {
		t.Fatalf("weak design secret: %v", err)
	}

	for key, bad := range map[string]string{"DESIGN_SCREENSHOT_URL_TTL": "1s", "DESIGN_SHOP_QUOTA_BYTES": "10", "DESIGN_MAX_SCREENSHOT_ATTEMPTS": "10"} {
		t.Setenv(key, bad)
		if err := loadDesign(&Config{Env: EnvTest}); err == nil {
			t.Errorf("%s=%s was accepted", key, bad)
		}
		t.Setenv(key, "")
	}
}
