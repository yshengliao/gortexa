package kernel_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/kernel"
	"github.com/yshengliao/gortexa/testutil"
)

// Consumer interceptors injected via WithServerOptions run inside the framework
// chain, not merely somewhere: because gRPC appends them after the stock chain,
// they execute AFTER the auth stage — so they observe the claims auth injected.
// (A grpc.UnaryInterceptor installed outermost would run before auth and see no
// claims, so "claims present" is what distinguishes inside-chain from outside.)
// Covers both the unary and stream paths.
func TestWithServerOptionsRunsInsideChain(t *testing.T) {
	v := auth.MustNewVerifier(testutil.DefaultSecret, "gortexa")
	set, err := interceptor.NewSet(interceptor.Config{Verifier: v}) // no AuthSkip: calls must carry a token
	if err != nil {
		t.Fatal(err)
	}
	tok, err := v.Sign("u-1", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var unaryCalls, streamCalls atomic.Int64
	var unarySawClaims, streamSawClaims atomic.Bool
	customUnary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		unaryCalls.Add(1)
		if _, ok := auth.ClaimsFrom(ctx); ok {
			unarySawClaims.Store(true)
		}
		return h(ctx, req)
	}
	customStream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		streamCalls.Add(1)
		if _, ok := auth.ClaimsFrom(ss.Context()); ok {
			streamSawClaims.Store(true)
		}
		return h(srv, ss)
	}

	cfg := &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}}
	app, err := kernel.New(
		kernel.WithConfig(cfg),
		kernel.WithLogger(quietLogger()),
		kernel.WithInterceptors(set),
		kernel.WithServerOptions(
			grpc.ChainUnaryInterceptor(customUnary),
			grpc.ChainStreamInterceptor(customStream),
		),
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
	hc := grpc_health_v1.NewHealthClient(conn)
	authed := metadata.AppendToOutgoingContext(context.Background(), auth.MetadataKey, "Bearer "+tok)

	// Unary: Check through the chain.
	callCtx, callCancel := context.WithTimeout(authed, 5*time.Second)
	defer callCancel()
	if _, err := hc.Check(callCtx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("unary Check: %v", err)
	}
	if unaryCalls.Load() != 1 || !unarySawClaims.Load() {
		t.Fatalf("unary interceptor: calls=%d sawClaims=%v (want 1, true — must run after auth)", unaryCalls.Load(), unarySawClaims.Load())
	}

	// Stream: Watch through the chain (cancel it so the server stream unwinds).
	watchCtx, watchCancel := context.WithCancel(authed)
	stream, err := hc.Watch(watchCtx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("open Watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Watch Recv: %v", err)
	}
	watchCancel()
	if streamCalls.Load() != 1 || !streamSawClaims.Load() {
		t.Fatalf("stream interceptor: calls=%d sawClaims=%v (want 1, true — must run after auth)", streamCalls.Load(), streamSawClaims.Load())
	}
}
