package health_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/health"
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

// Register used to be a plain map assignment, so two subsystems claiming the
// same name — plausible with a primary pool and a read replica, or after
// `gortexa gen` adds a second domain — left one silently unmonitored while
// /readyz kept answering for the winner. Boot-time registration is
// single-threaded and unconditional at every call site, so a collision is a
// wiring mistake worth failing on immediately.
func TestRegisterRejectsDuplicateNames(t *testing.T) {
	r := health.NewRegistry()
	r.Register("db", static(health.Healthy))

	defer func() {
		if recover() == nil {
			t.Fatal("a duplicate check name must panic")
		}
		// The first registration survives, so the panic cannot half-apply.
		if got, ok := r.State(context.Background(), "db"); !ok || got != health.Healthy {
			t.Fatalf("original check must be intact after the rejected duplicate; got %v ok=%v", got, ok)
		}
	}()
	r.Register("db", static(health.Unhealthy))
}

// Replace is the explicit form of what Register used to do by accident, for the
// cases where substituting a check really is the intent.
func TestReplaceSubstitutesAnExistingCheck(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	r.Register("db", static(health.Healthy))
	r.Replace("db", static(health.Unhealthy))

	if got, ok := r.State(ctx, "db"); !ok || got != health.Unhealthy {
		t.Fatalf("Replace must install the new check; got %v ok=%v", got, ok)
	}
	if names := r.Names(); len(names) != 1 {
		t.Fatalf("Replace must not add an entry; got %v", names)
	}
	// Replace on an unused name is a plain insert, so callers need not branch.
	r.Replace("cache", static(health.Degraded))
	if got, ok := r.State(ctx, "cache"); !ok || got != health.Degraded {
		t.Fatalf("Replace must insert an absent name; got %v ok=%v", got, ok)
	}
}
