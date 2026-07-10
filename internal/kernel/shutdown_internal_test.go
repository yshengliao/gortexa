package kernel

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/health"
	"github.com/yshengliao/gortexa/testutil"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// New() with a nil config applies the built-in defaults. WriteTimeout is
// deliberately 0 (disabled) so long-lived gRPC server-streams and MCP SSE are
// not killed mid-flight by a per-HTTP/2-stream write deadline. The httpSrv is
// constructed eagerly in New(), so the field is observable before serve().
func TestNewDefaultServerTimeouts(t *testing.T) {
	app, err := New(WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	if app.httpSrv == nil {
		t.Fatal("New() must construct httpSrv eagerly")
	}
	if app.httpSrv.WriteTimeout != 0 {
		t.Fatalf("default WriteTimeout = %v, want 0 (disabled)", app.httpSrv.WriteTimeout)
	}
	// ReadTimeout is disabled for the same reason as WriteTimeout: it is armed
	// per-HTTP/2-stream and would reset long-lived client-streaming/bidi bodies.
	if app.httpSrv.ReadTimeout != 0 {
		t.Fatalf("default ReadTimeout = %v, want 0 (disabled)", app.httpSrv.ReadTimeout)
	}
	if app.httpSrv.IdleTimeout != 60*time.Second {
		t.Fatalf("default IdleTimeout = %v, want 60s", app.httpSrv.IdleTimeout)
	}
	if app.httpSrv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("default ReadHeaderTimeout = %v, want 5s", app.httpSrv.ReadHeaderTimeout)
	}
}

// A Shutdown issued before serve() must be safe and make a subsequent serve()
// return promptly with no work: because New() builds httpSrv eagerly, the
// earlier httpSrv.Shutdown makes a later Serve return http.ErrServerClosed
// (mapped to a nil serve() error) instead of serving forever. No goroutine leaks.
func TestShutdownBeforeServeReturnsPromptly(t *testing.T) {
	defer testutil.AssertNoLeak(t)

	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: time.Second}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("pre-serve shutdown: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan error, 1)
	go func() { done <- app.serve(context.Background(), ln) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve after pre-serve shutdown = %v, want nil (ErrServerClosed)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return promptly after a pre-serve shutdown")
	}
}

// If httpSrv.Serve fails (e.g. a listener error), serve() must return that error
// and still run the best-effort teardown (loopback gRPC server/conn + hooks), so
// this exit path leaks neither the loopback goroutine nor the telemetry flush.
func TestServeErrorPathTearsDown(t *testing.T) {
	defer testutil.AssertNoLeak(t)

	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: time.Second}}
	var hookRan bool
	app, err := New(WithConfig(cfg), WithLogger(quiet()),
		WithShutdownHook(func(context.Context) error { hookRan = true; return nil }))
	if err != nil {
		t.Fatal(err)
	}

	// A closed listener makes Serve fail immediately with a non-ErrServerClosed
	// error, driving the error-return branch of serve().
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()

	done := make(chan error, 1)
	go func() { done <- app.serve(context.Background(), ln) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve on a closed listener = nil, want the listener error")
		}
		if err == http.ErrServerClosed {
			t.Fatalf("serve returned ErrServerClosed, want the underlying listener error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return on a listener error")
	}
	if !hookRan {
		t.Fatal("serve error path must still run shutdown hooks")
	}
	// Teardown marked the loopback closed; a later Loopback must be refused.
	if _, err := app.Loopback(); err == nil {
		t.Fatal("Loopback after serve-error teardown must be refused")
	}
}

// handleReadyz must evaluate the health snapshot exactly once so the reported
// 'status' can never contradict the per-check states. Feed a degraded and a
// healthy check and assert the aggregate matches the listed checks.
func TestReadyzSnapshotConsistent(t *testing.T) {
	app, err := New(WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	app.health.Register("healthy-dep", func(context.Context) health.State { return health.Healthy })
	app.health.Register("degraded-dep", func(context.Context) health.State { return health.Degraded })

	rec := httptest.NewRecorder()
	app.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("readyz code = %d, want 200 (degraded still serves)", rec.Code)
	}
	body := rec.Body.String()
	// Aggregate status is degraded (the max), and both checks are listed with
	// their own states — a single-snapshot evaluation keeps them consistent.
	if !strings.Contains(body, `"status":"degraded"`) {
		t.Fatalf("readyz status not degraded: %s", body)
	}
	if !strings.Contains(body, `"degraded-dep":"degraded"`) ||
		!strings.Contains(body, `"healthy-dep":"healthy"`) {
		t.Fatalf("readyz checks inconsistent with status: %s", body)
	}
}

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
	// It closes `entered` first so the test can cancel only once the request is
	// actually blocking in the handler — no timing sleep (flaky on loaded CI).
	entered := make(chan struct{})
	var once sync.Once
	app.SetGateway(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
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

	select {
	case <-entered: // the request is now blocking in the handler
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached the blocking handler")
	}
	cancel() // serve → Shutdown

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

// A pre-serve bind failure in Run() must still run the shutdown hooks: serve()
// (which owns shutdown on the later error paths) is never reached, so without
// this the buffered startup telemetry would never be flushed. Mirrors
// TestServeErrorPathTearsDown for Run()'s earlier net.Listen-failure branch.
func TestRunBindFailureRunsShutdownHooks(t *testing.T) {
	defer testutil.AssertNoLeak(t)

	// Occupy an address so Run()'s net.Listen fails with EADDRINUSE.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })

	cfg := &config.Config{Server: config.ServerConfig{Addr: busy.Addr().String(), ShutdownTimeout: time.Second}}
	var hookRan bool
	app, err := New(WithConfig(cfg), WithLogger(quiet()),
		WithShutdownHook(func(context.Context) error { hookRan = true; return nil }))
	if err != nil {
		t.Fatal(err)
	}

	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run() on an occupied address = nil, want the bind error")
	}
	if !hookRan {
		t.Fatal("Run() bind-failure path must still run shutdown hooks (telemetry flush)")
	}
}
