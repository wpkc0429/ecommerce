package design

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRedactPII(t *testing.T) {
	cases := []struct {
		name, in, want string
		flags          []string
	}{
		{"email", "mail wang@example.com please", "mail [redacted] please", []string{FlagEmail}},
		{"tw mobile", "call 0912345678 now", "call [redacted] now", []string{FlagPhone}},
		{"tw mobile dashes", "call 0912-345-678 now", "call [redacted] now", []string{FlagPhone}},
		{"tw mobile intl", "+886912345678 ok", "[redacted] ok", []string{FlagPhone}},
		{"visa test number", "card 4111 1111 1111 1111 end", "card [redacted] end", []string{FlagCardLike}},
		{"card dashes", "4111-1111-1111-1111", "[redacted]", []string{FlagCardLike}},
		{"16 digits failing luhn is kept", "order 1234567890123456 done", "order 1234567890123456 done", nil},
		{"short number kept", "make the logo 120 px wide", "make the logo 120 px wide", nil},
		{"landline kept", "ring 0223456789", "ring 0223456789", nil},
		{"all three", "a@b.co 0987654321 4242424242424242", "[redacted] [redacted] [redacted]", []string{FlagEmail, FlagPhone, FlagCardLike}},
	}
	for _, tc := range cases {
		got, flags := RedactPII(tc.in)
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
		if strings.Join(flags, ",") != strings.Join(tc.flags, ",") {
			t.Errorf("%s: flags %v want %v", tc.name, flags, tc.flags)
		}
	}
}

func TestSanitizeTextDropsControlAndBidiCharacters(t *testing.T) {
	in := "a\u202eb\u2066c\u2069d\x00e\x07f\ng\th<script>alert(1)</script>"
	got := SanitizeText(in)
	if got != "abcdef\ng\th<script>alert(1)</script>" {
		t.Fatalf("got %q", got)
	}
}

func TestTransitionTableMatrix(t *testing.T) {
	// The complete matrix: every ordered pair of the nine states, for each actor.
	legal := map[string]string{
		"drafting>previewing": "service", "previewing>awaiting_approval": "service", "previewing>drafting": "service",
		"awaiting_approval>drafting": "service,user", "awaiting_approval>approved": "user", "awaiting_approval>rejected": "user",
		"approved>published": "service", "published>reverted": "system",
		"drafting>failed": "service,system", "previewing>failed": "service,system", "awaiting_approval>failed": "service,system", "approved>failed": "service,system",
		"drafting>cancelled": "user", "previewing>cancelled": "user", "awaiting_approval>cancelled": "user", "approved>cancelled": "user",
	}
	for _, from := range AllStatuses {
		for _, to := range AllStatuses {
			for _, actor := range []string{ActorService, ActorUser, ActorSystem} {
				want := false
				for _, a := range strings.Split(legal[from+">"+to], ",") {
					want = want || a == actor
				}
				if got := Allowed(from, to, actor); got != want {
					t.Errorf("%s -> %s by %s: got %v want %v", from, to, actor, got, want)
				}
			}
		}
	}
	// The property the whole design leans on: only a person can reach approved.
	for _, from := range AllStatuses {
		if Allowed(from, StatusApproved, ActorService) || Allowed(from, StatusApproved, ActorSystem) {
			t.Errorf("a non-person can reach approved from %s", from)
		}
	}
	// Terminal states have no way out (published may only be marked reverted).
	for _, from := range []string{StatusRejected, StatusFailed, StatusCancelled, StatusReverted} {
		for _, to := range AllStatuses {
			for _, actor := range []string{ActorService, ActorUser, ActorSystem} {
				if Allowed(from, to, actor) {
					t.Errorf("terminal %s -> %s by %s must be illegal", from, to, actor)
				}
			}
		}
	}
}

