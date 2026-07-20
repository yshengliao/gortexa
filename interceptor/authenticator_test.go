package interceptor_test

import (
	"context"
	"crypto/subtle"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/interceptor"
)

// staticBearer is a non-JWT Authenticator: it accepts one fixed opaque token,
// compared in constant time. It shows a consumer can run the stock chain with
// any internal scheme, not only HS256 JWT.
type staticBearer struct{ token string }

func (s staticBearer) Authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	vals := md.Get(auth.MetadataKey)
	if len(vals) == 0 {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	tok, ok := auth.BearerToken(vals[0])
	if !ok {
		return ctx, apperr.New(apperr.CatUnauthenticated, "malformed authorization")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
		return ctx, apperr.New(apperr.CatUnauthenticated, "invalid token")
	}
	return ctx, nil
}

func bearerCtx(tok string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(auth.MetadataKey, "Bearer "+tok))
}

// (a) A custom non-JWT Authenticator set on Config drives the auth stage of the
// stock chain — pass and reject.
func TestConfigAuthenticatorDrivesAuthStage(t *testing.T) {
	set, err := interceptor.NewSet(interceptor.Config{Authenticator: staticBearer{token: "s3cret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	info := unaryInfo("/svc/M")

	if _, err := set.Auth(bearerCtx("s3cret-token"), nil, info, okHandler); err != nil {
		t.Fatalf("valid static bearer should pass: %v", err)
	}
	if _, err := set.Auth(bearerCtx("wrong"), nil, info, okHandler); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("wrong static bearer err = %v, want Unauthenticated", err)
	}
	if _, err := set.Auth(context.Background(), nil, info, okHandler); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("missing metadata err = %v, want Unauthenticated", err)
	}
}

// Authenticator takes precedence over Verifier when both are set: a valid JWT is
// rejected because the static authenticator doesn't accept it.
func TestConfigAuthenticatorOverridesVerifier(t *testing.T) {
	v := auth.MustNewVerifier(jwtSecret, "gortexa")
	tok, _ := v.Sign("u-1", nil, time.Hour)
	set, err := interceptor.NewSet(interceptor.Config{
		Authenticator: staticBearer{token: "s3cret-token"},
		Verifier:      v, // present but ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	info := unaryInfo("/svc/M")
	if _, err := set.Auth(bearerCtx(tok), nil, info, okHandler); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("JWT under static authenticator err = %v, want Unauthenticated (Authenticator must win)", err)
	}
	if _, err := set.Auth(bearerCtx("s3cret-token"), nil, info, okHandler); err != nil {
		t.Fatalf("static token should pass: %v", err)
	}
}

// (c) Verifier-only config still authenticates a JWT — the pre-Authenticator
// path is unchanged (NewSet builds a JWT authenticator from Verifier).
func TestNewSetFallsBackToVerifier(t *testing.T) {
	v := auth.MustNewVerifier(jwtSecret, "gortexa")
	set, err := interceptor.NewSet(interceptor.Config{Verifier: v})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := v.Sign("u-9", []string{"admin"}, time.Hour)
	info := unaryInfo("/svc/M")
	var gotSub string
	if _, err := set.Auth(bearerCtx(tok), nil, info, func(c context.Context, _ any) (any, error) {
		if cl, ok := auth.ClaimsFrom(c); ok {
			gotSub = cl.Subject
		}
		return "ok", nil
	}); err != nil || gotSub != "u-9" {
		t.Fatalf("verifier-only JWT: sub=%q err=%v", gotSub, err)
	}
}

// Neither Authenticator nor Verifier is a fail-loud startup error, not a
// request-time nil panic.
func TestNewSetRequiresAuth(t *testing.T) {
	if _, err := interceptor.NewSet(interceptor.Config{}); err == nil {
		t.Fatal("NewSet with no Authenticator/Verifier must return an error")
	}
}
