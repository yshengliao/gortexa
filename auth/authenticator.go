package auth

import (
	"context"

	"google.golang.org/grpc/metadata"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// Authenticator authenticates an incoming request from its context and returns
// a context carrying the authenticated principal (or an error). It is the seam
// that lets a consumer run the framework's stock interceptor chain with any
// internal auth scheme — JWT, static bearer, mTLS, API key — instead of only
// HS256 JWT. The auth interceptor calls it for every non-skipped method.
//
// Implementations must not leak internal detail in the returned error: return
// an *apperr.Error with Category CatUnauthenticated (its message is the only
// thing forwarded to the caller).
type Authenticator interface {
	Authenticate(ctx context.Context) (context.Context, error)
}

// jwtAuthenticator adapts a *Verifier to the Authenticator interface: it reads
// the bearer token from the incoming gRPC metadata, verifies it, and stores the
// claims in the returned context. This is the exact logic the auth interceptor
// used before Authenticator existed, so JWT is now simply one implementation.
type jwtAuthenticator struct{ v *Verifier }

// NewJWTAuthenticator returns an Authenticator that verifies an HS256 JWT bearer
// token with v. It is the default when interceptor.Config sets Verifier but not
// Authenticator, so existing configurations behave identically.
func NewJWTAuthenticator(v *Verifier) Authenticator {
	return jwtAuthenticator{v: v}
}

func (a jwtAuthenticator) Authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	vals := md.Get(MetadataKey)
	if len(vals) == 0 {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	tok, ok := BearerToken(vals[0])
	if !ok {
		return ctx, apperr.New(apperr.CatUnauthenticated, "malformed authorization")
	}
	claims, err := a.v.Verify(tok)
	if err != nil {
		return ctx, err
	}
	return WithClaims(ctx, claims), nil
}
