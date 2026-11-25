package hook

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeResolver answers from a script; every call is counted.
type fakeResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr // answer per call; the last one repeats
	err     error
	calls   int
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	i := f.calls - 1
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	return f.answers[i], nil
}

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func TestParseURLRules(t *testing.T) {
	p := Policy{ExtraHosts: []string{"hooks.example.com", "*.deploy.example.org"}}
	good := []string{
		"https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/0123abcd-4567-89ef-0123-456789abcdef",
		"https://api.cloudflare.com/client/v4/pages/webhooks/deploy_hooks/0123abcd45678901234567890abcdef0",
		"https://hooks.example.com/anything?x=1",
		"https://a.deploy.example.org/x",
		"https://HOOKS.example.com:443/x",
	}
	for _, u := range good {
		if _, err := p.ParseURL(u); err != nil {
			t.Errorf("%s should pass: %v", u, err)
		}
	}
	bad := map[string]string{
		"http://hooks.example.com/x":                                             CodeScheme,
		"ftp://hooks.example.com/x":                                              CodeScheme,
		"javascript:alert(1)":                                                    CodeInvalid,
		"https://user:pw@hooks.example.com/x":                                    CodeUserinfo,
		"https://hooks.example.com@evil.test/x":                                  CodeUserinfo,
		"https://hooks.example.com/x#frag":                                       CodeFragment,
		"https://hooks.example.com:8443/x":                                       CodePort,
		"https://hooks.example.com:80/x":                                         CodePort,
		"https://127.0.0.1/x":                                                    CodeIPLiteral,
		"https://10.0.0.5/x":                                                     CodeIPLiteral,
		"https://[::1]/x":                                                        CodeIPLiteral,
		"https://[::ffff:10.0.0.1]/x":                                            CodeIPLiteral,
		"https://169.254.169.254/latest/meta-data":                               CodeIPLiteral,
		"https://2130706433/x":                                                   CodeHostNotAllowed, // decimal IP is just an unlisted name
		"https://evil.test/x":                                                    CodeHostNotAllowed,
		"https://hooks.example.com.evil.test/x":                                  CodeHostNotAllowed,
		"https://deploy.example.org/x":                                           CodeHostNotAllowed, // wildcard needs a label
		"https://api.cloudflare.com/client/v4/user":                              CodePath,
		"https://api.cloudflare.com/client/v4/accounts/x/workers/scripts":        CodePath,
		"https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/short": CodePath,
		"https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/0123abcd-4567/../../../user": CodePath,
		"https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/0123abcd-4567-89ef?x=1":      CodePath,
		"https://hooks.example.com/x y":  CodeInvalid,
		"https://hooks.example.com/\x00": CodeInvalid,
		"https://höoks.example.com/x":    CodeInvalid,
		"//hooks.example.com/x":          CodeScheme,
		"":                               CodeInvalid,
		"https://hooks.example.com/" + strings.Repeat("a", 600): CodeTooLong,
	}
	for u, want := range bad {
		_, err := p.ParseURL(u)
		var ue *URLError
		if !errors.As(err, &ue) {
			t.Errorf("%q should be refused (%s)", u, want)
			continue
		}
		if ue.Code != want {
			t.Errorf("%q: code %s, want %s", u, ue.Code, want)
		}
		if strings.Contains(ue.Message, "hooks.example.com") || strings.Contains(ue.Message, "evil") {
			t.Errorf("error message echoes the URL: %q", ue.Message)
		}
	}
}

func TestExtraHostsOnlyAdd(t *testing.T) {
	// With no extra hosts the built-in Cloudflare host still works.
	p := Policy{}
	if _, err := p.ParseURL("https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/0123abcd-4567-89ef-0123"); err != nil {
		t.Fatal(err)
	}
	// An extra host does not widen the built-in host's path rule.
	p = Policy{ExtraHosts: []string{"hooks.example.com"}}
	if _, err := p.ParseURL("https://api.cloudflare.com/client/v4/user"); err == nil {
		t.Fatal("extra hosts must not widen the built-in host")
	}
}

func TestIPBlocked(t *testing.T) {
	blocked := []string{
		"10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.1", "127.0.0.1", "127.1.2.3",
		"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.255", "0.0.0.0", "224.0.0.1",
		"255.255.255.255", "240.0.0.1", "198.18.0.1", "192.0.2.1", "203.0.113.9", "198.51.100.1",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", // IPv4-mapped
		"64:ff9b::a00:1", "2002:a00:1::1", "2001:db8::1", "2001::1", "100::1",
	}
	for _, s := range blocked {
		if !IPBlocked(netip.MustParseAddr(s), false) {
			t.Errorf("%s must be blocked", s)
		}
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "104.18.2.55", "172.15.0.1", "172.32.0.1", "100.63.255.255", "2606:4700::1111", "::ffff:8.8.8.8"}
	for _, s := range allowed {
		if IPBlocked(netip.MustParseAddr(s), false) {
			t.Errorf("%s must be allowed", s)
		}
	}
	// The dev switch lifts loopback only.
	if IPBlocked(netip.MustParseAddr("127.0.0.1"), true) || IPBlocked(netip.MustParseAddr("::1"), true) {
		t.Error("loopback should be allowed in dev mode")
	}
	for _, s := range []string{"10.0.0.5", "169.254.169.254", "192.168.0.1"} {
		if !IPBlocked(netip.MustParseAddr(s), true) {
			t.Errorf("%s must stay blocked in dev mode", s)
		}
	}
}

