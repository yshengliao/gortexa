package kernel_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/kernel"
	"github.com/yshengliao/gortexa/testutil"
)

// A consumer unary interceptor injected via WithServerOptions runs on real
// calls, inside the framework chain (gRPC appends it after the stock chain).
func TestWithServerOptionsInterceptorRuns(t *testing.T) {
	set, err := interceptor.NewSet(interceptor.Config{
		Verifier: auth.MustNewVerifier(testutil.DefaultSecret, "gortexa"),
		// Skip auth for the health method so the call needs no token; the custom
		// interceptor still runs regardless of the auth decision.
		AuthSkip: func(m string) bool { return strings.HasPrefix(m, "/grpc.health.") },
	})
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int64
	custom := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		calls.Add(1)
		return handler(ctx, req)
	}

	cfg := &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}}
	app, err := kernel.New(
		kernel.WithConfig(cfg),
		kernel.WithLogger(quietLogger()),
		kernel.WithInterceptors(set),
		kernel.WithServerOptions(grpc.ChainUnaryInterceptor(custom)),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-runDone
	})

	conn, err := app.Loopback()
	if err != nil {
		t.Fatalf("loopback: %v", err)
	}
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	// WaitForReady blocks until Run has started serving the loopback listener.
	if _, err := grpc_health_v1.NewHealthClient(conn).Check(callCtx,
		&grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("health check over loopback: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("custom interceptor call count = %d, want 1", got)
	}
}
