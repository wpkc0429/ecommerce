package config

import (
	"strings"
	"testing"
)

// Audit M3: production never defaults to, or accepts, the mock provider.
func TestPaymentDefaultProviderByEnv(t *testing.T) {
	hermeticEnv(t, EnvProduction)
	t.Setenv("PAYMENT_DEFAULT_PROVIDER", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.PaymentDefaultProvider != "ecpay" {
		t.Errorf("production default = %q, want ecpay", cfg.PaymentDefaultProvider)
	}

	hermeticEnv(t, EnvProduction)
	t.Setenv("PAYMENT_DEFAULT_PROVIDER", "mock")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAYMENT_DEFAULT_PROVIDER") {
		t.Errorf("production accepted mock default: %v", err)
	}

	for _, env := range []string{EnvDevelopment, EnvTest} {
		hermeticEnv(t, env)
		t.Setenv("PAYMENT_DEFAULT_PROVIDER", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s Load(): %v", env, err)
		}
		if cfg.PaymentDefaultProvider != "mock" {
			t.Errorf("%s default = %q, want mock", env, cfg.PaymentDefaultProvider)
		}
	}
}

// Audit M4: baseline limits are on by default in production, off in dev/test,
// and overridable by env.
func TestBaselineRateLimitDefaults(t *testing.T) {
	hermeticEnv(t, EnvProduction)
	t.Setenv("PAYMENT_DEFAULT_PROVIDER", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaselineRateLimit != 300 || cfg.WebhookRateLimit != 600 {
		t.Errorf("production defaults = %d/%d, want 300/600", cfg.BaselineRateLimit, cfg.WebhookRateLimit)
	}

	hermeticEnv(t, EnvDevelopment)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaselineRateLimit != 0 || cfg.WebhookRateLimit != 0 {
		t.Errorf("dev defaults = %d/%d, want disabled", cfg.BaselineRateLimit, cfg.WebhookRateLimit)
	}

	hermeticEnv(t, EnvDevelopment)
	t.Setenv("RATE_LIMIT_BASELINE_LIMIT", "50")
	t.Setenv("RATE_LIMIT_WEBHOOK_WINDOW", "30s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaselineRateLimit != 50 || cfg.WebhookRateWindow.Seconds() != 30 {
		t.Errorf("overrides not applied: %d / %v", cfg.BaselineRateLimit, cfg.WebhookRateWindow)
	}
}
