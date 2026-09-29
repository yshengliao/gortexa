// Package auth implements JWT verification (HS256 with a shared secret, or
// RS256/ES256 against a public key set) and HS256 signing, plus context helpers
// for propagating verified claims. The auth interceptor and the HTTP
// gateway both flow through this single verifier, so HTTP and gRPC share one
// authentication path.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// ErrExpiredToken tags an authentication failure caused by an expired token. It
// is carried as the (never-serialized) cause of the Unauthenticated error so the
// auth interceptor can attribute the denial reason without changing the generic
// client-facing message.
var ErrExpiredToken = errors.New("token expired")

// minSecretBytes is the minimum HS256 secret length. Config validation enforces
// the same bound at startup; NewVerifier re-checks it so the verifier can never
// be built with a weak key regardless of how it's constructed.
const minSecretBytes = 32

// clockSkewLeeway absorbs small clock differences between the host that signs a
// token and the host that verifies it (a Verifier commonly validates tokens
// minted by another instance sharing the secret). Without it, jwt/v5 uses zero
// leeway and rejects a still-valid token right at its exp boundary on a
// slightly-behind verifier. 60s is the conventional allowance — small enough
// that a stolen expired token's usable window barely widens.
const clockSkewLeeway = 60 * time.Second

// MetadataKey is the gRPC metadata / HTTP header carrying the bearer token.
const MetadataKey = "authorization"

// Claims is Gortexa's JWT payload.
type Claims struct {
	Roles []string `json:"roles,omitempty"`
	jwt.RegisteredClaims
}

// Verifier verifies tokens for a fixed issuer, and optionally a fixed audience.
// A secret-based Verifier (NewVerifier) signs and verifies HS256; a key-set
// Verifier (NewKeySetVerifier) only verifies RS256/ES256, since it holds no
// private key.
type Verifier struct {
	secret   []byte
	keys     *KeySet
	issuer   string
	audience string
}

// NewVerifier builds a Verifier with its own copy of the HS256 secret (so a
// caller mutating the passed slice can't change the key), rejecting secrets
// shorter than minSecretBytes. An optional audience isolates services that
// share a secret and issuer: when set, Sign stamps it into `aud` and Verify
// requires it, so a token minted for service A is rejected by service B.
// Without it, issuer alone provides no cross-service isolation.
func NewVerifier(secret []byte, issuer string, audience ...string) (*Verifier, error) {
	if len(secret) < minSecretBytes {
		return nil, fmt.Errorf("auth: verifier secret must be at least %d bytes", minSecretBytes)
	}
	v := &Verifier{secret: append([]byte(nil), secret...), issuer: issuer}
	if len(audience) > 0 {
		v.audience = audience[0]
	}
	return v, nil
}

// NewKeySetVerifier builds a verification-only Verifier that accepts RS256 and
// ES256 tokens signed by a key in keys (see NewJWKS, NewStaticKeySet). Services
// holding only public keys cannot mint tokens, so one compromised verifier
// cannot forge tokens for the others, which a shared HS256 secret allows.
func NewKeySetVerifier(keys *KeySet, issuer string, audience ...string) (*Verifier, error) {
	if keys == nil {
		return nil, errors.New("auth: key set is nil")
	}
	v := &Verifier{keys: keys, issuer: issuer}
	if len(audience) > 0 {
		v.audience = audience[0]
	}
	return v, nil
}

// MustNewVerifier builds a Verifier or panics. Intended for startup and test
// setup where construction failure should be fatal (fail-loud).
func MustNewVerifier(secret []byte, issuer string, audience ...string) *Verifier {
	v, err := NewVerifier(secret, issuer, audience...)
	if err != nil {
		panic(err)
	}
	return v
}

// Sign issues a token for subject with the given roles and TTL. ttl must be
// positive: Verify's clock-skew leeway would otherwise still accept a token
// minted already expired (e.g. from an unset TTL setting).
func (v *Verifier) Sign(subject string, roles []string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", apperr.New(apperr.CatInternal, "sign token: ttl must be positive")
	}
	if v.secret == nil {
		return "", apperr.New(apperr.CatInternal, "sign token: verifier holds no signing key")
	}
	now := time.Now()
	claims := Claims{
		Roles:     roles,
		Subject:   subject,
		Issuer:    v.issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}
	if v.audience != "" {
		claims.Audience = jwt.ClaimStrings{v.audience}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(v.secret)
	if err != nil {
		return "", apperr.Wrap(apperr.CatInternal, "sign token", err)
	}
	return signed, nil
}

// Verify parses and validates a token, returning its claims. All failures map
// to Unauthenticated with no internal detail leaked.
func (v *Verifier) Verify(tokenStr string) (*Claims, error) {
	var claims Claims
	// WithExpirationRequired rejects tokens that omit `exp` (jwt/v5 otherwise
	// treats a missing expiry as "never expires"). Every token Sign issues sets
	// exp, so this only closes the door on forged/non-expiring tokens.
	methods := []string{"HS256"}
	if v.keys != nil {
		methods = []string{"RS256", "ES256"}
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(methods),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(clockSkewLeeway),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	tok, err := jwt.ParseWithClaims(tokenStr, &claims, v.keyFunc, opts...)
	if err != nil || !tok.Valid {
		// Keep the client message generic, but tag an expired token (as the
		// non-serialized cause) so the interceptor can report reason="expired".
		if err != nil && errors.Is(err, jwt.ErrTokenExpired) {
			return nil, apperr.Wrap(apperr.CatUnauthenticated, "invalid or expired token", ErrExpiredToken)
		}
		return nil, apperr.New(apperr.CatUnauthenticated, "invalid or expired token")
	}
	return &claims, nil
}

// keyFunc returns the key for a token whose alg WithValidMethods already
// allowed. For a key set, the kid's key must also match the alg's key type, so
// an RSA key is never fed to the ECDSA check or the reverse.
func (v *Verifier) keyFunc(t *jwt.Token) (any, error) {
	if v.keys == nil {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperr.New(apperr.CatUnauthenticated, "unexpected signing method")
		}
		return v.secret, nil
	}
	kid, _ := t.Header["kid"].(string)
	k, ok := v.keys.key(context.Background(), kid)
	if !ok {
		return nil, apperr.New(apperr.CatUnauthenticated, "unknown signing key")
	}
	switch k.(type) {
	case *rsa.PublicKey:
		if _, ok := t.Method.(*jwt.SigningMethodRSA); ok {
			return k, nil
		}
	case *ecdsa.PublicKey:
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); ok {
			return k, nil
		}
	}
	return nil, apperr.New(apperr.CatUnauthenticated, "signing method does not match key")
}

// BearerToken extracts the token from an "Authorization: Bearer <jwt>" value.
func BearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):]), true
	}
	return "", false
}

type ctxKey struct{}

// WithClaims stores verified claims in the context.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// ClaimsFrom retrieves verified claims from the context.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}
