package health

import (
	"context"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/observability"
	"github.com/yshengliao/gortexa/testutil"
)

// TestStartMetricsExportStopsOnContextCancel guards the export goroutine's
// lifecycle: it must observe ctx.Done and return, leaving no leaked goroutine.
func TestStartMetricsExportStopsOnContextCancel(t *testing.T) {
	defer testutil.AssertNoLeak(t)

	gm, err := observability.NewGovernanceMetrics()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	r.Register("self", func(context.Context) State { return Healthy })

	ctx, cancel := context.WithCancel(context.Background())
	r.StartMetricsExport(ctx, gm, time.Millisecond)
	time.Sleep(5 * time.Millisecond) // let it tick at least once
	cancel()
	// goleak.VerifyNone retries with backoff; it fails if the goroutine did not
	// exit on ctx.Done.
}

// TestStartMetricsExportNilMetricsNoGoroutine covers the guard clause: a nil
// metrics sink is a no-op that spawns nothing.
func TestStartMetricsExportNilMetricsNoGoroutine(t *testing.T) {
	defer testutil.AssertNoLeak(t)

	r := NewRegistry()
	r.Register("self", func(context.Context) State { return Healthy })
	// No goroutine should start; goleak confirms none leaked.
	r.StartMetricsExport(context.Background(), nil, time.Millisecond)
}

// TestStartMetricsExportDefaultInterval covers the interval<=0 branch that
// substitutes the 15s default; cancelling stops the goroutine before it ticks.
func TestStartMetricsExportDefaultInterval(t *testing.T) {
	defer testutil.AssertNoLeak(t)

	gm, err := observability.NewGovernanceMetrics()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	r.Register("self", func(context.Context) State { return Healthy })

	ctx, cancel := context.WithCancel(context.Background())
	r.StartMetricsExport(ctx, gm, 0) // <=0 selects the 15s default ticker
	cancel()                         // return on ctx.Done well before the first tick
}
