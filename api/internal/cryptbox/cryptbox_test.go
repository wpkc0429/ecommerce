package cryptbox

import (
	"encoding/hex"
	"strings"
	"testing"
)

const testHexKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestRoundTrip(t *testing.T) {
	box, err := ParseKey(testHexKey)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	ct, err := box.Encrypt("pwFHCqoQZGmho4w6")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := box.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if pt != "pwFHCqoQZGmho4w6" {
		t.Fatalf("got %q, want %q", pt, "pwFHCqoQZGmho4w6")
	}
}

func TestEncryptIsNotPlaintext(t *testing.T) {
	box, _ := ParseKey(testHexKey)
	ct, err := box.Encrypt("EkRm7iFT261dpevs")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(string(ct), "EkRm7iFT261dpevs") {
		t.Fatal("ciphertext contains the plaintext verbatim")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	box, _ := ParseKey(testHexKey)
	ct, err := box.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	other, _ := ParseKey("1111111111111111111111111111111111111111111111111111111111111111")
	if _, err := other.Decrypt(ct); err == nil {
		t.Fatal("expected decrypt with a different key to fail")
	}
}

func TestDecryptTamperedCiphertextFails(t *testing.T) {
	box, _ := ParseKey(testHexKey)
	ct, err := box.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct[len(ct)-1] ^= 0xFF
	if _, err := box.Decrypt(ct); err == nil {
		t.Fatal("expected decrypt of tampered ciphertext to fail")
	}
}

func TestParseKeyRejectsWrongLength(t *testing.T) {
	if _, err := ParseKey(hex.EncodeToString([]byte("too short"))); err == nil {
		t.Fatal("expected ErrInvalidKey for a short key")
	}
	if _, err := ParseKey("not-hex-or-base64!!"); err == nil {
		t.Fatal("expected ErrInvalidKey for garbage input")
	}
}

func TestParseKeyAcceptsBase64(t *testing.T) {
	// 32 raw bytes, base64-encoded.
	const b64Key = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	if _, err := ParseKey(b64Key); err != nil {
		t.Fatalf("ParseKey with base64: %v", err)
	}
}
