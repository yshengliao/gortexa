package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/internal/health"
)

func TestAppHandlerRoutingAndHealth(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	app.health.Register("dep", func(context.Context) health.State { return health.Degraded })
	app.SetGateway(stub("gateway"))
	app.SetMCPHandler(stub("mcp"))
	h := app.handler()

	// /healthz → always ok
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body.String())
	}

	// /readyz → degraded still serves (200) and reports the state
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "degraded") {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body.String())
	}

	// /mcp and gateway fallthrough route to their handlers
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Header().Get("X-Stub") != "mcp" {
		t.Fatalf("/mcp routed to %q", rec.Header().Get("X-Stub"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/resources", nil))
	if rec.Header().Get("X-Stub") != "gateway" {
		t.Fatalf("gateway routed to %q", rec.Header().Get("X-Stub"))
	}
}

func TestReadyzUnhealthy(t *testing.T) {
	app, _ := New()
	app.health.Register("dep", func(context.Context) health.State { return health.Unhealthy })
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy readyz = %d, want 503", rec.Code)
	}
}

func TestLoopbackConn(t *testing.T) {
	app, _ := New()
	conn, err := app.Loopback()
	if err != nil || conn == nil {
		t.Fatalf("loopback = %v, %v", conn, err)
	}
	// idempotent
	conn2, _ := app.Loopback()
	if conn2 != conn {
		t.Fatal("Loopback should return the same conn")
	}
	_ = app.Shutdown(context.Background())
}