func chainOf(t *testing.T, n int) []EventRecord {
	t.Helper()
	prev := GenesisHash
	at := time.Date(2026, 11, 25, 10, 0, 0, 123456000, time.UTC)
	var out []EventRecord
	for i := 1; i <= n; i++ {
		e := EventRecord{ShopID: 7, RequestID: "r", Seq: i, Kind: EvHeadUpdated, ActorType: ActorService,
			Detail: json.RawMessage(`{"b":2,"a":[1,2]}`), CreatedAt: at.Add(time.Duration(i) * time.Second), PrevHash: prev}
		h, err := ComputeHash(e)
		if err != nil {
			t.Fatal(err)
		}
		e.StoredHash = h
		prev = h
		out = append(out, e)
	}
	return out
}

func TestChainDetectsTampering(t *testing.T) {
	evs := chainOf(t, 5)
	if seq, why := VerifyChain(evs); seq != 0 {
		t.Fatalf("intact chain reported broken at %d: %s", seq, why)
	}
	// jsonb reorders keys and drops whitespace: the hash must not care.
	evs[2].Detail = json.RawMessage(`{ "a": [1, 2], "b": 2 }`)
	if seq, _ := VerifyChain(evs); seq != 0 {
		t.Fatalf("key order must not matter, broke at %d", seq)
	}
	// A rewritten middle row.
	evs[2].Detail = json.RawMessage(`{"a":[1,2],"b":3}`)
	if seq, _ := VerifyChain(evs); seq != 3 {
		t.Fatalf("rewritten row 3 reported at %d", seq)
	}
	// A deleted middle row.
	evs = chainOf(t, 5)
	gap := append(append([]EventRecord{}, evs[:2]...), evs[3:]...)
	if seq, why := VerifyChain(gap); seq == 0 {
		t.Fatalf("deleted row not detected (%s)", why)
	}
	// An inserted row with a forged but self-consistent hash does not link in.
	evs = chainOf(t, 3)
	forged := evs[1]
	forged.Seq, forged.PrevHash = 3, GenesisHash
	forged.StoredHash, _ = ComputeHash(forged)
	if seq, _ := VerifyChain([]EventRecord{evs[0], evs[1], forged}); seq == 0 {
		t.Fatal("a forged row with the wrong predecessor was accepted")
	}
}

func TestTimestampPrecisionDoesNotChangeTheHash(t *testing.T) {
	e := EventRecord{ShopID: 1, RequestID: "r", Seq: 1, Kind: EvCreated, ActorType: ActorSystem, PrevHash: GenesisHash,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)}
	a, _ := ComputeHash(e)
	e.CreatedAt = Micros(e.CreatedAt) // what Postgres hands back
	b, _ := ComputeHash(e)
	if a != b {
		t.Fatal("hash depends on sub-microsecond precision that the database drops")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{{"0.3.0", "0.3.0", 0}, {"0.3.0", "0.10.0", -1}, {"1.0", "0.9.9", 1}, {"", "0.1.0", -1}, {"0.3", "0.3.0", 0}}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPreviewOriginAndCIDR(t *testing.T) {
	for _, ok := range []string{"https://p-3f9a.preview.example", "http://localhost:8080", "https://a.example/"} {
		if _, err := PreviewOrigin(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://x/path", "https://x?q=1", "https://x#f", "https://u:p@x", "x.example"} {
		if _, err := PreviewOrigin(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if !cidrAllows([]string{"203.0.113.0/24"}, ClientAddr("203.0.113.9:5000")) || cidrAllows([]string{"203.0.113.0/24"}, ClientAddr("198.51.100.1")) {
		t.Error("cidr allow-list wrong")
	}
	if _, err := ParseCIDRs([]string{"not-a-cidr"}); err == nil {
		t.Error("garbage cidr accepted")
	}
}

func TestKeyHashIsStableAndKeyHasPrefix(t *testing.T) {
	a, b := HashKey("dsk_abc"), HashKey("dsk_abc")
	if a != b || a == HashKey("dsk_abd") || len(HashKey("x")) != 64 {
		t.Fatal("hash")
	}
}
