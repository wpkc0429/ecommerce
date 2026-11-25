package config

import (
	"fmt"
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
}

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
	return nil
}
