package ecpay_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// referenceCheckMac is a deliberately independent second implementation of
// 綠界's checksum, used only by the tests in this package.
//
// **Why a second implementation exists at all.** The official vector
// (TestCheckMacValueOfficialVector) anchors the whole algorithm to reality, but
// it exercises one particular set of characters. The sort rule and the encoding
// table have branches that vector does not reach — `~`, `!`, `*`, `(`, `)`, and
// a key pair whose case-insensitive order differs from its ASCII order. Testing
// those against the implementation itself would be a tautology.
//
// So this one is written straight from the documentation, in a different shape:
// the caller states the key order explicitly (so it cannot inherit a sorting
// bug), and the encoder is a hand-rolled byte loop rather than url.QueryEscape
// plus fixups (so it cannot inherit an encoding bug). Where the two agree, they
// agree for two independent reasons.
func referenceCheckMac(hashKey, hashIV string, orderedKeys []string, params map[string]string) string {
	var b strings.Builder
	b.WriteString("HashKey=")
	b.WriteString(hashKey)
	for _, k := range orderedKeys {
		b.WriteString("&")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params[k])
	}
	b.WriteString("&HashIV=")
	b.WriteString(hashIV)

	sum := sha256.Sum256([]byte(strings.ToLower(referenceEncode(b.String()))))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// referenceEncode implements 綠界's URLEncode 轉換表 (/2904/) as a byte loop:
// unreserved-per-.NET characters pass through, space becomes '+', everything
// else becomes %XX.
func referenceEncode(s string) string {
	const passthrough = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.!*()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('+')
		case strings.IndexByte(passthrough, c) >= 0:
			b.WriteByte(c)
		default:
			// Note: '~' is NOT in passthrough — 綠界's table keeps it encoded,
			// which is the one place .NET is stricter than the RFC's unreserved
			// set and the one place Go's url.QueryEscape disagrees.
			b.WriteString("%")
			b.WriteString(hex.EncodeToString([]byte{c}))
		}
	}
	return b.String()
}
