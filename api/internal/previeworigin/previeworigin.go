// Package previeworigin parses and matches the operator-configured list of
// PREVIEW admin origins (ADMIN_PREVIEW_ORIGINS, change preview-readonly-session).
//
// A preview origin is where the AI service deploys a design preview (a
// version preview or a pre-approval preview). It is deliberately a different
// origin class from a normal admin origin: a session created from it is a
// read-only session, enforced on the server.
//
// Two entry shapes are accepted, nothing else:
//
//	exact     https://p-3f9a.preview.example.test[:port]
//	wildcard  https://*.preview.example.test[:port]
//
// A wildcard stands for EXACTLY ONE DNS label directly under the base domain
// ("a.preview.example.test" matches, "a.b.preview.example.test" and
// "preview.example.test" itself do not), the label must be plain LDH ASCII and
// must not be punycode ("xn--"), and the base domain needs at least two labels
// so "https://*.com" cannot be written. The scheme is https, except plain http
// for loopback hosts when the caller allows it (development).
package previeworigin

import (
	"fmt"
	"net/netip"
	"strings"
)

// Set is an immutable set of preview origin patterns. The zero value and nil
// are an empty set: the feature is off and nothing matches.
type Set struct {
	exact    map[string]bool
	wildcard []wildcard
}

type wildcard struct {
	scheme string
	suffix string // ".preview.example.test"
	port   string // "" = no explicit port
}

// Enabled reports whether any pattern is configured.
func (s *Set) Enabled() bool { return s != nil && (len(s.exact) > 0 || len(s.wildcard) > 0) }

// Parse validates every entry strictly and returns the set. An empty list is a
// valid, empty set. allowLoopbackHTTP admits plain http for localhost,
// 127.0.0.1 and [::1] (never for wildcards).
func Parse(entries []string, allowLoopbackHTTP bool) (*Set, error) {
	s := &Set{exact: map[string]bool{}}
	for _, raw := range entries {
		if err := s.add(raw, allowLoopbackHTTP); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Set) add(raw string, allowLoopbackHTTP bool) error {
	bad := func(why string) error {
		return fmt.Errorf("preview origin %q: %s", raw, why)
	}
	if raw != strings.TrimSpace(raw) || raw == "" {
		return bad("must not be empty or padded with whitespace")
	}
	if raw != strings.ToLower(raw) {
		return bad("must be lowercase")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || (scheme != "https" && scheme != "http") {
		return bad("must start with https:// (or http:// for loopback)")
	}
	if strings.ContainsAny(rest, "/?#@\\ \t") {
		return bad("must be scheme://host[:port] only (no userinfo, path, query or fragment)")
	}
	host, port, err := splitHostPort(rest)
	if err != nil {
		return bad(err.Error())
	}
	if strings.HasPrefix(host, "*") {
		if scheme != "https" {
			return bad("a wildcard requires https")
		}
		base, ok := strings.CutPrefix(host, "*.")
		if !ok || strings.Contains(base, "*") {
			return bad("the wildcard must be the whole first label: https://*.base.example")
		}
		if err := validateHost(base, 2); err != nil {
			return bad("base domain: " + err.Error())
		}
		s.wildcard = append(s.wildcard, wildcard{scheme: scheme, suffix: "." + base, port: port})
		return nil
	}
	if strings.Contains(host, "*") {
		return bad("a wildcard is only allowed as the whole first label")
	}
	if isLoopbackHost(host) {
		if scheme == "http" && !allowLoopbackHTTP {
			return bad("http is only allowed for loopback hosts in development")
		}
	} else {
		if scheme != "https" {
			return bad("http is only allowed for loopback hosts in development")
		}
		if err := validateHost(host, 2); err != nil {
			return bad(err.Error())
		}
	}
	s.exact[canonical(scheme, host, port)] = true
	return nil
}

// splitHostPort splits rest into host and optional numeric port.
func splitHostPort(rest string) (host, port string, err error) {
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 {
			return "", "", fmt.Errorf("malformed IPv6 host")
		}
		host, tail := rest[:end+1], rest[end+1:]
		if tail == "" {
			return host, "", nil
		}
		p, ok := strings.CutPrefix(tail, ":")
		if !ok || !validPort(p) {
			return "", "", fmt.Errorf("invalid port")
		}
		return host, p, nil
	}
	host, p, hasPort := strings.Cut(rest, ":")
	if hasPort {
		if !validPort(p) {
			return "", "", fmt.Errorf("invalid port")
		}
		port = p
	}
	if host == "" {
		return "", "", fmt.Errorf("empty host")
	}
	return host, port, nil
}

func validPort(p string) bool {
	if p == "" || len(p) > 5 || p[0] == '0' {
		return false
	}
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || host == "[::1]" {
		return true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.IsLoopback()
	}
	return false
}

// validateHost enforces plain LDH ASCII DNS names with at least minLabels
// labels, no trailing dot, no empty label, and not an IP literal.
func validateHost(host string, minLabels int) error {
	if host == "" || len(host) > 253 {
		return fmt.Errorf("invalid host length")
	}
	if strings.HasSuffix(host, ".") || strings.HasPrefix(host, ".") {
		return fmt.Errorf("must not start or end with a dot")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return fmt.Errorf("an IP address is not allowed")
	}
	labels := strings.Split(host, ".")
	if len(labels) < minLabels {
		return fmt.Errorf("needs at least %d labels", minLabels)
	}
	for _, l := range labels {
		if !validLabel(l) {
			return fmt.Errorf("label %q is not a plain lowercase LDH label", l)
		}
	}
	return nil
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func canonical(scheme, host, port string) string {
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

// Match reports whether origin (a browser Origin value) is a preview origin,
// and returns its canonical form. It is stricter than tenant.NormalizeOrigin:
// anything with userinfo, a path, a trailing dot, a non-ASCII host or a
// non-LDH label never matches. Case is folded (hostnames are case
// insensitive and a client that varies case must not escape the read-only
// class by doing so).
func (s *Set) Match(origin string) (string, bool) {
	if !s.Enabled() {
		return "", false
	}
	o := strings.ToLower(strings.TrimSpace(origin))
	scheme, rest, ok := strings.Cut(o, "://")
	if !ok || strings.ContainsAny(rest, "/?#@\\ \t*") {
		return "", false
	}
	if scheme != "https" && scheme != "http" {
		return "", false
	}
	host, port, err := splitHostPort(rest)
	if err != nil || host == "" {
		return "", false
	}
	canon := canonical(scheme, host, port)
	if s.exact[canon] {
		return canon, true
	}
	if scheme != "https" {
		return "", false
	}
	for _, w := range s.wildcard {
		if w.scheme != scheme || w.port != port {
			continue
		}
		label, ok := strings.CutSuffix(host, w.suffix)
		if !ok || label == "" || strings.Contains(label, ".") {
			continue
		}
		if !validLabel(label) || strings.HasPrefix(label, "xn--") {
			continue
		}
		return canon, true
	}
	return "", false
}

// Overlaps reports whether origin (any origin, e.g. a normal admin origin) is
// matched by the set. Used to keep the two classes disjoint.
func (s *Set) Overlaps(origin string) bool {
	_, ok := s.Match(origin)
	return ok
}
