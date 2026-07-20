package interceptor

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"

	apperr "github.com/yshengliao/gortexa/apperr"
	authpkg "github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/observability"
)

// SkipFunc decides whether a method bypasses authentication.
type SkipFunc func(fullMethod string) bool

// AuthWith authenticates via a pluggable authpkg.Authenticator, injecting the
// returned context into the handler unless skip says otherwise. It is the core
// of the auth stage; Auth is the JWT convenience wrapper. This is what lets a
// non-JWT consumer (static bearer, mTLS, API key) run the stock chain.
func AuthWith(authr authpkg.Authenticator, skip SkipFunc, metrics ...*observability.GovernanceMetrics) grpc.UnaryServerInterceptor {
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if skip != nil && skip(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := authr.Authenticate(ctx)
		if err != nil {
			if gm != nil {
				gm.AuthDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("method", info.FullMethod), attribute.String("reason", authReason(err))))
			}
			return nil, err
		}
		return handler(ctx, req)
	}
}

// AuthStreamWith is the streaming counterpart of AuthWith.
func AuthStreamWith(authr authpkg.Authenticator, skip SkipFunc, metrics ...*observability.GovernanceMetrics) grpc.StreamServerInterceptor {
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if skip != nil && skip(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := authr.Authenticate(ss.Context())
		if err != nil {
			if gm != nil {
				gm.AuthDenied.Add(ss.Context(), 1, metric.WithAttributes(attribute.String("method", info.FullMethod), attribute.String("reason", authReason(err))))
			}
			return err
		}
		return handler(srv, wrapStream(ss, ctx))
	}
}

// Auth verifies an HS256 JWT bearer token and injects claims, unless skip says
// otherwise. It is a thin wrapper over AuthWith with a JWT authenticator, kept
// for backward compatibility — its signature is unchanged.
func Auth(v *authpkg.Verifier, skip SkipFunc, metrics ...*observability.GovernanceMetrics) grpc.UnaryServerInterceptor {
	return AuthWith(authpkg.NewJWTAuthenticator(v), skip, metrics...)
}

// AuthStream is the streaming counterpart of Auth.
func AuthStream(v *authpkg.Verifier, skip SkipFunc, metrics ...*observability.GovernanceMetrics) grpc.StreamServerInterceptor {
	return AuthStreamWith(authpkg.NewJWTAuthenticator(v), skip, metrics...)
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
