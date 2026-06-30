// Package observability wires Gortexa's logging, tracing and metrics. The gRPC
// instrumentation is installed as a StatsHandler (not interceptors), which
// covers unary and streaming RPCs uniformly. Tracing/metrics export via OTLP
// when an endpoint is configured, and are no-ops otherwise.
package observability

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
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

type fanoutHandler []slog.Handler

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, rec slog.Record) error {
	for _, h := range f {
		if h.Enabled(ctx, rec.Level) {
			if err := h.Handle(ctx, rec.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// SetupLogs builds the process logger and, when configured, an OTel Logs exporter.
func SetupLogs(ctx context.Context, logCfg config.LogConfig, obsCfg config.ObservConfig) (*slog.Logger, ShutdownFunc, error) {
	opts := &slog.HandlerOptions{Level: parseLevel(logCfg.Level)}
	var stdout slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if logCfg.Format == "text" {
		stdout = slog.NewTextHandler(os.Stdout, opts)
	}
	if obsCfg.LogsOTLP == "" {
		return slog.New(stdout), noopShutdown, nil
	}
	exp, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(obsCfg.LogsOTLP), otlploggrpc.WithInsecure())
	if err != nil {
		return nil, nil, err
	}
	res, err := newResource(ctx, obsCfg.ServiceName, obsCfg.ServiceVersion)
	if err != nil {
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)), sdklog.WithResource(res))
	otelHandler := otelslog.NewHandler("gortexa", otelslog.WithLoggerProvider(lp))
	return slog.New(fanoutHandler{stdout, otelHandler}), lp.Shutdown, nil
}

// NewLogger returns an explicitly supplied logger, or builds the legacy stdout logger.
func NewLogger(cfg config.LogConfig, logger ...*slog.Logger) *slog.Logger {
	if len(logger) > 0 && logger[0] != nil {
		return logger[0]
	}
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
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

func newResource(ctx context.Context, service, version string) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{attribute.String("service.name", service)}
	if version != "" {
		attrs = append(attrs, attribute.String("service.version", version))
	}
	return resource.New(ctx, resource.WithAttributes(attrs...))
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
	res, err := newResource(ctx, cfg.ServiceName, cfg.ServiceVersion)
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
	res, err := newResource(ctx, cfg.ServiceName, cfg.ServiceVersion)
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
