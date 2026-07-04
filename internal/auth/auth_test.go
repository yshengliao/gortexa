package auth_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestSignVerifyRoundTrip(t *testing.T) {
	v := auth.MustNewVerifier(secret, "gortexa")
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
	v := auth.MustNewVerifier(secret, "gortexa")
	tok, _ := v.Sign("u", nil, time.Hour)
	if _, err := v.Verify(tok + "x"); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("tampered token err = %v, want Unauthenticated", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	signer := auth.MustNewVerifier(secret, "gortexa")
	tok, _ := signer.Sign("u", nil, time.Hour)
	other := auth.MustNewVerifier([]byte("ffffffffffffffffffffffffffffffff"), "gortexa")
	if _, err := other.Verify(tok); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("wrong-secret err = %v, want Unauthenticated", err)
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	signer := auth.MustNewVerifier(secret, "evil")
	tok, _ := signer.Sign("u", nil, time.Hour)
	v := auth.MustNewVerifier(secret, "gortexa")
	if _, err := v.Verify(tok); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("wrong-issuer err = %v, want Unauthenticated", err)
	}
}

// TTL expiry is verified deterministically with a fake clock.
func TestVerifyExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := auth.MustNewVerifier(secret, "gortexa")
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

// TestVerifyClockSkewLeeway pins the clock-skew allowance: a token that expired
// within the leeway window is still accepted (a verifier whose clock runs a
// little ahead must not reject a peer's freshly-valid token), but one expired
// well beyond it is rejected.
func TestVerifyClockSkewLeeway(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := auth.MustNewVerifier(secret, "gortexa")
		tok, err := v.Sign("u", nil, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// 90s > 60s TTL but within the 60s leeway of the exp instant.
		time.Sleep(90 * time.Second)
		if _, err := v.Verify(tok); err != nil {
			t.Fatalf("token expired within leeway should still verify: %v", err)
		}
		// Past exp + leeway: must be rejected.
		time.Sleep(60 * time.Second)
		if _, err := v.Verify(tok); !apperr.Is(err, apperr.CatUnauthenticated) {
			t.Fatalf("token past leeway err = %v, want Unauthenticated", err)
		}
	})
}

// A token without an exp claim must be rejected: jwt/v5 otherwise treats a
// missing expiry as "never expires", so WithExpirationRequired closes that gap.
func TestVerifyRejectsTokenWithoutExpiry(t *testing.T) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u",
		"iss": "gortexa",
	})
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	v := auth.MustNewVerifier(secret, "gortexa")
	if _, err := v.Verify(signed); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("no-exp token err = %v, want Unauthenticated", err)
	}
}

func TestNewVerifierRejectsShortSecret(t *testing.T) {
	if _, err := auth.NewVerifier([]byte("too-short"), "gortexa"); err == nil {
		t.Fatal("NewVerifier should reject a secret shorter than 32 bytes")
	}
}

// NewVerifier must copy the secret so a caller mutating the slice afterwards
// can't change the verifier's key.
func TestNewVerifierCopiesSecret(t *testing.T) {
	s := append([]byte(nil), secret...)
	v, err := auth.NewVerifier(s, "gortexa")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := v.Sign("u", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := range s {
		s[i] ^= 0xff // scribble over the caller's slice
	}
	if _, err := v.Verify(tok); err != nil {
		t.Fatalf("verifier must keep its own secret copy: %v", err)
	}
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
