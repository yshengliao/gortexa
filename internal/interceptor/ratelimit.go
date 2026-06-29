package interceptor

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// RateLimitConfig configures the per-peer token bucket. RPS <= 0 disables limiting.
type RateLimitConfig struct {
	RPS        float64       // tokens per second per peer
	Burst      int           // bucket size
	TTL        time.Duration // evict idle peers after this long
	MaxEntries int           // hard cap on tracked peers (memory bound); <=0 → default
}

// evictBatch bounds how many entries a single call scans for stale eviction, so
// the lock is never held for an O(N) sweep over the whole map.
const evictBatch = 128

// RateLimiter is a per-peer token-bucket limiter. Eviction is incremental
// (bounded work per call) and the entry count is capped, so a distributed surge
// of distinct IPs can neither OOM the process nor cause a single request to do
// O(N) work under the lock. No background goroutine, so nothing to leak.
type RateLimiter struct {
	rps        rate.Limit
	burst      int
	ttl        time.Duration
	maxEntries int

	mu         sync.Mutex
	entries    map[string]*rlEntry
	evictCount uint64
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
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 100_000
	}
	return &RateLimiter{
		rps:        rate.Limit(cfg.RPS),
		burst:      burst,
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]*rlEntry),
	}
}

func (l *RateLimiter) enabled() bool { return l.rps > 0 }

// ForwardedForMetaKey carries the real client IP across the in-process loopback.
// The HTTP gateway and MCP bridge forward every request through one bufconn
// connection, so their gRPC peer is always the synthetic address "bufconn";
// without this, all gateway+MCP traffic would collapse into a single shared
// rate-limit bucket. The gateway/MCP set it from their own observed client
// address, never from an untrusted inbound header.
const ForwardedForMetaKey = "x-forwarded-for"

// loopbackNetwork is the net.Addr network reported by grpc's bufconn listener.
const loopbackNetwork = "bufconn"

// peerKey keys the limiter by client IP only. Including the ephemeral port
// would let a client reset its bucket by reconnecting (rate-limit bypass) and
// would grow the entries map unbounded (one entry per connection).
func peerKey(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "unknown"
	}
	// Calls forwarded by the HTTP gateway / MCP bridge arrive over the in-process
	// bufconn loopback, where every call shares the peer "bufconn". Trust a
	// forwarded client IP ONLY on that loopback — an external gRPC client has a
	// real network peer, so it can neither reach this branch nor spoof the key.
	if p.Addr.Network() == loopbackNetwork {
		if ip := forwardedIP(ctx); ip != "" {
			return ip
		}
	}
	addr := p.Addr.String()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// forwardedIP returns the first entry of the x-forwarded-for metadata, if any.
func forwardedIP(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(ForwardedForMetaKey)
	if len(vals) == 0 || vals[0] == "" {
		return ""
	}
	first := vals[0]
	if i := strings.IndexByte(first, ','); i >= 0 {
		first = first[:i] // X-Forwarded-For may be a list; the first is the origin.
	}
	return strings.TrimSpace(first)
}

func (l *RateLimiter) allow(ctx context.Context) bool {
	if !l.enabled() {
		return true
	}
	key := peerKey(ctx)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Amortized incremental eviction: run the bounded sweep only once every
	// evictBatch calls, so the average eviction cost is ~1 step/request and the
	// global lock isn't held for O(evictBatch) on every request (this is a
	// process-wide interceptor). The entry cap below is the hard memory bound.
	l.evictCount++
	if l.evictCount%evictBatch == 0 {
		scanned := 0
		for k, e := range l.entries {
			if scanned >= evictBatch {
				break
			}
			scanned++
			if now.Sub(e.seen) > l.ttl {
				delete(l.entries, k)
			}
		}
	}

	if e, ok := l.entries[key]; ok {
		e.seen = now
		return e.lim.Allow()
	}
	// New peer: enforce the cap to bound memory under a distributed surge.
	// Shedding the request (treating it as rate-limited) is preferable to OOM.
	if len(l.entries) >= l.maxEntries {
		return false
	}
	e := &rlEntry{lim: rate.NewLimiter(l.rps, l.burst), seen: now}
	l.entries[key] = e
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
