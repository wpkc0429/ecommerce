package payment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const testSecret = "test-mock-webhook-secret"

func mockBody(ref, status string) []byte {
	return []byte(`{"provider_reference":"` + ref + `","status":"` + status + `"}`)
}

func TestMockProviderInitiatePayment(t *testing.T) {
	p := NewMockProvider(testSecret)
	res, err := p.InitiatePayment(context.Background(), InitiateRequest{
		ShopID: 1, OrderID: 2, Amount: 1000, Currency: "TWD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(res.ProviderReference, "mock_") {
		t.Fatalf("expected reference prefixed with mock_, got %q", res.ProviderReference)
	}
	if !strings.Contains(res.RedirectURL, res.ProviderReference) {
		t.Fatalf("expected redirect URL to embed the reference, got %q", res.RedirectURL)
	}

	res2, err := p.InitiatePayment(context.Background(), InitiateRequest{ShopID: 1, OrderID: 2, Amount: 1000, Currency: "TWD"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderReference == res2.ProviderReference {
		t.Fatal("expected distinct references across calls")
	}
}

func TestMockProviderVerifyWebhookMissingSignature(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := mockBody("mock_abc", "succeeded")
	_, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: http.Header{}, Body: body})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestMockProviderVerifyWebhookWrongSignature(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := mockBody("mock_abc", "succeeded")
	h := http.Header{}
	h.Set(mockSignatureHeader, SignMockWebhook("a-completely-different-secret", body))
	_, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: body})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestMockProviderVerifyWebhookTamperedBody(t *testing.T) {
	p := NewMockProvider(testSecret)
	signedBody := mockBody("mock_abc", "succeeded")
	sig := SignMockWebhook(testSecret, signedBody)
	h := http.Header{}
	h.Set(mockSignatureHeader, sig)
	// Signature was computed over signedBody but the request carries a
	// different body — must still be rejected even though the signature is
	// well-formed and was legitimately produced by SignMockWebhook.
	tamperedBody := mockBody("mock_abc", "failed")
	_, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: tamperedBody})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestMockProviderVerifyWebhookSucceeded(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := mockBody("mock_abc", "succeeded")
	h := http.Header{}
	h.Set(mockSignatureHeader, SignMockWebhook(testSecret, body))
	res, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: body})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderReference != "mock_abc" {
		t.Fatalf("expected reference mock_abc, got %q", res.ProviderReference)
	}
	if res.Outcome != OutcomeSucceeded {
		t.Fatalf("expected OutcomeSucceeded, got %v", res.Outcome)
	}
}

func TestMockProviderVerifyWebhookFailed(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := mockBody("mock_xyz", "failed")
	h := http.Header{}
	h.Set(mockSignatureHeader, SignMockWebhook(testSecret, body))
	res, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: body})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeFailed {
		t.Fatalf("expected OutcomeFailed, got %v", res.Outcome)
	}
}

func TestMockProviderVerifyWebhookMalformedBody(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := []byte(`not json`)
	h := http.Header{}
	h.Set(mockSignatureHeader, SignMockWebhook(testSecret, body))
	if _, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: body}); err == nil {
		t.Fatal("expected an error for malformed JSON body")
	}
}

func TestMockProviderVerifyWebhookUnknownStatus(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := mockBody("mock_abc", "pending")
	h := http.Header{}
	h.Set(mockSignatureHeader, SignMockWebhook(testSecret, body))
	if _, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: body}); err == nil {
		t.Fatal("expected an error for unknown status value")
	}
}

func TestMockProviderVerifyWebhookMissingReference(t *testing.T) {
	p := NewMockProvider(testSecret)
	body := mockBody("", "succeeded")
	h := http.Header{}
	h.Set(mockSignatureHeader, SignMockWebhook(testSecret, body))
	if _, err := p.VerifyWebhook(context.Background(), WebhookRequest{Headers: h, Body: body}); err == nil {
		t.Fatal("expected an error for missing provider_reference")
	}
}
