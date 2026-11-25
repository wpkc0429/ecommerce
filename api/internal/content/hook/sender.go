package hook

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// Result categories (content_publish_hooks.last_status). They are the only
// thing that leaves the sender: never the response body, never the URL, never
// the underlying error text (a url.Error carries the full URL).
const (
	StatusOK           = "ok"
	StatusHTTPError    = "http_error"
	StatusTimeout      = "timeout"
	StatusBlocked      = "blocked"
	StatusNetworkError = "network_error"
	StatusNotAllowed   = "not_allowed"
	StatusRateLimited  = "rate_limited"
)

// Result is the outcome of one POST.
type Result struct {
	Status     string
	HTTPStatus int
	// Retryable: timeouts, 5xx and 429 (design D7 point 8).
	Retryable bool
	// URLCode is set when Status is not_allowed.
	URLCode string
}

// OK reports whether the receiver accepted the call (2xx).
func (r Result) OK() bool { return r.Status == StatusOK }

// Resolver is the DNS lookup the sender uses; *net.Resolver satisfies it and
// tests supply a fake.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Poster is what the notifier needs from the sender.
type Poster interface {
	Post(ctx context.Context, rawURL string, body []byte, headers map[string]string) Result
}

// Sender makes the one outbound request of the hook feature.
type Sender struct {
	Policy   Policy
	Resolver Resolver // default net.DefaultResolver
	// Timeout bounds the whole exchange (default 5 s).
	Timeout time.Duration
	// DialContext connects to the already-checked ip:port (default net.Dialer).
	// Tests wrap it to see which address was dialed.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLSConfig is for tests (a trusted httptest certificate); nil = system roots.
	TLSConfig *tls.Config
}

const maxReadBody = 4 << 10

// Post sends body as a POST to rawURL after validating the URL, resolving the
// host itself, refusing any blocked address and pinning the connection to the
// checked address.
func (s *Sender) Post(ctx context.Context, rawURL string, body []byte, headers map[string]string) Result {
	t, err := s.Policy.ParseURL(rawURL)
	if err != nil {
		code := CodeInvalid
		var ue *URLError
		if errors.As(err, &ue) {
			code = ue.Code
		}
		return Result{Status: StatusNotAllowed, URLCode: code}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolver := s.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", t.Host)
	if err != nil {
		return failureResult(ctx, err)
	}
	if len(addrs) == 0 {
		return Result{Status: StatusNetworkError}
	}
	// Every answer must be acceptable: one private address among public ones is
	// how rebinding-style mixes are built, so it is refused as a whole.
	checked := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if IPBlocked(a, s.Policy.DevInsecure) {
			return Result{Status: StatusBlocked}
		}
		checked = append(checked, a.Unmap())
	}

	dial := s.DialContext
	if dial == nil {
		d := &net.Dialer{Timeout: timeout}
		dial = d.DialContext
	}
	tr := &http.Transport{
		// The connection goes to the checked address, never to a second
		// resolution of the name (DNS rebinding). The request URL keeps the
		// host name so SNI and certificate verification use it.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, a := range checked {
				c, derr := dial(ctx, network, net.JoinHostPort(a.String(), t.Port))
				if derr == nil {
					return c, nil
				}
				last = derr
			}
			return nil, last
		},
		TLSClientConfig:        s.tlsConfig(t.Host),
		Proxy:                  nil,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 16 << 10,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
		// Redirects are never followed: a 3xx is a failure, so the receiver
		// cannot bounce the request to an internal address.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL.String(), bytes.NewReader(body))
	if err != nil {
		return Result{Status: StatusNotAllowed, URLCode: CodeInvalid}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ecommerce-content-hook/1")
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return failureResult(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Read at most 4 KiB and drop it: the response is never shown to anyone.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReadBody))
	res := Result{HTTPStatus: resp.StatusCode}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Status = StatusOK
	default:
		res.Status = StatusHTTPError
		res.Retryable = resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
	}
	return res
}

func (s *Sender) tlsConfig(host string) *tls.Config {
	var c *tls.Config
	if s.TLSConfig != nil {
		c = s.TLSConfig.Clone()
	} else {
		c = &tls.Config{}
	}
	c.ServerName = host
	if c.MinVersion == 0 {
		c.MinVersion = tls.VersionTLS12
	}
	return c
}

func failureResult(ctx context.Context, err error) Result {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded || (errors.As(err, &ne) && ne.Timeout()) {
		return Result{Status: StatusTimeout, Retryable: true}
	}
	return Result{Status: StatusNetworkError}
}
