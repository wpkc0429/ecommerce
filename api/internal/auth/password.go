// Package auth implements password hashing, the two isolated JWT systems, and
// refresh-token rotation (design D9).
package auth

import (
	"fmt"
	"strings"
	"sync"

	"github.com/alexedwards/argon2id"
)

// argonParams follows the OWASP Argon2id baseline recommendation
// (m=19456 KiB, t=2, p=1) — design D9.
var argonParams = &argon2id.Params{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// HashPassword returns the Argon2id PHC-format hash ($argon2id$...).
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", fmt.Errorf("auth: empty password")
	}
	hash, err := argon2id.CreateHash(plain, argonParams)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		return "", fmt.Errorf("auth: unexpected hash format")
	}
	return hash, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// EqualizeVerifyTiming burns one Argon2id verification so "account not found"
// and "wrong password" take comparable time (anti-enumeration).
func EqualizeVerifyTiming() {
	dummyOnce.Do(func() {
		dummyHash, _ = argon2id.CreateHash("timing-equalizer", argonParams)
	})
	_, _ = argon2id.ComparePasswordAndHash("never-matches", dummyHash)
}

// VerifyPassword reports whether plain matches the stored Argon2id hash.
// Any malformed hash verifies as false without error leakage.
func VerifyPassword(plain, hash string) bool {
	if plain == "" || hash == "" {
		return false
	}
	ok, err := argon2id.ComparePasswordAndHash(plain, hash)
	if err != nil {
		return false
	}
	return ok
}
