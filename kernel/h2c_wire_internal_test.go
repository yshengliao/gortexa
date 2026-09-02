package kernel

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/yshengliao/gortexa/config"
)

// One h2c port stays one port: a stock gRPC client speaking cleartext HTTP/2
// with prior knowledge, and a plain HTTP/1.1 client, must both be served on the
// same TCP listener. Every other kernel gRPC assertion rides the in-process
// bufconn loopback, which never touches httpSrv — so only a real dial exercises
// h2cProtocols() and mux dispatch on a negotiated connection.
func TestSinglePortServesGRPCAndHTTPOverTheWire(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{ShutdownTimeout: time.Second}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.serve(ctx, ln) }()

	addr := ln.Addr().String()
	conn, err := grpc.NewClient("passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	resp, err := grpc_health_v1.NewHealthClient(conn).Check(callCtx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("external gRPC over the h2c port failed: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health status = %v, want SERVING", resp.GetStatus())
	}

	// Same port, HTTP/1.1: the multiplexing, not just the gRPC half.
	httpResp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("HTTP/JSON on the same port failed: %v", err)
	}
	_ = httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", httpResp.StatusCode)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}
