package auth

import "testing"

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		raw       string
		want      string
		wantValid bool
	}{
		{"  Alice@Example.COM  ", "alice@example.com", true},
		{"alice@example.com", "alice@example.com", true},
		{"a@b.co", "a@b.co", true},
		{"", "", false},
		{"alice", "alice", false},
		{"alice@example", "alice@example", false},
		{"alice @example.com", "alice @example.com", false},
		{"ali@ce@example.com", "ali@ce@example.com", false},
	}
	for _, tc := range cases {
		got, valid := NormalizeEmail(tc.raw)
		if got != tc.want || valid != tc.wantValid {
			t.Errorf("NormalizeEmail(%q) = (%q, %v), want (%q, %v)", tc.raw, got, valid, tc.want, tc.wantValid)
		}
	}
}

// Normalization must be idempotent and case-folding, otherwise the same person
// could end up with two identities at one shop that UNIQUE(shop_id, email)
// considers distinct.
func TestNormalizeEmailIsIdempotent(t *testing.T) {
	once, _ := NormalizeEmail(" Bob@Example.com ")
	twice, _ := NormalizeEmail(once)
	if once != twice {
		t.Fatalf("not idempotent: %q then %q", once, twice)
	}
}
