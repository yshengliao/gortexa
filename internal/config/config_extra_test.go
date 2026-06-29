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
	tests := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name: "valid config",
			config: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Auth:   config.AuthConfig{JWTSecret: validSecret},
			},
			wantErr: "",
		},
		{
			name: "missing server addr",
			config: config.Config{
				Server: config.ServerConfig{Addr: ""},
				Auth:   config.AuthConfig{JWTSecret: validSecret},
			},
			wantErr: "invalid config: server.addr is required",
		},
		{
			name: "missing jwt secret",
			config: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Auth:   config.AuthConfig{JWTSecret: ""},
			},
			wantErr: "invalid config: auth.jwt_secret is required",
		},
		{
			name: "short jwt secret",
			config: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Auth:   config.AuthConfig{JWTSecret: "short"},
			},
			wantErr: "invalid config: auth.jwt_secret must be at least 32 bytes",
		},
		{
			name: "multiple errors",
			config: config.Config{
				Server: config.ServerConfig{Addr: ""},
				Auth:   config.AuthConfig{JWTSecret: "short"},
			},
			wantErr: "invalid config: server.addr is required; auth.jwt_secret must be at least 32 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error %q, got nil", tt.wantErr)
				} else if err.Error() != tt.wantErr {
					t.Errorf("expected error %q, got %q", tt.wantErr, err.Error())
				}
			}
		})
	}
}
