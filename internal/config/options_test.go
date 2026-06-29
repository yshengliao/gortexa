package config

import (
	"reflect"
	"testing"
)

func TestOptions(t *testing.T) {
	t.Run("WithDotEnvFile", func(t *testing.T) {
		o := &options{}
		WithDotEnvFile("test.env")(o)
		if o.dotenvFile != "test.env" {
			t.Errorf("expected dotenvFile to be 'test.env', got '%s'", o.dotenvFile)
		}
	})

	t.Run("WithConfigFile", func(t *testing.T) {
		o := &options{}
		WithConfigFile("config.yaml")(o)
		if o.configFile != "config.yaml" {
			t.Errorf("expected configFile to be 'config.yaml', got '%s'", o.configFile)
		}
	})

	t.Run("WithEnvPrefix", func(t *testing.T) {
		o := &options{}
		WithEnvPrefix("TEST_")(o)
		if o.prefix != "TEST_" {
			t.Errorf("expected prefix to be 'TEST_', got '%s'", o.prefix)
		}
	})

	t.Run("WithEnviron", func(t *testing.T) {
		o := &options{}
		mockEnviron := func() []string { return []string{"A=B"} }
		WithEnviron(mockEnviron)(o)

		// we can verify if calling the function returns the expected result
		if o.environ == nil {
			t.Errorf("expected environ function to be set, got nil")
		} else {
			result := o.environ()
			if len(result) != 1 || result[0] != "A=B" {
				t.Errorf("expected environ to return ['A=B'], got %v", result)
			}
		}

		// also verify function pointer using reflect
		if reflect.ValueOf(o.environ).Pointer() != reflect.ValueOf(mockEnviron).Pointer() {
			t.Errorf("expected environ to be the same function pointer")
		}
	})
}
