package client

import "testing"

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
