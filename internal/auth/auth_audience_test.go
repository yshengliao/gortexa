package auth_test

import (
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/auth"
)

var audSecret = []byte("test-secret-0123456789abcdef-0123456789")

// TestAudienceIsolation pins that a configured audience isolates services
// sharing a secret and issuer: tokens minted for one audience are rejected by
// a verifier expecting another, and by-audience verification round-trips.
func TestAudienceIsolation(t *testing.T) {
	svcA := auth.MustNewVerifier(audSecret, "gortexa", "service-a")
	svcB := auth.MustNewVerifier(audSecret, "gortexa", "service-b")

	tok, err := svcA.Sign("u1", []string{"admin"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.Verify(tok); err != nil {
		t.Fatalf("same-audience verify: %v", err)
	}
	if _, err := svcB.Verify(tok); err == nil {
		t.Fatal("service B accepted a token minted for service A's audience")
	}
}

// TestAudienceUnsetKeepsLegacyBehaviour pins backwards compatibility: with no
// audience configured, tokens carry no aud claim and verification does not
// require one.
func TestAudienceUnsetKeepsLegacyBehaviour(t *testing.T) {
	v := auth.MustNewVerifier(audSecret, "gortexa")
	tok, err := v.Sign("u1", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("legacy verify: %v", err)
	}
	if len(claims.Audience) != 0 {
		t.Fatalf("unexpected aud claim without configured audience: %v", claims.Audience)
	}

	// A verifier WITH an audience must reject a legacy token lacking aud.
	strict := auth.MustNewVerifier(audSecret, "gortexa", "service-a")
	if _, err := strict.Verify(tok); err == nil {
		t.Fatal("audience-requiring verifier accepted a token without aud")
	}
}
