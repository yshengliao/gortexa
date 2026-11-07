package interceptor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	apperr "github.com/yshengliao/gortexa/apperr"
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

// maxLoggedCauseBytes bounds the error.cause attribute. A cause is whatever a
// handler or a driver wrapped, and some of those echo request values (a
// protovalidate error renders one line per violation; a database error quotes
// the offending literal), so an unbounded copy lets a caller size the server's
// ERROR records. The head of the message is what identifies the fault.
const maxLoggedCauseBytes = 2048

// truncateUTF8 cuts s to at most n bytes on a rune boundary, marking the cut.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…(truncated)"
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
		if e, ok := errors.AsType[*apperr.Error](err); ok && e != nil {
			retryable := false
			if m, ok := apperr.Default.Lookup(e.Category); ok {
				retryable = m.Retryable
			}
			attrs = append(attrs, "error.category", string(e.Category), "error.retryable", retryable, "error.msg", e.Msg)
			// Server-side only: Recovery strips the cause before the client sees it.
			if cause := errors.Unwrap(e); cause != nil {
				attrs = append(attrs, "error.cause", truncateUTF8(cause.Error(), maxLoggedCauseBytes))
			}
			for k, v := range e.Fields() {
				attrs = append(attrs, k, v)
			}
		} else if !ok {
			attrs = append(attrs, "error", err.Error())
		}
		log.ErrorContext(ctx, "rpc", attrs...)
		return
	}
	log.InfoContext(ctx, "rpc", attrs...)
}
