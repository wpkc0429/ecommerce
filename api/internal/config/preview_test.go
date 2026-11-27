package config

import (
	"strings"
	"testing"
	"time"
)

func TestAdminPreviewDefaultsOff(t *testing.T) {
	t.Setenv("ADMIN_PREVIEW_ORIGINS", "")
	t.Setenv("ADMIN_PREVIEW_SESSION_TTL", "")
	cfg := &Config{Env: EnvProduction}
	if err := loadAdminPreview(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPreview.Set.Enabled() || len(cfg.AdminPreview.Origins) != 0 {
		t.Fatal("empty ADMIN_PREVIEW_ORIGINS must leave the feature off")
	}
	if cfg.AdminPreview.SessionTTL != 30*time.Minute {
		t.Fatalf("default session TTL %v", cfg.AdminPreview.SessionTTL)
	}
}

func TestAdminPreviewStrictValidation(t *testing.T) {
	for _, bad := range []string{
		"https://*.com", "https://*.preview.example.test/x", "http://a.example.test",
		"https://A.example.test", "https://user@a.example.test", "https://a.*.example.test",
		"https://ok.example.test,https://*", "*.example.test",
	} {
		t.Setenv("ADMIN_PREVIEW_ORIGINS", bad)
		if err := loadAdminPreview(&Config{Env: EnvProduction}); err == nil {
			t.Errorf("%q was accepted", bad)
		} else if !strings.Contains(err.Error(), "ADMIN_PREVIEW_ORIGINS") {
			t.Errorf("error should name the key: %v", err)
		}
	}
	t.Setenv("ADMIN_PREVIEW_ORIGINS", " https://*.preview.example.test , https://x.example.test:8443 ")
	cfg := &Config{Env: EnvProduction}
	if err := loadAdminPreview(cfg); err != nil || len(cfg.AdminPreview.Origins) != 2 {
		t.Fatalf("valid list: %v %+v", err, cfg.AdminPreview)
	}
	// http loopback is development-only.
	t.Setenv("ADMIN_PREVIEW_ORIGINS", "http://localhost:3002")
	if err := loadAdminPreview(&Config{Env: EnvProduction}); err == nil {
		t.Error("http loopback must be refused in production")
	}
	if err := loadAdminPreview(&Config{Env: EnvDevelopment}); err != nil {
		t.Errorf("http loopback must be fine in development: %v", err)
	}
}

func TestAdminPreviewSessionTTLBounds(t *testing.T) {
	t.Setenv("ADMIN_PREVIEW_ORIGINS", "")
	for _, bad := range []string{"30s", "31m", "2h", "nope"} {
		t.Setenv("ADMIN_PREVIEW_SESSION_TTL", bad)
		if err := loadAdminPreview(&Config{Env: EnvProduction}); err == nil {
			t.Errorf("ADMIN_PREVIEW_SESSION_TTL=%s was accepted", bad)
		}
	}
	t.Setenv("ADMIN_PREVIEW_SESSION_TTL", "20m")
	cfg := &Config{Env: EnvProduction}
	if err := loadAdminPreview(cfg); err != nil || cfg.AdminPreview.SessionTTL != 20*time.Minute {
		t.Fatalf("%v %v", err, cfg.AdminPreview.SessionTTL)
	}
	// Never longer than the (configured) version/change preview TTL.
	cfg = &Config{Env: EnvProduction}
	cfg.Design.VersionPreviewTTL = 10 * time.Minute
	if err := loadAdminPreview(cfg); err == nil {
		t.Error("a session longer than the preview TTL must be refused")
	}
	t.Setenv("ADMIN_PREVIEW_SESSION_TTL", "")
	cfg = &Config{Env: EnvProduction}
	cfg.Design.VersionPreviewTTL = 10 * time.Minute
	if err := loadAdminPreview(cfg); err != nil || cfg.AdminPreview.SessionTTL != 10*time.Minute {
		t.Fatalf("default follows the preview TTL: %v %v", err, cfg.AdminPreview.SessionTTL)
	}
}

func TestAdminPreviewMustNotOverlapNormalOrigins(t *testing.T) {
	t.Setenv("ADMIN_PREVIEW_SESSION_TTL", "")
	t.Setenv("ADMIN_PREVIEW_ORIGINS", "https://*.example.test")
	mk := func(mut func(*Config)) *Config {
		c := &Config{Env: EnvProduction}
		mut(c)
		return c
	}
	for name, c := range map[string]*Config{
		"cors":          mk(func(c *Config) { c.CORSAllowedOrigins = []string{"https://admin.example.test"} }),
		"admin public":  mk(func(c *Config) { c.AdminPublicURL = "https://admin.example.test" }),
		"website":       mk(func(c *Config) { c.WebsiteURL = "https://www.example.test" }),
		"platform host": mk(func(c *Config) { c.PlatformAdminHosts = []string{"platform.example.test"} }),
	} {
		if err := loadAdminPreview(c); err == nil {
			t.Errorf("overlap with %s was accepted", name)
		}
	}
	ok := mk(func(c *Config) { c.CORSAllowedOrigins = []string{"https://admin.other.test"} })
	if err := loadAdminPreview(ok); err != nil {
		t.Errorf("disjoint origins refused: %v", err)
	}
}
