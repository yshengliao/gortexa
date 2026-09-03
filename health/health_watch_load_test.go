package health_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/health"
)

// maxWatchers mirrors the unexported ceiling in health.go. Keep in sync.
const maxWatchers = 1024

// TestGRPCHealthWatchSharesEvaluation pins the cost invariant: Watch is exempt
// from auth and from load shedding, so its polling must cost one registry
// evaluation per interval for the whole server, not one per stream per interval.
// Without the shared snapshot, 50 idle anonymous streams drive 50 dependency
// checks per second — work the chain neither admits nor counts.
func TestGRPCHealthWatchSharesEvaluation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var evals atomic.Int64
		r := health.NewRegistry()
		r.Register("svc", func(context.Context) health.State {
			evals.Add(1)
			return health.Healthy
		})
		srv := r.GRPCHealthServer()

		const watchers = 50
		const ticks = 4

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range watchers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: ctx}); err != nil {
					t.Errorf("Watch = %v, want nil", err)
				}
			}()
		}
		synctest.Wait() // every watcher has made its initial evaluation
		synctest.Sleep(ticks*watchInterval + watchInterval/2)
		got := evals.Load()

		cancel()
		wg.Wait()

		// The initial evaluation plus one per elapsed tick, shared by all
		// watchers. Per-stream evaluation would be (ticks+1)*watchers.
		if want := int64(ticks + 1); got != want {
			t.Fatalf("check evaluated %d times for %d watchers over %d intervals, want %d", got, watchers, ticks, want)
		}
	})
}

// TestGRPCHealthWatchCapsConcurrentStreams verifies the ceiling that stands in
// for the load shedder the health methods are exempt from: past maxWatchers live
// streams a further Watch is refused, and refusing one frees a slot again.
func TestGRPCHealthWatchCapsConcurrentStreams(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := health.NewRegistry()
		r.Register("svc", static(health.Healthy))
		srv := r.GRPCHealthServer()

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range maxWatchers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: ctx}); err != nil {
					t.Errorf("Watch = %v, want nil", err)
				}
			}()
		}
		synctest.Wait()

		// Run the over-cap watch in a goroutine: if it is wrongly accepted it
		// blocks on its ticker instead of returning, and the test reports that
		// rather than hanging.
		overCtx, overCancel := context.WithCancel(context.Background())
		defer overCancel()
		over := &lockedWatch{ctx: overCtx}
		overErr := make(chan error, 1)
		go func() { overErr <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, over) }()
		synctest.Wait()
		select {
		case err := <-overErr:
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("Watch past the cap = %v, want ResourceExhausted", err)
			}
		default:
			t.Fatal("Watch past the cap was accepted, want ResourceExhausted")
		}
		if got := over.statuses(); len(got) != 0 {
			t.Fatalf("refused watch sent %v, want nothing", got)
		}

		// A refused stream must not have consumed a slot; draining the live ones
		// must free theirs.
		cancel()
		wg.Wait()
		accepted, freeCancel := context.WithCancel(context.Background())
		defer freeCancel()
		done := make(chan error, 1)
		go func() { done <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: accepted}) }()
		synctest.Wait()
		freeCancel()
		if err := <-done; err != nil {
			t.Fatalf("Watch after the cap drained = %v, want nil", err)
		}
	})
}
