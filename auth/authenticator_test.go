package auth_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
)

var authnSecret = []byte("0123456789abcdef0123456789abcdef")

func incoming(authorization string) context.Context {
	if authorization == "" {
		return context.Background()
	}
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(auth.MetadataKey, authorization))
}

func TestJWTAuthenticator(t *testing.T) {
	v := auth.MustNewVerifier(authnSecret, "gortexa")
	authr := auth.NewJWTAuthenticator(v)
	tok, _ := v.Sign("u-1", []string{"admin"}, time.Hour)

	// Valid token → claims land in the returned context.
	ctx, err := authr.Authenticate(incoming("Bearer " + tok))
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	cl, ok := auth.ClaimsFrom(ctx)
	if !ok || cl.Subject != "u-1" {
		t.Fatalf("claims not injected: ok=%v claims=%+v", ok, cl)
	}

	// Every rejection is Unauthenticated and never a panic.
	for name, authz := range map[string]string{
		"no metadata":  "",
		"empty header": "",
		"not bearer":   "Basic zzz",
		"bad token":    "Bearer not-a-jwt",
	} {
		c := incoming(authz)
		if name == "empty header" {
			c = metadata.NewIncomingContext(context.Background(), metadata.MD{})
		}
		if _, err := authr.Authenticate(c); !apperr.Is(err, apperr.CatUnauthenticated) {
			t.Errorf("%s: err = %v, want Unauthenticated", name, err)
		}
	}
}
