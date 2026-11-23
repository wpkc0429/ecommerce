package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
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
func NewClientIPMiddleware(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := resolveClientIP(r, trusted)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
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
