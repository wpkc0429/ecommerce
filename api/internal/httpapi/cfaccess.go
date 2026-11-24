package httpapi

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// CFAccessHeader carries the Cloudflare Access application token on every
// request Access proxies.
const CFAccessHeader = "Cf-Access-Jwt-Assertion"

const (
	jwksTTL        = time.Hour
	jwksFetchLimit = 1 << 20
)

type cfEmailKey struct{}

// cfAccessEmail returns the e-mail claim of the validated Access JWT.
func cfAccessEmail(ctx context.Context) (string, bool) {
	e, ok := ctx.Value(cfEmailKey{}).(string)
	return e, ok && e != ""
}

// CFAccessVerifier validates Cloudflare Access JWTs (change
// admin-api-public-exposure, design D2). It exists so that a tunnel ingress
// rule that drifts - the file lives outside the repo, unreviewed and untested -
// cannot silently expose the back-office API without Access in front of it.
//
// Validation is deliberately fail CLOSED: when the signing keys cannot be
// fetched the request is rejected. That is the opposite of the rate limiter's
// fail-open, because this is authentication, not quota.
type CFAccessVerifier struct {
	issuer   string
	auds     []string
	jwksURL  string
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time
	cacheTTL time.Duration
	// minRefetch bounds how often an unknown kid may force a re-download.
	minRefetch time.Duration

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func splitAudiences(raw string) []string {
	var out []string
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// NewCFAccessVerifier builds a verifier for team domain "<team>.cloudflareaccess.com".
func NewCFAccessVerifier(cfg config.CFAccessConfig, log *slog.Logger) *CFAccessVerifier {
	return &CFAccessVerifier{
		issuer:     "https://" + cfg.TeamDomain,
		auds:       splitAudiences(cfg.Audience),
		jwksURL:    "https://" + cfg.TeamDomain + "/cdn-cgi/access/certs",
		client:     &http.Client{Timeout: 5 * time.Second},
		log:        log,
		now:        time.Now,
		cacheTTL:   jwksTTL,
		minRefetch: 10 * time.Second,
	}
}

type jwkSet struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

// refresh downloads the JWKS and replaces the cache. Caller holds v.mu.
func (v *CFAccessVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("cfaccess: build jwks request: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("cfaccess: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cfaccess: jwks status %d", resp.StatusCode)
	}
	var set jwkSet
	if err := json.NewDecoder(io.LimitReader(resp.Body, jwksFetchLimit)).Decode(&set); err != nil {
		return fmt.Errorf("cfaccess: decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(nb) == 0 || len(eb) == 0 {
			continue
		}
		e := new(big.Int).SetBytes(eb)
		if !e.IsInt64() {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}
	}
	if len(keys) == 0 {
		return errors.New("cfaccess: jwks contains no usable RSA key")
	}
	v.keys = keys
	v.fetchedAt = v.now()
	return nil
}

// key returns the public key for kid. The cache lives cacheTTL; an unknown kid
// forces ONE re-fetch (key rotation) unless the cache was fetched moments ago,
// so a stream of garbage kids cannot turn into a stream of outbound requests.
func (v *CFAccessVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	stale := v.keys == nil || v.now().Sub(v.fetchedAt) > v.cacheTTL
	if stale {
		if err := v.refresh(ctx); err != nil {
			return nil, err
		}
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if !stale && v.now().Sub(v.fetchedAt) >= v.minRefetch {
		if err := v.refresh(ctx); err != nil {
			return nil, err
		}
		if k, ok := v.keys[kid]; ok {
			return k, nil
		}
	}
	return nil, errors.New("cfaccess: unknown kid")
}

type cfClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// Verify checks signature (RS256 only), iss, aud and exp and returns the
// token's e-mail claim.
func (v *CFAccessVerifier) Verify(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", errors.New("cfaccess: missing token")
	}
	var claims cfClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("cfaccess: token has no kid")
		}
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil {
		return "", fmt.Errorf("cfaccess: verify: %w", err)
	}
	// aud must contain one of the configured application tags (several Access
	// applications - e.g. shop admin domains and the platform admin domain -
	// may front the same API, each with its own AUD).
	okAud := false
	for _, want := range v.auds {
		for _, got := range claims.Audience {
			if got == want {
				okAud = true
			}
		}
	}
	if !okAud {
		return "", errors.New("cfaccess: audience mismatch")
	}
	return strings.ToLower(strings.TrimSpace(claims.Email)), nil
}

// NewCFAccessMiddleware is mounted on the /api/v1/admin group unconditionally
// (the route table never depends on settings); with v == nil it passes
// everything through. With a verifier, every request needs a valid
// Cf-Access-Jwt-Assertion or gets 403.
func NewCFAccessMiddleware(v *CFAccessVerifier, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if v == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			email, err := v.Verify(r.Context(), r.Header.Get(CFAccessHeader))
			if err != nil {
				if log != nil {
					log.Warn("cf_access_rejected", "path", r.URL.Path, "err", err)
				}
				httpx.Forbidden(w)
				return
			}
			ctx := context.WithValue(r.Context(), cfEmailKey{}, email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NewCFAccessBindEmailMiddleware (CF_ACCESS_BIND_EMAIL, off by default) runs
// after AdminMW: the Access JWT e-mail must equal the signed-in back-office
// account's e-mail, so a stolen access token is useless without the same
// person's Access session. The invitation accept path is exempt: the invitee
// may be signing in with an account whose e-mail differs from the one Access
// verified. The unauthenticated auth routes sit outside AdminMW and are
// therefore never checked.
func NewCFAccessBindEmailMiddleware(client *ent.Client, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == adminPrefix+"/invitations/accept" {
				next.ServeHTTP(w, r)
				return
			}
			id, ok := auth.AdminFrom(r.Context())
			jwtEmail, hasEmail := cfAccessEmail(r.Context())
			if !ok || !hasEmail {
				httpx.Forbidden(w)
				return
			}
			u, err := client.User.Get(r.Context(), id.UserID)
			if err != nil {
				if ent.IsNotFound(err) {
					httpx.Forbidden(w)
					return
				}
				log.Error("cf access bind email: load user", "err", err)
				httpx.Internal(w)
				return
			}
			if !strings.EqualFold(u.Email, jwtEmail) {
				httpx.Forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetJWKSURL overrides where signing keys are fetched from. Production derives
// it from the team domain; integration tests point it at a local server.
func (v *CFAccessVerifier) SetJWKSURL(u string) { v.jwksURL = u }
