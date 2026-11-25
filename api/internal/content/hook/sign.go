package hook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Header names of the signed request.
const (
	HeaderTimestamp = "X-Content-Timestamp"
	HeaderSignature = "X-Content-Signature"
)

// Sign returns "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)),
// the value of X-Content-Signature. The receiver recomputes it from the two
// headers and the raw body. Deploy hooks of Cloudflare ignore it; it exists for
// receivers on operator-approved hosts.
func Sign(secret []byte, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = m.Write([]byte("."))
	_, _ = m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify is the receiver-side check (constant time).
func Verify(secret []byte, timestamp int64, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, timestamp, body)), []byte(signature))
}
