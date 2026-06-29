package interceptor

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	authpkg "github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
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
func Auth(v *authpkg.Verifier, skip SkipFunc) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if skip != nil && skip(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := authenticate(ctx, v)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// AuthStream is the streaming counterpart of Auth.
func AuthStream(v *authpkg.Verifier, skip SkipFunc) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if skip != nil && skip(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := authenticate(ss.Context(), v)
		if err != nil {
			return err
		}
		return handler(srv, wrapStream(ss, ctx))
	}
}
