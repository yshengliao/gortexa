package observability_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"go.opentelemetry.io/otel"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/observability"
)

func TestNewLogger(t *testing.T) {
	if observability.NewLogger(config.LogConfig{Level: "debug", Format: "json"}) == nil {
		t.Fatal("nil logger")
	}
	if observability.NewLogger(config.LogConfig{Format: "text"}) == nil {
		t.Fatal("nil text logger")
	}
}

func TestNewLoggerExplicit(t *testing.T) {
	supplied := slog.Default()
	if got := observability.NewLogger(config.LogConfig{Level: "invalid-would-panic"}, supplied); got != supplied {
		t.Fatal("explicit logger not returned")
	}
}

func TestNewLoggerPanicsOnInvalidLevel(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on invalid log level")
		}
	}()
	observability.NewLogger(config.LogConfig{Level: "bogus"})
}

func TestSetupLogs(t *testing.T) {
	tests := []struct {
		name    string
		logCfg  config.LogConfig
		obsCfg  config.ObservConfig
		wantErr bool
	}{
		{name: "invalid level", logCfg: config.LogConfig{Level: "bogus"}, wantErr: true},
		{name: "json stdout no otlp", logCfg: config.LogConfig{Level: "info", Format: "json"}},
		{name: "text stdout no otlp", logCfg: config.LogConfig{Level: "debug", Format: "text"}},
		{
			name:   "otlp fanout path",
			logCfg: config.LogConfig{Level: "info"},
			obsCfg: config.ObservConfig{
				ServiceName:    "test",
				ServiceVersion: "0.0.1",
				LogsOTLP:       "127.0.0.1:1", // never dialed eagerly
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			logger, shutdown, err := observability.SetupLogs(ctx, tt.logCfg, tt.obsCfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetupLogs: %v", err)
			}
			if logger == nil || shutdown == nil {
				t.Fatal("nil logger or shutdown")
			}
			sctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
			defer cancel()
			// The OTLP exporter cannot reach an endpoint; tolerate its error.
			_ = shutdown(sctx)
		})
	}
}

func TestSetupTracingWithEndpoint(t *testing.T) {
	ctx := context.Background()
	shutdown, err := observability.SetupTracing(ctx, config.ObservConfig{
		ServiceName:    "test",
		ServiceVersion: "0.0.1",
		TracingOTLP:    "127.0.0.1:1",
		SampleRatio:    0.5,
	})
	if err != nil {
		t.Fatalf("SetupTracing: %v", err)
	}
	if shutdown == nil {
		t.Fatal("nil shutdown")
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	_ = shutdown(sctx) // unreachable exporter; the bounded ctx keeps this quick
}

func TestSetupMetricsWithEndpoint(t *testing.T) {
	ctx := context.Background()
	shutdown, err := observability.SetupMetrics(ctx, config.ObservConfig{
		ServiceName: "test",
		MetricsOTLP: "127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	if shutdown == nil {
		t.Fatal("nil shutdown")
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	_ = shutdown(sctx) // unreachable exporter; the bounded ctx keeps this quick
}

func TestNewGovernanceMetrics(t *testing.T) {
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
	m, err := observability.NewGovernanceMetrics()
	if err != nil {
		t.Fatalf("NewGovernanceMetrics: %v", err)
	}
	if m.LoadShedTotal == nil || m.RateLimitTotal == nil || m.CBStateChanges == nil ||
		m.AuthDenied == nil || m.ValidationFails == nil || m.HealthStateGauge == nil {
		t.Fatal("governance metrics has nil instrument")
	}
	m.LoadShedTotal.Add(context.Background(), 1)
	m.HealthStateGauge.Record(context.Background(), 1)
}

func TestServerStatsHandler(t *testing.T) {
	if observability.ServerStatsHandler() == nil {
		t.Fatal("nil stats handler")
	}
}

func TestSetupNoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	st, err := observability.SetupTracing(ctx, config.ObservConfig{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st(ctx); err != nil {
		t.Fatal(err)
	}
	sm, err := observability.SetupMetrics(ctx, config.ObservConfig{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sm(ctx); err != nil {
		t.Fatal(err)
	}
}
