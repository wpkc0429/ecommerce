// Package config loads application configuration from environment variables,
// optionally hydrated from a repo-root .env file for local development.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/shoptz"
)

// Config carries every runtime setting for the API service.
type Config struct {
	// Env is development | test | production. Unset means production
	// (fail closed): only an explicit development/test may boot with the
	// built-in dev secrets — see validateSecrets.
	Env      string
	Addr     string
	LogLevel string

	DatabaseURL string
	RedisAddr   string
	// RedisPassword is sent as AUTH on every connection (Redis requirepass).
	// Empty means no AUTH — CI's throwaway Redis service has no password; the
	// local docker-compose Redis requires one (REDIS_PASSWORD in .env).
	RedisPassword string

	AdminJWTSecret  string
	MemberJWTSecret string
	JWTIssuer       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	PreviewTokenTTL time.Duration

	// OrderAccessTokenTTL bounds how long a guest's order access token stays
	// usable (change guest-checkout, design D3). Its lower bound is the payment
	// round trip, its upper bound is "this is an unrevocable bearer value" —
	// 24h covers "place the order, come back later today" and expires the next
	// day. Long-term access is not this token's job: registering with the same
	// email claims the identity the order is already bound to.
	OrderAccessTokenTTL time.Duration

	// CheckoutIdempotencyTTL bounds how long a checkout Idempotency-Key stays
	// replayable (change checkout-idempotency, design D8). Its lower bound is the
	// real retry span — a 15s client timeout, a customer reopening the tab that
	// evening. Its upper bound is the *reverse* failure: too long, and a client
	// that wrongly reuses a key turns a genuine second purchase into a silent
	// replay, with no order for the merchant and no error for the customer. 24h
	// is the boundary between "still today" and "certainly a new order", the same
	// reasoning (and the same number) as OrderAccessTokenTTL.
	CheckoutIdempotencyTTL time.Duration

	// PaymentMockWebhookSecret keys the mock payment provider's HMAC-SHA256
	// webhook signature (change payment-integration, design D2/D9). The mock
	// provider is a demo/reference implementation, but its webhook route is
	// always mounted, so outside development/test validateSecrets refuses the
	// public default (it would let anyone forge "payment succeeded").
	PaymentMockWebhookSecret string

	// PaymentDefaultProvider names the provider used when a checkout request
	// does not ask for one (change ecpay-payment-provider, design D13).
	//
	// It defaults to "mock" so that **adding the ECPay provider changes nothing
	// for any existing caller**: DefaultProvider decides the `provider`,
	// `provider_reference` and `redirect_form` a v1 response carries, and
	// switching it would alter two live endpoints' output without anyone having
	// asked. A real deployment sets PAYMENT_DEFAULT_PROVIDER=ecpay; tests and
	// e2e pass {"provider":"ecpay"} explicitly.
	PaymentDefaultProvider string

	// ECPay carries the 綠界 全方位金流 endpoints. Per-merchant credentials
	// (MerchantID/HashKey/HashIV/ChoosePayment) moved to per-shop, encrypted
	// storage (change ecpay-per-shop-credentials, design D5) — this is what is
	// left that is genuinely platform-wide: which environment (simulator vs.
	// 綠界's own test/production hosts) the whole deployment talks to.
	ECPay ECPayConfig

	// PaymentCredentialsEncryptionKey encrypts the per-shop payment provider
	// secrets stored in shop_payment_provider_credentials (change
	// ecpay-per-shop-credentials, design D4) — hex or base64, must decode to
	// exactly cryptbox.KeySize bytes. Validated at boot in every environment,
	// the same treatment ReportDefaultTimezone gets and for the same reason: a
	// bad value here does not disable a feature, it makes every credential
	// read/write 500 — discoverable in one second at boot, not through a
	// support ticket. The default below is a fixed, publicly-known value (it
	// ships in this repository) — fine for local development, MUST be
	// overridden before any real ECPay credential is ever stored.
	PaymentCredentialsEncryptionKey string

	// SMTP carries the outbound mail credentials for order notification emails
	// (change order-notification-email).
	SMTP SMTPConfig

	// Notify carries the notification dispatch tuning (worker pool bounds).
	Notify NotifyConfig

	// ReportDefaultTimezone is the IANA zone merchant reports fall back to when
	// a shop has not set one (change merchant-reports, design D1).
	//
	// It is the ONE new setting that makes boot fail when it is wrong, and that
	// is deliberate — the opposite treatment from PaymentMockWebhookSecret and
	// the ECPay/SMTP blocks. Those are optional features whose absence means
	// "off". This one is not optional and has no "off": a typo here makes every
	// report, for every shop that has not configured a zone, answer 500. That is
	// discoverable in one second at boot and only through a support ticket
	// otherwise.
	//
	// Unset behaves exactly as the default value, and since no report existed
	// before this change, "unchanged from before" is automatic.
	ReportDefaultTimezone string

	// TrustedProxies are the peers (CIDRs) whose CF-Connecting-IP header is
	// believed when deriving the client IP (TRUSTED_PROXIES, comma-separated
	// IPs or CIDRs). Empty = trust nobody: the client IP is always the TCP
	// peer. For a Cloudflare Tunnel whose cloudflared runs on this host, set
	// 127.0.0.1/32,::1/128. X-Forwarded-For is never consulted.
	TrustedProxies []netip.Prefix

	// CORSAllowedOrigins may call the API from a browser (admin SPA, web dev).
	CORSAllowedOrigins []string

	// RateLimit carries the auth-endpoint sliding-window thresholds (design
	// auth-rate-limiting D2/D4).
	RateLimit RateLimitConfig
}