func TestSignAndVerify(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"event":"content.published"}`)
	sig := Sign(secret, 1700000000, body)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != len("sha256=")+64 {
		t.Fatalf("shape: %s", sig)
	}
	if !Verify(secret, 1700000000, body, sig) {
		t.Fatal("independent verification failed")
	}
	if Verify(secret, 1700000001, body, sig) || Verify([]byte("other"), 1700000000, body, sig) || Verify(secret, 1700000000, append(body, ' '), sig) {
		t.Fatal("signature must bind secret, timestamp and body")
	}
}

func newSender(t *testing.T, res Resolver, extra ...string) *Sender {
	t.Helper()
	return &Sender{Policy: Policy{ExtraHosts: extra}, Resolver: res, Timeout: 2 * time.Second}
}

func TestSenderBlocksPrivateResolution(t *testing.T) {
	for _, ip := range []string{"10.0.0.5", "127.0.0.1", "169.254.169.254", "100.64.0.9", "::1", "fd00::1", "::ffff:192.168.0.1"} {
		var dialed int32
		s := newSender(t, &fakeResolver{answers: [][]netip.Addr{addrs(ip)}}, "hooks.example.com")
		s.DialContext = func(context.Context, string, string) (net.Conn, error) {
			atomic.AddInt32(&dialed, 1)
			return nil, errors.New("x")
		}
		r := s.Post(context.Background(), "https://hooks.example.com/x", []byte("{}"), nil)
		if r.Status != StatusBlocked || dialed != 0 {
			t.Errorf("%s: status %s dialed %d", ip, r.Status, dialed)
		}
	}
	// A mixed answer (public + private) is refused as a whole.
	s := newSender(t, &fakeResolver{answers: [][]netip.Addr{addrs("8.8.8.8", "10.0.0.1")}}, "hooks.example.com")
	if r := s.Post(context.Background(), "https://hooks.example.com/x", nil, nil); r.Status != StatusBlocked {
		t.Fatalf("mixed answer: %s", r.Status)
	}
}

func TestSenderRefusesDisallowedURLWithoutResolving(t *testing.T) {
	res := &fakeResolver{answers: [][]netip.Addr{addrs("8.8.8.8")}}
	s := newSender(t, res, "hooks.example.com")
	r := s.Post(context.Background(), "https://evil.test/x", nil, nil)
	if r.Status != StatusNotAllowed || r.URLCode != CodeHostNotAllowed || res.calls != 0 {
		t.Fatalf("%+v calls=%d", r, res.calls)
	}
}

func TestSenderResolverFailureAndTimeout(t *testing.T) {
	s := newSender(t, &fakeResolver{err: errors.New("no such host")}, "hooks.example.com")
	if r := s.Post(context.Background(), "https://hooks.example.com/x", nil, nil); r.Status != StatusNetworkError {
		t.Fatalf("%+v", r)
	}
	s = newSender(t, &fakeResolver{err: context.DeadlineExceeded}, "hooks.example.com")
	if r := s.Post(context.Background(), "https://hooks.example.com/x", nil, nil); r.Status != StatusTimeout || !r.Retryable {
		t.Fatalf("%+v", r)
	}
}

// tlsServer starts an HTTPS receiver on loopback and returns a sender whose
// fake resolver maps example.com to loopback (dev policy) and whose
// TLS config trusts the receiver's certificate.
func tlsServer(t *testing.T, h http.Handler) (*httptest.Server, *Sender, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	s := &Sender{
		Policy:    Policy{ExtraHosts: []string{"example.com"}, DevInsecure: true},
		Resolver:  &fakeResolver{answers: [][]netip.Addr{addrs("127.0.0.1")}},
		Timeout:   2 * time.Second,
		TLSConfig: &tls.Config{RootCAs: pool},
	}
	return srv, s, "https://example.com:" + port + "/hook-secret-path-1234"
}

func TestSenderPostsSignedBodyAndDiscardsResponse(t *testing.T) {
	var gotBody, gotSig, gotTS, gotMethod, gotHost, gotCT string
	srv, s, url := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotSig, gotTS, gotMethod, gotHost, gotCT = string(b), r.Header.Get(HeaderSignature), r.Header.Get(HeaderTimestamp), r.Method, r.Host, r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(strings.Repeat("INTERNAL-DETAIL ", 1000)))
	}))
	_ = srv
	body := []byte(`{"event":"content.published"}`)
	r := s.Post(context.Background(), url, body, map[string]string{HeaderTimestamp: "1700000000", HeaderSignature: Sign([]byte("k"), 1700000000, body)})
	if !r.OK() || r.HTTPStatus != 200 {
		t.Fatalf("%+v", r)
	}
	if gotMethod != "POST" || gotBody != string(body) || gotCT != "application/json" || !strings.HasPrefix(gotHost, "example.com:") {
		t.Fatalf("request: %s %q %q host=%s", gotMethod, gotBody, gotCT, gotHost)
	}
	if !Verify([]byte("k"), 1700000000, []byte(gotBody), gotSig) || gotTS != "1700000000" {
		t.Fatal("receiver could not verify the signature")
	}
}

func TestSenderDoesNotFollowRedirects(t *testing.T) {
	var internalHits int32
	internal := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&internalHits, 1) }))
	defer internal.Close()
	for _, code := range []int{301, 302, 307, 308} {
		_, s, url := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, internal.URL+"/steal", code)
		}))
		r := s.Post(context.Background(), url, []byte("{}"), nil)
		if r.Status != StatusHTTPError || r.HTTPStatus != code || r.Retryable {
			t.Errorf("%d: %+v", code, r)
		}
	}
	if internalHits != 0 {
		t.Fatal("a redirect was followed")
	}
}

func TestSenderRetryClassification(t *testing.T) {
	for code, retry := range map[int]bool{500: true, 502: true, 503: true, 429: true, 400: false, 401: false, 404: false, 410: false} {
		_, s, url := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		r := s.Post(context.Background(), url, nil, nil)
		if r.Status != StatusHTTPError || r.Retryable != retry || r.HTTPStatus != code {
			t.Errorf("%d: %+v", code, r)
		}
	}
}

func TestSenderTimeout(t *testing.T) {
	release := make(chan struct{})
	_, s, url := tlsServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer close(release)
	s.Timeout = 150 * time.Millisecond
	start := time.Now()
	r := s.Post(context.Background(), url, nil, nil)
	if r.Status != StatusTimeout || !r.Retryable {
		t.Fatalf("%+v", r)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not time out promptly")
	}
}

// DNS rebinding: the first answer is public, any later one is private. The
// sender resolves exactly once and connects to the address it checked.
func TestSenderPinsTheCheckedAddress(t *testing.T) {
	var dialedAddr string
	var mu sync.Mutex
	srv, s, url := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	res := &fakeResolver{answers: [][]netip.Addr{addrs("127.0.0.1"), addrs("10.0.0.5"), addrs("169.254.169.254")}}
	s.Resolver = res
	real := (&net.Dialer{Timeout: time.Second}).DialContext
	s.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialedAddr = addr
		mu.Unlock()
		return real(ctx, network, addr)
	}
	r := s.Post(context.Background(), url, nil, nil)
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	if res.calls != 1 {
		t.Fatalf("resolved %d times, want exactly once", res.calls)
	}
	if dialedAddr != "127.0.0.1:"+port {
		t.Fatalf("dialed %q, want the checked address", dialedAddr)
	}
	// A second Post sees the rebound answer and is refused outright.
	r = s.Post(context.Background(), url, nil, nil)
	if r.Status != StatusBlocked {
		t.Fatalf("rebound answer must be blocked, got %+v", r)
	}
}

func TestSenderIPv6LiteralAndNonStandardPortRefusedInProductionPolicy(t *testing.T) {
	s := newSender(t, &fakeResolver{answers: [][]netip.Addr{addrs("8.8.8.8")}}, "hooks.example.com")
	for _, u := range []string{"https://[2606:4700::1111]/x", "https://hooks.example.com:8443/x", "http://hooks.example.com/x", "https://u:p@hooks.example.com/x"} {
		if r := s.Post(context.Background(), u, nil, nil); r.Status != StatusNotAllowed {
			t.Errorf("%s: %+v", u, r)
		}
	}
}

func TestDevInsecureAllowsHTTPLoopbackOnlyWithAllowlistedHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	s := &Sender{
		Policy:   Policy{ExtraHosts: []string{"localhost"}, DevInsecure: true},
		Resolver: &fakeResolver{answers: [][]netip.Addr{addrs("127.0.0.1")}},
		Timeout:  time.Second,
	}
	if r := s.Post(context.Background(), "http://localhost:"+port+"/x", nil, nil); !r.OK() || hits != 1 {
		t.Fatalf("%+v hits=%d", r, hits)
	}
	// Not in the allowlist: still refused in dev mode.
	if r := s.Post(context.Background(), "http://other.test:"+port+"/x", nil, nil); r.Status != StatusNotAllowed {
		t.Fatalf("%+v", r)
	}
}
