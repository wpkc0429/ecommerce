// Package shoptz resolves and validates a shop's IANA time zone (change
// merchant-reports, design D1/D2).
//
// It exists so that the fallback rule — "the shop's own zone if it set one,
// otherwise the platform default" — has exactly ONE implementation. The moment
// each caller writes its own `if shop.Timezone == nil` the two halves of a
// report can disagree about which day a midnight order belongs to, and nothing
// would fail.
//
// ── Why this package imports time/tzdata ──────────────────────────────
//
// time.LoadLocation reads the SYSTEM tz database (/usr/share/zoneinfo). A
// development machine always has one; a container does not have to. api's
// Dockerfile does `apk add --no-cache ca-certificates tzdata` today — but that
// line was added for something else, **nothing tests it**, and deleting it
// would turn nothing red while making every time-zone validation in production
// answer 422 forever.
//
// The blank import below embeds the IANA database into the binary (~450KB).
// Go's runtime consults it only when the system database is missing, so an
// environment that has one behaves byte for byte as before — and any binary
// that links this package gets the guarantee without anyone remembering to add
// a line to its main().
//
// The Dockerfile's `tzdata` stays: removing it is an unrelated change, and
// belt-and-braces here costs nothing.
//
// ── The residual risk, stated ─────────────────────────────────────────
//
// Postgres has its OWN tz database, and `AT TIME ZONE` (internal/report) uses
// that one. A name Go accepts but Postgres does not would surface as a 500.
// PG16 ships the complete IANA set, so this does not happen today, and
// internal/report's integration tests run real queries through three
// representative zones — so the assumption has a check that turns red rather
// than a sentence in a document.
package shoptz

import (
	"fmt"
	"strings"
	"time"

	// Embeds the IANA time zone database. See the package doc: this is a
	// correctness guarantee, not an optimization, and removing it makes this
	// package's behavior depend on the base image.
	_ "time/tzdata"
)

// SourceShop / SourceDefault name where an effective zone came from.
//
// These two values are the whole reason shops.timezone is nullable (design D1):
// "the merchant chose Asia/Taipei" and "the merchant never chose anything and
// the platform default happens to be Asia/Taipei" produce the same arithmetic
// and must never produce the same sentence on screen.
const (
	SourceShop    = "shop"
	SourceDefault = "default"
)

// Effective is the zone a report actually used, plus where it came from. It
// travels in the response body on purpose (design D12): a disclosure that ships
// with the numbers cannot drift away from them the way documentation does.
type Effective struct {
	Name   string
	Source string
}

// IsFallback reports whether the merchant has not configured a zone.
func (e Effective) IsFallback() bool { return e.Source == SourceDefault }

// Validate reports whether name is a loadable IANA time zone name.
//
// Deliberately rejects "" (callers express "clear the setting" with a nil
// pointer, not an empty string — an empty string in the column would be a third
// state meaning neither "set" nor "unset") and "Local" (it resolves to whatever
// the SERVER's TZ happens to be, which is exactly the ambiguity this whole
// column exists to remove).
func Validate(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("shoptz: time zone name must not be empty")
	}
	if name == "Local" {
		return fmt.Errorf("shoptz: %q is not an IANA time zone — it resolves to the server's own zone", name)
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("shoptz: %q is not a known IANA time zone", name)
	}
	return nil
}

// Resolve returns the effective zone for a shop.
//
// shopTZ is the nullable shops.timezone column; defaultTZ is the platform
// setting (config.ReportDefaultTimezone), which Load() has already validated at
// boot — so the only error this can return in practice is a shop-level value
// that was valid when written and is not any more.
func Resolve(shopTZ *string, defaultTZ string) (Effective, *time.Location, error) {
	// An empty platform default is rejected loudly instead of being passed to
	// LoadLocation, which answers UTC for "" without an error. That silent UTC
	// is precisely the failure mode of forgetting to wire this value: every
	// unconfigured shop's day boundaries would shift by eight hours and nothing
	// would complain. The wiring audit (check ⑤) cannot see it either — the
	// field is a plain string, not a nilable dependency — so the guard has to
	// live here.
	if strings.TrimSpace(defaultTZ) == "" {
		return Effective{}, nil, fmt.Errorf("shoptz: platform default time zone is not configured")
	}
	name, source := defaultTZ, SourceDefault
	if shopTZ != nil && strings.TrimSpace(*shopTZ) != "" {
		name, source = *shopTZ, SourceShop
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Effective{}, nil, fmt.Errorf("shoptz: load %q: %w", name, err)
	}
	return Effective{Name: name, Source: source}, loc, nil
}
