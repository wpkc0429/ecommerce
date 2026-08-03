// Package cryptbox is application-level symmetric encryption for the handful
// of genuine secrets this system stores in its own database (change
// ecpay-per-shop-credentials, design D4) — as opposed to identifiers like the
// publishable key, which are stored in plaintext by design because knowing
// them grants nothing.
//
// AES-256-GCM with a key from an environment variable, not a KMS/Vault
// integration: this deployment is single-instance with no key-rotation
// requirement today, and a managed key-management service would add a new
// external dependency (this project has none — see openspec/project.md
// External Dependencies) for a capability nothing here uses yet. Revisit if
// that changes.
package cryptbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// KeySize is the required decoded key length: AES-256.
const KeySize = 32

// ErrInvalidKey means the configured key is not exactly KeySize bytes once
// decoded. Callers MUST treat this as fatal at startup (design D4) rather than
// letting it surface later as a decrypt failure on the first real request.
var ErrInvalidKey = errors.New("cryptbox: key must decode to exactly 32 bytes")

// Box encrypts/decrypts with one fixed key.
type Box struct {
	aead cipher.AEAD
}

// ParseKey decodes a key from hex or base64 (whichever succeeds and yields
// KeySize bytes) and builds a Box. Returns ErrInvalidKey for anything else —
// a malformed or wrong-length key must fail loudly, not silently encrypt with
// a truncated/garbage key.
func ParseKey(raw string) (*Box, error) {
	key, err := decodeKey(raw)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cryptbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptbox: %w", err)
	}
	return &Box{aead: aead}, nil
}

func decodeKey(raw string) ([]byte, error) {
	if key, err := hex.DecodeString(raw); err == nil && len(key) == KeySize {
		return key, nil
	}
	if key, err := base64.StdEncoding.DecodeString(raw); err == nil && len(key) == KeySize {
		return key, nil
	}
	return nil, ErrInvalidKey
}

// Encrypt returns nonce||ciphertext, suitable for storing verbatim in a bytea
// column. plaintext may be empty (encrypts to a non-empty value regardless —
// callers distinguish "no credential" at the NULL-column level, not by an
// empty ciphertext).
func (b *Box) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("cryptbox: generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt reverses Encrypt. Returns an error if data is shorter than one
// nonce or the authentication tag does not verify (wrong key, or the bytes
// were tampered with/corrupted).
func (b *Box) Decrypt(data []byte) (string, error) {
	ns := b.aead.NonceSize()
	if len(data) < ns {
		return "", errors.New("cryptbox: ciphertext shorter than nonce")
	}
	nonce, ciphertext := data[:ns], data[ns:]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("cryptbox: decrypt: %w", err)
	}
	return string(plaintext), nil
}
