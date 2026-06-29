package interceptor

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/grpc"

	authpkg "github.com/yshengliao/gortexa/internal/auth"
)

// Config builds a Set.
type Config struct {
	Logger         *slog.Logger
	Verifier       *authpkg.Verifier
	AuthSkip       SkipFunc
	RateLimit      RateLimitConfig
	CircuitBreaker CBConfig
	LoadShedding   LoadSheddingConfig
}

// Set holds the interceptors in both unary and stream form. The chain order is
// fixed by UnaryChain/StreamChain, not by field order.
type Set struct {
	Recovery       grpc.UnaryServerInterceptor
	RequestID      grpc.UnaryServerInterceptor
	Logger         grpc.UnaryServerInterceptor
	LoadShedding   grpc.UnaryServerInterceptor
	RateLimit      grpc.UnaryServerInterceptor
	CircuitBreaker grpc.UnaryServerInterceptor
	Auth           grpc.UnaryServerInterceptor
	Validation     grpc.UnaryServerInterceptor

	RecoveryStream       grpc.StreamServerInterceptor
	RequestIDStream      grpc.StreamServerInterceptor
	LoggerStream         grpc.StreamServerInterceptor
	LoadSheddingStream   grpc.StreamServerInterceptor
	RateLimitStream      grpc.StreamServerInterceptor
	CircuitBreakerStream grpc.StreamServerInterceptor
	AuthStream           grpc.StreamServerInterceptor
	ValidationStream     grpc.StreamServerInterceptor
}

// NewSet constructs every interceptor from config, sharing stateful objects
// (limiter, breaker, shedder, validator) between the unary and stream forms.
func NewSet(cfg Config) (Set, error) {
	validator, err := NewValidator()
	if err != nil {
		return Set{}, fmt.Errorf("interceptor: build validator: %w", err)
	}
	limiter := NewRateLimiter(cfg.RateLimit)
	breaker := NewCircuitBreaker(cfg.CircuitBreaker)
	shedder := NewLoadShedder(cfg.LoadShedding)

	return Set{
		Recovery:       Recovery(cfg.Logger),
		RequestID:      RequestID(),
		Logger:         Logger(cfg.Logger),
		LoadShedding:   shedder.Unary(),
		RateLimit:      limiter.Unary(),
		CircuitBreaker: breaker.Unary(),
		Auth:           Auth(cfg.Verifier, cfg.AuthSkip),
		Validation:     Validation(validator),

		RecoveryStream:       RecoveryStream(cfg.Logger),
		RequestIDStream:      RequestIDStream(),
		LoggerStream:         LoggerStream(cfg.Logger),
		LoadSheddingStream:   shedder.Stream(),
		RateLimitStream:      limiter.Stream(),
		CircuitBreakerStream: breaker.Stream(),
		AuthStream:           AuthStream(cfg.Verifier, cfg.AuthSkip),
		ValidationStream:     ValidationStream(validator),
	}, nil
}

// UnaryChain returns the unary interceptors in fixed outer→inner order,
// panicking if any required interceptor is nil (fail-loud at startup).
func (s Set) UnaryChain() []grpc.UnaryServerInterceptor {
	ordered := []struct {
		name string
		ic   grpc.UnaryServerInterceptor
	}{
		{"recovery", s.Recovery},
		{"requestid", s.RequestID},
		{"logger", s.Logger},
		{"loadshedding", s.LoadShedding},
		{"ratelimit", s.RateLimit},
		{"circuitbreaker", s.CircuitBreaker},
		{"auth", s.Auth},
		{"validation", s.Validation},
	}
	out := make([]grpc.UnaryServerInterceptor, 0, len(ordered))
	for _, o := range ordered {
		if o.ic == nil {
			panic("interceptor: missing required unary interceptor: " + o.name)
		}
		out = append(out, o.ic)
	}
	return out
}

// StreamChain returns the stream interceptors in fixed order (validation last),
// panicking on any nil required interceptor.
func (s Set) StreamChain() []grpc.StreamServerInterceptor {
	ordered := []struct {
		name string
		ic   grpc.StreamServerInterceptor
	}{
		{"recovery", s.RecoveryStream},
		{"requestid", s.RequestIDStream},
		{"logger", s.LoggerStream},
		{"loadshedding", s.LoadSheddingStream},
		{"ratelimit", s.RateLimitStream},
		{"circuitbreaker", s.CircuitBreakerStream},
		{"auth", s.AuthStream},
		{"validation", s.ValidationStream},
	}
	out := make([]grpc.StreamServerInterceptor, 0, len(ordered))
	for _, o := range ordered {
		if o.ic == nil {
			panic("interceptor: missing required stream interceptor: " + o.name)
		}
		out = append(out, o.ic)
	}
	return out
}

// ChainUnary composes UnaryChain into a single interceptor (outermost first).
func (s Set) ChainUnary() grpc.UnaryServerInterceptor {
	ics := s.UnaryChain()
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		var run func(int, context.Context, any) (any, error)
		run = func(i int, c context.Context, r any) (any, error) {
			if i == len(ics) {
				return handler(c, r)
			}
			return ics[i](c, r, info, func(c2 context.Context, r2 any) (any, error) {
				return run(i+1, c2, r2)
			})
		}
		return run(0, ctx, req)
	}
}
