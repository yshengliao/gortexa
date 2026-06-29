// Package observability wires Gortexa's logging, tracing and metrics. The gRPC
// instrumentation is installed as a StatsHandler (not interceptors), which
// covers unary and streaming RPCs uniformly. Tracing/metrics export via OTLP
// when an endpoint is configured, and are no-ops otherwise.
package observability

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/stats"

	"github.com/yshengliao/gortexa/internal/config"
)

// ShutdownFunc flushes and stops an exporter.
type ShutdownFunc func(context.Context) error

func noopShutdown(context.Context) error { return nil }

// NewLogger builds a slog.Logger per config (JSON by default, text optional).
func NewLogger(cfg config.LogConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ServerStatsHandler returns the OTel gRPC server StatsHandler.
func ServerStatsHandler() stats.Handler { return otelgrpc.NewServerHandler() }

func clampRatio(r float64) float64 {
	switch {
	case r <= 0:
		return 0
	case r >= 1:
		return 1
	default:
		return r
	}
}

func newResource(ctx context.Context, service string) (*resource.Resource, error) {
	return resource.New(ctx, resource.WithAttributes(attribute.String("service.name", service)))
}

// SetupTracing installs the global tracer provider. With no OTLP endpoint it
// installs a no-op provider and a no-op shutdown.
func SetupTracing(ctx context.Context, cfg config.ObservConfig) (ShutdownFunc, error) {
	if cfg.TracingOTLP == "" {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return noopShutdown, nil
	}
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.TracingOTLP),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, err := newResource(ctx, cfg.ServiceName)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(clampRatio(cfg.SampleRatio)))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// SetupMetrics installs the global meter provider. No-op without an endpoint.
func SetupMetrics(ctx context.Context, cfg config.ObservConfig) (ShutdownFunc, error) {
	if cfg.MetricsOTLP == "" {
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		return noopShutdown, nil
	}
	exp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.MetricsOTLP),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, err := newResource(ctx, cfg.ServiceName)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
	)
	otel.SetMeterProvider(mp)
	return mp.Shutdown, nil
}
