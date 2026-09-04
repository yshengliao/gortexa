package kernel_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/kernel"
	"github.com/yshengliao/gortexa/testutil"
)

// CLAUDE.md states consumer interceptors are chained "after the stock chain,
// INSIDE RECOVER", and kernel.WithServerOptions repeats it. The existing test
// proves only the *order* half — that the interceptor sees auth's claims, which
// distinguishes after-auth from outermost and says nothing about recover, which
// sits at the opposite end of the chain. Neither injected interceptor there
// ever panics.
//
// This asserts the recover half directly: a panic raised inside a
// consumer-supplied interceptor must come back as an apperr-shaped Internal
// status, not tear the process down, and the connection must stay usable
// afterwards.
func TestWithServerOptionsPanicIsRecovered(t *testing.T) {
	v := auth.MustNewVerifier(testutil.DefaultSecret, "gortexa")
	set, err := interceptor.NewSet(interceptor.Config{Verifier: v})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := v.Sign("u-1", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	panicking := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get("x-please-panic")) > 0 {
			panic("consumer interceptor exploded")
		}
		return h(ctx, req)
	}

	cfg := &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}}
	app, err := kernel.New(
		kernel.WithConfig(cfg),
		kernel.WithLogger(quietLogger()),
		kernel.WithInterceptors(set),
		kernel.WithServerOptions(grpc.ChainUnaryInterceptor(panicking)),
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

	// The panicking call: recover must convert it into a status, and the process
	// must survive to answer.
	panicCtx, panicCancel := context.WithTimeout(
		metadata.AppendToOutgoingContext(authed, "x-please-panic", "1"), 5*time.Second)
	defer panicCancel()
	_, err = hc.Check(panicCtx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true))
	if err == nil {
		t.Fatal("a panicking consumer interceptor must surface as an error, not a success")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("panic surfaced as %v, want Internal: recover maps it through apperr", got)
	}
	// The panic value must not reach the client — apperr never serialises a cause.
	if msg := status.Convert(err).Message(); msg == "consumer interceptor exploded" {
		t.Fatalf("the panic value leaked to the client: %q", msg)
	}

	// And the server is still serving, which is the property recover exists for.
	okCtx, okCancel := context.WithTimeout(authed, 5*time.Second)
	defer okCancel()
	if _, err := hc.Check(okCtx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("server must still serve after a recovered panic: %v", err)
	}
}