// ECPayConfig carries the platform-wide 綠界 endpoints — what is left after
// per-merchant credentials moved to per-shop storage (change
// ecpay-per-shop-credentials, design D5/D7). Every field here decides "which
// environment the whole deployment talks to", not "whose money this is",
// which is why it stays platform-level: the same 綠界 production host serves
// every shop that has gone live.
type ECPayConfig struct {
	// CheckoutURL is where the customer's browser POSTs the cashier form.
	//
	// **This single value is the entire difference between simulated and live
	// operation** (design D1). It defaults to the local simulator
	// (api/cmd/ecpay-sim, `make ecpay-sim`) so the full flow runs with no ECPay
	// account and no outbound network. Point it at
	// https://payment-stage.ecpay.com.tw/Cashier/AioCheckOut/V5 (or the
	// production host) and not one byte of the form or its checksum changes.
	CheckoutURL string
	// ReturnURL is the BASE of the absolute, publicly reachable URL 綠界 POSTs
	// the payment result to — the per-shop webhook path
	// (`/webhooks/payments/ecpay/{shopID}`) is appended to it at InitiatePayment
	// time (change ecpay-per-shop-credentials, design D2), because 綠界 only
	// ever echoes back whatever ReturnURL we hand it. 綠界 only supports ports
	// 80 and 443 and does not accept internationalized domain names — a
	// self-hosted dev environment needs a tunnel or reverse proxy, which this
	// change deliberately does not solve.
	ReturnURL string
	// ClientBackURL is where 綠界's page sends the browser afterwards. Optional.
	ClientBackURL string
}

// SMTPConfig carries the outbound mail account for order notification emails
// (change order-notification-email, design D9/D10).
//
// **Nothing is required, and an unset account means "do not send"** — the same
// treatment PaymentMockWebhookSecret and ECPayConfig get, for a stronger reason:
// with no credentials this system must behave byte for byte as it did before
// notifications existed. No mail, no error, no rows, no goroutines.
//
// The defaults point at Gmail because that is what this deployment uses today
// (design D9), but no code in internal/notify is Gmail-specific. Moving to a
// transactional provider with domain authentication is, in the cheapest case,
// these three values.
type SMTPConfig struct {
	Host string
	Port int
	// Username/Password: for Gmail, Password MUST be an **App Password**
	// generated after enabling 2-step verification — Google removed "less secure
	// app access", and an account password simply fails to authenticate.
	Username string
	Password string
	// From defaults to Username. Gmail rewrites From to the authenticated
	// account unless the address is a verified alias, so setting this to
	// something else looks like it works and does not.
	From string
	// FromName is the display name. Today it is a platform-level value: the
	// sender is the platform's mailbox, not the merchant's, and shops carry no
	// sender identity at all (design D3 — including what has to change when that
	// stops being acceptable, which is when the second merchant goes live).
	FromName string
	// Timeout bounds one whole submission (dial + TLS + auth + DATA). Without a
	// hard bound a black-holed route parks a worker on the kernel's TCP retry
	// schedule for ~130 seconds.
	Timeout time.Duration
}

