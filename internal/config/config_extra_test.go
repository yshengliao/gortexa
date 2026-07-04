package config_test

import (
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
)

func TestDotEnvLayerAndCustomPrefix(t *testing.T) {
	envFile := writeFile(t, ".env", "APP_SERVER__ADDR=:6000\nAPP_AUTH__JWT_SECRET="+validSecret+"\nAPP_CACHE__TTL=30s\n")
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
	if c.Cache.TTL != 30*time.Second {
		t.Errorf("cache ttl = %v", c.Cache.TTL)
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
