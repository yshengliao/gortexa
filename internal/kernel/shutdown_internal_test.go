package kernel

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A hung connection (e.g. an MCP SSE GET that blocks on ctx.Done) must not make
// graceful shutdown block past the deadline — the Close() fallback force-closes.
func TestShutdownBoundedWithHungConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: 200 * time.Millisecond}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	// Gateway handler that never returns until its request context is cancelled.
	app.SetGateway(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.serve(ctx, ln) }()

	// Fire a request the blocking handler will hold open.
	addr := ln.Addr().String()
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		resp, err := http.Get("http://" + addr + "/hang")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	time.Sleep(100 * time.Millisecond) // let the request reach the handler
	cancel()                           // serve → Shutdown

	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked on a hung connection")
	}
	<-reqDone // the forced close unblocks the client too
}

func TestLoopbackRefusedAfterShutdown(t *testing.T) {
	app, err := New(WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Loopback(); err == nil {
		t.Fatal("Loopback after Shutdown must error, not create a leaked conn")
	}
}
