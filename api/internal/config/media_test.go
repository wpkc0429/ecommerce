package config

import (
	"strings"
	"testing"
)

var mediaKeys = []string{
	"R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_BUCKET",
	"IMAGE_PUBLIC_BASE_URL", "R2_ENDPOINT", "IMAGE_EXTRA_ALLOWED_ORIGINS",
}

// mediaEnv gives a hermetic env (no .env) with every media key cleared, then
// applies set.
func mediaEnv(t *testing.T, appEnv string, set map[string]string) {
	t.Helper()
	hermeticEnv(t, appEnv)
	for _, k := range mediaKeys {
		t.Setenv(k, "")
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
}

func fullMedia() map[string]string {
	return map[string]string{
		"R2_ACCOUNT_ID":         "acct-fixture",
		"R2_ACCESS_KEY_ID":      "akid-fixture",
		"R2_SECRET_ACCESS_KEY":  "test-fixture-r2-secret-3e7a91c5",
		"R2_BUCKET":             "bucket-fixture",
		"IMAGE_PUBLIC_BASE_URL": "https://img.example.test",
	}
}

func TestMediaDisabledWhenAllEmpty(t *testing.T) {
	mediaEnv(t, EnvProduction, nil)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Media.Enabled() {
		t.Fatal("media must be disabled when nothing is configured")
	}
	if cfg.Media.MaxBytes != 15<<20 || cfg.Media.ProcessConcurrency != 2 {
		t.Errorf("defaults wrong: %+v", cfg.Media)
	}
}

func TestMediaEnabledWhenComplete(t *testing.T) {
	mediaEnv(t, EnvProduction, fullMedia())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !cfg.Media.Enabled() {
		t.Fatal("media must be enabled")
	}
	if want := "https://acct-fixture.r2.cloudflarestorage.com"; cfg.Media.Endpoint != want {
		t.Errorf("default endpoint = %q, want %q", cfg.Media.Endpoint, want)
	}
}

func TestMediaPartialConfigurationNamesMissingKeys(t *testing.T) {
	mediaEnv(t, EnvTest, map[string]string{"R2_BUCKET": "b", "R2_ACCOUNT_ID": "a"})
	_, err := Load()
	if err == nil {
		t.Fatal("partial media configuration must refuse to start")
	}
	for _, k := range []string{"R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "IMAGE_PUBLIC_BASE_URL"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error does not name missing key %s: %v", k, err)
		}
	}
}

func TestMediaTrailingSlashNormalised(t *testing.T) {
	set := fullMedia()
	set["IMAGE_PUBLIC_BASE_URL"] = "https://img.example.test//"
	mediaEnv(t, EnvTest, set)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Media.PublicBaseURL != "https://img.example.test" {
		t.Errorf("PublicBaseURL = %q", cfg.Media.PublicBaseURL)
	}
}

func TestMediaHTTPOnlyInDevOrTest(t *testing.T) {
	set := fullMedia()
	set["IMAGE_PUBLIC_BASE_URL"] = "http://127.0.0.1:9000/bucket"
	mediaEnv(t, EnvTest, set)
	if _, err := Load(); err != nil {
		t.Fatalf("http base URL must be accepted in test: %v", err)
	}
	mediaEnv(t, EnvProduction, set)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "IMAGE_PUBLIC_BASE_URL") {
		t.Fatalf("http base URL must be refused in production, got %v", err)
	}
}

func TestMediaProductionRefusesKnownDefaultSecret(t *testing.T) {
	set := fullMedia()
	set["R2_SECRET_ACCESS_KEY"] = "minioadmin"
	mediaEnv(t, EnvProduction, set)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "R2_SECRET_ACCESS_KEY") {
		t.Fatalf("production must refuse minioadmin, got %v", err)
	}
	if strings.Contains(err.Error(), "minioadmin") {
		t.Error("error must name the key, never the value")
	}
}

func TestMediaExtraAllowedOrigins(t *testing.T) {
	set := fullMedia()
	set["IMAGE_EXTRA_ALLOWED_ORIGINS"] = "https://old.example.test/, https://b.example.test"
	mediaEnv(t, EnvTest, set)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Media.ExtraAllowedOrigins) != 2 || cfg.Media.ExtraAllowedOrigins[0] != "https://old.example.test" {
		t.Errorf("extra origins = %v", cfg.Media.ExtraAllowedOrigins)
	}
}
