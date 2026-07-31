package auth

import (
	"regexp"
	"strings"
)

// emailRe is the single definition of "an email address this API accepts".
// Deliberately permissive — it rejects obvious garbage and normalizes the rest;
// proving an address exists is what email verification is for, and this system
// has none (see change guest-checkout, design D6).
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// NormalizeEmail lowercases and trims raw, and reports whether the result is
// well-formed. Every path that resolves a customer identity by email —
// registration, login, guest checkout — MUST normalize through here, otherwise
// the same person could end up with two identities differing only in case,
// which UNIQUE(shop_id, email) would happily allow.
//
// Callers that only need the normalized form (login, which must answer 401
// identically for malformed and unknown addresses) may ignore the bool.
func NormalizeEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	return email, emailRe.MatchString(email)
}
