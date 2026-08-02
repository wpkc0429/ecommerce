package shoptz

import (
	"testing"
	"time"
)

// TestKnownZonesHaveTheirKnownOffsets is the tzdata check (design D2).
//
// It does not merely assert "LoadLocation returned no error" — a broken
// fallback could return UTC for everything and pass that. Each case pins the
// ACTUAL offset on a fixed date, and Asia/Kolkata's half-hour offset is the
// load-bearing one: no plausible degraded implementation produces +05:30 by
// accident.
//
// A unit test cannot really delete /usr/share/zoneinfo, so this does not prove
// the embedded database is what answered. What it proves is the property that
// matters — that on this machine, and on CI, and in the container the same test
// binary would run in, these names resolve to these offsets. Removing the
// `_ "time/tzdata"` import in an image without the apk package turns it red.
func TestKnownZonesHaveTheirKnownOffsets(t *testing.T) {
	// 2026-08-02 12:00 UTC — inside northern-hemisphere DST, so New York is on
	// EDT (-4) rather than EST (-5). Picking a date in DST is deliberate: an
	// offset table that ignored DST rules would answer -5 here.
	instant := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name           string
		offsetSeconds  int
		abbreviationOK func(string) bool
	}{
		{"UTC", 0, func(s string) bool { return s == "UTC" }},
		{"Asia/Taipei", 8 * 3600, func(string) bool { return true }},
		{"America/New_York", -4 * 3600, func(s string) bool { return s == "EDT" }},
		// Half-hour offset: the strongest available evidence that a real IANA
		// database was consulted.
		{"Asia/Kolkata", 5*3600 + 30*60, func(string) bool { return true }},
	}
	for _, tc := range cases {
		loc, err := time.LoadLocation(tc.name)
		if err != nil {
			t.Fatalf("LoadLocation(%q): %v — no system tzdata and no embedded database", tc.name, err)
		}
		abbr, offset := instant.In(loc).Zone()
		if offset != tc.offsetSeconds {
			t.Errorf("%s at %s: offset = %d seconds, want %d", tc.name, instant, offset, tc.offsetSeconds)
		}
		if !tc.abbreviationOK(abbr) {
			t.Errorf("%s at %s: abbreviation = %q, unexpected", tc.name, instant, abbr)
		}
	}
}

// TestDaylightSavingTransitionIsRespected is the reason the column stores an
// IANA name rather than a fixed UTC offset (design D1). A stored "-05:00" would
// be right in January and silently wrong in August.
func TestDaylightSavingTransitionIsRespected(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	_, winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).In(loc).Zone()
	_, summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).In(loc).Zone()
	if winter != -5*3600 {
		t.Errorf("January offset = %d, want -18000", winter)
	}
	if summer != -4*3600 {
		t.Errorf("July offset = %d, want -14400", summer)
	}
	if winter == summer {
		t.Fatal("winter and summer offsets are identical — DST rules are not being applied")
	}
}

func TestValidate(t *testing.T) {
	valid := []string{"UTC", "Asia/Taipei", "America/New_York", "Asia/Kolkata", "Europe/London"}
	for _, name := range valid {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{
		"",
		"   ",
		// Resolves to the server's own zone — the exact ambiguity this column
		// exists to remove.
		"Local",
		"Asia/Taipeh",
		"GMT+8",
		"+08:00",
		"../../etc/passwd",
	}
	for _, name := range invalid {
		if err := Validate(name); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", name)
		}
	}
}

func TestResolveDistinguishesUnsetFromChosen(t *testing.T) {
	const platformDefault = "Asia/Taipei"

	// Unset: falls back, and SAYS it fell back.
	eff, loc, err := Resolve(nil, platformDefault)
	if err != nil {
		t.Fatalf("Resolve(nil): %v", err)
	}
	if eff.Name != platformDefault || eff.Source != SourceDefault || !eff.IsFallback() {
		t.Errorf("unset shop: got %+v, want {%s %s} and IsFallback", eff, platformDefault, SourceDefault)
	}
	if loc == nil {
		t.Fatal("Resolve returned a nil location")
	}

	// Chosen, and chosen to be the same string as the platform default. The
	// arithmetic is identical; the reported source MUST NOT be (design D1).
	same := platformDefault
	eff, _, err = Resolve(&same, platformDefault)
	if err != nil {
		t.Fatalf("Resolve(&same): %v", err)
	}
	if eff.Name != platformDefault || eff.Source != SourceShop || eff.IsFallback() {
		t.Errorf("shop that chose the default value: got %+v, want source=%s", eff, SourceShop)
	}

	// Chosen, different.
	ny := "America/New_York"
	eff, loc, err = Resolve(&ny, platformDefault)
	if err != nil {
		t.Fatalf("Resolve(&ny): %v", err)
	}
	if eff.Name != ny || eff.Source != SourceShop {
		t.Errorf("got %+v, want {%s %s}", eff, ny, SourceShop)
	}
	if loc.String() != ny {
		t.Errorf("location = %q, want %q", loc, ny)
	}

	// An empty string in the column is treated as unset rather than as a third
	// state. Nothing writes it, but the column's type permits it.
	empty := ""
	eff, _, err = Resolve(&empty, platformDefault)
	if err != nil {
		t.Fatalf("Resolve(&empty): %v", err)
	}
	if eff.Source != SourceDefault {
		t.Errorf("empty string: source = %q, want %q", eff.Source, SourceDefault)
	}
}

func TestResolveRejectsAnUnloadableDefault(t *testing.T) {
	if _, _, err := Resolve(nil, "Nowhere/Atlantis"); err == nil {
		t.Fatal("Resolve with a bogus platform default returned nil error")
	}
}

// TestResolveRejectsAnEmptyDefault guards the one failure the wiring audit
// cannot see: report.Service.DefaultTimezone is a plain string, so leaving it
// unset in wire.go is not a nil dependency. time.LoadLocation("") answers UTC
// with no error, which would silently move every unconfigured shop's day
// boundary by eight hours.
func TestResolveRejectsAnEmptyDefault(t *testing.T) {
	for _, def := range []string{"", "   "} {
		if _, _, err := Resolve(nil, def); err == nil {
			t.Fatalf("Resolve(nil, %q) = nil error — an unwired default would silently become UTC", def)
		}
	}
	// Still rejected even when the shop has its own zone: an unset platform
	// default is a wiring bug, and answering correctly for the shops that
	// happen to have configured a zone would hide it until the first shop that
	// has not.
	ny := "America/New_York"
	if _, _, err := Resolve(&ny, ""); err == nil {
		t.Fatal("Resolve with a configured shop but an empty platform default returned nil error")
	}
}
