package config

import (
	"testing"
)

func TestOptions(t *testing.T) {
	opts := &options{}

	WithConfigFile("config.yaml")(opts)
	if opts.configFile != "config.yaml" {
		t.Errorf("WithConfigFile: expected 'config.yaml', got %q", opts.configFile)
	}

	WithDotEnvFile(".env")(opts)
	if opts.dotenvFile != ".env" {
		t.Errorf("WithDotEnvFile: expected '.env', got %q", opts.dotenvFile)
	}

	WithEnvPrefix("TEST_")(opts)
	if opts.prefix != "TEST_" {
		t.Errorf("WithEnvPrefix: expected 'TEST_', got %q", opts.prefix)
	}

	envFunc := func() []string { return []string{"K=V"} }
	WithEnviron(envFunc)(opts)
	if opts.environ == nil {
		t.Fatal("WithEnviron: expected environ function to be set")
	}
	res := opts.environ()
	if len(res) != 1 || res[0] != "K=V" {
		t.Errorf("WithEnviron: unexpected result from environ function: %v", res)
	}
}
