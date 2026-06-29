package health_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/internal/health"
)

type fakeWatch struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*grpc_health_v1.HealthCheckResponse
}

func (f *fakeWatch) Send(r *grpc_health_v1.HealthCheckResponse) error {
	f.sent = append(f.sent, r)
	return nil
}
func (f *fakeWatch) Context() context.Context { return f.ctx }

func TestGRPCHealthWatchEmitsStatus(t *testing.T) {
	r := health.NewRegistry()
	r.Register("svc", static(health.Healthy))
	srv := r.GRPCHealthServer()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Watch sends current status, then returns once ctx is done
	fw := &fakeWatch{ctx: ctx}
	if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{}, fw); err != nil {
		t.Fatal(err)
	}
	if len(fw.sent) != 1 || fw.sent[0].GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("watch sent = %+v", fw.sent)
	}
}

func TestNamesAndSnapshot(t *testing.T) {
	r := health.NewRegistry()
	r.Register("b", static(health.Healthy))
	r.Register("a", static(health.Degraded))
	names := r.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %v, want sorted [a b]", names)
	}
	snap := r.Snapshot(context.Background())
	if snap["a"] != health.Degraded || snap["b"] != health.Healthy {
		t.Fatalf("snapshot = %v", snap)
	}
}

func TestStateString(t *testing.T) {
	cases := map[health.State]string{
		health.Healthy:   "healthy",
		health.Degraded:  "degraded",
		health.Unhealthy: "unhealthy",
		health.State(99): "unknown",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("State(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
}

type errorWatch struct {
	grpc.ServerStream
	ctx context.Context
	err error
}

func (e *errorWatch) Send(*grpc_health_v1.HealthCheckResponse) error {
	return e.err
}

func (e *errorWatch) Context() context.Context { return e.ctx }

func TestGRPCHealthWatchSendError(t *testing.T) {
	r := health.NewRegistry()
	srv := r.GRPCHealthServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := context.DeadlineExceeded // arbitrary error
	ew := &errorWatch{ctx: ctx, err: expectedErr}

	err := srv.Watch(&grpc_health_v1.HealthCheckRequest{}, ew)
	if err != expectedErr {
		t.Fatalf("Watch returned error %v, want %v", err, expectedErr)
	}
}
