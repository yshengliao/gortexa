package kernel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
)

// A single long-lived stream held open across SIGTERM makes httpSrv.Shutdown
// burn the whole ShutdownTimeout and return context.DeadlineExceeded. That is
// the expected outcome of a *bounded* drain — the Close() fallback then does
// its job — so it must not surface as a serve()/Run error: the framework's own
// documented caller (log.Fatal / os.Exit(1)) would report every rolling deploy
// with an open MCP SSE stream or Health.Watch as a crash.
func TestDrainDeadlineIsNotAServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: 200 * time.Millisecond}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	var once sync.Once
	app.SetGateway(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.serve(ctx, ln) }()

	addr := ln.Addr().String()
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		resp, err := http.Get("http://" + addr + "/hang")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached the blocking handler")
	}
	cancel() // SIGTERM → ctx.Done() → Shutdown

	var gotErr error
	select {
	case gotErr = <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked on a hung connection")
	}
	<-reqDone

	if gotErr != nil {
		t.Fatalf("serve() returned %v after a bounded, force-closed drain; a caller that "+
			"treats any Run error as fatal would exit non-zero on a clean shutdown "+
			"(errors.Is(err, context.DeadlineExceeded) = %v)",
			gotErr, errors.Is(gotErr, context.DeadlineExceeded))
	}
}
