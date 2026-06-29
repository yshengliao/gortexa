package health

import (
	"testing"
)

func TestGRPCHealthServer_Adapter(t *testing.T) {
	r := NewRegistry()
	srv := r.GRPCHealthServer()

	gh, ok := srv.(*grpcHealth)
	if !ok {
		t.Fatalf("GRPCHealthServer() returned %T, want *grpcHealth", srv)
	}
	if gh.reg != r {
		t.Errorf("grpcHealth.reg = %p, want %p (the registry)", gh.reg, r)
	}
}
