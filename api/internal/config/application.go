package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// DefaultTurnstileVerifyURL is Cloudflare's siteverify endpoint.
const DefaultTurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// DefaultConsentVersion is the placeholder terms version until the operator
// supplies real terms text (change platform-website, Q11). The website's
// config.js must send exactly the value the API holds.
const DefaultConsentVersion = "placeholder-v0"

// cloudflareTestSecrets are the dummy secret keys Cloudflare publishes for
// testing (https://developers.cloudflare.com/turnstile/troubleshooting/testing/).
// "1x..." always passes, so in production it would be the silent-disable switch
// this guard exists to prevent: refused outside development/test.
var cloudflareTestSecrets = map[string]struct{}{
	"1x0000000000000000000000000000000AA": {},
	"2x0000000000000000000000000000000AA": {},
	"3x0000000000000000000000000000000AA": {},
}

// TurnstileConfig is the Cloudflare Turnstile verification setup of the public
// merchant application endpoint (design D3).
type TurnstileConfig struct {
	// SecretKey: required when APP_ENV=production. In development/test an empty
	// value selects a local verifier that makes no network call.
	SecretKey string
	// SiteKey is public and only documented here (the website's config.js holds
	// the real copy); the API never uses it.
	SiteKey string
	// VerifyURL overrides the siteverify endpoint (tests and local fakes).
	VerifyURL string
}

// ApplicationConfig carries the merchant application policy (change
// platform-website).
type ApplicationConfig struct {
	// DailyCap bounds accepted applications per rolling 24h across the platform.
	DailyCap int
	// ReapplyCooldown: after a rejection (or approval) the same e-mail cannot
	// open a new application for this long. Default 30 days.
	ReapplyCooldown time.Duration
	// RejectedRetention / ApprovedRetention: how long PII of a decided
	// application is kept before cmd/purge-applications blanks it. 90 days /
	// 1 year, PENDING LEGAL CONFIRMATION by the operator.
	RejectedRetention time.Duration
	ApprovedRetention time.Duration
	// ConsentVersion is the current terms/privacy version the form must echo.
	ConsentVersion string
}

func loadApplication(cfg *Config) error {
	var err error
	cfg.Turnstile = TurnstileConfig{
		SecretKey: strings.TrimSpace(getenv("TURNSTILE_SECRET_KEY", "")),
		SiteKey:   strings.TrimSpace(getenv("TURNSTILE_SITE_KEY", "")),
		VerifyURL: strings.TrimSpace(getenv("TURNSTILE_VERIFY_URL", DefaultTurnstileVerifyURL)),
	}
	if u, perr := url.Parse(cfg.Turnstile.VerifyURL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("config: TURNSTILE_VERIFY_URL must be an http(s) URL")
	}
	site, err := parseOrigin("WEBSITE_URL", getenv("WEBSITE_URL", "http://localhost:4000"))
	if err != nil {
		return err
	}
	cfg.WebsiteURL = site

	a := &cfg.Application
	if a.DailyCap, err = parseInt("APPLICATION_DAILY_CAP", 200); err != nil {
		return err
	}
	if a.ReapplyCooldown, err = parseDuration("APPLICATION_REAPPLY_COOLDOWN", 30*24*time.Hour); err != nil {
		return err
	}
	if a.RejectedRetention, err = parseDuration("APPLICATION_REJECTED_RETENTION", 90*24*time.Hour); err != nil {
		return err
	}
	if a.ApprovedRetention, err = parseDuration("APPLICATION_APPROVED_RETENTION", 365*24*time.Hour); err != nil {
		return err
	}
	a.ConsentVersion = strings.TrimSpace(getenv("APPLICATION_CONSENT_VERSION", DefaultConsentVersion))
	if a.ConsentVersion == "" || len(a.ConsentVersion) > 32 || strings.ContainsAny(a.ConsentVersion, "\r\n") {
		return errors.New("config: APPLICATION_CONSENT_VERSION must be 1-32 characters")
	}
	if a.DailyCap < 1 {
		return errors.New("config: APPLICATION_DAILY_CAP must be at least 1")
	}
	if a.ReapplyCooldown < 0 || a.RejectedRetention <= 0 || a.ApprovedRetention <= 0 {
		return errors.New("config: application retention and cooldown durations must be positive")
	}

	rl := &cfg.RateLimit
	for _, p := range []struct {
		limit  *int
		window *time.Duration
		key    string
		l      int
		w      time.Duration
	}{
		{&rl.ApplicationBroadLimit, &rl.ApplicationBroadWindow, "RATE_LIMIT_APPLICATION_BROAD", 20, 10 * time.Minute},
		{&rl.ApplicationTightLimit, &rl.ApplicationTightWindow, "RATE_LIMIT_APPLICATION_TIGHT", 5, 10 * time.Minute},
		{&rl.ApplicationEmailLimit, &rl.ApplicationEmailWindow, "RATE_LIMIT_APPLICATION_EMAIL", 3, 24 * time.Hour},
	} {
		if *p.limit, err = parseInt(p.key+"_LIMIT", p.l); err != nil {
			return err
		}
		if *p.window, err = parseDuration(p.key+"_WINDOW", p.w); err != nil {
			return err
		}
	}
	return nil
}

// validateApplication is the production-only part: the only bot gate must not
// be silently off (design D3).
func (c *Config) validateApplication() error {
	var errs []error
	if c.Turnstile.SecretKey == "" {
		errs = append(errs, errors.New("TURNSTILE_SECRET_KEY is required (the public merchant application endpoint has no other bot gate)"))
	} else {
		if _, ok := cloudflareTestSecrets[c.Turnstile.SecretKey]; ok {
			errs = append(errs, errors.New("TURNSTILE_SECRET_KEY is one of Cloudflare's published test secrets"))
		}
		if IsWeakSecret(c.Turnstile.SecretKey) {
			errs = append(errs, errors.New("TURNSTILE_SECRET_KEY is a known default or an unfilled placeholder"))
		}
	}
	if strings.TrimSpace(getenv("WEBSITE_URL", "")) == "" {
		errs = append(errs, errors.New("WEBSITE_URL is required (base of e-mail verification links and the only CORS origin of the public application API)"))
	} else if !strings.HasPrefix(c.WebsiteURL, "https://") {
		errs = append(errs, errors.New("WEBSITE_URL must be an https origin"))
	}
	if c.Turnstile.VerifyURL != DefaultTurnstileVerifyURL {
		errs = append(errs, errors.New("TURNSTILE_VERIFY_URL must not be overridden in production"))
	}
	return errors.Join(errs...)
}

// parseOrigin validates an http(s) origin (no path, query, fragment) and strips
// a trailing slash.
func parseOrigin(key, raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.Fragment != "" || u.RawQuery != "" || u.Path != "" || u.User != nil {
		return "", fmt.Errorf("config: %s must be an http(s) origin without path, query or fragment, got %q", key, raw)
	}
	return raw, nil
}
