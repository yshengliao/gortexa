package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/config"
)

const canaryFuzz = "CANARY-JWT-8f2a"

const sampleConfigYAML = `server:
  addr: ":8080"
  shutdown_timeout: 20s
  enable_cors: true
  openapi: true
  reflection: false

auth:
  jwt_secret: "dev-only-insecure-secret-change-me-please"
  issuer: "gortexa"
  ttl: 1h

cache:
  driver: "memory"

mq:
  driver: "nats"
  url: ""
  group_id: ""

log:
  level: "info"
  format: "json"

observ:
  service_name: "gortexa"
  otlp_insecure: true
  sample_ratio: 1.0
`

func FuzzBuildUnvalidated(f *testing.F) {
	seeds := []string{
		sampleConfigYAML,
		"server:\n  shutdown_timeout: 30\n",
		"auth:\n  jwt_secret: [a, b]\n",
		"a:\n  b:\n    c:\n      d:\n        e:\n          f: 1\n",
		"auth:\n  jwt_secret: " + canaryFuzz + "\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write temp config: %v", err)
		}
		cfg, err := config.BuildUnvalidated(
			config.WithConfigFile(path),
			config.WithEnviron(func() []string { return nil }),
		)
		if err != nil {
			return
		}
		rendered := fmt.Sprintf("%+v %#v %v", cfg, cfg, cfg.Auth.JWTSecret)
		if strings.Contains(rendered, canaryFuzz) {
			t.Fatalf("canary leaked through rendering: %s", rendered)
		}
	})
}
