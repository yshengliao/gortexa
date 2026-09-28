package config_test

import (
	"testing"

	"github.com/yshengliao/gortexa/config"
)

func TestDotEnvLayerAndCustomPrefix(t *testing.T) {
	envFile := writeFile(t, ".env", "APP_SERVER__ADDR=:6000\nAPP_AUTH__JWT_SECRET="+validSecret+"\nAPP_CACHE__DB=7\n")
	c, err := config.BuildUnvalidated(
		config.WithEnvPrefix("APP_"),
		config.WithDotEnvFile(envFile),
		config.WithEnviron(func() []string { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Addr != ":6000" {
		t.Errorf("addr from .env = %q", c.Server.Addr)
	}
	if c.Cache.DB != 7 {
		t.Errorf("cache db from .env = %d", c.Cache.DB)
	}
	if c.Auth.JWTSecret.Reveal() != validSecret {
		t.Errorf("secret from .env not loaded")
	}
}

func TestBuildMissingConfigFile(t *testing.T) {
	if _, err := config.BuildUnvalidated(config.WithConfigFile("/no/such/file.yaml")); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestValidateDirect(t *testing.T) {
	c := &config.Config{}
	if err := c.Validate(); err == nil {
		t.Fatal("empty config should fail validation")
	}
}

func TestValidateJWKSReplacesSecret(t *testing.T) {
	c := &config.Config{}
	c.Server.Addr = ":8080"
	c.Auth.Issuer = "gortexa"
	c.Auth.JWKSURL = "https://issuer.example/jwks.json"
	if err := c.Validate(); err != nil {
		t.Fatalf("jwks_url without jwt_secret should validate: %v", err)
	}
	c.Auth.JWKSURL = ""
	if err := c.Validate(); err == nil {
		t.Fatal("neither jwks_url nor jwt_secret should fail validation")
	}
}
