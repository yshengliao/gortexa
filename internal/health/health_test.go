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
	cases := []struct {
		name   string
		checks map[string]health.State
		want   health.State
	}{
		{
			name:   "empty registry is healthy",
			checks: nil,
			want:   health.Healthy,
		},
		{
			name: "all healthy",
			checks: map[string]health.State{
				"a": health.Healthy,
				"b": health.Healthy,
			},
			want: health.Healthy,
		},
		{
			name: "degraded overrides healthy",
			checks: map[string]health.State{
				"a": health.Healthy,
				"b": health.Degraded,
				"c": health.Healthy,
			},
			want: health.Degraded,
		},
		{
			name: "unhealthy overrides degraded",
			checks: map[string]health.State{
				"a": health.Healthy,
				"b": health.Degraded,
				"c": health.Unhealthy,
			},
			want: health.Unhealthy,
		},
		{
			name: "all unhealthy",
			checks: map[string]health.State{
				"a": health.Unhealthy,
				"b": health.Unhealthy,
			},
			want: health.Unhealthy,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := health.NewRegistry()
			for name, state := range tc.checks {
				r.Register(name, static(state))
			}
			if got := r.Overall(context.Background()); got != tc.want {
				t.Fatalf("Overall() = %v, want %v", got, tc.want)
			}
		})
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
