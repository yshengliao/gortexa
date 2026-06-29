package interceptor

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// RequestIDMetadataKey is the metadata/header key for the request id.
const RequestIDMetadataKey = "x-request-id"

type requestIDKey struct{}

// RequestIDFrom returns the request id stored in the context, if any.
func RequestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func incomingRequestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get(RequestIDMetadataKey); len(vals) > 0 && vals[0] != "" {
			return vals[0]
		}
	}
	return ""
}

// RequestID establishes a request id (reusing an inbound one or minting a new
// one), stores it in the context, and echoes it as a response header.
func RequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := incomingRequestID(ctx)
		if id == "" {
			id = newRequestID()
		}
		ctx = context.WithValue(ctx, requestIDKey{}, id)
		_ = grpc.SetHeader(ctx, metadata.Pairs(RequestIDMetadataKey, id))
		return handler(ctx, req)
	}
}

// RequestIDStream is the streaming counterpart of RequestID.
func RequestIDStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		id := incomingRequestID(ss.Context())
		if id == "" {
			id = newRequestID()
		}
		ctx := context.WithValue(ss.Context(), requestIDKey{}, id)
		_ = ss.SetHeader(metadata.Pairs(RequestIDMetadataKey, id))
		return handler(srv, wrapStream(ss, ctx))
	}
}
