package interceptor

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yshengliao/gortexa/observability"
)

// TestBreakerHalfOpenWedgesForever locks in the half-open episode deadline. Probe
// slots are released only by record(), which never runs for a handler that never
// returns (a long-lived stream, or a hung call with no deadline). Without an
// elapsed-time escape of its own, half-open would refuse every subsequent caller
// for as long as those probes live — cbOpen's timer only governs entry into the
// episode, not the episode itself.
func TestBreakerHalfOpenWedgesForever(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gm, err := observability.NewGovernanceMetrics()
		if err != nil {
			t.Fatal(err)
		}
		c := NewCircuitBreaker(CBConfig{MaxFailures: 1, OpenInterval: time.Second, HalfOpenMax: 2}, gm)
		b := c.get("/svc/M")
		ctx := context.Background()

		// Trip the breaker open with a single failure.
		a := b.allow()
		b.record(ctx, "/svc/M", outcomeFailure, a, c)
		if b.state != cbOpen {
			t.Fatalf("state = %v, want open", b.state)
		}

		// Wait past openFor: breaker moves to half-open.
		time.Sleep(2 * time.Second)

		// Two long-lived callers (e.g. streams) are admitted as probes and never
		// complete: their handlers hang, so record() is never invoked for them.
		p1 := b.allow()
		p2 := b.allow()
		if !p1.ok || !p1.probe || !p2.ok || !p2.probe {
			t.Fatalf("expected both probes admitted: p1=%+v p2=%+v", p1, p2)
		}
		if b.state != cbHalfOpen {
			t.Fatalf("state = %v, want half_open", b.state)
		}

		// A third caller is refused: HalfOpenMax (2) is exhausted. This is
		// expected/correct behavior right after entering half-open.
		if a := b.allow(); a.ok {
			t.Fatalf("expected refusal once HalfOpenMax probes are outstanding: %+v", a)
		}

		// Now wait a long time — far longer than openFor — WITHOUT the hung
		// probes ever completing (they never call record()). The breaker must
		// abandon the stale episode and give a fresh caller a chance again.
		time.Sleep(1 * time.Hour)

		got := b.allow()
		if !got.ok {
			t.Fatalf("breaker permanently wedged in half-open: allow() = %+v after 1h with hung probes; "+
				"no time-based escape exists for cbHalfOpen (only cbOpen checks elapsed time)", got)
		}
		if !got.probe || got.gen == p1.gen {
			t.Fatalf("stale-episode escape = %+v, want a probe in a fresh generation (was %d)", got, p1.gen)
		}

		// The abandoned probes must not be able to resolve the fresh episode when
		// they eventually return: the generation guard drops them.
		b.record(ctx, "/svc/M", outcomeSuccess, p1, c)
		if b.state != cbHalfOpen {
			t.Fatalf("abandoned probe resolved the fresh episode: state=%v", b.state)
		}

		// The fresh probe still resolves it normally.
		b.record(ctx, "/svc/M", outcomeSuccess, got, c)
		if b.state != cbClosed {
			t.Fatalf("state = %v, want closed after the fresh probe succeeded", b.state)
		}
	})
}
