package design

import (
	"regexp"
	"strings"
	"unicode"
)

// Redacted is what a personal-data match is replaced with.
const Redacted = "[redacted]"

// Prompt flags recorded when something was redacted.
const (
	FlagEmail    = "email"
	FlagPhone    = "phone"
	FlagCardLike = "card_like"
)

var (
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)
	// A run of digits that may be separated by single spaces or dashes and may
	// start with +. Each run is then judged on its digits alone.
	digitRunRE = regexp.MustCompile(`\+?\d(?:[ -]?\d)*`)
	phoneRE    = regexp.MustCompile(`^(?:09\d{8}|\+?8869\d{8})$`)
)

// SanitizeText drops control characters (keeping newline and tab) and the
// bidirectional override/isolate characters (U+202A-202E, U+2066-2069). The
// rest is kept verbatim: the API treats text as data and never as markup.
func SanitizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
		case r >= 0x202A && r <= 0x202E:
		case r >= 0x2066 && r <= 0x2069:
		case r == 0x200E || r == 0x200F || r == 0x061C: // implicit direction marks
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RedactPII replaces e-mail addresses, Taiwan mobile numbers and card-like
// numbers (13-19 digits passing the Luhn check) with "[redacted]" and reports
// which classes were found.
func RedactPII(s string) (string, []string) {
	flags := map[string]bool{}
	s = emailRE.ReplaceAllStringFunc(s, func(string) string {
		flags[FlagEmail] = true
		return Redacted
	})
	s = digitRunRE.ReplaceAllStringFunc(s, func(m string) string {
		if class := classifyDigits(m); class != "" {
			flags[class] = true
			return Redacted
		}
		// Two numbers separated by a space ("0987654321 4242...") form one run:
		// judge each space-separated part on its own.
		if !strings.Contains(m, " ") {
			return m
		}
		parts := strings.Split(m, " ")
		for i, p := range parts {
			if class := classifyDigits(p); class != "" {
				flags[class] = true
				parts[i] = Redacted
			}
		}
		return strings.Join(parts, " ")
	})
	var out []string
	for _, f := range []string{FlagEmail, FlagPhone, FlagCardLike} {
		if flags[f] {
			out = append(out, f)
		}
	}
	return s, out
}

func luhn(digits string) bool {
	sum, alt := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// classifyDigits says whether a run of digits (with optional single spaces or
// dashes) is a card-like number or a Taiwan mobile number.
func classifyDigits(m string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, m)
	if n := len(digits); n >= 13 && n <= 19 && luhn(digits) {
		return FlagCardLike
	}
	if phoneRE.MatchString(strings.NewReplacer(" ", "", "-", "").Replace(m)) {
		return FlagPhone
	}
	return ""
}
