package interceptor

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"go.opentelemetry.io/otel/baggage"
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

// maxRequestIDLen bounds a client-supplied request id. UUID/hex ids fit easily;
// the cap stops an unbounded id from being stored, logged, and reflected.
const maxRequestIDLen = 128

// ValidRequestID reports whether s is a safe-to-reflect request id: non-empty,
// within maxRequestIDLen, and limited to an unambiguous, log-safe charset. The
// gateway/MCP layers reuse it before echoing a client-supplied X-Request-Id.
func ValidRequestID(s string) bool {
	if s == "" || len(s) > maxRequestIDLen {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func incomingRequestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		// An invalid/oversized inbound id is dropped so a fresh one is minted,
		// rather than propagated and reflected.
		if vals := md.Get(RequestIDMetadataKey); len(vals) > 0 && ValidRequestID(vals[0]) {
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
		ctx = withRequestIDBaggage(context.WithValue(ctx, requestIDKey{}, id), id)
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
		ctx := withRequestIDBaggage(context.WithValue(ss.Context(), requestIDKey{}, id), id)
		_ = ss.SetHeader(metadata.Pairs(RequestIDMetadataKey, id))
		return handler(srv, wrapStream(ss, ctx))
	}
}

func withRequestIDBaggage(ctx context.Context, id string) context.Context {
	member, err := baggage.NewMember(RequestIDMetadataKey, id)
	if err != nil {
		return ctx
	}
	bag, err := baggage.New(member)
	if err != nil {
		return ctx
	}
	return baggage.ContextWithBaggage(ctx, bag)
}
