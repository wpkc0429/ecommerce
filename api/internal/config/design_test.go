package config

import (
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
		d.UntrackedRetention != 730*day || d.CredentialRetention != 90*day {
		t.Fatalf("defaults: %+v", d)
	}
	for key, bad := range map[string]string{
		"DESIGN_APPROVAL_TTL": "1s", "DESIGN_CLAIM_LEASE": "48h", "DESIGN_DAILY_REQUEST_CAP": "0",
		"DESIGN_PROMPT_RETENTION": "1h", "DESIGN_AUDIT_RETENTION": "999999h", "DESIGN_APPROVAL_PENDING_TIMEOUT": "nope",
	} {
		t.Setenv(key, bad)
		if err := loadDesign(&Config{}); err == nil {
			t.Errorf("%s=%s was accepted", key, bad)
		}
		t.Setenv(key, "")
	}
}
