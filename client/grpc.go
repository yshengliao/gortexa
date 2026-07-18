// Package client provides outbound gRPC and HTTP client constructors with
// OTel instrumentation wired in: dialing another service from a Gortexa app
// (or any consumer) gets trace propagation without per-call setup.
package client

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCClientConfig configures NewGRPCConn.
type GRPCClientConfig struct {
	// Target is the dial target (host:port or a gRPC name-resolution scheme).
	Target string
	// Insecure dials without TLS. Local development only.
	Insecure bool
}

// NewGRPCConn returns a client connection with the OTel stats handler
// installed. TLS by default; cleartext only when cfg.Insecure is set.
func NewGRPCConn(cfg GRPCClientConfig) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{grpc.WithStatsHandler(otelgrpc.NewClientHandler())}
	if cfg.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}
	return grpc.NewClient(cfg.Target, opts...)
}
