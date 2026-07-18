package client

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type GRPCClientConfig struct {
	Target   string
	Insecure bool
}

func NewGRPCConn(cfg GRPCClientConfig) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{grpc.WithStatsHandler(otelgrpc.NewClientHandler())}
	if cfg.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}
	return grpc.NewClient(cfg.Target, opts...)
}
