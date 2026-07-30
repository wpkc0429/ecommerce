package tenant

import (
	"strings"
	"testing"
)

// TestNormalizeOriginRejectsUnusableValues covers the normalization rules the
// registration API enforces (spec storefront-client-auth: 每商家瀏覽器 origin 登記).
func TestNormalizeOriginRejectsUnusableValues(t *testing.T) {
	ok := map[string]string{
		"https://ondo.example":       "https://ondo.example",
		"https://ONDO.example/":      "https://ondo.example",
		"HTTP://localhost:3000":      "http://localhost:3000",
		"  https://a.b.c:8443  ":     "https://a.b.c:8443",
		"https://ondo.pages.dev/":    "https://ondo.pages.dev",
		"http://192.0.2.10:8080":     "http://192.0.2.10:8080",
		"https://xn--fsq.example":    "https://xn--fsq.example",
		"https://ondo.example:443//": "https://ondo.example:443",
	}
	for in, want := range ok {
		if got := NormalizeOrigin(in); got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{
		"", "null", "ondo.example", "*.ondo.example", "https://*.ondo.example",
		"ftp://ondo.example", "file://x", "https://ondo.example/path",
		"https://ondo.example?q=1", "https://ondo.example#f", "https://u:p@ondo.example",
	} {
		if got := NormalizeOrigin(bad); got != "" {
			t.Errorf("NormalizeOrigin(%q) = %q, want rejection", bad, got)
		}
	}
}

// TestPublishableKeyFormat pins design D8's format guarantees.
func TestPublishableKeyFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		k, err := NewPublishableKey()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(k, KeyPrefix) {
			t.Fatalf("missing prefix: %q", k)
		}
		// The random segment must stay free of "_" so a future environment
		// infix ("pk_test_") remains unambiguously parseable.
		if strings.Contains(strings.TrimPrefix(k, KeyPrefix), "_") {
			t.Fatalf("random segment must not contain an underscore: %q", k)
		}
		if !ValidKeyFormat(k) {
			t.Fatalf("generated key rejected by its own format check: %q", k)
		}
		if seen[k] {
			t.Fatalf("duplicate key generated: %q", k)
		}
		seen[k] = true
	}
	for _, bad := range []string{"", "pk_", "not-a-key", "PK_abcdefgh", "pk_ABCDEFGH", "pk_abc!", strings.Repeat("pk_a", 30)} {
		if ValidKeyFormat(bad) {
			t.Fatalf("ValidKeyFormat(%q) must be false", bad)
		}
	}
}
