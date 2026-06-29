package interceptor

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// Logger emits a structured slog record per RPC with the final mapped code,
// latency and request id.
func Logger(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logRPC(ctx, log, info.FullMethod, err, time.Since(start))
		return resp, err
	}
}

// LoggerStream is the streaming counterpart of Logger.
func LoggerStream(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logRPC(ss.Context(), log, info.FullMethod, err, time.Since(start))
		return err
	}
}

func logRPC(ctx context.Context, log *slog.Logger, method string, err error, dur time.Duration) {
	if log == nil {
		return
	}
	code := codes.OK
	if err != nil {
		code = apperr.ToGRPCStatus(err).Code()
	}
	attrs := []any{"method", method, "code", code.String(), "duration_ms", dur.Milliseconds()}
	if id, ok := RequestIDFrom(ctx); ok {
		attrs = append(attrs, "request_id", id)
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
		log.ErrorContext(ctx, "rpc", attrs...)
		return
	}
	log.InfoContext(ctx, "rpc", attrs...)
}
