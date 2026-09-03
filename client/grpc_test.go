package client

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

func TestNewGRPCConn(t *testing.T) {
	conn, err := NewGRPCConn(GRPCClientConfig{Target: "passthrough:///localhost:1", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if conn == nil {
		t.Fatal("nil conn")
	}
	_ = conn.Close()
}

// TestNewGRPCConn_TLSByDefault pins the security invariant documented on
// NewGRPCConn: TLS is the default transport, and only cfg.Insecure opts out
// of it. grpc.NewClient dials lazily, so the credential choice is otherwise
// unobserved by any test; here a plaintext server forces the handshake and
// distinguishes the two configs by outcome (TLS client fails against it,
// cleartext client succeeds).
func TestNewGRPCConn_TLSByDefault(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	target := "passthrough:///" + lis.Addr().String()

	waitState(t, target, false /* insecure */, connectivity.TransientFailure)
	waitState(t, target, true /* insecure */, connectivity.Ready)
}

// waitState dials target with the given Insecure setting and asserts the
// connection reaches want before the deadline.
func waitState(t *testing.T, target string, insecure bool, want connectivity.State) {
	t.Helper()

	conn, err := NewGRPCConn(GRPCClientConfig{Target: target, Insecure: insecure})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn.Connect()
	for {
		state := conn.GetState()
		if state == want {
			return
		}
		if !conn.WaitForStateChange(ctx, state) {
			t.Fatalf("insecure=%v: timed out waiting for state %v, last state %v", insecure, want, state)
		}
	}
}
