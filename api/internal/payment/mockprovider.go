package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrInvalidSignature is returned by Provider.VerifyWebhook when the
// signature is missing or does not match — the sole condition the HTTP
// layer maps to 401 (design D10/webhook handler task 4.4).
var ErrInvalidSignature = errors.New("payment: invalid webhook signature")

// mockSignatureHeader is the header MockProvider reads/expects the
// signature on. Named after the "X-Payment-Signature" convention documented
// in design D2 — a stand-in for whatever header scheme a real provider
// (Stripe uses "Stripe-Signature", etc.) would use.
const mockSignatureHeader = "X-Payment-Signature"

// mockWebhookBody is the wire shape MockProvider's webhook payload takes.
// A real provider's payload would look nothing like this — this shape is
// entirely internal to MockProvider and never leaks into the Provider
// interface (design D1).
type mockWebhookBody struct {
	ProviderReference string `json:"provider_reference"`
	Status            string `json:"status"` // "succeeded" | "failed"
}

// MockProvider is the reference/demo Provider implementation (design D2):
// no external network calls, but a REAL HMAC-SHA256 signature scheme — not
// a stub that always verifies. This keeps the webhook handler's signature
// verification code path genuinely exercised so it is already correct the
// day a real provider replaces MockProvider.
type MockProvider struct {
	secret string
}

// NewMockProvider constructs a MockProvider keyed by secret (design D9 —
// callers pass config.Config.PaymentMockWebhookSecret).
func NewMockProvider(secret string) *MockProvider {
	return &MockProvider{secret: secret}
}

func (p *MockProvider) Name() string { return "mock" }

// InitiatePayment never calls out anywhere — it just mints a unique
// reference and a fake redirect URL (design D2). The reference is what
// SignMockWebhook/webhook payloads must echo back for VerifyWebhook +
// Service.HandleWebhook to find the matching payments row.
func (p *MockProvider) InitiatePayment(_ context.Context, req InitiateRequest) (*InitiateResult, error) {
	ref := "mock_" + uuid.NewString()
	return &InitiateResult{
		ProviderReference: ref,
		RedirectURL:       "https://mock-payments.test/pay/" + ref,
	}, nil
}

// VerifyWebhook checks the HMAC-SHA256 signature first (constant-time
// compare via hmac.Equal — a plain == would leak timing information) and
// only parses the body once the signature has checked out (design D2).
func (p *MockProvider) VerifyWebhook(_ context.Context, req WebhookRequest) (*WebhookResult, error) {
	sig := req.Headers.Get(mockSignatureHeader)
	if sig == "" {
		return nil, ErrInvalidSignature
	}
	want := mockHMAC(p.secret, req.Body)
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return nil, ErrInvalidSignature
	}

	var body mockWebhookBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return nil, fmt.Errorf("payment: decode mock webhook body: %w", err)
	}
	var outcome Outcome
	switch body.Status {
	case "succeeded":
		outcome = OutcomeSucceeded
	case "failed":
		outcome = OutcomeFailed
	default:
		return nil, fmt.Errorf("payment: unknown mock webhook status %q", body.Status)
	}
	if body.ProviderReference == "" {
		return nil, errors.New("payment: mock webhook missing provider_reference")
	}
	return &WebhookResult{ProviderReference: body.ProviderReference, Outcome: outcome}, nil
}

// mockHMAC computes the raw HMAC-SHA256 digest of body under secret.
func mockHMAC(secret string, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return mac.Sum(nil)
}

// SignMockWebhook produces the hex-encoded signature MockProvider.
// VerifyWebhook expects on mockSignatureHeader for a given body. Exported
// so integration tests can construct valid signed webhook requests without
// duplicating the HMAC scheme (mirrors how stripe-go exposes a test-signing
// helper for the same reason).
func SignMockWebhook(secret string, body []byte) string {
	return hex.EncodeToString(mockHMAC(secret, body))
}
