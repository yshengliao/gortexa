package kernel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/health"
)

// /readyz is unauthenticated and outside the chain, so it must serve the
// coalesced snapshot (not ping every dependency per request) and WARN only when
// readiness flips, not on every 503 a probe loop provokes.
func TestReadyzCoalescesAndWarnsOnTransition(t *testing.T) {
	var buf bytes.Buffer
	app, err := New(WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	app.Health().Register("db", func(context.Context) health.State { calls.Add(1); return health.Unhealthy })

	for range 5 {
		rec := httptest.NewRecorder()
		app.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("5 /readyz requests ran the check %d times, want 1", got)
	}
	if n := strings.Count(buf.String(), "readiness not serving"); n != 1 {
		t.Fatalf("not-serving WARN logged %d times, want once per transition", n)
	}
}

// Once Shutdown starts, both /readyz surfaces must report 503 so a load
// balancer stops routing while the main port drains.
func TestReadyzReportsDrainingAfterShutdown(t *testing.T) {
	app, err := New(WithLogger(quiet()), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	app.Health().Register("db", func(context.Context) health.State { return health.Healthy })
	_ = app.Shutdown(context.Background())

	for name, h := range map[string]http.Handler{"main": app.handler(), "admin": app.adminHealthHandler()} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s /readyz while draining = %d, want 503", name, rec.Code)
		}
	}
}

// An open Health.Watch on the main port used to pin httpSrv.Shutdown for the
// whole ShutdownTimeout (GOAWAY does not cancel the handler's context). Drain
// must push NOT_SERVING and end the stream so shutdown finishes promptly.
func TestShutdownIsNotPinnedByHealthWatch(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: 5 * time.Second}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.serve(ctx, ln) }()

	conn, err := grpc.NewClient("passthrough:///"+ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wcancel()
	stream, err := grpc_health_v1.NewHealthClient(conn).Watch(wctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := stream.Recv(); err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("first watch update = %v %v, want SERVING", resp.GetStatus(), err)
	}

	start := time.Now()
	cancel()
	if resp, err := stream.Recv(); err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("watch update on drain = %v %v, want NOT_SERVING", resp.GetStatus(), err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v with a Watch open; the stream pinned the drain", elapsed)
	}
}

// A Shutdown called from another goroutine closes the listener first, and serve
// used to return nil on ErrServerClosed at once — before the drain and flush
// hooks ran, dropping their error. It must wait and surface the hook error.
func TestServeWaitsForConcurrentShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hookErr := errors.New("flush failed")
	var hookDone atomic.Bool
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: time.Second}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()), WithoutInterceptors(),
		WithShutdownHook(func(context.Context) error {
			time.Sleep(200 * time.Millisecond)
			hookDone.Store(true)
			return hookErr
		}))
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- app.serve(context.Background(), ln) }()
	waitServing(t, ln.Addr().String())

	shut := make(chan error, 1)
	go func() { shut <- app.Shutdown(context.Background()) }()
	if err := <-served; !errors.Is(err, hookErr) || !hookDone.Load() {
		t.Fatalf("serve = %v (hook done %v), want the hook error after the hooks ran", err, hookDone.Load())
	}
	if err := <-shut; !errors.Is(err, hookErr) {
		t.Fatalf("Shutdown = %v, want the hook error", err)
	}
}

// A caller-provided WithExtraListener listener that is never handed to Serve
// (the main bind fails first) used to stay open: http.Server.Shutdown closes
// only listeners Serve tracked.
func TestRunBindFailureClosesUnservedExtraListener(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	extra, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Addr: busy.Addr().String(), ShutdownTimeout: time.Second}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()), WithoutInterceptors(),
		WithExtraListener(extra, http.NotFoundHandler()))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run on a busy address must fail")
	}
	if err := extra.Close(); err == nil {
		t.Fatal("the unserved extra listener was left open")
	}
}

func waitServing(t *testing.T, addr string) {
	t.Helper()
	for range 100 {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server never came up")
}
