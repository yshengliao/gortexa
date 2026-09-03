package health

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/yshengliao/gortexa/observability"
)

// TestStartMetricsExportStaleAttributeSeries guards against encoding the
// state both as the gauge value and as an attribute: doing so makes each
// observed (component, state) pair its own permanent series under
// cumulative-temporality collection, so a component that has since changed
// state keeps re-exporting its old state value forever alongside the new
// one.
func TestStartMetricsExportStaleAttributeSeries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		meter := provider.Meter("gortexa-test")
		gauge, err := meter.Int64Gauge("gortexa_health_state")
		if err != nil {
			t.Fatalf("Int64Gauge: %v", err)
		}
		gm := &observability.GovernanceMetrics{HealthStateGauge: gauge}

		r := NewRegistry()
		var state atomic.Int32
		state.Store(int32(Unhealthy))
		r.Register("db", func(context.Context) State { return State(state.Load()) })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r.StartMetricsExport(ctx, gm, time.Second)

		// First tick: db is Unhealthy (state=2).
		time.Sleep(time.Second)
		synctest.Wait()

		// db recovers before the next tick.
		state.Store(int32(Healthy))

		// Second tick: db is Healthy (state=0).
		time.Sleep(time.Second)
		synctest.Wait()

		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("Collect: %v", err)
		}

		dps := findGaugeDataPoints(t, &rm, "gortexa_health_state")

		seenStates := map[int64]bool{}
		for _, dp := range dps {
			for _, kv := range dp.Attributes.ToSlice() {
				if string(kv.Key) == "component" && kv.Value.AsString() == "db" {
					seenStates[dp.Value] = true
				}
			}
		}

		if len(seenStates) != 1 {
			t.Fatalf("component db: expected exactly one live state after recovery, got states=%v (stale attribute-set series keep old values reporting forever)", seenStates)
		}
		if !seenStates[int64(Healthy)] {
			t.Fatalf("component db: expected current state Healthy(0) to be reported, got states=%v", seenStates)
		}
	})
}

func findGaugeDataPoints(t *testing.T, rm *metricdata.ResourceMetrics, name string) []metricdata.DataPoint[int64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok {
				return g.DataPoints
			}
		}
	}
	t.Fatalf("metric %s not found", name)
	return nil
}
