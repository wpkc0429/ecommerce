package application

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"ksdevworks/ecommerce/api/internal/auth"
)

// Field limits (design D2). Lengths are runes, not bytes.
const (
	MaxEmailLen    = 254
	MaxNameLen     = 100
	MaxPhoneLen    = 20
	MaxSummaryLen  = 1000
	MaxReasonLen   = 1000
	MaxNoteLen     = 2000
	MaxConsentLen  = 32
	tooFastSeconds = 3
)

// ValidationError is a 422 the caller can map to a pointer.
type ValidationError struct {
	Pointer string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(pointer, msg string) error { return &ValidationError{Pointer: pointer, Message: msg} }

// SubmitInput is the raw, untrusted content of a public application.
type SubmitInput struct {
	Email           string
	ContactName     string
	Phone           string
	ShopName        string
	BusinessSummary string
	ConsentVersion  string
	// ElapsedSeconds is the form-load-to-submit time reported by the website's
	// locked core; nil when not reported. Below tooFastSeconds it only raises a
	// review flag, it never rejects.
	ElapsedSeconds *float64
	ClientIP       string
}

// Valid is a validated, normalized SubmitInput (obtained from Service.Validate).
type Valid struct {
	email, contactName, phone, shopName, summary, consent string
	tooFast                                               bool
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// stripControl removes control characters (including CR/LF).
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func (in SubmitInput) validate(consentVersion string) (Valid, error) {
	var c Valid

	email, ok := auth.NormalizeEmail(in.Email)
	if !ok || utf8.RuneCountInString(email) > MaxEmailLen || hasControl(email) {
		return c, invalid("/email", "must be a valid e-mail address (at most 254 characters)")
	}
	c.email = email

	c.contactName = strings.TrimSpace(in.ContactName)
	if n := utf8.RuneCountInString(c.contactName); n == 0 || n > MaxNameLen || hasControl(c.contactName) {
		return c, invalid("/contact_name", "required, at most 100 characters, no control characters")
	}

	c.shopName = strings.TrimSpace(stripControl(in.ShopName))
	if n := utf8.RuneCountInString(c.shopName); n == 0 || n > MaxNameLen {
		return c, invalid("/shop_name", "required, at most 100 characters")
	}

	c.phone = strings.TrimSpace(in.Phone)
	if n := utf8.RuneCountInString(c.phone); n > MaxPhoneLen {
		return c, invalid("/phone", "at most 20 characters")
	}
	for _, r := range c.phone {
		if (r < '0' || r > '9') && !strings.ContainsRune("+-() ", r) {
			return c, invalid("/phone", "only digits and + - ( ) and spaces are allowed")
		}
	}

	c.summary = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == '\r' {
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, in.BusinessSummary))
	if n := utf8.RuneCountInString(c.summary); n == 0 || n > MaxSummaryLen {
		return c, invalid("/business_summary", "required, at most 1000 characters")
	}

	if in.ConsentVersion != consentVersion {
		return c, invalid("/consent_version", "must equal the current terms version")
	}
	c.consent = consentVersion

	if in.ElapsedSeconds != nil && *in.ElapsedSeconds >= 0 && *in.ElapsedSeconds < tooFastSeconds {
		c.tooFast = true
	}
	return c, nil
}

// Email is the normalized address.
func (v *Valid) Email() string { return v.email }
