package config

import (
	"os"
	"strings"
	"testing"
)

// TestMain: Load fails closed (unset APP_ENV = production + default-key
// guard). The pre-existing tests in this package exercise other settings with
// the dev defaults, so they run in test mode unless APP_ENV was exported.
func TestMain(m *testing.M) {
	if os.Getenv("APP_ENV") == "" {
		_ = os.Setenv("APP_ENV", EnvTest)
	}
	os.Exit(m.Run())
}

// Strong, non-default values for every guarded secret (test-only fixtures).
var strongSecrets = map[string]string{
	"ADMIN_JWT_SECRET":                   "test-fixture-admin-jwt-4f9a1c7e2b8d6a0f3e5c9b1d7a2e4f6c",
	"MEMBER_JWT_SECRET":                  "test-fixture-member-jwt-8b2d4f6a0c1e3a5b7d9f1c3e5a7b9d0f",
	"DATABASE_URL":                       "postgres://ecommerce:test-fixture-db-pw-6c1e9a3f@localhost:5432/ecommerce?sslmode=disable",
	"REDIS_PASSWORD":                     "test-fixture-redis-pw-2a7f5c3e",
	"PAYMENT_MOCK_WEBHOOK_SECRET":        "test-fixture-mock-webhook-9d3b1f7a",
	"PAYMENT_CREDENTIALS_ENCRYPTION_KEY": "1111111111111111111111111111111111111111111111111111111111111111",
}

// hermeticEnv isolates Load from the developer's .env (no .env above a temp
// dir) and from values earlier Load calls copied into the process env.
func hermeticEnv(t *testing.T, appEnv string) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", appEnv)
	for k, v := range strongSecrets {
		t.Setenv(k, v)
	}
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("REPORT_DEFAULT_TIMEZONE", "")
	// Media is optional ("unset = off"); a developer's .env pointing at MinIO
	// (and an earlier Load copying it into the process env) must not leak in.
	for _, k := range mediaKeys {
		t.Setenv(k, "")
	}
}

func TestProductionAcceptsStrongSecrets(t *testing.T) {
	hermeticEnv(t, EnvProduction)
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected strong secrets: %v", err)
	}
}

func TestProductionRefusesWeakSecrets(t *testing.T) {
	cases := []struct {
		name, key, value, wantInErr string
	}{
		{"admin jwt config default", "ADMIN_JWT_SECRET", "dev-only-admin-secret", "ADMIN_JWT_SECRET"},
		{"admin jwt old .env.example", "ADMIN_JWT_SECRET", "dev-only-admin-secret-change-me", "ADMIN_JWT_SECRET"},
		{"admin jwt placeholder", "ADMIN_JWT_SECRET", "<generate: openssl rand -base64 48>", "ADMIN_JWT_SECRET"},
		{"admin jwt too short", "ADMIN_JWT_SECRET", "short-but-random-Qx7", "at least 32 bytes"},
		{"member jwt empty", "MEMBER_JWT_SECRET", "", "MEMBER_JWT_SECRET"},
		{"member equals admin", "MEMBER_JWT_SECRET", strongSecrets["ADMIN_JWT_SECRET"], "must differ"},
		{"db password old default", "DATABASE_URL", "postgres://ecommerce:ecommerce@localhost:5432/ecommerce", "DATABASE_URL password"},
		{"db password placeholder", "DATABASE_URL", "postgres://ecommerce:<POSTGRES_PASSWORD>@localhost:5432/ecommerce", "DATABASE_URL"},
		{"db no password", "DATABASE_URL", "postgres://ecommerce@localhost:5432/ecommerce", "DATABASE_URL password"},
		{"db kv dsn default", "DATABASE_URL", "host=localhost user=ecommerce password=ecommerce dbname=ecommerce", "DATABASE_URL password"},
		{"db empty", "DATABASE_URL", "", "DATABASE_URL is required"},
		{"redis empty", "REDIS_PASSWORD", "", "REDIS_PASSWORD"},
		{"redis placeholder", "REDIS_PASSWORD", "<generate: openssl rand -hex 32>", "REDIS_PASSWORD"},
		{"mock webhook default", "PAYMENT_MOCK_WEBHOOK_SECRET", "", "PAYMENT_MOCK_WEBHOOK_SECRET"},
		{"mock webhook .env.example", "PAYMENT_MOCK_WEBHOOK_SECRET", "dev-only-mock-payment-webhook-secret-change-me", "PAYMENT_MOCK_WEBHOOK_SECRET"},
		{"encryption key public default", "PAYMENT_CREDENTIALS_ENCRYPTION_KEY", "", "PAYMENT_CREDENTIALS_ENCRYPTION_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hermeticEnv(t, EnvProduction)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted %s = weak value in production", tc.key)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error does not mention %q: %v", tc.wantInErr, err)
			}
			// Never echo a secret back: neither the offending value nor any of
			// the strong ones may appear in the message.
			for _, v := range append([]string{tc.value}, values(strongSecrets)...) {
				if len(v) >= 8 && strings.Contains(err.Error(), v) {
					t.Errorf("error leaks a secret value: %v", err)
				}
			}
		})
	}
}

func TestSMTPPlaceholderRefusedOnlyWhenConfigured(t *testing.T) {
	hermeticEnv(t, EnvProduction)
	t.Setenv("SMTP_USERNAME", "shop@example.com")
	t.Setenv("SMTP_PASSWORD", "<your gmail app password>")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_PASSWORD") {
		t.Fatalf("placeholder SMTP password accepted: %v", err)
	}
}

// Unset APP_ENV must fail closed: it is production, not development.
func TestUnsetAppEnvIsProduction(t *testing.T) {
	hermeticEnv(t, "")
	t.Setenv("ADMIN_JWT_SECRET", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_JWT_SECRET") {
		t.Fatalf("unset APP_ENV booted with an empty JWT secret: %v", err)
	}
	hermeticEnv(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Env != EnvProduction || cfg.IsDevOrTest() {
		t.Errorf("unset APP_ENV gave Env=%q", cfg.Env)
	}
}

func TestDevAndTestModesAllowDevSecrets(t *testing.T) {
	for _, mode := range []string{EnvDevelopment, EnvTest} {
		t.Run(mode, func(t *testing.T) {
			hermeticEnv(t, mode)
			for k := range strongSecrets {
				t.Setenv(k, "")
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() in %s with dev defaults: %v", mode, err)
			}
			if cfg.AdminJWTSecret == "" || cfg.MemberJWTSecret == "" {
				t.Error("dev JWT fallbacks not applied")
			}
		})
	}
}

func TestUnknownAppEnvRefused(t *testing.T) {
	hermeticEnv(t, "staging")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("APP_ENV=staging accepted: %v", err)
	}
}

func TestTrustedProxiesParsing(t *testing.T) {
	hermeticEnv(t, EnvTest)
	t.Setenv("TRUSTED_PROXIES", " 127.0.0.1 , ::1/128,10.1.2.3/8 ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	want := []string{"127.0.0.1/32", "::1/128", "10.0.0.0/8"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	for i, p := range cfg.TrustedProxies {
		if p.String() != want[i] {
			t.Errorf("TrustedProxies[%d] = %s, want %s", i, p, want[i])
		}
	}

	hermeticEnv(t, EnvTest)
	t.Setenv("TRUSTED_PROXIES", "localhost")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
		t.Fatalf("invalid TRUSTED_PROXIES accepted: %v", err)
	}

	hermeticEnv(t, EnvTest)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("default TrustedProxies = %v, want none", cfg.TrustedProxies)
	}
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
