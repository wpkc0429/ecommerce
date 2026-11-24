package tenant

import "regexp"

var (
	domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	prefixRe = regexp.MustCompile(`^/[a-z0-9-]+(/[a-z0-9-]+)*$`)
)

// ValidDomain reports whether host (already normalized by NormalizeHost) is an
// acceptable site domain: a lowercase hostname with at least two labels. It is
// the single definition shared by the sites API and shop provisioning.
func ValidDomain(host string) bool {
	return host != "" && domainRe.MatchString(host)
}

// ValidPathPrefix reports whether prefix (already normalized by
// NormalizePathPrefix, and non-empty) is an acceptable path prefix.
func ValidPathPrefix(prefix string) bool {
	return prefixRe.MatchString(prefix)
}
