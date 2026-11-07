package config_test

import (
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/config"
)

// TestYAMLIntOverflowRejected pins that a YAML integer outside the target
// field's range fails loading instead of being truncated. mapstructure's
// int→int32 conversion has no range check, so `db.max_conns: 4294967306`
// silently became 10 and 4294967296 became 0 (the pgx default), a
// misconfiguration that ran degraded with no error. Env and dotenv values are
// strings and already fail on overflow; only the YAML path was unchecked.
func TestYAMLIntOverflowRejected(t *testing.T) {
	for _, v := range []string{"4294967306", "4294967296", "-2147483649"} {
		f := writeFile(t, "config.yaml", "db:\n  max_conns: "+v+"\n")
		_, err := config.BuildUnvalidated(config.WithConfigFile(f), config.WithEnviron(func() []string {
			return []string{"GORTEXA_AUTH__JWT_SECRET=" + validSecret}
		}))
		if err == nil {
			t.Fatalf("max_conns: %s accepted (truncated to int32)", v)
		}
		if !strings.Contains(err.Error(), "max_conns") {
			t.Fatalf("error must name the field: %v", err)
		}
	}
	// In-range values still load.
	f := writeFile(t, "config.yaml", "db:\n  max_conns: 25\n")
	cfg, err := config.BuildUnvalidated(config.WithConfigFile(f), config.WithEnviron(func() []string {
		return []string{"GORTEXA_AUTH__JWT_SECRET=" + validSecret}
	}))
	if err != nil || cfg.DB.MaxConns != 25 {
		t.Fatalf("max_conns: 25 → %v, %v", cfg, err)
	}
}
