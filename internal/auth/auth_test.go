package auth_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestSignVerifyRoundTrip(t *testing.T) {
	v := auth.NewVerifier(secret, "gortexa")
	tok, err := v.Sign("user-1", []string{"admin"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	v := auth.NewVerifier(secret, "gortexa")
	tok, _ := v.Sign("u", nil, time.Hour)
	if _, err := v.Verify(tok + "x"); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("tampered token err = %v, want Unauthenticated", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	signer := auth.NewVerifier(secret, "gortexa")
	tok, _ := signer.Sign("u", nil, time.Hour)
	other := auth.NewVerifier([]byte("ffffffffffffffffffffffffffffffff"), "gortexa")
	if _, err := other.Verify(tok); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("wrong-secret err = %v, want Unauthenticated", err)
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	signer := auth.NewVerifier(secret, "evil")
	tok, _ := signer.Sign("u", nil, time.Hour)
	v := auth.NewVerifier(secret, "gortexa")
	if _, err := v.Verify(tok); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("wrong-issuer err = %v, want Unauthenticated", err)
	}
}

// TTL expiry is verified deterministically with a fake clock.
func TestVerifyExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := auth.NewVerifier(secret, "gortexa")
		tok, err := v.Sign("u", nil, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(tok); err != nil {
			t.Fatalf("token should be valid immediately: %v", err)
		}
		time.Sleep(2 * time.Hour) // advance the fake clock past expiry
		if _, err := v.Verify(tok); !apperr.Is(err, apperr.CatUnauthenticated) {
			t.Fatalf("expired token err = %v, want Unauthenticated", err)
		}
	})
}

func TestBearerTokenAndContext(t *testing.T) {
	if tok, ok := auth.BearerToken("Bearer abc.def.ghi"); !ok || tok != "abc.def.ghi" {
		t.Fatalf("BearerToken = %q, %v", tok, ok)
	}
	if _, ok := auth.BearerToken("Basic xyz"); ok {
		t.Fatal("BearerToken should reject non-bearer")
	}
	ctx := auth.WithClaims(context.Background(), &auth.Claims{})
	if _, ok := auth.ClaimsFrom(ctx); !ok {
		t.Fatal("ClaimsFrom should find stored claims")
	}
}
