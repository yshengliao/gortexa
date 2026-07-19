package client

import (
	"testing"
	"time"
)

func TestNewHTTPClient(t *testing.T) {
	c := NewHTTPClient(HTTPClientConfig{})
	if c.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v", c.Timeout)
	}
	if c.Transport == nil {
		t.Fatal("nil transport")
	}
}
