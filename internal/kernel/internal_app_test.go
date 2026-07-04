package kernel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/internal/config"
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

// listServices calls grpc.reflection.v1 ServerReflection.ServerReflectionInfo
// with a ListServices request over the loopback conn, returning its
// ListServiceResponse (or the RPC error, e.g. codes.Unimplemented when
// reflection isn't registered).
func listServices(t *testing.T, app *App) (*grpc_reflection_v1.ListServiceResponse, error) {
	t.Helper()
	conn, err := app.Loopback()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := grpc_reflection_v1.NewServerReflectionClient(conn).ServerReflectionInfo(context.Background())
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: "*"},
	}); err != nil {
		return nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	return resp.GetListServicesResponse(), nil
}

// Reflection is gated by config.ServerConfig.Reflection, off by default: with
// it enabled, ServerReflectionInfo lists the health service; with it left off,
// the RPC fails Unimplemented (no reflection service registered at all).
func TestReflectionGatedByConfig(t *testing.T) {
	app, err := New(WithConfig(&config.Config{Server: config.ServerConfig{Reflection: true}}))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.serve(ctx, ln) }()

	list, err := listServices(t, app)
	if err != nil {
		t.Fatalf("ServerReflectionInfo: %v", err)
	}
	var found bool
	for _, s := range list.GetService() {
		if s.GetName() == "grpc.health.v1.Health" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reflection service list = %v, want grpc.health.v1.Health", list.GetService())
	}
	_ = app.Shutdown(context.Background())
}

func TestReflectionOffByDefault(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.serve(ctx, ln) }()

	if _, err := listServices(t, app); status.Code(err) != codes.Unimplemented {
		t.Fatalf("ServerReflectionInfo with Reflection=false: code = %v, want Unimplemented", status.Code(err))
	}
	_ = app.Shutdown(context.Background())
}
