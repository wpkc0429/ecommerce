// Package turnstile verifies Cloudflare Turnstile tokens for the public merchant
// application endpoint (change platform-website, design D3).
//
// It is the ONLY bot gate on an unauthenticated write endpoint, so its failure
// mode is the opposite of the rate limiter's: when siteverify cannot be reached
// or answers 5xx, Verify returns ErrUnavailable and the endpoint answers 503. It
// never lets the request through.
package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrInvalid means Cloudflare (or the dev verifier) judged the token bad,
	// missing, expired or already used. It depends only on the request.
	ErrInvalid = errors.New("turnstile: token rejected")
	// ErrUnavailable means the verdict could not be obtained (network, timeout,
	// 5xx, or a server-side misconfiguration such as a wrong secret). Callers
	// MUST NOT treat it as a pass.
	ErrUnavailable = errors.New("turnstile: verification unavailable")
)

// MaxTokenLen is Cloudflare's documented maximum token length.
const MaxTokenLen = 2048

// DummyToken is what Cloudflare's published test site keys produce, and the
// only token DevVerifier accepts.
const DummyToken = "XXXX.DUMMY.TOKEN.XXXX"

// Verifier checks one Turnstile response token. remoteIP may be empty.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// HTTPVerifier calls Cloudflare's siteverify endpoint.
type HTTPVerifier struct {
	Secret string
	URL    string
	// Client defaults to a client with a 3 second timeout.
	Client *http.Client
}

// NewHTTPVerifier builds the production verifier (3 second timeout).
func NewHTTPVerifier(secret, verifyURL string) *HTTPVerifier {
	return &HTTPVerifier{Secret: secret, URL: verifyURL, Client: &http.Client{Timeout: 3 * time.Second}}
}

type siteverifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify implements Verifier.
func (v *HTTPVerifier) Verify(ctx context.Context, token, remoteIP string) error {
	if token == "" || len(token) > MaxTokenLen {
		return ErrInvalid
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	form := url.Values{"secret": {v.Secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out siteverifyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("%w: malformed response", ErrUnavailable)
	}
	if out.Success {
		return nil
	}
	for _, c := range out.ErrorCodes {
		switch c {
		case "internal-error", "missing-input-secret", "invalid-input-secret":
			// Cloudflare-side trouble or OUR configuration: not the visitor's fault.
			return fmt.Errorf("%w: %s", ErrUnavailable, c)
		}
	}
	return ErrInvalid
}

// DevVerifier is the no-network verifier used only in development/test when no
// secret is configured: it accepts exactly Cloudflare's published dummy token.
// config refuses to boot a production deployment without a real secret, so this
// type cannot be reached there.
type DevVerifier struct{}

// Verify implements Verifier.
func (DevVerifier) Verify(_ context.Context, token, _ string) error {
	if token == DummyToken {
		return nil
	}
	return ErrInvalid
}

// StubVerifier is a scriptable fake for tests.
type StubVerifier struct {
	Err   error
	Calls int
	// LastToken / LastIP record the most recent call.
	LastToken, LastIP string
}

// Verify implements Verifier.
func (s *StubVerifier) Verify(_ context.Context, token, ip string) error {
	s.Calls++
	s.LastToken, s.LastIP = token, ip
	return s.Err
}
