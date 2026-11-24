package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Environment modes (APP_ENV). Only development and test are allowed to run
// with the built-in/public dev secrets; every other mode — including an unset
// APP_ENV — is treated as a real deployment and must pass validateSecrets.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// devPaymentCredentialsEncryptionKey is the fixed, PUBLIC dev key (it ships in
// this repository). Fine for development/CI; refused anywhere else.
const devPaymentCredentialsEncryptionKey = "87cdc594ad8dada3e6447ee26c8e6ac18842eadaf7170f4820d53057acb78ae4"

// devMockWebhookSecret is the built-in default for PAYMENT_MOCK_WEBHOOK_SECRET.
// The mock provider's webhook route is always mounted, so in a real deployment
// a publicly-known key would let anyone forge "payment succeeded" callbacks.
const devMockWebhookSecret = "dev-only-mock-payment-webhook-secret"

// minJWTSecretLen is the shortest HMAC JWT secret accepted outside dev/test.
// 32 bytes = the HS256 output size; anything shorter is brute-forceable
// offline from a single captured token.
const minJWTSecretLen = 32

// knownDefaultSecrets are values that have shipped in this repository (config
// defaults, .env.example, docs, CI) at some point. They are public, so they are
// never acceptable outside development/test, whatever field they appear in.
var knownDefaultSecrets = map[string]struct{}{
	"dev-only-admin-secret":                          {},
	"dev-only-member-secret":                         {},
	"dev-only-admin-secret-change-me":                {},
	"dev-only-member-secret-change-me":               {},
	devMockWebhookSecret:                             {},
	"dev-only-mock-payment-webhook-secret-change-me": {},
	devPaymentCredentialsEncryptionKey:               {},
	"admin-change-me":                                {},
	"ecommerce":                                      {}, // old POSTGRES_PASSWORD default / CI
	"postgres":                                       {},
	"password":                                       {},
	"secret":                                         {},
	"changeme":                                       {},
	"change-me":                                      {},
	"minioadmin":                                     {}, // MinIO default root credentials
}

// IsDevOrTest reports whether the service runs in a mode that may use the
// built-in development secrets. Anything else is a real deployment.
func (c *Config) IsDevOrTest() bool {
	return c.Env == EnvDevelopment || c.Env == EnvTest
}

// IsWeakSecret reports whether v is empty, a known public default, or an
// unfilled placeholder from .env.example (e.g. "<generate: openssl rand ...>").
// It never returns or logs the value itself.
func IsWeakSecret(v string) bool {
	s := strings.TrimSpace(v)
	if s == "" {
		return true
	}
	if _, ok := knownDefaultSecrets[s]; ok {
		return true
	}
	if _, ok := knownDefaultSecrets[strings.ToLower(s)]; ok {
		return true
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(s, "<") || strings.Contains(s, ">"): // <generate: …>, <POSTGRES_PASSWORD>
		return true
	case strings.HasPrefix(lower, "dev-only-"):
		return true
	case strings.Contains(lower, "change-me"), strings.Contains(lower, "changeme"), strings.Contains(lower, "change_me"):
		return true
	}
	return false
}

// validateSecrets is the default-key guard: outside development/test it
// refuses to boot when any secret is empty, a known default, or a placeholder.
// Errors name the offending setting only — never its value.
func (c *Config) validateSecrets() error {
	var errs []error
	weak := func(key, v string) {
		if IsWeakSecret(v) {
			errs = append(errs, fmt.Errorf("%s is empty, a known default, or an unfilled placeholder", key))
		}
	}

	weak("ADMIN_JWT_SECRET", c.AdminJWTSecret)
	weak("MEMBER_JWT_SECRET", c.MemberJWTSecret)
	if len(c.AdminJWTSecret) < minJWTSecretLen && !IsWeakSecret(c.AdminJWTSecret) {
		errs = append(errs, fmt.Errorf("ADMIN_JWT_SECRET must be at least %d bytes", minJWTSecretLen))
	}
	if len(c.MemberJWTSecret) < minJWTSecretLen && !IsWeakSecret(c.MemberJWTSecret) {
		errs = append(errs, fmt.Errorf("MEMBER_JWT_SECRET must be at least %d bytes", minJWTSecretLen))
	}
	if c.AdminJWTSecret != "" && c.AdminJWTSecret == c.MemberJWTSecret {
		errs = append(errs, errors.New("ADMIN_JWT_SECRET and MEMBER_JWT_SECRET must differ (design D9)"))
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	} else if pw, err := databasePassword(c.DatabaseURL); err != nil {
		errs = append(errs, fmt.Errorf("DATABASE_URL: %w", err))
	} else {
		weak("DATABASE_URL password", pw)
	}

	weak("REDIS_PASSWORD", c.RedisPassword)
	weak("PAYMENT_MOCK_WEBHOOK_SECRET", c.PaymentMockWebhookSecret)
	weak("PAYMENT_CREDENTIALS_ENCRYPTION_KEY", c.PaymentCredentialsEncryptionKey)

	// SMTP is optional ("unset = off"), but a configured account must not use
	// a placeholder password.
	if c.SMTP.Username != "" || c.SMTP.Password != "" {
		weak("SMTP_PASSWORD", c.SMTP.Password)
	}

	// Media is optional ("unset = off"), but a configured secret key must not be
	// a known default such as MinIO's minioadmin (change product-images-r2).
	if c.Media.Enabled() {
		weak("R2_SECRET_ACCESS_KEY", c.Media.SecretAccessKey)
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("config: refusing to start with APP_ENV=%q (only %q/%q may use dev secrets): %w",
		c.Env, EnvDevelopment, EnvTest, errors.Join(errs...))
}

// databasePassword extracts the password from a postgres URL or a libpq
// key=value DSN. The error never includes the DSN (it carries the password).
func databasePassword(dsn string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", errors.New("not a valid URL (unfilled placeholder?)")
		}
		if u.User == nil {
			return "", nil
		}
		pw, _ := u.User.Password()
		return pw, nil
	}
	for _, field := range strings.Fields(dsn) {
		if k, v, ok := strings.Cut(field, "="); ok && k == "password" {
			return strings.Trim(v, `'`), nil
		}
	}
	return "", nil
}
