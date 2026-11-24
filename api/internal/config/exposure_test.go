package config

import (
	"strings"
	"testing"
	"time"
)

func TestCFAccessConfig(t *testing.T) {
	cases := []struct {
		name          string
		team, aud, bi string
		wantErr       string
		enabled, bind bool
	}{
		{name: "unset", enabled: false},
		{name: "both", team: "team.cloudflareaccess.com", aud: "abc", enabled: true},
		{name: "scheme and slash tolerated", team: "https://Team.cloudflareaccess.com/", aud: "abc", enabled: true},
		{name: "team only", team: "team.cloudflareaccess.com", wantErr: "together"},
		{name: "aud only", aud: "abc", wantErr: "together"},
		{name: "bind without access", bi: "true", wantErr: "BIND_EMAIL"},
		{name: "bind with access", team: "team.cloudflareaccess.com", aud: "abc", bi: "true", enabled: true, bind: true},
		{name: "bad bool", team: "team.cloudflareaccess.com", aud: "abc", bi: "maybe", wantErr: "BIND_EMAIL"},
		{name: "path in team", team: "team.cloudflareaccess.com/x", aud: "abc", wantErr: "bare host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hermeticEnv(t, EnvDevelopment)
			t.Setenv("CF_ACCESS_TEAM_DOMAIN", c.team)
			t.Setenv("CF_ACCESS_AUD", c.aud)
			t.Setenv("CF_ACCESS_BIND_EMAIL", c.bi)
			cfg, err := Load()
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.CFAccess.Enabled() != c.enabled || cfg.CFAccess.BindEmail != c.bind {
				t.Fatalf("got %+v", cfg.CFAccess)
			}
			if c.enabled && cfg.CFAccess.TeamDomain != "team.cloudflareaccess.com" {
				t.Fatalf("team not normalized: %q", cfg.CFAccess.TeamDomain)
			}
		})
	}
}

func TestPlatformAdminHostsAndLockoutDefaults(t *testing.T) {
	hermeticEnv(t, EnvDevelopment)
	t.Setenv("CF_ACCESS_TEAM_DOMAIN", "")
	t.Setenv("CF_ACCESS_AUD", "")
	t.Setenv("CF_ACCESS_BIND_EMAIL", "")
	t.Setenv("PLATFORM_ADMIN_HOSTS", " Admin.Example.com , ,other.example.com:8443")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.PlatformAdminHosts, "|"); got != "admin.example.com|other.example.com:8443" {
		t.Fatalf("hosts: %q", got)
	}
	if cfg.RateLimit.LoginAccountFailures != 10 || cfg.RateLimit.LoginAccountWindow != 15*time.Minute {
		t.Fatalf("lockout defaults: %+v", cfg.RateLimit)
	}
	t.Setenv("PLATFORM_ADMIN_HOSTS", "https://admin.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("a URL in PLATFORM_ADMIN_HOSTS must be rejected")
	}
	t.Setenv("PLATFORM_ADMIN_HOSTS", "")
	t.Setenv("RATE_LIMIT_LOGIN_ACCOUNT_FAILURES", "3")
	t.Setenv("RATE_LIMIT_LOGIN_ACCOUNT_WINDOW", "1m")
	cfg, err = Load()
	if err != nil || cfg.RateLimit.LoginAccountFailures != 3 || cfg.RateLimit.LoginAccountWindow != time.Minute || len(cfg.PlatformAdminHosts) != 0 {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
}
