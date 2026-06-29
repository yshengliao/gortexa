package kernel_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/kernel"
	"github.com/yshengliao/gortexa/testutil"
)

func TestMain(m *testing.M) { testutil.VerifyTestMain(m) }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newApp(t *testing.T, addr string) *kernel.App {
	t.Helper()
	cfg := &config.Config{Server: config.ServerConfig{
		Addr:              addr,
		ShutdownTimeout:   2 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}}
	app, err := kernel.New(kernel.WithConfig(cfg), kernel.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestShutdownIdempotent(t *testing.T) {
	app := newApp(t, "127.0.0.1:0")
	ctx := context.Background()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := app.Shutdown(ctx); err != nil { // must not panic or block
		t.Fatalf("second shutdown: %v", err)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	app := newApp(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	time.Sleep(50 * time.Millisecond) // let it bind
	cancel()

	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestHealthEndpoints(t *testing.T) {
	app := newApp(t, "127.0.0.1:0")
	if app.Health() == nil {
		t.Fatal("nil health registry")
	}
	if app.GRPCServer() == nil || app.Container() == nil {
		t.Fatal("nil grpc server or container")
	}
}
