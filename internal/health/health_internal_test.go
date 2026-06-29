package health

import "testing"

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry() returned nil")
	}
	if r.checks == nil {
		t.Fatal("NewRegistry() did not initialize checks map")
	}
}
