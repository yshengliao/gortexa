package interceptor

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yshengliao/gortexa/observability"
)

// TestBreakerStaleRequestGuard drives allow()/record() directly through the full
// lifecycle — closed → (failures) → open → (openInterval) → half-open probe →
// success → closed — and asserts the stale-request guard: a call admitted while
// the breaker was closed that only completes during a later half-open episode
// must NOT steal the probe slot or flip state.
func TestBreakerStaleRequestGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gm, err := observability.NewGovernanceMetrics()
		if err != nil {
			t.Fatal(err)
		}
		c := NewCircuitBreaker(CBConfig{MaxFailures: 2, OpenInterval: time.Second, HalfOpenMax: 1}, gm)
		b := c.get("/svc/M")
		ctx := context.Background()

		// A call admitted while closed whose handler lingers (records much later).
		stale := b.allow()
		if !stale.ok || stale.probe {
			t.Fatalf("closed admission = %+v, want ok non-probe", stale)
		}

		// Two closed-state failures trip the breaker.
		for range 2 {
			a := b.allow()
			b.record(ctx, "/svc/M", false, a, c)
		}
		if b.state != cbOpen {
			t.Fatalf("state = %v, want open after MaxFailures", b.state)
		}

		// Open: a call before the interval elapses is refused (no probe).
		if a := b.allow(); a.ok {
			t.Fatalf("open breaker admitted a call before openInterval: %+v", a)
		}

		// After the interval, half-open admits exactly one probe.
		time.Sleep(2 * time.Second)
		probe := b.allow()
		if !probe.ok || !probe.probe || !probe.changed {
			t.Fatalf("half-open admission = %+v, want ok probe changed", probe)
		}
		if b.state != cbHalfOpen {
			t.Fatalf("state = %v, want half_open", b.state)
		}

		// A concurrent second admission is refused: HalfOpenMax caps probes.
		if a := b.allow(); a.ok {
			t.Fatalf("half-open admitted a second probe past HalfOpenMax: %+v", a)
		}

		// The stale closed-episode call now completes SUCCESS during half-open. It
		// must not resolve the half-open episode (wrong episode), so state and the
		// probe slot are untouched.
		b.record(ctx, "/svc/M", true, stale, c)
		if b.state != cbHalfOpen {
			t.Fatalf("stale request flipped state to %v", b.state)
		}
		if b.probes != 1 {
			t.Fatalf("stale request stole the probe slot: probes=%d", b.probes)
		}

		// The genuine probe completes success → breaker closes and clears failures.
		b.record(ctx, "/svc/M", true, probe, c)
		if b.state != cbClosed {
			t.Fatalf("state = %v, want closed after successful probe", b.state)
		}
		if b.failures != 0 {
			t.Fatalf("failures = %d, want 0 after close", b.failures)
		}
	})
}

// TestBreakerStaleProbeIgnoredAcrossEpisodes covers the generation guard: a probe
// admitted in one half-open episode that completes during a *later* half-open
// episode must not resolve the later episode (gen mismatch).
func TestBreakerStaleProbeIgnoredAcrossEpisodes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := NewCircuitBreaker(CBConfig{MaxFailures: 1, OpenInterval: time.Second, HalfOpenMax: 2}, c0Metrics(t))
		b := c.get("/svc/M")
		ctx := context.Background()

		// Trip to open with a single failure.
		a := b.allow()
		b.record(ctx, "/svc/M", false, a, c)
		if b.state != cbOpen {
			t.Fatalf("state = %v, want open", b.state)
		}

		// Enter half-open episode A and admit two probes.
		time.Sleep(2 * time.Second)
		p1 := b.allow()
		p2 := b.allow()
		if !p1.probe || !p2.probe || p1.gen != p2.gen {
			t.Fatalf("episode-A probes = %+v %+v", p1, p2)
		}

		// p1 fails → breaker re-opens and bumps generation; p2 is now stale.
		b.record(ctx, "/svc/M", false, p1, c)
		if b.state != cbOpen {
			t.Fatalf("state = %v, want open after probe failure", b.state)
		}

		// Enter half-open episode B (new generation) and admit p3.
		time.Sleep(2 * time.Second)
		p3 := b.allow()
		if !p3.probe || p3.gen == p1.gen {
			t.Fatalf("episode-B probe = %+v, want fresh generation", p3)
		}

		// p2 (episode A) completes SUCCESS during episode B: gen mismatch → ignored,
		// so it neither closes the breaker nor releases episode B's probe slot.
		b.record(ctx, "/svc/M", true, p2, c)
		if b.state != cbHalfOpen {
			t.Fatalf("stale probe flipped state to %v", b.state)
		}
		if b.probes != 1 {
			t.Fatalf("stale probe stole a slot: probes=%d", b.probes)
		}

		// p3 (current episode) success closes the breaker.
		b.record(ctx, "/svc/M", true, p3, c)
		if b.state != cbClosed {
			t.Fatalf("state = %v, want closed", b.state)
		}
	})
}

// TestBreakerClosedIgnoresLateProbe covers the closed-state guard: a half-open
// probe outcome that lands after the breaker already re-closed must not be
// counted as a normal closed-state failure.
func TestBreakerClosedIgnoresLateProbe(t *testing.T) {
	c := NewCircuitBreaker(CBConfig{MaxFailures: 2, OpenInterval: time.Second, HalfOpenMax: 1}, c0Metrics(t))
	b := c.get("/svc/M")

	// A probe-flagged failure recorded while closed must be dropped, not counted.
	b.record(context.Background(), "/svc/M", false, admission{ok: true, probe: true}, c)
	if b.failures != 0 {
		t.Fatalf("late probe counted as closed failure: failures=%d", b.failures)
	}
	if b.state != cbClosed {
		t.Fatalf("state = %v, want closed", b.state)
	}

	// A plain closed-state success just resets the failure counter.
	b.failures = 1
	b.record(context.Background(), "/svc/M", true, admission{ok: true}, c)
	if b.failures != 0 {
		t.Fatalf("success did not reset failures: %d", b.failures)
	}
}

func TestCBStateString(t *testing.T) {
	cases := map[cbState]string{cbClosed: "closed", cbOpen: "open", cbHalfOpen: "half_open", cbState(99): "unknown"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("cbState(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// c0Metrics builds a real GovernanceMetrics so state-change recording paths run.
func c0Metrics(t *testing.T) *observability.GovernanceMetrics {
	t.Helper()
	gm, err := observability.NewGovernanceMetrics()
	if err != nil {
		t.Fatal(err)
	}
	return gm
}
