package turnstile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func server(t *testing.T, h http.HandlerFunc) *HTTPVerifier {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &HTTPVerifier{Secret: "test-secret", URL: ts.URL, Client: ts.Client()}
}

func TestVerifySuccessSendsSecretTokenAndIP(t *testing.T) {
	var form map[string]string
	v := server(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = map[string]string{"secret": r.PostForm.Get("secret"), "response": r.PostForm.Get("response"), "remoteip": r.PostForm.Get("remoteip")}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if err := v.Verify(context.Background(), "tok", "203.0.113.9"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if form["secret"] != "test-secret" || form["response"] != "tok" || form["remoteip"] != "203.0.113.9" {
		t.Fatalf("form = %v", form)
	}
}

func TestVerifyRejectionCodesAreInvalid(t *testing.T) {
	for _, code := range []string{"invalid-input-response", "timeout-or-duplicate", "bad-request"} {
		v := server(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["` + code + `"]}`))
		})
		if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", code, err)
		}
	}
}

func TestVerifyServerSideCodesAreUnavailable(t *testing.T) {
	for _, code := range []string{"internal-error", "invalid-input-secret", "missing-input-secret"} {
		v := server(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["` + code + `"]}`))
		})
		if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: err = %v, want ErrUnavailable", code, err)
		}
	}
}

func TestVerify5xxAndGarbageAreUnavailable(t *testing.T) {
	v := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("5xx: %v", err)
	}
	v = server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) })
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("garbage: %v", err)
	}
}

func TestVerifyTimeoutIsUnavailable(t *testing.T) {
	done := make(chan struct{})
	v := server(t, func(http.ResponseWriter, *http.Request) { <-done })
	t.Cleanup(func() { close(done) })
	v.Client = &http.Client{Timeout: 50 * time.Millisecond}
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("timeout: %v", err)
	}
}

func TestVerifyNetworkErrorIsUnavailable(t *testing.T) {
	v := &HTTPVerifier{Secret: "s", URL: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second}}
	if err := v.Verify(context.Background(), "tok", ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("network: %v", err)
	}
}

func TestVerifyEmptyOrOversizeTokenNeverCallsOut(t *testing.T) {
	called := false
	v := server(t, func(http.ResponseWriter, *http.Request) { called = true })
	if err := v.Verify(context.Background(), "", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty: %v", err)
	}
	if err := v.Verify(context.Background(), strings.Repeat("a", MaxTokenLen+1), ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversize: %v", err)
	}
	if called {
		t.Error("siteverify must not be called for an unusable token")
	}
}

func TestDevVerifierAcceptsOnlyTheDummyToken(t *testing.T) {
	var d DevVerifier
	if err := d.Verify(context.Background(), DummyToken, ""); err != nil {
		t.Errorf("dummy token: %v", err)
	}
	for _, tok := range []string{"", "x", "XXXX.DUMMY.TOKEN.XXXY"} {
		if err := d.Verify(context.Background(), tok, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", tok, err)
		}
	}
}
