package health

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// The metrics exporter calls checks from a bare goroutine, so a panicking check
// used to kill the process on the first tick. It must read as Unhealthy instead,
// without disturbing the other checks.
func TestSnapshotRecoversPanickingCheck(t *testing.T) {
	r := NewRegistry()
	r.Register("boom", func(context.Context) State { panic("nil db") })
	r.Register("ok", func(context.Context) State { return Healthy })

	snap := r.Snapshot(context.Background())
	if snap["boom"] != Unhealthy || snap["ok"] != Healthy {
		t.Fatalf("snapshot = %v, want boom=unhealthy ok=healthy", snap)
	}
}

// Checks used to run one after another, so N hung checks took N*checkTimeout
// and a check evaluated after a hung one inherited an already-spent budget.
// Concurrent evaluation bounds the whole snapshot at one checkTimeout and gives
// a ctx-honouring check its full budget regardless of map order.
func TestSnapshotEvaluatesChecksConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hang := func(ctx context.Context) State { <-ctx.Done(); return Unhealthy }
		r := NewRegistry()
		r.Register("a", hang)
		r.Register("b", hang)
		r.Register("pg", func(ctx context.Context) State {
			if ctx.Err() != nil {
				return Unhealthy
			}
			return Healthy
		})

		start := time.Now()
		snap := r.Snapshot(context.Background())
		if elapsed := time.Since(start); elapsed != checkTimeout {
			t.Fatalf("snapshot took %v, want one checkTimeout (%v)", elapsed, checkTimeout)
		}
		if snap["pg"] != Healthy {
			t.Fatalf("pg = %v, want healthy: a hung sibling must not spend its budget", snap["pg"])
		}
	})
}

// The unary Check RPC is unauthenticated and exempt from load shedding, so it
// must share the coalesced snapshot rather than evaluate every check per call.
func TestCheckIsCoalesced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		r := NewRegistry()
		r.Register("db", func(context.Context) State { calls.Add(1); return Healthy })
		srv := r.GRPCHealthServer()

		for range 10 {
			if _, err := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{}); err != nil {
				t.Fatal(err)
			}
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("10 Check calls ran the check %d times, want 1", got)
		}
		time.Sleep(watchInterval)
		_, _ = srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: "db"})
		if got := calls.Load(); got != 2 {
			t.Fatalf("after watchInterval the check ran %d times, want 2", got)
		}
	})
}

type drainWatch struct {
	grpc.ServerStream
	mu   sync.Mutex
	sent []grpc_health_v1.HealthCheckResponse_ServingStatus
}

func (f *drainWatch) Send(r *grpc_health_v1.HealthCheckResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r.GetStatus())
	return nil
}

func (f *drainWatch) Context() context.Context { return context.Background() }

// A Watch stream only ended on its own context, which a server drain does not
// cancel, so one open stream held the graceful shutdown for its whole budget.
// Drain must tell the watcher NOT_SERVING and end the stream.
func TestDrainEndsWatchWithNotServing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry()
		r.Register("db", func(context.Context) State { return Healthy })
		srv := r.GRPCHealthServer()

		fw := &drainWatch{}
		done := make(chan error, 1)
		go func() { done <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, fw) }()
		synctest.Wait()

		r.Drain()
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Watch = %v, want nil", err)
			}
		default:
			t.Fatal("Watch still running after Drain")
		}
		want := []grpc_health_v1.HealthCheckResponse_ServingStatus{
			grpc_health_v1.HealthCheckResponse_SERVING, grpc_health_v1.HealthCheckResponse_NOT_SERVING,
		}
		if len(fw.sent) != 2 || fw.sent[0] != want[0] || fw.sent[1] != want[1] {
			t.Fatalf("sent = %v, want %v", fw.sent, want)
		}
		resp, err := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
		if err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			t.Fatalf("Check while draining = %v %v, want NOT_SERVING", resp.GetStatus(), err)
		}
	})
}
