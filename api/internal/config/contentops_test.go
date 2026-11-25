package config

import (
	"testing"
	"time"
)

func TestContentOpsDefaults(t *testing.T) {
	for _, k := range []string{"CONTENT_HOOK_ALLOWED_HOSTS", "CONTENT_HOOK_DEBOUNCE", "CONTENT_HOOK_DAILY_CAP", "CONTENT_HOOK_DEV_INSECURE", "CONTENT_SCHEDULER_ENABLED", "CONTENT_AUDIT_RETENTION"} {
		t.Setenv(k, "")
	}
	cfg := &Config{Env: EnvProduction}
	if err := loadContentOps(cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg.ContentOps
	if c.HookDebounce != 30*time.Second || c.HookMinInterval != time.Minute || c.HookDailyCap != 30 || c.HookQueueSize != 256 {
		t.Fatalf("hook defaults: %+v", c)
	}
	if c.ScheduleEnabled {
		t.Fatal("the in-process scheduler must be off by default")
	}
	if c.AuditRetention != 180*24*time.Hour || c.ScheduleMinLead != 2*time.Minute || c.ScheduleMaxAhead != 90*24*time.Hour {
		t.Fatalf("defaults: %+v", c)
	}
	if len(c.HookExtraHosts) != 0 || c.HookDevInsecure {
		t.Fatalf("no extra hosts and no dev mode by default: %+v", c)
	}
}

func TestContentOpsHostsAndDevSwitch(t *testing.T) {
	t.Setenv("CONTENT_HOOK_ALLOWED_HOSTS", " Hooks.Example.com , *.deploy.example.org ,")
	cfg := &Config{Env: EnvProduction}
	if err := loadContentOps(cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ContentOps.HookExtraHosts; len(got) != 2 || got[0] != "hooks.example.com" || got[1] != "*.deploy.example.org" {
		t.Fatalf("hosts: %v", got)
	}
	for _, bad := range []string{"https://x.test", "x.test/path", "x.test:8443", "a@b.test", "*", "x.*.test"} {
		t.Setenv("CONTENT_HOOK_ALLOWED_HOSTS", bad)
		if err := loadContentOps(&Config{Env: EnvProduction}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	t.Setenv("CONTENT_HOOK_ALLOWED_HOSTS", "")
	t.Setenv("CONTENT_HOOK_DEV_INSECURE", "1")
	if err := loadContentOps(&Config{Env: EnvProduction}); err == nil {
		t.Fatal("the dev-insecure hook switch must be refused in production")
	}
	if err := loadContentOps(&Config{Env: EnvDevelopment}); err != nil {
		t.Fatalf("dev: %v", err)
	}
}

func TestContentOpsRejectsOutOfRange(t *testing.T) {
	t.Setenv("CONTENT_HOOK_DAILY_CAP", "0")
	if err := loadContentOps(&Config{Env: EnvTest}); err == nil {
		t.Fatal("cap 0 accepted")
	}
	t.Setenv("CONTENT_HOOK_DAILY_CAP", "")
	t.Setenv("CONTENT_HOOK_DEBOUNCE", "0s")
	if err := loadContentOps(&Config{Env: EnvTest}); err == nil {
		t.Fatal("debounce 0 accepted")
	}
}
