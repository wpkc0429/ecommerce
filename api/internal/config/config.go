// Package config loads application configuration from environment variables,
// optionally hydrated from a repo-root .env file for local development.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config carries every runtime setting for the API service.
type Config struct {
	Env      string // development | test | production
	Addr     string
	LogLevel string

	DatabaseURL string
	RedisAddr   string

	AdminJWTSecret  string
	MemberJWTSecret string
	JWTIssuer       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	PreviewTokenTTL time.Duration

	// PaymentMockWebhookSecret keys the mock payment provider's HMAC-SHA256
	// webhook signature (change payment-integration, design D2/D9). Unlike
	// the JWT secrets, this is NOT required to be set in production — the
	// mock provider is a demo/reference implementation, not meant to carry
	// real production payment traffic; a real provider's own credentials
	// would get their own config + validation when that change lands.
	PaymentMockWebhookSecret string

	// CORSAllowedOrigins may call the API from a browser (admin SPA, web dev).
	CORSAllowedOrigins []string

	// RateLimit carries the auth-endpoint sliding-window thresholds (design
	// auth-rate-limiting D2/D4).
	RateLimit RateLimitConfig
}

// RateLimitConfig carries the sliding-window thresholds for the
// authentication endpoints (design auth-rate-limiting D2). Each pair applies
// identically to the admin and member variants of that endpoint class;
// Register gets its own (tighter) pair since brand-new signups are rarer
// than repeat logins. Refresh uses a single IP-scoped (or shop+IP-scoped)
// rule — the refresh token itself is already a high-entropy secret, so the
// concern there is abuse/DoS, not credential brute force.
type RateLimitConfig struct {
	LoginBroadLimit  int
	LoginBroadWindow time.Duration
	LoginTightLimit  int
	LoginTightWindow time.Duration

	RegisterBroadLimit  int
	RegisterBroadWindow time.Duration
	RegisterTightLimit  int
	RegisterTightWindow time.Duration

	RefreshLimit  int
	RefreshWindow time.Duration
}

// Load reads configuration from the environment. A .env file (searched in the
// working directory and up to two parent directories) fills in unset variables
// so `go run ./cmd/server` works from /api without exporting anything.
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		Env:             getenv("APP_ENV", "development"),
		Addr:            getenv("API_ADDR", ":8080"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
		DatabaseURL:     getenv("DATABASE_URL", ""),
		RedisAddr:       getenv("REDIS_ADDR", "localhost:6379"),
		AdminJWTSecret:  getenv("ADMIN_JWT_SECRET", ""),
		MemberJWTSecret: getenv("MEMBER_JWT_SECRET", ""),
		JWTIssuer:       getenv("JWT_ISSUER", "ecommerce-api"),

		PaymentMockWebhookSecret: getenv("PAYMENT_MOCK_WEBHOOK_SECRET", "dev-only-mock-payment-webhook-secret"),
	}

	origins := getenv("CORS_ALLOWED_ORIGINS", "http://localhost:3001,http://localhost:3000")
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.CORSAllowedOrigins = append(cfg.CORSAllowedOrigins, o)
		}
	}

	var err error
	if cfg.AccessTokenTTL, err = parseDuration("ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RefreshTokenTTL, err = parseDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.PreviewTokenTTL, err = parseDuration("PREVIEW_TOKEN_TTL", 10*time.Minute); err != nil {
		return nil, err
	}

	// Rate limiting (design auth-rate-limiting D2/D4): sane defaults, no
	// production-only requirement (unlike the JWT secrets) — thresholds are
	// a tunable, not a secret.
	if cfg.RateLimit.LoginBroadLimit, err = parseInt("RATE_LIMIT_LOGIN_BROAD_LIMIT", 20); err != nil {
		return nil, err
	}
	if cfg.RateLimit.LoginBroadWindow, err = parseDuration("RATE_LIMIT_LOGIN_BROAD_WINDOW", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RateLimit.LoginTightLimit, err = parseInt("RATE_LIMIT_LOGIN_TIGHT_LIMIT", 5); err != nil {
		return nil, err
	}
	if cfg.RateLimit.LoginTightWindow, err = parseDuration("RATE_LIMIT_LOGIN_TIGHT_WINDOW", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RateLimit.RegisterBroadLimit, err = parseInt("RATE_LIMIT_REGISTER_BROAD_LIMIT", 10); err != nil {
		return nil, err
	}
	if cfg.RateLimit.RegisterBroadWindow, err = parseDuration("RATE_LIMIT_REGISTER_BROAD_WINDOW", 10*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RateLimit.RegisterTightLimit, err = parseInt("RATE_LIMIT_REGISTER_TIGHT_LIMIT", 3); err != nil {
		return nil, err
	}
	if cfg.RateLimit.RegisterTightWindow, err = parseDuration("RATE_LIMIT_REGISTER_TIGHT_WINDOW", 10*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RateLimit.RefreshLimit, err = parseInt("RATE_LIMIT_REFRESH_LIMIT", 30); err != nil {
		return nil, err
	}
	if cfg.RateLimit.RefreshWindow, err = parseDuration("RATE_LIMIT_REFRESH_WINDOW", 5*time.Minute); err != nil {
		return nil, err
	}

	if cfg.Env == "production" {
		if cfg.AdminJWTSecret == "" || cfg.MemberJWTSecret == "" {
			return nil, fmt.Errorf("config: ADMIN_JWT_SECRET and MEMBER_JWT_SECRET are required in production")
		}
		if cfg.AdminJWTSecret == cfg.MemberJWTSecret {
			return nil, fmt.Errorf("config: admin and member JWT secrets must differ (design D9)")
		}
	}
	// Development fallbacks keep local bootstrapping friction-free.
	if cfg.AdminJWTSecret == "" {
		cfg.AdminJWTSecret = "dev-only-admin-secret"
	}
	if cfg.MemberJWTSecret == "" {
		cfg.MemberJWTSecret = "dev-only-member-secret"
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return d, nil
}

func parseInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return n, nil
}

// loadDotEnv applies KEY=VALUE lines from the first .env found walking up
// from the working directory (tests run from nested package dirs).
// Existing environment variables always win.
func loadDotEnv() {
	for _, dir := range []string{".", "..", "../..", "../../..", "../../../.."} {
		data, err := os.ReadFile(dir + "/.env")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			val = strings.Trim(strings.TrimSpace(val), `"'`)
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
		return
	}
}