// Enabled reports whether outbound mail is configured. Both halves of the
// credential are required: a username without a password is a half-finished
// configuration, and treating it as "on" would produce authentication failures
// on every order instead of the silence an unconfigured deployment expects.
func (c SMTPConfig) Enabled() bool { return c.Username != "" && c.Password != "" }

// NotifyConfig tunes the notification dispatcher (change
// order-notification-email, design D5). Both values bound an in-process,
// non-durable worker pool — this is deliberately not a work queue, and the
// design records what that costs.
type NotifyConfig struct {
	Workers   int
	QueueSize int
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

	// Guest checkout (change guest-checkout, design D9) gets its own pair
	// rather than borrowing Register's: it is the only unauthenticated endpoint
	// that writes business data and holds stock, so it needs its own dial. The
	// broad rule stays generous because CGNAT and corporate egress routinely
	// put many real buyers behind one IP, and a false 429 here loses a sale;
	// the tight (per-email) rule is what actually bounds "mass-create orders
	// under one address" and "probe for addresses".
	GuestCheckoutBroadLimit  int
	GuestCheckoutBroadWindow time.Duration
	GuestCheckoutTightLimit  int
	GuestCheckoutTightWindow time.Duration
}

// Load reads configuration from the environment. A .env file (searched in the
// working directory and up to two parent directories) fills in unset variables
// so `go run ./cmd/server` works from /api without exporting anything.
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		Env:             getenv("APP_ENV", EnvProduction),
		Addr:            getenv("API_ADDR", ":8080"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
		DatabaseURL:     getenv("DATABASE_URL", ""),
		RedisAddr:       getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:   getenv("REDIS_PASSWORD", ""),
		AdminJWTSecret:  getenv("ADMIN_JWT_SECRET", ""),
		MemberJWTSecret: getenv("MEMBER_JWT_SECRET", ""),
		JWTIssuer:       getenv("JWT_ISSUER", "ecommerce-api"),

		PaymentMockWebhookSecret: getenv("PAYMENT_MOCK_WEBHOOK_SECRET", "dev-only-mock-payment-webhook-secret"),
		PaymentDefaultProvider:   getenv("PAYMENT_DEFAULT_PROVIDER", "mock"),

		ReportDefaultTimezone: getenv("REPORT_DEFAULT_TIMEZONE", "Asia/Taipei"),

		ECPay: ECPayConfig{
			CheckoutURL:   getenv("ECPAY_CHECKOUT_URL", "http://localhost:8090/Cashier/AioCheckOut/V5"),
			ReturnURL:     getenv("ECPAY_RETURN_URL", "http://localhost:8080/api/v1/webhooks/payments/ecpay"),
			ClientBackURL: getenv("ECPAY_CLIENT_BACK_URL", ""),
		},

		// change ecpay-per-shop-credentials, design D4: a fixed dev-only value.
		// It is public (it ships in this repository) — fine for local
		// development and CI, MUST be overridden with a private value before
		// any real credential is stored.
		PaymentCredentialsEncryptionKey: getenv("PAYMENT_CREDENTIALS_ENCRYPTION_KEY",
			devPaymentCredentialsEncryptionKey),

		SMTP: SMTPConfig{
			Host:     getenv("SMTP_HOST", "smtp.gmail.com"),
			Username: getenv("SMTP_USERNAME", ""),
			Password: getenv("SMTP_PASSWORD", ""),
			From:     getenv("SMTP_FROM", ""),
			FromName: getenv("SMTP_FROM_NAME", ""),
		},
	}

	origins := getenv("CORS_ALLOWED_ORIGINS", "http://localhost:3001,http://localhost:3000")
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.CORSAllowedOrigins = append(cfg.CORSAllowedOrigins, o)
		}
	}

	switch cfg.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		return nil, fmt.Errorf("config: APP_ENV must be %q, %q or %q", EnvDevelopment, EnvTest, EnvProduction)
	}

	var err error
	if cfg.TrustedProxies, err = parsePrefixes("TRUSTED_PROXIES"); err != nil {
		return nil, err
	}
	if cfg.AccessTokenTTL, err = parseDuration("ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RefreshTokenTTL, err = parseDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.PreviewTokenTTL, err = parseDuration("PREVIEW_TOKEN_TTL", 10*time.Minute); err != nil {
		return nil, err
	}
	if cfg.OrderAccessTokenTTL, err = parseDuration("ORDER_ACCESS_TOKEN_TTL", 24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.CheckoutIdempotencyTTL, err = parseDuration("CHECKOUT_IDEMPOTENCY_TTL", 24*time.Hour); err != nil {
		return nil, err
	}

	// Outbound mail (change order-notification-email). All optional; an unset
	// SMTP_USERNAME/SMTP_PASSWORD means notifications are off entirely.
	if cfg.SMTP.Port, err = parseInt("SMTP_PORT", 587); err != nil {
		return nil, err
	}
	if cfg.SMTP.Timeout, err = parseDuration("SMTP_TIMEOUT", 20*time.Second); err != nil {
		return nil, err
	}
	if cfg.SMTP.From == "" {
		// Gmail would rewrite anything else anyway (see SMTPConfig.From).
		cfg.SMTP.From = cfg.SMTP.Username
	}
	if cfg.Notify.Workers, err = parseInt("NOTIFY_WORKERS", 4); err != nil {
		return nil, err
	}
	if cfg.Notify.QueueSize, err = parseInt("NOTIFY_QUEUE_SIZE", 256); err != nil {
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
	if cfg.RateLimit.GuestCheckoutBroadLimit, err = parseInt("RATE_LIMIT_GUEST_CHECKOUT_BROAD_LIMIT", 20); err != nil {
		return nil, err
	}
	if cfg.RateLimit.GuestCheckoutBroadWindow, err = parseDuration("RATE_LIMIT_GUEST_CHECKOUT_BROAD_WINDOW", 10*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RateLimit.GuestCheckoutTightLimit, err = parseInt("RATE_LIMIT_GUEST_CHECKOUT_TIGHT_LIMIT", 5); err != nil {
		return nil, err
	}
	if cfg.RateLimit.GuestCheckoutTightWindow, err = parseDuration("RATE_LIMIT_GUEST_CHECKOUT_TIGHT_WINDOW", 10*time.Minute); err != nil {
		return nil, err
	}

	// change merchant-reports (design D1): validated at boot, in every
	// environment. A bad value here is not a degraded feature, it is a 500 on
	// every report of every unconfigured shop — and it is free to catch now.
	if err := shoptz.Validate(cfg.ReportDefaultTimezone); err != nil {
		return nil, fmt.Errorf("config: REPORT_DEFAULT_TIMEZONE: %w", err)
	}

	// change ecpay-per-shop-credentials (design D4): same treatment, same
	// reason. Only Load itself needs the parsed Box (to fail fast); callers
	// that actually encrypt/decrypt re-parse it from the validated string.
	if _, err := cryptbox.ParseKey(cfg.PaymentCredentialsEncryptionKey); err != nil {
		return nil, fmt.Errorf("config: PAYMENT_CREDENTIALS_ENCRYPTION_KEY: %w", err)
	}

	// Default-key guard: outside development/test, refuse to boot with any
	// empty, known-default or placeholder secret (names only, never values).
	if !cfg.IsDevOrTest() {
		if err := cfg.validateSecrets(); err != nil {
			return nil, err
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

// parsePrefixes reads a comma-separated list of IPs or CIDRs. A bare IP
// becomes a single-address prefix.
func parsePrefixes(key string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(os.Getenv(key), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("config: %s: %w", key, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", key, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
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
