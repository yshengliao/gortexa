package health

import (
	"context"
	"testing"
)

func TestRegister(t *testing.T) {
	r := NewRegistry()
	if len(r.checks) != 0 {
		t.Fatalf("expected empty registry, got %d", len(r.checks))
	}

	mockCheck := func(context.Context) State { return Healthy }

	// Add a check
	r.Register("test_check", mockCheck)
	if len(r.checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(r.checks))
	}

	// Add another check
	r.Register("test_check_2", mockCheck)
	if len(r.checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(r.checks))
	}

	// Replace an existing check
	r.Register("test_check", func(context.Context) State { return Degraded })
	if len(r.checks) != 2 {
		t.Fatalf("expected 2 checks after replace, got %d", len(r.checks))
	}
}
