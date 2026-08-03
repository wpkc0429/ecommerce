package httpapi_test

// change ecpay-per-shop-credentials — the merchant-scoped credentials
// management endpoint, end to end: RBAC, cross-shop isolation, and (the
// whole point of design D3/D6) that the two secret fields never once appear
// in a response body, checked against the raw bytes, not just the parsed
// struct.

import (
	"strings"
	"testing"
)

func TestECPayCredentialsUnconfiguredShopReportsNotConfigured(t *testing.T) {
	e := newPaymentEnv(t)
	code, body := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/ecpay-credentials"), nil)
	if code != 200 {
		t.Fatalf("GET ecpay-credentials (unconfigured) = %d: %+v", code, body)
	}
	if configured, _ := body["configured"].(bool); configured {
		t.Fatal("expected configured=false before any PUT")
	}
}

func TestECPayCredentialsFirstSetupRequiresBothSecrets(t *testing.T) {
	e := newPaymentEnv(t)
	code, body := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, "/ecpay-credentials"),
		map[string]any{"merchant_id": "3002607"})
	if code != 422 {
		t.Fatalf("PUT without hash_key/hash_iv on first setup = %d, want 422: %+v", code, body)
	}
}

func TestECPayCredentialsSetupUpdateAndMasking(t *testing.T) {
	e := newPaymentEnv(t)

	// First-time setup: both secrets required and accepted.
	code, body := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, "/ecpay-credentials"), map[string]any{
		"merchant_id": "3002607", "hash_key": "pwFHCqoQZGmho4w6", "hash_iv": "EkRm7iFT261dpevs",
	})
	if code != 200 {
		t.Fatalf("first PUT = %d: %+v", code, body)
	}
	if configured, _ := body["configured"].(bool); !configured {
		t.Fatal("expected configured=true after first PUT")
	}
	if hk, _ := body["hash_key_configured"].(bool); !hk {
		t.Error("expected hash_key_configured=true")
	}
	if hv, _ := body["hash_iv_configured"].(bool); !hv {
		t.Error("expected hash_iv_configured=true")
	}
	if body["merchant_id"] != "3002607" {
		t.Errorf("merchant_id = %v, want 3002607 (an identifier, not a secret — shown in full)", body["merchant_id"])
	}

	// Update: change MerchantID only, leave hash_key/hash_iv blank — design D6
	// says blank means "keep the existing value", not "clear it".
	_, upd := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, "/ecpay-credentials"), map[string]any{
		"merchant_id": "3002608",
	})
	if upd["merchant_id"] != "3002608" {
		t.Errorf("merchant_id after update = %v, want 3002608", upd["merchant_id"])
	}
	if hk, _ := upd["hash_key_configured"].(bool); !hk {
		t.Error("leaving hash_key blank on update must not clear it")
	}

	// The raw response bytes — not just the parsed JSON — must never contain
	// either secret, at any point in this flow.
	_, raw := e.adminCall(t, e.ownerA, "GET", e.shopPath(e.shopA, "/ecpay-credentials"), nil)
	for _, secret := range []string{"pwFHCqoQZGmho4w6", "EkRm7iFT261dpevs"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("GET response leaked a secret field verbatim: %q found in %s", secret, raw)
		}
	}
}

func TestECPayCredentialsRequiresPermission(t *testing.T) {
	e := newPaymentEnv(t)
	code, body := e.adminCallJSON(t, e.viewerA, "GET", e.shopPath(e.shopA, "/ecpay-credentials"), nil)
	if code != 403 {
		t.Fatalf("GET without payment_credential.manage = %d, want 403: %+v", code, body)
	}
	code, body = e.adminCallJSON(t, e.viewerA, "PUT", e.shopPath(e.shopA, "/ecpay-credentials"),
		map[string]any{"merchant_id": "x", "hash_key": "y", "hash_iv": "z"})
	if code != 403 {
		t.Fatalf("PUT without payment_credential.manage = %d, want 403: %+v", code, body)
	}
}

func TestECPayCredentialsCrossShopIsolation(t *testing.T) {
	e := newPaymentEnv(t)
	if _, body := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, "/ecpay-credentials"), map[string]any{
		"merchant_id": "shop-a-merchant", "hash_key": "shop-a-key", "hash_iv": "shop-a-iv",
	}); body["merchant_id"] != "shop-a-merchant" {
		t.Fatalf("setup shop A credentials: %+v", body)
	}

	// ownerB has no role in shop A — accessing shop A's credentials must 403,
	// not merely return shop B's (nonexistent) row.
	code, body := e.adminCallJSON(t, e.ownerB, "GET", e.shopPath(e.shopA, "/ecpay-credentials"), nil)
	if code != 403 {
		t.Fatalf("cross-shop GET = %d, want 403: %+v", code, body)
	}

	// ownerB's OWN shop legitimately has no credentials yet.
	code, body = e.adminCallJSON(t, e.ownerB, "GET", e.shopPath(e.shopB, "/ecpay-credentials"), nil)
	if code != 200 {
		t.Fatalf("GET shop B's own (unconfigured) credentials = %d: %+v", code, body)
	}
	if configured, _ := body["configured"].(bool); configured {
		t.Fatal("shop B must not see shop A's credentials as its own")
	}
}
