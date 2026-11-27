package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/previeworigin"
)

// AdminPreviewConfig is the read-only preview session policy (change
// preview-readonly-session). Empty Origins = feature off: no origin is treated
// as a preview origin and no preview_ro token can be issued or accepted.
type AdminPreviewConfig struct {
	// Origins is ADMIN_PREVIEW_ORIGINS as written (exact or *.wildcard entries).
	Origins []string
	// Set is the strictly validated, parsed form of Origins.
	Set *previeworigin.Set
	// SessionTTL is ADMIN_PREVIEW_SESSION_TTL: the ABSOLUTE lifetime of a
	// preview session (access + refresh combined), never extended by refresh.
	SessionTTL time.Duration
}

// MaxAdminPreviewSessionTTL caps a preview session: it must never outlive the
// 30 minute preview TTL of version/change previews (design D25).
const MaxAdminPreviewSessionTTL = 30 * time.Minute

func loadAdminPreview(cfg *Config) error {
	c := &cfg.AdminPreview
	for _, o := range strings.Split(os.Getenv("ADMIN_PREVIEW_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.Origins = append(c.Origins, o)
		}
	}
	set, err := previeworigin.Parse(c.Origins, cfg.Env != EnvProduction)
	if err != nil {
		return fmt.Errorf("config: ADMIN_PREVIEW_ORIGINS: %w", err)
	}
	c.Set = set

	limit := MaxAdminPreviewSessionTTL
	if cfg.Design.VersionPreviewTTL > 0 && cfg.Design.VersionPreviewTTL < limit {
		limit = cfg.Design.VersionPreviewTTL
	}
	if c.SessionTTL, err = parseDuration("ADMIN_PREVIEW_SESSION_TTL", limit); err != nil {
		return err
	}
	if c.SessionTTL < time.Minute || c.SessionTTL > limit {
		return fmt.Errorf("config: ADMIN_PREVIEW_SESSION_TTL must be between 1m and %s (the preview TTL)", limit)
	}
	if !set.Enabled() {
		return nil
	}

	// The two origin classes must stay disjoint: a preview origin may never
	// also be a normal admin (or other platform) origin.
	var others []string
	others = append(others, cfg.CORSAllowedOrigins...)
	others = append(others, cfg.AdminPublicURL, cfg.WebsiteURL)
	for _, h := range cfg.PlatformAdminHosts {
		others = append(others, "https://"+h, "http://"+h)
	}
	for _, o := range others {
		if o != "" && set.Overlaps(o) {
			return fmt.Errorf("config: ADMIN_PREVIEW_ORIGINS must not overlap a normal admin/platform origin (%s)", o)
		}
	}
	return nil
}
