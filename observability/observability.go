// Package observability wires Gortexa's logging, tracing and metrics. The gRPC
// instrumentation is installed as a StatsHandler (not interceptors), which
// covers unary and streaming RPCs uniformly. Tracing/metrics export via OTLP
// when an endpoint is configured, and are no-ops otherwise.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/yshengliao/gortexa/config"
)

// ShutdownFunc flushes and stops an exporter.
type ShutdownFunc func(context.Context) error

// OTLP transport security is TLS by default and cleartext only when explicitly
// opted in (obsCfg.OTLPInsecure) — telemetry, including GenAI-captured content,
// must not cross an untrusted network to a remote collector in cleartext.
func logSecurity(insecure bool) otlploggrpc.Option {
	if insecure {
		return otlploggrpc.WithInsecure()
	}
	return otlploggrpc.WithTLSCredentials(credentials.NewTLS(nil))
}

func traceSecurity(insecure bool) otlptracegrpc.Option {
	if insecure {
		return otlptracegrpc.WithInsecure()
	}
	return otlptracegrpc.WithTLSCredentials(credentials.NewTLS(nil))
}

func metricSecurity(insecure bool) otlpmetricgrpc.Option {
	if insecure {
		return otlpmetricgrpc.WithInsecure()
	}
	return otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(nil))
}

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

// minLevelHandler gates a handler that has no level of its own (the otelslog
// bridge accepts every level) so log.level applies to every sink.
type minLevelHandler struct {
	slog.Handler
	min slog.Level
}

func (h minLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.Handler.Enabled(ctx, level)
}

func (h minLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevelHandler{h.Handler.WithAttrs(attrs), h.min}
}

func (h minLevelHandler) WithGroup(name string) slog.Handler {
	return minLevelHandler{h.Handler.WithGroup(name), h.min}
}

// installErrorHandler routes OTel export errors (collector down, TLS
// mismatch) into the structured slog pipeline exactly once per process.
// Without it they hit OTel's default unstructured stderr handler and a dead
// collector is invisible until traces are needed. Rate-limited so a long
// outage cannot flood the log.
var installErrorHandlerOnce sync.Once

func installErrorHandler() {
	installErrorHandlerOnce.Do(func() {
		var mu sync.Mutex
		var last time.Time
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			mu.Lock()
			defer mu.Unlock()
			if time.Since(last) < 30*time.Second {
				return
			}
			last = time.Now()
			slog.Warn("otel export error (rate-limited to one per 30s)", "error", err)
		}))
	})
}

// SetupLogs builds the process logger and, when configured, an OTel Logs
// exporter, and installs the logger as the slog default so package-level slog
// calls (including OTel export errors) share its format and sinks.
func SetupLogs(ctx context.Context, logCfg config.LogConfig, obsCfg config.ObservConfig) (*slog.Logger, ShutdownFunc, error) {
	installErrorHandler()
	lvl, err := parseLevel(logCfg.Level)
	if err != nil {
		return nil, nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var stdout slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if logCfg.Format == "text" {
		stdout = slog.NewTextHandler(os.Stdout, opts)
	}
	if obsCfg.LogsOTLP == "" {
		logger := slog.New(stdout)
		slog.SetDefault(logger)
		return logger, noopShutdown, nil
	}
	exp, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(obsCfg.LogsOTLP), logSecurity(obsCfg.OTLPInsecure))
	if err != nil {
		return nil, nil, err
	}
	res, err := newResource(ctx, obsCfg.ServiceName, obsCfg.ServiceVersion)
	if err != nil {
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)), sdklog.WithResource(res))
	otelHandler := minLevelHandler{otelslog.NewHandler("gortexa", otelslog.WithLoggerProvider(lp)), lvl}
	logger := slog.New(fanoutHandler{stdout, otelHandler})
	slog.SetDefault(logger)
	return logger, lp.Shutdown, nil
}

// NewLogger returns an explicitly supplied logger, or builds the legacy stdout logger.
func NewLogger(cfg config.LogConfig, logger ...*slog.Logger) *slog.Logger {
	if len(logger) > 0 && logger[0] != nil {
		return logger[0]
	}
	lvl, err := parseLevel(cfg.Level)
	if err != nil {
		panic("invalid log level: " + cfg.Level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q", s)
	}
}

// otherMethod is the span name and method label recorded for an RPC whose
// method is not a known service method.
const otherMethod = "_OTHER"

// ServerStatsHandler returns the OTel gRPC server StatsHandler. grpc-go tags an
// RPC before it looks the method up, so an unknown method name is recorded as
// otherMethod: otherwise any caller could mint one metric series per name and
// push real methods past the SDK cardinality limit.
func ServerStatsHandler() stats.Handler {
	return knownMethodHandler{otelgrpc.NewServerHandler()}
}

type knownMethodHandler struct{ stats.Handler }

func (h knownMethodHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	if !knownMethod(info.FullMethodName) {
		c := *info
		c.FullMethodName = otherMethod
		info = &c
	}
	return h.Handler.TagRPC(ctx, info)
}

// knownMethod reports whether fullMethod ("/pkg.Service/Method") names a method
// of a service linked into the binary. The set is fixed at build time, which
// bounds the recorded names.
func knownMethod(fullMethod string) bool {
	service, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return false
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return false
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	return ok && sd.Methods().ByName(protoreflect.Name(method)) != nil
}

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
	installErrorHandler()
	// Propagate W3C trace context and baggage across services and the in-process
	// loopback, regardless of whether this service exports spans.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if cfg.TracingOTLP == "" {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return noopShutdown, nil
	}
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.TracingOTLP),
		traceSecurity(cfg.OTLPInsecure),
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
	installErrorHandler()
	if cfg.MetricsOTLP == "" {
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		return noopShutdown, nil
	}
	exp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.MetricsOTLP),
		metricSecurity(cfg.OTLPInsecure),
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
