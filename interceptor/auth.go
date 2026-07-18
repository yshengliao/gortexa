package interceptor

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	apperr "github.com/yshengliao/gortexa/apperr"
	authpkg "github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/observability"
)

// SkipFunc decides whether a method bypasses authentication.
type SkipFunc func(fullMethod string) bool

func authenticate(ctx context.Context, v *authpkg.Verifier) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	vals := md.Get(authpkg.MetadataKey)
	if len(vals) == 0 {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	tok, ok := authpkg.BearerToken(vals[0])
	if !ok {
		return ctx, apperr.New(apperr.CatUnauthenticated, "malformed authorization")
	}
	claims, err := v.Verify(tok)
	if err != nil {
		return ctx, err
	}
	return authpkg.WithClaims(ctx, claims), nil
}

// Auth verifies the bearer token and injects claims, unless skip says otherwise.
func Auth(v *authpkg.Verifier, skip SkipFunc, metrics ...*observability.GovernanceMetrics) grpc.UnaryServerInterceptor {
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if skip != nil && skip(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := authenticate(ctx, v)
		if err != nil {
			if gm != nil {
				gm.AuthDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("method", info.FullMethod), attribute.String("reason", authReason(err))))
			}
			return nil, err
		}
		return handler(ctx, req)
	}
}

// AuthStream is the streaming counterpart of Auth.
func AuthStream(v *authpkg.Verifier, skip SkipFunc, metrics ...*observability.GovernanceMetrics) grpc.StreamServerInterceptor {
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if skip != nil && skip(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := authenticate(ss.Context(), v)
		if err != nil {
			if gm != nil {
				gm.AuthDenied.Add(ss.Context(), 1, metric.WithAttributes(attribute.String("method", info.FullMethod), attribute.String("reason", authReason(err))))
			}
			return err
		}
		return handler(srv, wrapStream(ss, ctx))
	}
}

func authReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, authpkg.ErrExpiredToken) {
		return "expired"
	}
	if e, ok := errors.AsType[*apperr.Error](err); ok && e != nil && e.Msg == "missing authorization" {
		return "missing"
	}
	return "invalid"
}
