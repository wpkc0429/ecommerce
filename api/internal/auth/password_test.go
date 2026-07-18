package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordFormat(t *testing.T) {
	h, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	// Spec (authentication/Password hashing): 資料庫僅存 $argon2id$ 開頭的雜湊字串.
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("hash %q does not start with $argon2id$", h)
	}
	if strings.Contains(h, "s3cret-password") {
		t.Fatal("hash leaks the plaintext")
	}
}

func TestHashPasswordUniqueSalt(t *testing.T) {
	h1, _ := HashPassword("same-password")
	h2, _ := HashPassword("same-password")
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword("correct horse battery staple", h) {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword("wrong password", h) {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("", h) {
		t.Fatal("empty password accepted")
	}
	if VerifyPassword("x", "not-a-hash") {
		t.Fatal("malformed hash accepted")
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password must be rejected")
	}
}
