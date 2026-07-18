package health_test

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/health"
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

	r.Register("svc", static(health.Unhealthy))
	resp, _ = srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING", resp.GetStatus())
	}
}

// TestGRPCHealthPerService covers the health protocol's service field: a
// registered name reports that component's status, an unknown name is NotFound.
func TestGRPCHealthPerService(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	r.Register("db", static(health.Healthy))
	r.Register("cache", static(health.Unhealthy))
	srv := r.GRPCHealthServer()

	resp, err := srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "db"})
	if err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("db check = %v %v, want SERVING nil", resp.GetStatus(), err)
	}
	resp, err = srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "cache"})
	if err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("cache check = %v %v, want NOT_SERVING nil", resp.GetStatus(), err)
	}
	if _, err := srv.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown service err = %v, want NotFound", err)
	}
}

func TestConcurrentRegisterSnapshot(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); r.Register("x", static(health.Healthy)) }()
		go func() { defer wg.Done(); _ = r.Overall(ctx) }()
	}
	wg.Wait()
}
