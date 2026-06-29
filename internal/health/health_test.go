package health_test

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/internal/health"
)

func static(s health.State) health.Check {
	return func(context.Context) health.State { return s }
}

func TestOverallWorstOf(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	if r.Overall(ctx) != health.Healthy {
		t.Fatal("empty registry should be Healthy")
	}
	r.Register("a", static(health.Healthy))
	r.Register("b", static(health.Degraded))
	if got := r.Overall(ctx); got != health.Degraded {
		t.Fatalf("overall = %v, want Degraded", got)
	}
	r.Register("c", static(health.Unhealthy))
	if got := r.Overall(ctx); got != health.Unhealthy {
		t.Fatalf("overall = %v, want Unhealthy", got)
	}
}

func TestServingSemantics(t *testing.T) {
	if !health.Healthy.Serving() || !health.Degraded.Serving() {
		t.Error("healthy and degraded must be serving")
	}
	if health.Unhealthy.Serving() {
		t.Error("unhealthy must not be serving")
	}
}

func TestGRPCHealthBridge(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	r.Register("svc", static(health.Healthy))
	srv := r.GRPCHealthServer()

	resp, err := srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}

	r.Register("svc", static(health.Degraded))
	resp, err = srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING for degraded state", resp.GetStatus())
	}

	r.Register("svc", static(health.Unhealthy))
	resp, _ = srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING", resp.GetStatus())
	}
}

func TestConcurrentRegisterSnapshot(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.Register("x", static(health.Healthy)) }()
		go func() { defer wg.Done(); _ = r.Overall(ctx) }()
	}
	wg.Wait()
}
