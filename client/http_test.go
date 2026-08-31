package client

import (
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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

// TestNewHTTPClientCustomTimeout pins that a non-zero Timeout is honoured
// as-is instead of being replaced by the 30s default.
func TestNewHTTPClientCustomTimeout(t *testing.T) {
	c := NewHTTPClient(HTTPClientConfig{Timeout: 5 * time.Second})
	if c.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", c.Timeout)
	}
}

// TestNewHTTPClientOTelTransport pins the observability contract: the client
// must ship the otelhttp-instrumented transport, not a bare http.Transport.
func TestNewHTTPClientOTelTransport(t *testing.T) {
	c := NewHTTPClient(HTTPClientConfig{})
	if _, ok := c.Transport.(*otelhttp.Transport); !ok {
		t.Fatalf("transport = %T, want *otelhttp.Transport", c.Transport)
	}
}
