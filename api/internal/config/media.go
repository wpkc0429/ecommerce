package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults and bounds for media (change product-images-r2).
const (
	DefaultMediaMaxBytes           = 15 << 20 // 15 MiB per file
	DefaultMediaMaxPixels          = 40_000_000
	DefaultMediaProcessConcurrency = 2
	DefaultMediaWebPQuality        = 80
	DefaultMediaUploadURLTTL       = 10 * time.Minute
)

// MediaConfig is the object-storage (Cloudflare R2, or MinIO locally) and
// image-pipeline configuration (design D2).
//
// The five required values are all-or-nothing: all present → media enabled,
// all empty → disabled (every media route still mounts and answers 503), some
// present → the service refuses to start, naming what is missing.
type MediaConfig struct {
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	// PublicBaseURL is the public image domain, no trailing slash. Image URLs are
	// composed from it at read time; the database stores keys only (design D5).
	PublicBaseURL string
	// Endpoint overrides https://<account>.r2.cloudflarestorage.com (MinIO).
	Endpoint string
	// ExtraAllowedOrigins are additional image-URL prefixes CMS image fields may
	// use (migration period).
	ExtraAllowedOrigins []string

	MaxBytes           int64
	MaxPixels          int
	ProcessConcurrency int
	WebPQuality        int
	UploadURLTTL       time.Duration
}

// Enabled reports whether media storage is configured.
func (m MediaConfig) Enabled() bool { return m.Bucket != "" && m.PublicBaseURL != "" }

func loadMedia(env string) (MediaConfig, error) {
	m := MediaConfig{
		AccountID:       strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID")),
		AccessKeyID:     strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID")),
		SecretAccessKey: strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY")),
		Bucket:          strings.TrimSpace(os.Getenv("R2_BUCKET")),
		PublicBaseURL:   strings.TrimSpace(os.Getenv("IMAGE_PUBLIC_BASE_URL")),
		Endpoint:        strings.TrimRight(strings.TrimSpace(os.Getenv("R2_ENDPOINT")), "/"),
	}
	for _, o := range strings.Split(os.Getenv("IMAGE_EXTRA_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			m.ExtraAllowedOrigins = append(m.ExtraAllowedOrigins, o)
		}
	}

	var err error
	if m.MaxBytes, err = parseInt64("MEDIA_MAX_BYTES", DefaultMediaMaxBytes); err != nil {
		return m, err
	}
	if m.MaxPixels, err = parseInt("MEDIA_MAX_PIXELS", DefaultMediaMaxPixels); err != nil {
		return m, err
	}
	if m.ProcessConcurrency, err = parseInt("MEDIA_PROCESS_CONCURRENCY", DefaultMediaProcessConcurrency); err != nil {
		return m, err
	}
	if m.WebPQuality, err = parseInt("MEDIA_WEBP_QUALITY", DefaultMediaWebPQuality); err != nil {
		return m, err
	}
	if m.UploadURLTTL, err = parseDuration("MEDIA_UPLOAD_URL_TTL", DefaultMediaUploadURLTTL); err != nil {
		return m, err
	}
	if m.MaxBytes <= 0 || m.MaxPixels <= 0 || m.ProcessConcurrency <= 0 || m.UploadURLTTL <= 0 {
		return m, errors.New("config: MEDIA_MAX_BYTES, MEDIA_MAX_PIXELS, MEDIA_PROCESS_CONCURRENCY and MEDIA_UPLOAD_URL_TTL must be positive")
	}
	if m.WebPQuality < 1 || m.WebPQuality > 100 {
		return m, errors.New("config: MEDIA_WEBP_QUALITY must be between 1 and 100")
	}

	required := []struct{ key, val string }{
		{"R2_ACCOUNT_ID", m.AccountID},
		{"R2_ACCESS_KEY_ID", m.AccessKeyID},
		{"R2_SECRET_ACCESS_KEY", m.SecretAccessKey},
		{"R2_BUCKET", m.Bucket},
		{"IMAGE_PUBLIC_BASE_URL", m.PublicBaseURL},
	}
	var missing []string
	set := 0
	for _, r := range required {
		if r.val == "" {
			missing = append(missing, r.key)
		} else {
			set++
		}
	}
	if set == 0 {
		return m, nil // media disabled
	}
	if len(missing) > 0 {
		return m, fmt.Errorf("config: media storage is partially configured; missing %s (set all of R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_BUCKET, IMAGE_PUBLIC_BASE_URL, or none)",
			strings.Join(missing, ", "))
	}

	m.PublicBaseURL = strings.TrimRight(m.PublicBaseURL, "/")
	if err := checkImageBaseURL(m.PublicBaseURL, env); err != nil {
		return m, err
	}
	if m.Endpoint == "" {
		m.Endpoint = "https://" + m.AccountID + ".r2.cloudflarestorage.com"
	}
	return m, nil
}

// checkImageBaseURL: https only (http allowed in development/test for MinIO),
// no query/fragment, host required.
func checkImageBaseURL(u, env string) error {
	switch {
	case strings.HasPrefix(u, "https://"):
	case strings.HasPrefix(u, "http://") && (env == EnvDevelopment || env == EnvTest):
	default:
		return errors.New("config: IMAGE_PUBLIC_BASE_URL must start with https:// (http:// is allowed only when APP_ENV is development or test)")
	}
	rest := u[strings.Index(u, "://")+3:]
	if rest == "" || strings.ContainsAny(rest, "?#") {
		return errors.New("config: IMAGE_PUBLIC_BASE_URL must be a bare origin or path prefix without query or fragment")
	}
	return nil
}

func parseInt64(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return n, nil
}
