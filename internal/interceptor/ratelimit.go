package interceptor

import (
	"context"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// RateLimitConfig configures the per-peer token bucket. RPS <= 0 disables limiting.
type RateLimitConfig struct {
	RPS   float64       // tokens per second per peer
	Burst int           // bucket size
	TTL   time.Duration // evict idle peers after this long
}

// RateLimiter is a per-peer token-bucket limiter with lazy TTL eviction (no
// background goroutine, so nothing to leak or close).
type RateLimiter struct {
	rps   rate.Limit
	burst int
	ttl   time.Duration

	mu        sync.Mutex
	entries   map[string]*rlEntry
	lastSweep time.Time
}

type rlEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimiter builds a RateLimiter from config.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	burst := cfg.Burst
	if burst <= 0 {
		burst = 1
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &RateLimiter{
		rps:     rate.Limit(cfg.RPS),
		burst:   burst,
		ttl:     ttl,
		entries: make(map[string]*rlEntry),
	}
}

func (l *RateLimiter) enabled() bool { return l.rps > 0 }

// peerKey keys the limiter by client IP only. Including the ephemeral port
// would let a client reset its bucket by reconnecting (rate-limit bypass) and
// would grow the entries map unbounded (one entry per connection).
func peerKey(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		addr := p.Addr.String()
		if host, _, err := net.SplitHostPort(addr); err == nil {
			return host
		}
		return addr
	}
	return "unknown"
}

func (l *RateLimiter) allow(ctx context.Context) bool {
	if !l.enabled() {
		return true
	}
	key := peerKey(ctx)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > l.ttl {
		for k, e := range l.entries {
			if now.Sub(e.seen) > l.ttl {
				delete(l.entries, k)
			}
		}
		l.lastSweep = now
	}
	e := l.entries[key]
	if e == nil {
		e = &rlEntry{lim: rate.NewLimiter(l.rps, l.burst)}
		l.entries[key] = e
	}
	e.seen = now
	return e.lim.Allow()
}

// Unary returns the unary interceptor.
func (l *RateLimiter) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !l.allow(ctx) {
			return nil, apperr.New(apperr.CatResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// Stream returns the stream interceptor.
func (l *RateLimiter) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !l.allow(ss.Context()) {
			return apperr.New(apperr.CatResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, ss)
	}
}
