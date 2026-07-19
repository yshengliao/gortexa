package client

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPClientConfig configures NewHTTPClient. A zero Timeout defaults to 30s —
// outbound calls always have a deadline.
type HTTPClientConfig struct{ Timeout time.Duration }

// NewHTTPClient returns an *http.Client with an OTel-instrumented transport
// and an enforced timeout.
func NewHTTPClient(cfg HTTPClientConfig) *http.Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
}
