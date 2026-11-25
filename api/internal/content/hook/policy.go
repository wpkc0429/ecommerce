// Package hook is the publish hook of the headless content store (change
// headless-content-store slice 3, design D7): after a document is published or
// unpublished, a configured deploy-hook URL is called so the merchant's
// storefront rebuilds.
//
// The URL is a credential (a Cloudflare deploy hook URL needs no other
// authorization), so every rule here exists to keep the one outbound request
// path honest: a host allowlist, https only, no userinfo, no IP literals,
// DNS resolved by us and the result checked against private / loopback /
// link-local / metadata ranges, the connection pinned to the checked address,
// no redirects, a short timeout and a response that is never returned or
// logged. There is exactly one code path that makes the request: Sender.Post.
package hook

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Error codes of URLError; they are safe to show to the person who typed the
// URL (they never echo it).
const (
	CodeInvalid        = "invalid_url"
	CodeTooLong        = "url_too_long"
	CodeScheme         = "scheme_not_allowed"
	CodeUserinfo       = "userinfo_not_allowed"
	CodeFragment       = "fragment_not_allowed"
	CodePort           = "port_not_allowed"
	CodeIPLiteral      = "ip_literal_not_allowed"
	CodeHostNotAllowed = "host_not_allowed"
	CodePath           = "path_not_allowed"
)

// MaxURLLen is the longest accepted hook URL.
const MaxURLLen = 512

// URLError says why a URL was refused. Message never contains the URL.
type URLError struct {
	Code    string
	Message string
}

func (e *URLError) Error() string { return e.Message }

// builtinHost is a host that ships in the allowlist, with the shape of the URL
// the product documents for it. api.cloudflare.com is the whole Cloudflare API,
// so for it the path is pinned to the deploy-hook endpoints.
type builtinHost struct {
	host string
	path *regexp.Regexp
}

// Q9 (DECIDED 2026-11-25, see design.md): Cloudflare deploy hooks, both
// products, are POSTed at api.cloudflare.com.
//
//	Workers Builds: /client/v4/workers/builds/deploy_hooks/<id>
//	  (https://developers.cloudflare.com/workers/ci-cd/builds/deploy-hooks/)
//	Pages:          /client/v4/pages/webhooks/deploy_hooks/<id>
//	  (https://developers.cloudflare.com/pages/configuration/deploy-hooks/ shows
//	   the URL only in a dashboard screenshot; this is the long-standing Pages
//	   shape and must be re-checked by the operator when the hook is created)
var builtinHosts = []builtinHost{
	{
		host: "api.cloudflare.com",
		path: regexp.MustCompile(`^/client/v4/(?:workers/builds|pages/webhooks)/deploy_hooks/[A-Za-z0-9_-]{8,128}$`),
	},
}

// Policy decides which URLs and addresses are acceptable.
type Policy struct {
	// ExtraHosts are operator-approved hosts ADDED to the built-in allowlist:
	// exact names or "*.suffix". They can never relax the address checks.
	ExtraHosts []string
	// DevInsecure is for local e2e against a stub receiver only: it allows
	// http, any port and loopback addresses. Config refuses it in production.
	DevInsecure bool
}

// Target is a validated URL.
type Target struct {
	URL  *url.URL
	Host string
	Port string // "443" (or the dev port)
	// Hint is the last four characters of the URL (for the masked display).
	Hint string
}

var hostRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$`)

// ParseURL validates raw against the policy. It does not resolve DNS.
func (p Policy) ParseURL(raw string) (*Target, error) {
	if len(raw) > MaxURLLen {
		return nil, &URLError{CodeTooLong, "the URL is longer than 512 characters"}
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f {
			return nil, &URLError{CodeInvalid, "the URL contains spaces, control characters or non-ASCII characters"}
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return nil, &URLError{CodeInvalid, "not a valid absolute URL"}
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && p.DevInsecure:
	default:
		return nil, &URLError{CodeScheme, "only https URLs are allowed"}
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, &URLError{CodeUserinfo, "the URL must not contain a user name or password"}
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, &URLError{CodeFragment, "the URL must not contain a fragment"}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return nil, &URLError{CodeInvalid, "not a valid absolute URL"}
	}
	if _, err := netip.ParseAddr(host); err == nil || strings.ContainsAny(host, ":[]") {
		return nil, &URLError{CodeIPLiteral, "the host must be a name, not an IP address"}
	}
	if !hostRe.MatchString(host) || len(host) > 253 {
		return nil, &URLError{CodeInvalid, "the host name is not valid"}
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	if !p.DevInsecure && port != "443" {
		return nil, &URLError{CodePort, "only port 443 is allowed"}
	}
	if p.DevInsecure && !digitsOnly(port) {
		return nil, &URLError{CodePort, "invalid port"}
	}
	if !p.hostAllowed(host, u) {
		if isBuiltinHost(host) {
			return nil, &URLError{CodePath, "the path is not a deploy hook endpoint"}
		}
		return nil, &URLError{CodeHostNotAllowed, "the host is not on the platform's allowlist"}
	}
	hint := raw
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	}
	return &Target{URL: u, Host: host, Port: port, Hint: hint}, nil
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isBuiltinHost(host string) bool {
	for _, b := range builtinHosts {
		if b.host == host {
			return true
		}
	}
	return false
}

func (p Policy) hostAllowed(host string, u *url.URL) bool {
	for _, b := range builtinHosts {
		if b.host == host && b.path.MatchString(u.EscapedPath()) && u.RawQuery == "" {
			return true
		}
	}
	for _, h := range p.ExtraHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		switch {
		case h == "":
		case strings.HasPrefix(h, "*."):
			suffix := h[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		case h == host:
			return true
		}
	}
	return false
}

// ── address classes ───────────────────────────────────────────────────────

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32",
	"2001:2::/48", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// IPBlocked reports whether the sender must refuse to connect to ip: private,
// loopback, link-local (including the 169.254.169.254 metadata address),
// CGNAT, multicast, reserved and documentation ranges, and the IPv6 forms that
// embed an IPv4 address (IPv4-mapped, NAT64, 6to4). allowLoopback is the dev
// switch of Policy.DevInsecure and only ever lifts the loopback ranges.
func IPBlocked(ip netip.Addr, allowLoopback bool) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return true
	}
	if allowLoopback && ip.IsLoopback() {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
