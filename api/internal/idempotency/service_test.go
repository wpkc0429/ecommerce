package idempotency

import (
	"strings"
	"testing"
)

// ── ValidateKey: bounds only, never structure (design D2) ───────────────

func TestValidateKeyBounds(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want bool // want accepted
	}{
		// The lower bound exists to protect a client from a bug it cannot see:
		// a near-constant key collides with other customers of the same shop,
		// and the symptom of that collision lands on *them*.
		{"empty", "", false},
		{"one char", "k", false},
		{"seven chars, just under", "1234567", false},
		{"eight chars, exactly at the bound", "12345678", true},

		{"255 chars, exactly at the bound", strings.Repeat("a", 255), true},
		{"256 chars, one over", strings.Repeat("a", 256), false},

		// Printable ASCII only: control characters would reach logs and psql,
		// and multi-byte input makes "length" ambiguous.
		{"tab", "abcd\tefgh", false},
		{"newline (log injection)", "abcdefgh\nx", false},
		{"space", "abcd efgh", false},
		{"del", "abcdefg\x7f", false},
		{"non-ascii", "abcdefg日", false},

		// No structure is imposed — these must all be accepted.
		{"uuid v4", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"ONDO's own generator shape", "k-mfa3x1p-9qz2h7", true},
		{"opaque base64ish", "aGVsbG8gd29ybGQ=", true},
		{"punctuation soup", "!#$%&'*+-.^_`|~", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateKey(c.key)
			if c.want && err != nil {
				t.Fatalf("ValidateKey(%q) = %v, want accepted", c.key, err)
			}
			if !c.want && err == nil {
				t.Fatalf("ValidateKey(%q) accepted, want rejected", c.key)
			}
			if !c.want && err != ErrInvalidKey {
				t.Fatalf("ValidateKey(%q) = %v, want ErrInvalidKey", c.key, err)
			}
		})
	}
}

// ── Fingerprint (design D7) ─────────────────────────────────────────────

func TestFingerprintIsStableForIdenticalInput(t *testing.T) {
	body := []byte(`{"email":"a@b.c","items":[{"sku_id":1,"quantity":2}]}`)
	if a, b := Fingerprint(ScopeGuestCheckout, body), Fingerprint(ScopeGuestCheckout, body); a != b {
		t.Fatalf("identical input must fingerprint identically: %s vs %s", a, b)
	}
}

func TestFingerprintSeparatesCallers(t *testing.T) {
	// The case this exists for: member checkout's body is nothing but a shipping
	// address, so two customers sending to the same address produce byte-identical
	// bodies. Without the caller in the fingerprint, a key collision between them
	// would replay one customer's order to the other.
	body := []byte(`{"recipient_name":"王小明","phone":"0912345678","line1":"台北市信義區信義路五段7號"}`)
	a := Fingerprint(MemberCheckoutScope(11), body)
	b := Fingerprint(MemberCheckoutScope(12), body)
	if a == b {
		t.Fatal("two different members with a byte-identical body MUST NOT share a fingerprint")
	}
}

func TestFingerprintSeparatesEndpoints(t *testing.T) {
	body := []byte(`{}`)
	if Fingerprint(ScopeGuestCheckout, body) == Fingerprint(MemberCheckoutScope(1), body) {
		t.Fatal("the two checkout endpoints MUST NOT share a fingerprint for the same body")
	}
}

func TestFingerprintSeparatesBodies(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"quantity":1}`),
		[]byte(`{"quantity":2}`),
		// Raw bytes, no normalization: reordering or reformatting counts as a
		// different request. That is the deliberate cost of never needing to
		// maintain a canonicalizer that would silently erase fields it does not
		// know about yet.
		[]byte(`{ "quantity":1 }`),
	}
	seen := map[string]bool{}
	for _, b := range cases {
		fp := Fingerprint(ScopeGuestCheckout, b)
		if seen[fp] {
			t.Fatalf("distinct bodies collided on fingerprint %s (body %q)", fp, b)
		}
		seen[fp] = true
	}
}

func TestFingerprintScopeAndBodyCannotBeConfused(t *testing.T) {
	// The separator matters: without it, scope "ab" + body "c" and scope "a" +
	// body "bc" would hash the same.
	if Fingerprint("ab", []byte("c")) == Fingerprint("a", []byte("bc")) {
		t.Fatal("scope and body must not be concatenable into each other")
	}
}

func TestFingerprintIsHexSHA256(t *testing.T) {
	// The column is char(64); anything else would be silently truncated.
	fp := Fingerprint(ScopeGuestCheckout, []byte("x"))
	if len(fp) != 64 {
		t.Fatalf("fingerprint length = %d, want 64 (request_hash is char(64))", len(fp))
	}
	for _, r := range fp {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("fingerprint %q is not lowercase hex", fp)
		}
	}
}
