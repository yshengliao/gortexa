package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/health"
)

// TestShutdownDelayKeepsListenerOpenWhileDraining pins the pre-stop grace.
// Shutdown flipped /readyz to 503 and closed the listener in the same
// instant, so a load balancer that had not yet observed the 503 kept routing
// new connections to the pod for one probe period and they were refused. With
// server.shutdown_delay the listener stays open, answering 503, for the delay.
func TestShutdownDelayKeepsListenerOpenWhileDraining(t *testing.T) {
	const delay = 400 * time.Millisecond
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: time.Second, ShutdownDelay: delay}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	drainingSeen := make(chan bool, 1)
	go func() {
		time.Sleep(delay / 2)
		rec := httptest.NewRecorder()
		app.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		drainingSeen <- rec.Code == http.StatusServiceUnavailable
	}()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < delay {
		t.Fatalf("Shutdown returned after %v, want >= %v (no drain window)", took, delay)
	}
	if !<-drainingSeen {
		t.Fatal("/readyz did not report draining during the shutdown delay")
	}

	// An already-cancelled ctx skips the delay: the escape hatch still works.
	app2, _ := New(WithConfig(cfg), WithLogger(quiet()), WithoutInterceptors())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	_ = app2.Shutdown(cancelled)
	if took := time.Since(start); took >= delay {
		t.Fatalf("Shutdown with a cancelled ctx still waited %v", took)
	}
}

// TestReadyzPublicHidesCheckNames pins the split: the main port's /readyz
// answers only the aggregate status, while the admin listener's /readyz keeps
// the per-check detail. The public body listed every dependency by name and
// state to any unauthenticated caller, an inventory plus a live degradation
// signal (hit when the cache is down).
func TestReadyzPublicHidesCheckNames(t *testing.T) {
	app, err := New(WithLogger(quiet()), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	app.health.Register("postgres", func(context.Context) health.State { return health.Degraded })

	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"degraded"`) {
		t.Fatalf("public readyz = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "checks") {
		t.Fatalf("public readyz leaks dependency inventory: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	app.adminHealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if !strings.Contains(rec.Body.String(), `"postgres":"degraded"`) {
		t.Fatalf("admin readyz lost the per-check detail: %s", rec.Body.String())
	}
}
