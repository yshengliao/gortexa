package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
)

const validSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestThreeLayerPrecedence(t *testing.T) {
	// default server.addr = ":8080"; YAML overrides; env overrides YAML.
	yamlFile := writeFile(t, "config.yaml", "server:\n  addr: \":9000\"\n  shutdown_timeout: 30s\nlog:\n  level: warn\n")
	environ := func() []string {
		return []string{
			"GORTEXA_SERVER__ADDR=:7000",
			"GORTEXA_AUTH__JWT_SECRET=" + validSecret,
		}
	}
	c, err := config.Build(config.WithConfigFile(yamlFile), config.WithEnviron(environ))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Addr != ":7000" { // env wins over yaml(:9000) over default(:8080)
		t.Errorf("addr = %q, want :7000 (env wins)", c.Server.Addr)
	}
	if c.Server.ShutdownTimeout != 30*time.Second { // from yaml, duration parsed
		t.Errorf("shutdown_timeout = %v, want 30s", c.Server.ShutdownTimeout)
	}
	if c.Log.Level != "warn" { // yaml over default(info)
		t.Errorf("log.level = %q, want warn", c.Log.Level)
	}
	if c.Server.OpenAPI != true { // untouched default
		t.Errorf("openapi default not applied")
	}
	if c.Auth.JWTSecret.Reveal() != validSecret {
		t.Errorf("jwt secret not loaded from env")
	}
}

func TestMustBuildMissingRequiredPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on missing jwt secret")
		}
	}()
	config.MustBuild(config.WithEnviron(func() []string { return nil }))
}

func TestMustBuildShortSecretPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on short jwt secret")
		}
	}()
	config.MustBuild(config.WithEnviron(func() []string {
		return []string{"GORTEXA_AUTH__JWT_SECRET=tooshort"}
	}))
}

func TestMustBuildValid(t *testing.T) {
	c := config.MustBuild(config.WithEnviron(func() []string {
		return []string{"GORTEXA_AUTH__JWT_SECRET=" + validSecret}
	}))
	if c.Server.Addr != ":8080" {
		t.Errorf("default addr not applied: %q", c.Server.Addr)
	}
}

func TestSecretMasking(t *testing.T) {
	s := config.Secret(validSecret)
	if s.String() != "****" {
		t.Errorf("String() = %q, want ****", s.String())
	}
	if config.Secret("").String() != "" {
		t.Errorf("empty secret should mask to empty")
	}
	b, err := json.Marshal(struct{ S config.Secret }{S: s})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"S":"****"}` {
		t.Errorf("json = %s, want masked", b)
	}
	if s.Reveal() != validSecret {
		t.Errorf("Reveal should return raw value")
	}
}
