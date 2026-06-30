package client

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type HTTPClientConfig struct{ Timeout time.Duration }

func NewHTTPClient(cfg HTTPClientConfig) *http.Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
}
