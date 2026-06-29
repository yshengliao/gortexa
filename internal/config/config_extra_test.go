package config_test

import (
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
)

func TestDotEnvLayerAndCustomPrefix(t *testing.T) {
	envFile := writeFile(t, ".env", "APP_SERVER__ADDR=:6000\nAPP_AUTH__JWT_SECRET="+validSecret+"\nAPP_CACHE__TTL=30s\n")
	c, err := config.Build(
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
	if _, err := config.Build(config.WithConfigFile("/no/such/file.yaml")); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestValidateDirect(t *testing.T) {
	c := &config.Config{}
	if err := c.Validate(); err == nil {
		t.Fatal("empty config should fail validation")
	}
}

func TestBuildMissingDotEnvFile(t *testing.T) {
	if _, err := config.Build(config.WithDotEnvFile("/no/such/file.env")); err == nil {
		t.Fatal("expected error for missing dotenv file")
	}
}

func TestBuildUnmarshalError(t *testing.T) {
	// Provide invalid type for db.max_conns to trigger unmarshal error.
	environ := func() []string {
		return []string{
			"GORTEXA_DB__MAX_CONNS=not-an-int",
		}
	}
	if _, err := config.Build(config.WithEnviron(environ)); err == nil {
		t.Fatal("expected error for unmarshal type mismatch")
	}
}

func TestMustBuildBuildErrorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on Build error")
		}
	}()
	config.MustBuild(config.WithConfigFile("/no/such/file.yaml"))
}

func TestFourLayerPrecedence(t *testing.T) {
	// Precedence: built-in defaults < YAML file < .env file < process environment
	yamlFile := writeFile(t, "config2.yaml", "server:\n  addr: \":9000\"\n  shutdown_timeout: 30s\nlog:\n  level: warn\n")
	envFile := writeFile(t, ".env2", "GORTEXA_SERVER__ADDR=:8000\nGORTEXA_LOG__LEVEL=error\nGORTEXA_CACHE__TTL=10m\n")
	environ := func() []string {
		return []string{
			"GORTEXA_SERVER__ADDR=:7000",
			"GORTEXA_AUTH__JWT_SECRET=" + validSecret,
		}
	}
	c, err := config.Build(config.WithConfigFile(yamlFile), config.WithDotEnvFile(envFile), config.WithEnviron(environ))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Addr != ":7000" { // env wins over dotenv(:8000) over yaml(:9000) over default(:8080)
		t.Errorf("addr = %q, want :7000 (env wins)", c.Server.Addr)
	}
	if c.Log.Level != "error" { // dotenv wins over yaml(warn) over default(info)
		t.Errorf("log.level = %q, want error (dotenv wins over yaml)", c.Log.Level)
	}
	if c.Server.ShutdownTimeout != 30*time.Second { // from yaml, duration parsed
		t.Errorf("shutdown_timeout = %v, want 30s", c.Server.ShutdownTimeout)
	}
	if c.Cache.TTL != 10*time.Minute { // from dotenv
		t.Errorf("cache.ttl = %v, want 10m", c.Cache.TTL)
	}
}
