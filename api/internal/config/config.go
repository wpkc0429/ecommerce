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
	// webhook signature (change payment-integration, design D2/D9). Unlike
	// the JWT secrets, this is NOT required to be set in production — the
	// mock provider is a demo/reference implementation, not meant to carry
	// real production payment traffic; a real provider's own credentials
	// would get their own config + validation when that change lands.
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

	// ECPay carries the 綠界 全方位金流 credentials and endpoints.
	ECPay ECPayConfig

	// SMTP carries the outbound mail credentials for order notification emails
	// (change order-notification-email).
	SMTP SMTPConfig

	// Notify carries the notification dispatch tuning (worker pool bounds).
	Notify NotifyConfig

	// CORSAllowedOrigins may call the API from a browser (admin SPA, web dev).
	CORSAllowedOrigins []string

	// RateLimit carries the auth-endpoint sliding-window thresholds (design
	// auth-rate-limiting D2/D4).
	RateLimit RateLimitConfig
}

// ECPayConfig carries one 綠界 merchant account's credentials and endpoints
// (change ecpay-payment-provider).
//
// **Every field has a default, and none of them is required in production** —
// the same treatment PaymentMockWebhookSecret gets, for the same reason: the
// default provider is not ecpay, so an unconfigured deployment never reaches
// this code. Making it a boot-time hard failure would only stop people who just
// want to run the thing locally; a real deployment that forgets to swap the
// credentials discovers it the first time no money arrives.
type ECPayConfig struct {
	// MerchantID/HashKey/HashIV default to 綠界's **published AIO test store**
	// (3002607). These are printed in the public documentation and are not
	// secrets.
	//
	// Note for whoever changes them: the widely-circulated 2000132 /
	// 5294y06JbISpM5x9 / v77hoKGq4kWxNNIS is an OLD set, and the
	// 2000132 / ejCk326UnaZWKisg / q9jcZX8Ib9LM8wYk that search engines love
	// belongs to the **B2C 電子發票** API, not to AIO. Using either produces a
	// checksum that never matches — a symptom indistinguishable from a bug in
	// the algorithm.
	MerchantID string
	HashKey    string
	HashIV     string

	// CheckoutURL is where the customer's browser POSTs the cashier form.
	//
	// **This single value is the entire difference between simulated and live
	// operation** (design D1). It defaults to the local simulator
	// (api/cmd/ecpay-sim, `make ecpay-sim`) so the full flow runs with no ECPay
	// account and no outbound network. Point it at
	// https://payment-stage.ecpay.com.tw/Cashier/AioCheckOut/V5 (or the
	// production host) and not one byte of the form or its checksum changes.
	CheckoutURL string
	// ReturnURL is the absolute, publicly reachable URL 綠界 POSTs the payment
	// result to. 綠界 only supports ports 80 and 443 and does not accept
	// internationalized domain names — a self-hosted dev environment needs a
	// tunnel or reverse proxy, which this change deliberately does not solve.
	ReturnURL string
	// ClientBackURL is where 綠界's page sends the browser afterwards. Optional.
	ClientBackURL string
	// ChoosePayment defaults to Credit. 綠界's own guidance is to name a specific
	// method rather than ALL, because they keep adding methods and a new one can
	// break the notification flow. Non-instant methods (ATM/CVS/BARCODE) also
	// require the PaymentInfoURL notification this change does not implement.
	ChoosePayment string
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
		Env:             getenv("APP_ENV", "development"),
		Addr:            getenv("API_ADDR", ":8080"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
		DatabaseURL:     getenv("DATABASE_URL", ""),
		RedisAddr:       getenv("REDIS_ADDR", "localhost:6379"),
		AdminJWTSecret:  getenv("ADMIN_JWT_SECRET", ""),
		MemberJWTSecret: getenv("MEMBER_JWT_SECRET", ""),
		JWTIssuer:       getenv("JWT_ISSUER", "ecommerce-api"),

		PaymentMockWebhookSecret: getenv("PAYMENT_MOCK_WEBHOOK_SECRET", "dev-only-mock-payment-webhook-secret"),
		PaymentDefaultProvider:   getenv("PAYMENT_DEFAULT_PROVIDER", "mock"),

		ECPay: ECPayConfig{
			MerchantID:    getenv("ECPAY_MERCHANT_ID", "3002607"),
			HashKey:       getenv("ECPAY_HASH_KEY", "pwFHCqoQZGmho4w6"),
			HashIV:        getenv("ECPAY_HASH_IV", "EkRm7iFT261dpevs"),
			CheckoutURL:   getenv("ECPAY_CHECKOUT_URL", "http://localhost:8090/Cashier/AioCheckOut/V5"),
			ReturnURL:     getenv("ECPAY_RETURN_URL", "http://localhost:8080/api/v1/webhooks/payments/ecpay"),
			ClientBackURL: getenv("ECPAY_CLIENT_BACK_URL", ""),
			ChoosePayment: getenv("ECPAY_CHOOSE_PAYMENT", "Credit"),
		},

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
