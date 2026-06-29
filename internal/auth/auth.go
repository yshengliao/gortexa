// Package auth implements HS256 JWT verification and signing plus context
// helpers for propagating verified claims. The auth interceptor and the HTTP
// gateway both flow through this single verifier, so HTTP and gRPC share one
// authentication path.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// MetadataKey is the gRPC metadata / HTTP header carrying the bearer token.
const MetadataKey = "authorization"

// Claims is Gortexa's JWT payload.
type Claims struct {
	Roles []string `json:"roles,omitempty"`
	jwt.RegisteredClaims
}

// Verifier signs and verifies HS256 tokens for a fixed secret and issuer.
type Verifier struct {
	secret []byte
	issuer string
}

// NewVerifier builds a Verifier. The secret length policy (>= 32 bytes) is
// enforced by config validation at startup.
func NewVerifier(secret []byte, issuer string) *Verifier {
	return &Verifier{secret: secret, issuer: issuer}
}

// Sign issues a token for subject with the given roles and TTL.
func (v *Verifier) Sign(subject string, roles []string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Roles: roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    v.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
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
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}
	tok, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperr.New(apperr.CatUnauthenticated, "unexpected signing method")
		}
		return v.secret, nil
	}, opts...)
	if err != nil || !tok.Valid {
		return nil, apperr.New(apperr.CatUnauthenticated, "invalid or expired token")
	}
	return &claims, nil
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
