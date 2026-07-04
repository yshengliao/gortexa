package config_test

import (
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/internal/config"
)

// TestEnvSliceDecoding verifies a list-valued key can be set from the
// environment as a comma-separated string (previously it collapsed into a
// single-element slice).
func TestEnvSliceDecoding(t *testing.T) {
	c, err := config.Build(config.WithEnviron(func() []string {
		return []string{
			"GORTEXA_AUTH__JWT_SECRET=" + validSecret,
			"GORTEXA_SERVER__CORS_ORIGINS=https://a.example,https://b.example,https://c.example",
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := c.Server.CORSOrigins
	want := []string{"https://a.example", "https://b.example", "https://c.example"}
	if len(got) != len(want) {
		t.Fatalf("cors_origins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cors_origins[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestBareNumericDurationRejected verifies a bare number for a duration field is
// rejected instead of being silently read as nanoseconds.
func TestBareNumericDurationRejected(t *testing.T) {
	yaml := writeFile(t, "config.yaml", "server:\n  shutdown_timeout: 30\n")
	_, err := config.Build(
		config.WithConfigFile(yaml),
		config.WithEnviron(func() []string { return []string{"GORTEXA_AUTH__JWT_SECRET=" + validSecret} }),
	)
	if err == nil {
		t.Fatal("bare-number duration must be rejected")
	}
	if !strings.Contains(err.Error(), "duration must be a string") {
		t.Fatalf("error = %v, want a duration-format message", err)
	}
}

// TestDevPlaceholderSecretRejected verifies the server refuses the committed dev
// JWT secret (auth-bypass guard).
func TestDevPlaceholderSecretRejected(t *testing.T) {
	c, err := config.Build(config.WithEnviron(func() []string {
		return []string{"GORTEXA_AUTH__JWT_SECRET=dev-only-insecure-secret-change-me-please"}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("the built-in dev placeholder secret must be rejected")
	} else if !strings.Contains(err.Error(), "dev placeholder") {
		t.Fatalf("error = %v, want a placeholder-secret message", err)
	}
}

// TestEmptyIssuerRejected pins that blanking the issuer fails validation, so the
// jwt.WithIssuer check can never be silently disabled by GORTEXA_AUTH__ISSUER=.
func TestEmptyIssuerRejected(t *testing.T) {
	c, err := config.Build(config.WithEnviron(func() []string {
		return []string{
			"GORTEXA_AUTH__JWT_SECRET=" + validSecret,
			"GORTEXA_AUTH__ISSUER=",
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("an empty auth.issuer must be rejected")
	} else if !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("error = %v, want an issuer-required message", err)
	}
}

// TestOTLPSecureByDefault pins that OTLP export uses TLS unless cleartext is
// explicitly opted in, so telemetry is never sent in cleartext by accident.
func TestOTLPSecureByDefault(t *testing.T) {
	c, err := config.Build(config.WithEnviron(func() []string {
		return []string{"GORTEXA_AUTH__JWT_SECRET=" + validSecret}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Observ.OTLPInsecure {
		t.Fatal("observ.otlp_insecure must default to false (TLS)")
	}
}
