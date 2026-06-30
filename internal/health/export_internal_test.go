package health

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/yshengliao/gortexa/internal/observability"
)

// TestStartMetricsExportStopsOnContextCancel guards the export goroutine's
// lifecycle: it must observe ctx.Done and return, leaving no leaked goroutine.
func TestStartMetricsExportStopsOnContextCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

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
