package config_test

import (
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/config"
)

func TestBuildErrorPaths(t *testing.T) {
	cases := []struct {
		name string
		opts []config.Option
	}{
		{
			name: "missing dotenv file",
			opts: []config.Option{config.WithDotEnvFile("/no/such/dir/.env")},
		},
		{
			name: "unmarshal failure on bad duration",
			opts: []config.Option{config.WithEnviron(func() []string {
				return []string{"GORTEXA_CACHE__DIAL_TIMEOUT=not-a-duration"}
			})},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := config.BuildUnvalidated(tc.opts...); err == nil {
				t.Fatal("expected Build error")
			}
		})
	}
}

func TestMustBuildBuildErrorPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Build failure")
		}
		if s, ok := r.(string); !ok || !strings.HasPrefix(s, "config: ") {
			t.Fatalf("panic value = %v, want config-prefixed string", r)
		}
	}()
	config.MustBuild(config.WithConfigFile("/no/such/file.yaml"))
}
