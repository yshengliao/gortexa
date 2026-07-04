// Command server runs the Gortexa sample service: one h2c port serving gRPC,
// HTTP/JSON (grpc-gateway) and MCP for the resource.v1.ResourceService.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	// gortexa:import — `gortexa gen` inserts generated-package imports above this line
	"github.com/yshengliao/gortexa/internal/auth"
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/health"
	"github.com/yshengliao/gortexa/internal/httpcompat"
	"github.com/yshengliao/gortexa/internal/interceptor"
	"github.com/yshengliao/gortexa/internal/kernel"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/internal/mcp"
	"github.com/yshengliao/gortexa/internal/observability"
)

func main() {
	exportFormat := flag.String("export-ai-schemas", "", "print the ai.v1 tool schemas (mcp|openai|gemini) to stdout and exit")
	flag.Parse()
	if *exportFormat != "" {
		if err := exportSchemas(*exportFormat); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// authSkip exempts health checks from authentication (probes carry no tokens)
// and, only when reflection is enabled, the reflection service itself — the
// flag exists for schema-discovery tooling like `buf curl --reflect`, whose
// reflection stream carries no token. The trailing dots keep the prefixes from
// matching any user service (e.g. a "grpc.healthx" package).
func authSkip(reflection bool) func(method string) bool {
	return func(method string) bool {
		if strings.HasPrefix(method, "/grpc.health.") {
			return true
		}
		return reflection && strings.HasPrefix(method, "/grpc.reflection.")
	}
}

// mcpServices lists every service exposed over the MCP bridge and the ai.v1
// schema export.
func mcpServices() []protoreflect.FullName {
	return []protoreflect.FullName{
		"resource.v1.ResourceService",
		// gortexa:mcp — `gortexa gen` inserts "domain.v1.XxxService" entries above this line
	}
}

// exportSchemas renders the project's ai.v1 tool schemas without starting the
// server: the contract is compiled into this binary, so no config, storage or
// listener is needed.
func exportSchemas(format string) error {
	descs, err := mcp.ServiceDescriptors(mcpServices()...)
	if err != nil {
		return fmt.Errorf("mcp descriptors: %w", err)
	}
	out, err := mcp.ExportSchemas(format, descs)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}

func configOptions() []config.Option {
	var opts []config.Option
	path := os.Getenv("GORTEXA_CONFIG")
	if path == "" {
		path = "etc/config.yaml"
	}
	if _, err := os.Stat(path); err == nil {
		opts = append(opts, config.WithConfigFile(path))
	}
	return opts
}

func run() error {
	cfg := config.MustBuild(configOptions()...)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log, logShutdown, err := observability.SetupLogs(ctx, cfg.Log, cfg.Observ)
	if err != nil {
		return fmt.Errorf("setup logs: %w", err)
	}

	traceShutdown, err := observability.SetupTracing(ctx, cfg.Observ)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	metricShutdown, err := observability.SetupMetrics(ctx, cfg.Observ)
	if err != nil {
		return fmt.Errorf("setup metrics: %w", err)
	}
	// If run() returns before app.Run takes over lifecycle, flush the telemetry
	// providers here so a startup failure after observability setup still exports
	// buffered spans/metrics/logs. Once the app starts, its shutdown hooks own
	// this, so the guard below skips the double-flush.
	appStarted := false
	defer func() {
		if appStarted {
			return
		}
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = traceShutdown(flushCtx)
		_ = metricShutdown(flushCtx)
		_ = logShutdown(flushCtx)
	}()

	// Governance metrics use the global meter provider installed by SetupMetrics
	// (a no-op provider when no OTLP endpoint is configured, so this is safe).
	govMetrics, err := observability.NewGovernanceMetrics()
	if err != nil {
		return fmt.Errorf("setup governance metrics: %w", err)
	}

	verifier, err := auth.NewVerifier([]byte(cfg.Auth.JWTSecret.Reveal()), cfg.Auth.Issuer, cfg.Auth.Audience)
	if err != nil {
		return fmt.Errorf("build auth verifier: %w", err)
	}
	set, err := interceptor.NewSet(interceptor.Config{
		Logger:         log,
		Verifier:       verifier,
		Metrics:        govMetrics,
		AuthSkip:       authSkip(cfg.Server.Reflection),
		RateLimit:      interceptor.RateLimitConfig{RPS: 200, Burst: 100, TTL: 10 * time.Minute},
		CircuitBreaker: interceptor.CBConfig{MaxFailures: 5, OpenInterval: 10 * time.Second, HalfOpenMax: 2},
		// Exempt control-plane health checks from the inflight budget so a flood of
		// long-lived (unauthenticated) Health.Watch streams can't shed real traffic.
		LoadShedding: interceptor.LoadSheddingConfig{
			MaxInflight: 1024,
			Skip:        func(method string) bool { return strings.HasPrefix(method, "/grpc.health.") },
		},
	})
	if err != nil {
		return fmt.Errorf("build interceptors: %w", err)
	}

	app, err := kernel.New(
		kernel.WithConfig(cfg),
		kernel.WithLogger(log),
		kernel.WithInterceptors(set),
		kernel.WithStatsHandler(observability.ServerStatsHandler()),
		// CORS wraps outermost so /openapi.json (mounted when server.openapi is
		// enabled) gets CORS headers too.
		kernel.WithHTTPWrap(func(h http.Handler) http.Handler {
			h = httpcompat.OpenAPIRoute(h, cfg.Server, "gen/openapiv2/gortexa.swagger.json")
			return httpcompat.CORS(h, cfg.Server)
		}),
		kernel.WithShutdownHook(traceShutdown),
		kernel.WithShutdownHook(metricShutdown),
		kernel.WithShutdownHook(logShutdown),
	)
	if err != nil {
		return fmt.Errorf("build app: %w", err)
	}

	// Register the gRPC service and a self health check.
	resourcev1.RegisterResourceServiceServer(app.GRPCServer(), logic.NewResourceService())
	// gortexa:register — `gortexa gen` inserts RegisterXxxServiceServer calls above this line
	app.Health().Register("self", func(context.Context) health.State { return health.Healthy })
	// Export component health states as an OTel gauge; the goroutine stops on ctx.
	app.Health().StartMetricsExport(ctx, govMetrics, 0)

	// The gateway and MCP bridge forward through the in-process loopback so they
	// share the full interceptor chain.
	conn, err := app.Loopback()
	if err != nil {
		return fmt.Errorf("loopback: %w", err)
	}

	gateway := httpcompat.NewServeMux(apperr.Default)
	if err := resourcev1.RegisterResourceServiceHandler(ctx, gateway, conn); err != nil {
		return fmt.Errorf("register gateway: %w", err)
	}
	// gortexa:gateway — `gortexa gen` inserts RegisterXxxServiceHandler blocks above this line
	app.SetGateway(httpcompat.MaxBodyBytes(gateway))

	descs, err := mcp.ServiceDescriptors(mcpServices()...)
	if err != nil {
		return fmt.Errorf("mcp descriptors: %w", err)
	}
	bridge, err := mcp.NewBridge(conn, descs, apperr.Default, cfg.Observ)
	if err != nil {
		return fmt.Errorf("build mcp bridge: %w", err)
	}
	// DNS-rebinding protection for the MCP endpoint: browsers may only reach it
	// from an allowlisted origin (the same list CORS uses); non-browser clients
	// (no Origin header) are unaffected.
	bridge.SetAllowedOrigins(cfg.Server.CORSOrigins)
	app.SetMCPHandler(bridge.Handler())

	log.Info("gortexa starting", "addr", cfg.Server.Addr)
	appStarted = true // app.Run owns telemetry shutdown from here (kernel hooks)
	return app.Run(ctx)
}
