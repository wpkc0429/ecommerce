package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ksdevworks/ecommerce/api/internal/config"
)

const (
	cfTeam = "team-example.cloudflareaccess.com"
	cfAud  = "aud-tag-for-tests"
)

type jwkEntry struct {
	kid string
	pub *rsa.PublicKey
}

type cfFixture struct {
	key     *rsa.PrivateKey
	other   *rsa.PrivateKey
	srv     *httptest.Server
	status  atomic.Int32
	fetches atomic.Int32
	keys    atomic.Value // []jwkEntry
}

func newCFFixture(t *testing.T) *cfFixture {
	t.Helper()
	f := &cfFixture{}
	var err error
	if f.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if f.other, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	f.keys.Store([]jwkEntry{{"kid-1", &f.key.PublicKey}})
	f.status.Store(200)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.fetches.Add(1)
		if s := int(f.status.Load()); s != 200 {
			w.WriteHeader(s)
			return
		}
		out := struct {
			Keys []map[string]string `json:"keys"`
		}{}
		for _, k := range f.keys.Load().([]jwkEntry) {
			out.Keys = append(out.Keys, map[string]string{
				"kid": k.kid, "kty": "RSA",
				"n": base64.RawURLEncoding.EncodeToString(k.pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.pub.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *cfFixture) verifier() *CFAccessVerifier {
	v := NewCFAccessVerifier(config.CFAccessConfig{TeamDomain: cfTeam, Audience: cfAud}, slog.New(slog.DiscardHandler))
	v.jwksURL = f.srv.URL
	v.client = f.srv.Client()
	v.minRefetch = 0
	return v
}

func (f *cfFixture) sign(t *testing.T, key *rsa.PrivateKey, kid, iss string, aud []string, exp time.Duration, alg jwt.SigningMethod) string {
	t.Helper()
	claims := cfClaims{Email: "Someone@Example.com", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: iss, Audience: aud, ExpiresAt: jwt.NewNumericDate(time.Now().Add(exp)),
	}}
	tok := jwt.NewWithClaims(alg, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCFAccessVerify(t *testing.T) {
	f := newCFFixture(t)
	iss := "https://" + cfTeam
	v := f.verifier()
	good := f.sign(t, f.key, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256)
	email, err := v.Verify(context.Background(), good)
	if err != nil || email != "someone@example.com" {
		t.Fatalf("valid token: %q %v", email, err)
	}

	bad := map[string]string{
		"empty":       "",
		"wrong aud":   f.sign(t, f.key, "kid-1", iss, []string{"other"}, time.Hour, jwt.SigningMethodRS256),
		"wrong iss":   f.sign(t, f.key, "kid-1", "https://evil.cloudflareaccess.com", []string{cfAud}, time.Hour, jwt.SigningMethodRS256),
		"expired":     f.sign(t, f.key, "kid-1", iss, []string{cfAud}, -time.Minute, jwt.SigningMethodRS256),
		"wrong key":   f.sign(t, f.other, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256),
		"unknown kid": f.sign(t, f.key, "kid-zzz", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256),
		"rs512":       f.sign(t, f.key, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS512),
		"garbage":     "not.a.jwt",
	}
	for name, tok := range bad {
		if _, err := v.Verify(context.Background(), tok); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, cfClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: iss, Audience: []string{cfAud}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}})
	none.Header["kid"] = "kid-1"
	s, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := v.Verify(context.Background(), s); err == nil {
		t.Error("alg none accepted")
	}
}

func TestCFAccessKeyRotationAndJWKSFailure(t *testing.T) {
	f := newCFFixture(t)
	iss := "https://" + cfTeam
	v := f.verifier()
	if _, err := v.Verify(context.Background(), f.sign(t, f.key, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256)); err != nil {
		t.Fatal(err)
	}
	// Rotation: a new kid appears; the unknown kid triggers a re-fetch.
	f.keys.Store([]jwkEntry{{"kid-1", &f.key.PublicKey}, {"kid-2", &f.other.PublicKey}})
	if _, err := v.Verify(context.Background(), f.sign(t, f.other, "kid-2", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256)); err != nil {
		t.Fatalf("rotated key must be picked up: %v", err)
	}
	// A known kid is served from the cache.
	before := f.fetches.Load()
	if _, err := v.Verify(context.Background(), f.sign(t, f.key, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256)); err != nil {
		t.Fatal(err)
	}
	if f.fetches.Load() != before {
		t.Fatal("known kid must be served from cache")
	}

	// JWKS 500 with an empty cache: fail closed.
	f2 := newCFFixture(t)
	f2.status.Store(500)
	v2 := f2.verifier()
	if _, err := v2.Verify(context.Background(), f2.sign(t, f2.key, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256)); err == nil {
		t.Fatal("JWKS failure must fail closed")
	}
}

func TestCFAccessMiddleware(t *testing.T) {
	f := newCFFixture(t)
	iss := "https://" + cfTeam
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	do := func(mw func(http.Handler) http.Handler, hdr string) int {
		req := httptest.NewRequest("GET", "/api/v1/admin/me", nil)
		if hdr != "" {
			req.Header.Set(CFAccessHeader, hdr)
		}
		rec := httptest.NewRecorder()
		mw(ok).ServeHTTP(rec, req)
		return rec.Code
	}
	on := NewCFAccessMiddleware(f.verifier(), nil)
	if c := do(on, ""); c != http.StatusForbidden {
		t.Errorf("no header: %d", c)
	}
	if c := do(on, "junk"); c != http.StatusForbidden {
		t.Errorf("junk header: %d", c)
	}
	if c := do(on, f.sign(t, f.key, "kid-1", iss, []string{cfAud}, time.Hour, jwt.SigningMethodRS256)); c != http.StatusNoContent {
		t.Errorf("valid: %d", c)
	}
	off := NewCFAccessMiddleware(nil, nil)
	if c := do(off, ""); c != http.StatusNoContent {
		t.Errorf("unconfigured must pass through: %d", c)
	}
}

func TestCFAccessMultipleAudiences(t *testing.T) {
	f := newCFFixture(t)
	iss := "https://" + cfTeam
	v := f.verifier()
	v.auds = splitAudiences(" first-app , second-app ")
	for _, aud := range []string{"first-app", "second-app"} {
		if _, err := v.Verify(context.Background(), f.sign(t, f.key, "kid-1", iss, []string{aud}, time.Hour, jwt.SigningMethodRS256)); err != nil {
			t.Errorf("aud %s: %v", aud, err)
		}
	}
	if _, err := v.Verify(context.Background(), f.sign(t, f.key, "kid-1", iss, []string{"third"}, time.Hour, jwt.SigningMethodRS256)); err == nil {
		t.Error("an unlisted audience must be rejected")
	}
}
