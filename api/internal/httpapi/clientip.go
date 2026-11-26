package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// cfConnectingIPHeader is set by Cloudflare's edge (and forwarded verbatim by
// a Cloudflare Tunnel's cloudflared) to the visitor's IP.
const cfConnectingIPHeader = "CF-Connecting-IP"

type clientIPKey struct{}

// NewClientIPMiddleware resolves the request's client IP once and stores it
// in the context for clientIP (rate limiting, request logs).
//
// CF-Connecting-IP is believed ONLY when the TCP peer (RemoteAddr) is inside
// trusted — e.g. 127.0.0.1/32 when cloudflared runs on this host. From any
// other peer the header is ignored, so a client cannot forge its own
// rate-limit key. X-Forwarded-For / X-Real-IP are never consulted (unlike
// chi's middleware.RealIP, which trusts them from anyone). With an empty
// trusted list this is exactly the previous RemoteAddr-only behaviour.
func NewClientIPMiddleware(trusted []netip.Prefix, opts ...ClientIPOption) func(http.Handler) http.Handler {
	o := &clientIPOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o.warn && (len(r.Header.Values(cfConnectingIPHeader)) > 0 || len(r.Header.Values("X-Forwarded-For")) > 0) {
				o.warnOnce.Do(func() {
					if o.log != nil {
						o.log.Warn("requests carry CF-Connecting-IP / X-Forwarded-For but TRUSTED_PROXIES is empty: every client is keyed by the TCP peer address, so per-IP rate limits act as one global bucket behind a reverse proxy or Cloudflare Tunnel; set TRUSTED_PROXIES to the proxy address (127.0.0.1/32,::1/128 for cloudflared on this host)",
							"peer", remoteHost(r))
					}
				})
			}
			ip := resolveClientIP(r, trusted)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
}

type clientIPOptions struct {
	warn     bool
	log      *slog.Logger
	warnOnce sync.Once
}

// ClientIPOption configures NewClientIPMiddleware.
type ClientIPOption func(*clientIPOptions)

// WithUntrustedForwardingWarning logs one WARN (per process) the first time a
// request carries CF-Connecting-IP or X-Forwarded-For while enabled. It is
// meant for production with an empty TRUSTED_PROXIES, where the headers prove
// a proxy sits in front but the app cannot see real client IPs. It never
// changes which IP is used.
func WithUntrustedForwardingWarning(enabled bool, log *slog.Logger) ClientIPOption {
	return func(o *clientIPOptions) { o.warn, o.log = enabled, log }
}

// resolveClientIP implements the trust rule described on NewClientIPMiddleware.
func resolveClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := remoteHost(r)
	if len(trusted) == 0 {
		return peer
	}
	peerAddr, err := netip.ParseAddr(peer)
	if err != nil || !inPrefixes(peerAddr.Unmap(), trusted) {
		return peer
	}
	// Exactly one header with exactly one valid IP; anything else (absent,
	// repeated, a list, garbage) falls back to the peer.
	vals := r.Header.Values(cfConnectingIPHeader)
	if len(vals) != 1 {
		return peer
	}
	v := strings.TrimSpace(vals[0])
	a, err := netip.ParseAddr(v)
	if err != nil || a.Zone() != "" {
		return peer
	}
	return a.Unmap().String()
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// remoteHost is RemoteAddr without the port (raw RemoteAddr if it is not
// host:port).
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP returns the IP resolved by NewClientIPMiddleware, or the TCP peer
// when the middleware is not mounted (handlers/middleware built directly in
// tests) — never a client-supplied header.
func clientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey{}).(string); ok && ip != "" {
		return ip
	}
	return remoteHost(r)
}
