package kernel

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/yshengliao/gortexa/config"
)

// slowServiceDesc registers a single unary RPC whose handler ignores the
// request context entirely and blocks until released — modeling a slow report
// query or a wedged dependency that doesn't return promptly on ctx cancel.
func slowServiceDesc(entered chan struct{}, release <-chan struct{}) *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "test.SlowService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Slow",
				Handler: func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
					var req emptypb.Empty
					if err := dec(&req); err != nil {
						return nil, err
					}
					close(entered)
					<-release // ignores ctx.Done(); only the test releases it
					return &emptypb.Empty{}, nil
				},
			},
		},
		Streams: []grpc.StreamDesc{},
	}
}

// An RPC that outlives ShutdownTimeout without unwinding must not make
// Shutdown unbounded. Shutdown closes the loopback conn before starting
// GracefulStop, so grpc's connection set drains at once and GracefulStop parks
// in handlersWG.Wait() while holding the server mutex; a hard Stop() issued
// inline would then block on that same mutex until the handler finally returns,
// so Shutdown would never reach the telemetry-flush hooks and Run would hang
// until SIGKILL.
func TestShutdownBoundedWhenHardStopRacesGracefulStop(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: 200 * time.Millisecond}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	app.grpcSrv.RegisterService(slowServiceDesc(entered, release), nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.serve(ctx, ln) }()

	conn, err := app.Loopback()
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.Invoke(context.Background(), "/test.SlowService/Slow", &emptypb.Empty{}, &emptypb.Empty{})
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("slow handler never started")
	}

	cancel() // serve()'s SIGTERM path → Shutdown

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return within 5s despite ShutdownTimeout=200ms: " +
			"the hard stop blocked behind the abandoned GracefulStop")
	}

	// Let the handler finish so grpc's own goroutines unwind before goleak runs.
	close(release)
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight RPC never completed after release")
	}
}
