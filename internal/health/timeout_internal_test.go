package health

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// TestCheckTimeoutBoundsBlockingCheck pins that a check honouring its context is
// released at checkTimeout instead of stalling the caller indefinitely. The
// synctest bubble auto-advances fake time once the check is parked on ctx.Done,
// so this asserts the ceiling deterministically without a real wall-clock wait.
func TestCheckTimeoutBoundsBlockingCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry()
		r.Register("slow", func(ctx context.Context) State {
			<-ctx.Done() // a check that would block forever without the ceiling
			return Unhealthy
		})

		start := time.Now()
		got := r.Overall(context.Background())
		elapsed := time.Since(start)

		if got != Unhealthy {
			t.Fatalf("Overall = %v, want Unhealthy after the check's ctx expired", got)
		}
		if elapsed != checkTimeout {
			t.Fatalf("check released after %v, want checkTimeout (%v)", elapsed, checkTimeout)
		}
	})
}
