package health_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/internal/health"
)

// watchInterval mirrors the unexported constant in health.go; Watch re-evaluates
// once per second. Keep in sync with the source constant.
const watchInterval = time.Second

// lockedWatch is a race-safe Health_WatchServer stub that records the serving
// status of every Send. The embedded grpc.ServerStream satisfies the remaining
// interface methods (unused by Watch).
type lockedWatch struct {
	grpc.ServerStream
	ctx  context.Context
	mu   sync.Mutex
	sent []grpc_health_v1.HealthCheckResponse_ServingStatus
}

func (f *lockedWatch) Send(r *grpc_health_v1.HealthCheckResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r.GetStatus())
	return nil
}

func (f *lockedWatch) Context() context.Context { return f.ctx }

func (f *lockedWatch) statuses() []grpc_health_v1.HealthCheckResponse_ServingStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grpc_health_v1.HealthCheckResponse_ServingStatus(nil), f.sent...)
}

// TestRegistryState covers the per-service lookup: a registered name reports its
// current state with ok=true; an unknown name yields (Healthy, false).
func TestRegistryState(t *testing.T) {
	ctx := context.Background()
	r := health.NewRegistry()
	r.Register("db", static(health.Degraded))

	if st, ok := r.State(ctx, "db"); !ok || st != health.Degraded {
		t.Fatalf("State(db) = %v %v, want Degraded true", st, ok)
	}
	if st, ok := r.State(ctx, "missing"); ok || st != health.Healthy {
		t.Fatalf("State(missing) = %v %v, want Healthy false", st, ok)
	}
}

// TestGRPCHealthWatchUnknownService verifies an unknown non-empty service streams
// SERVICE_UNKNOWN rather than erroring.
func TestGRPCHealthWatchUnknownService(t *testing.T) {
	r := health.NewRegistry()
	srv := r.GRPCHealthServer()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // send current status once, then return on ctx.Done
	fw := &lockedWatch{ctx: ctx}
	if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{Service: "nope"}, fw); err != nil {
		t.Fatal(err)
	}
	got := fw.statuses()
	if len(got) != 1 || got[0] != grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN {
		t.Fatalf("watch statuses = %v, want [SERVICE_UNKNOWN]", got)
	}
}

// erroringWatch fails its Send on the failOn-th call (1-indexed); other sends
// succeed and are counted.
type erroringWatch struct {
	grpc.ServerStream
	ctx    context.Context
	failOn int
	calls  int
	err    error
}

func (f *erroringWatch) Send(*grpc_health_v1.HealthCheckResponse) error {
	f.calls++
	if f.calls == f.failOn {
		return f.err
	}
	return nil
}

func (f *erroringWatch) Context() context.Context { return f.ctx }

// TestGRPCHealthWatchInitialSendError propagates an error from the first Send.
func TestGRPCHealthWatchInitialSendError(t *testing.T) {
	r := health.NewRegistry()
	r.Register("svc", static(health.Healthy))
	srv := r.GRPCHealthServer()

	fw := &erroringWatch{ctx: context.Background(), failOn: 1, err: context.Canceled}
	if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{Service: "svc"}, fw); err != context.Canceled {
		t.Fatalf("Watch = %v, want context.Canceled", err)
	}
}

// TestGRPCHealthWatchTickSendError propagates an error from a Send triggered by a
// later tick after a status change.
func TestGRPCHealthWatchTickSendError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu    sync.Mutex
			state = health.Healthy
		)
		r := health.NewRegistry()
		r.Register("svc", func(context.Context) health.State {
			mu.Lock()
			defer mu.Unlock()
			return state
		})
		srv := r.GRPCHealthServer()

		fw := &erroringWatch{ctx: context.Background(), failOn: 2, err: context.Canceled}
		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.Watch(&grpc_health_v1.HealthCheckRequest{Service: "svc"}, fw)
		}()

		synctest.Wait() // initial (successful) send
		mu.Lock()
		state = health.Unhealthy
		mu.Unlock()
		time.Sleep(watchInterval + 100*time.Millisecond)
		synctest.Wait()

		if err := <-errCh; err != context.Canceled {
			t.Fatalf("Watch = %v, want context.Canceled", err)
		}
	})
}

// TestGRPCHealthWatchDedupsUnchanged confirms that ticks with no status change
// do not emit duplicate updates.
func TestGRPCHealthWatchDedupsUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := health.NewRegistry()
		r.Register("svc", static(health.Healthy))
		srv := r.GRPCHealthServer()

		ctx, cancel := context.WithCancel(context.Background())
		fw := &lockedWatch{ctx: ctx}
		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.Watch(&grpc_health_v1.HealthCheckRequest{Service: "svc"}, fw)
		}()

		synctest.Wait()
		// Let several ticks fire without any state change.
		time.Sleep(3 * watchInterval)
		synctest.Wait()
		if got := fw.statuses(); len(got) != 1 {
			t.Fatalf("statuses = %v, want a single SERVING (no duplicates)", got)
		}

		cancel()
		synctest.Wait()
		if err := <-errCh; err != nil {
			t.Fatalf("Watch returned %v, want nil", err)
		}
	})
}

// TestGRPCHealthWatchStreamsStateChange drives Watch through a state flip under a
// synctest bubble: it emits the initial SERVING, then after the check turns
// Unhealthy and the watch interval elapses it emits a second NOT_SERVING update.
// The bubble guarantees the Watch goroutine has exited before the test returns.
func TestGRPCHealthWatchStreamsStateChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu    sync.Mutex
			state = health.Healthy
		)
		r := health.NewRegistry()
		r.Register("svc", func(context.Context) health.State {
			mu.Lock()
			defer mu.Unlock()
			return state
		})
		srv := r.GRPCHealthServer()

		ctx, cancel := context.WithCancel(context.Background())
		fw := &lockedWatch{ctx: ctx}
		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.Watch(&grpc_health_v1.HealthCheckRequest{Service: "svc"}, fw)
		}()

		// Let the initial Send land and the goroutine block on the ticker.
		synctest.Wait()
		if got := fw.statuses(); len(got) != 1 || got[0] != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Fatalf("initial statuses = %v, want [SERVING]", got)
		}

		// Flip the check and advance virtual time past the watch interval; the
		// next tick re-evaluates and streams the changed status.
		mu.Lock()
		state = health.Unhealthy
		mu.Unlock()
		time.Sleep(watchInterval + 100*time.Millisecond)
		synctest.Wait()

		got := fw.statuses()
		if len(got) != 2 || got[1] != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			t.Fatalf("post-flip statuses = %v, want [SERVING NOT_SERVING]", got)
		}

		// Cancel and confirm Watch returns nil on ctx.Done.
		cancel()
		synctest.Wait()
		if err := <-errCh; err != nil {
			t.Fatalf("Watch returned %v, want nil", err)
		}
	})
}
