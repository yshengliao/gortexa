package interceptor

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// Recovery turns a panicking handler into an Internal error, logging the stack,
// and normalizes every returned error through the app registry at the transport
// boundary. It is the outermost interceptor so it covers every inner interceptor
// and handler: a *Error already maps to a safe status via GRPCStatus, but a
// fmt-wrapped Error or a plain error would otherwise reach the client as raw
// codes.Unknown text, leaking internals. Normalizing here reduces every error
// shape through Registry.resolve before grpc-go serializes it.
func Recovery(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		// Carry a slot inward for the inner RequestID stage to publish into, so
		// the panic record can be joined to the id the client was handed back.
		ctx = withRequestIDHolder(ctx)
		defer func() {
			if r := recover(); r != nil {
				logPanic(ctx, log, info.FullMethod, r)
				err = apperr.New(apperr.CatInternal, "internal error")
			}
			if err != nil {
				err = apperr.ToGRPCStatus(err).Err()
			}
		}()
		return handler(ctx, req)
	}
}

// RecoveryStream is the streaming counterpart of Recovery.
func RecoveryStream(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		// Same return path as the unary case: the stream RequestID hands inward
		// is derived from ours, so only a shared slot can carry the id back out.
		ss = wrapStream(ss, withRequestIDHolder(ss.Context()))
		defer func() {
			if r := recover(); r != nil {
				logPanic(ss.Context(), log, info.FullMethod, r)
				err = apperr.New(apperr.CatInternal, "internal error")
			}
			if err != nil {
				err = apperr.ToGRPCStatus(err).Err()
			}
		}()
		return handler(srv, ss)
	}
}

func logPanic(ctx context.Context, log *slog.Logger, method string, r any) {
	if log == nil {
		return
	}
	attrs := []any{"method", method, "panic", r, "stack", string(debug.Stack())}
	id, ok := RequestIDFrom(ctx)
	if !ok {
		// Under the fixed chain order this is the usual path: RequestID runs
		// inside Recovery, so its id reaches us only through the holder.
		id, ok = requestIDFromHolder(ctx)
	}
	if ok {
		attrs = append(attrs, "request_id", id)
	}
	log.ErrorContext(ctx, "panic recovered", attrs...)
}
