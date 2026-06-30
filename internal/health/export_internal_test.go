package health

import (
	"context"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/observability"
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
