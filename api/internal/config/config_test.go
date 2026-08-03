package config

import (
	"strings"
	"testing"
)

// TestLoadValidatesReportDefaultTimezone (change merchant-reports, design D1).
//
// REPORT_DEFAULT_TIMEZONE is the only new setting whose invalid value stops the
// service from booting, and this test is why that is safe to rely on: without
// it, "the default is validated at boot" is a sentence in a design document.
//
// Note the test sets the variable rather than reading whatever .env holds —
// Load() hydrates from a repo .env, so asserting on the ambient value would
// make this pass or fail depending on the developer's file.
func TestLoadValidatesReportDefaultTimezone(t *testing.T) {
	t.Setenv("REPORT_DEFAULT_TIMEZONE", "Nowhere/Atlantis")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a bogus REPORT_DEFAULT_TIMEZONE — the service would boot and 500 on every report")
	} else if !strings.Contains(err.Error(), "REPORT_DEFAULT_TIMEZONE") {
		t.Errorf("error does not name the offending setting: %v", err)
	}
}

func TestLoadAcceptsAValidReportDefaultTimezone(t *testing.T) {
	t.Setenv("REPORT_DEFAULT_TIMEZONE", "America/New_York")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ReportDefaultTimezone != "America/New_York" {
		t.Errorf("ReportDefaultTimezone = %q, want America/New_York", cfg.ReportDefaultTimezone)
	}
}

// TestReportDefaultTimezoneFallsBackToTaipei pins the built-in default. It is
// the value every unconfigured shop's report is cut by, so it is worth an
// assertion rather than a comment.
func TestReportDefaultTimezoneFallsBackToTaipei(t *testing.T) {
	t.Setenv("REPORT_DEFAULT_TIMEZONE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ReportDefaultTimezone != "Asia/Taipei" {
		t.Errorf("unset REPORT_DEFAULT_TIMEZONE gave %q, want Asia/Taipei", cfg.ReportDefaultTimezone)
	}
}

// TestLoadValidatesPaymentCredentialsEncryptionKey (change
// ecpay-per-shop-credentials, design D4): same treatment as
// REPORT_DEFAULT_TIMEZONE and for the same reason — a bad value here does not
// disable a feature, it makes every stored credential unreadable, silently,
// until someone tries to use it.
func TestLoadValidatesPaymentCredentialsEncryptionKey(t *testing.T) {
	t.Setenv("PAYMENT_CREDENTIALS_ENCRYPTION_KEY", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a bogus PAYMENT_CREDENTIALS_ENCRYPTION_KEY — the service would boot and fail only when a credential is read/written")
	} else if !strings.Contains(err.Error(), "PAYMENT_CREDENTIALS_ENCRYPTION_KEY") {
		t.Errorf("error does not name the offending setting: %v", err)
	}
}

func TestLoadDefaultsPaymentCredentialsEncryptionKeyToAValidDevKey(t *testing.T) {
	t.Setenv("PAYMENT_CREDENTIALS_ENCRYPTION_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.PaymentCredentialsEncryptionKey == "" {
		t.Fatal("unset PAYMENT_CREDENTIALS_ENCRYPTION_KEY produced an empty value")
	}
}
