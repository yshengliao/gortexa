// Command server runs the Gortexa sample service: one h2c port serving gRPC,
// HTTP/JSON (grpc-gateway) and MCP for the resource.v1.ResourceService.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
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
	log := observability.NewLogger(cfg.Log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	traceShutdown, err := observability.SetupTracing(ctx, cfg.Observ)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	metricShutdown, err := observability.SetupMetrics(ctx, cfg.Observ)
	if err != nil {
		return fmt.Errorf("setup metrics: %w", err)
	}

	verifier, err := auth.NewVerifier([]byte(cfg.Auth.JWTSecret.Reveal()), cfg.Auth.Issuer)
	if err != nil {
		return fmt.Errorf("build auth verifier: %w", err)
	}
	set, err := interceptor.NewSet(interceptor.Config{
		Logger:   log,
		Verifier: verifier,
		// Health checks are unauthenticated.
		AuthSkip:       func(method string) bool { return strings.HasPrefix(method, "/grpc.health.") },
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
		kernel.WithHTTPWrap(func(h http.Handler) http.Handler { return httpcompat.CORS(h, cfg.Server) }),
		kernel.WithShutdownHook(traceShutdown),
		kernel.WithShutdownHook(metricShutdown),
	)
	if err != nil {
		return fmt.Errorf("build app: %w", err)
	}

	// Register the gRPC service and a self health check.
	resourcev1.RegisterResourceServiceServer(app.GRPCServer(), logic.NewResourceService())
	// gortexa:register — `gortexa gen` inserts RegisterXxxServiceServer calls above this line
	app.Health().Register("self", func(context.Context) health.State { return health.Healthy })

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
	app.SetGateway(gateway)

	descs, err := mcp.ServiceDescriptors(
		"resource.v1.ResourceService",
		// gortexa:mcp — `gortexa gen` inserts "domain.v1.XxxService" entries above this line
	)
	if err != nil {
		return fmt.Errorf("mcp descriptors: %w", err)
	}
	bridge, err := mcp.NewBridge(conn, descs, apperr.Default)
	if err != nil {
		return fmt.Errorf("build mcp bridge: %w", err)
	}
	app.SetMCPHandler(bridge.Handler())

	log.Info("gortexa starting", "addr", cfg.Server.Addr)
	return app.Run(ctx)
}
