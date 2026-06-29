package interceptor

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// LoadSheddingConfig configures dual-signal shedding. Both signals are optional:
// MaxInflight <= 0 disables the concurrency signal; MaxCPU <= 0 (or a nil
// sampler) disables the CPU signal. The CPUSampler returns current utilization
// in [0,1]; it is injectable so the signal can be wired to any source (or faked
// in tests) without coupling the interceptor to a platform metric.
type LoadSheddingConfig struct {
	MaxInflight int
	MaxCPU      float64
	CPUSampler  func() float64
	// Skip exempts a method from admission counting (e.g. control-plane health
	// checks). Critical for long-lived server streams: without it, unauthenticated
	// Health.Watch streams — which hold an inflight slot for the whole stream
	// lifetime and run before auth — could occupy MaxInflight and shed all other
	// RPCs. nil skips nothing.
	Skip SkipFunc
}

// LoadShedder rejects excess load with ResourceExhausted.
type LoadShedder struct {
	cfg      LoadSheddingConfig
	inflight atomic.Int64
}

// NewLoadShedder builds a LoadShedder from config.
func NewLoadShedder(cfg LoadSheddingConfig) *LoadShedder { return &LoadShedder{cfg: cfg} }

func (s *LoadShedder) cpuOverloaded() bool {
	return s.cfg.MaxCPU > 0 && s.cfg.CPUSampler != nil && s.cfg.CPUSampler() > s.cfg.MaxCPU
}

// admit reports whether to accept the call; the returned release must be called
// when the call completes (it decrements the in-flight gauge).
func (s *LoadShedder) admit() (release func(), err error) {
	if s.cpuOverloaded() {
		return func() {}, apperr.New(apperr.CatResourceExhausted, "load shedding: cpu")
	}
	n := s.inflight.Add(1)
	release = func() { s.inflight.Add(-1) }
	if s.cfg.MaxInflight > 0 && n > int64(s.cfg.MaxInflight) {
		release()
		return func() {}, apperr.New(apperr.CatResourceExhausted, "load shedding: concurrency")
	}
	return release, nil
}

// Unary returns the unary interceptor.
func (s *LoadShedder) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if s.cfg.Skip != nil && s.cfg.Skip(info.FullMethod) {
			return handler(ctx, req)
		}
		release, err := s.admit()
		if err != nil {
			return nil, err
		}
		defer release()
		return handler(ctx, req)
	}
}

// Stream returns the stream interceptor.
func (s *LoadShedder) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if s.cfg.Skip != nil && s.cfg.Skip(info.FullMethod) {
			return handler(srv, ss)
		}
		release, err := s.admit()
		if err != nil {
			return err
		}
		defer release()
		return handler(srv, ss)
	}
}
