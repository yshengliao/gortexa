// Package interceptor provides Gortexa's gRPC server interceptors and the
// fixed-order chain that wires them. Recovery is outermost; validation is
// innermost. OTel is installed separately as a StatsHandler (see
// internal/observability), not as an interceptor.
package interceptor

import (
	"context"

	"google.golang.org/grpc"
)

// wrappedStream overrides a ServerStream's context so downstream handlers see
// values injected by interceptors (e.g. request id, claims).
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

func wrapStream(ss grpc.ServerStream, ctx context.Context) grpc.ServerStream {
	return &wrappedStream{ServerStream: ss, ctx: ctx}
}
