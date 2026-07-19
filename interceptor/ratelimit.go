package interceptor

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/observability"
)

// RateLimitConfig configures the per-peer token bucket. RPS <= 0 disables limiting.
type RateLimitConfig struct {
	RPS        float64       // tokens per second per peer
	Burst      int           // bucket size
	TTL        time.Duration // evict idle peers after this long
	MaxEntries int           // hard cap on tracked peers (memory bound); <=0 → default
}

const (
	// evictBatch bounds how many entries a single call scans for stale eviction,
	// so a lock is never held for an O(N) sweep over a whole map.
	evictBatch = 128
	// shardCount splits the peer map into independently-locked shards so RPCs for
	// different peers don't contend on one global mutex. Must be a power of two
	// for the mask in shardFor.
	shardCount = 16
)

// rlShard is one independently-locked partition of the peer map.
type rlShard struct {
	mu         sync.Mutex
	entries    map[string]*rlEntry
	evictCount uint64
}

// RateLimiter is a per-peer token-bucket limiter, sharded by peer key to cut lock
// contention. Eviction is incremental (bounded work per call) and the per-shard
// entry count is capped, so a distributed surge of distinct IPs can neither OOM
// the process nor cause a single request to do O(N) work under a lock. No
// background goroutine, so nothing to leak.
type RateLimiter struct {
	rps   rate.Limit
	burst int
	ttl   time.Duration
	// maxEntriesShard is the per-shard cap; the global bound is this × shardCount.
	maxEntriesShard int
	shards          [shardCount]*rlShard
	metrics         *observability.GovernanceMetrics
}

type rlEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimiter builds a RateLimiter from config.
func NewRateLimiter(cfg RateLimitConfig, metrics ...*observability.GovernanceMetrics) *RateLimiter {
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
	perShard := max(maxEntries/shardCount, 1)
	l := &RateLimiter{
		rps:             rate.Limit(cfg.RPS),
		burst:           burst,
		ttl:             ttl,
		maxEntriesShard: perShard,
	}
	if len(metrics) > 0 {
		l.metrics = metrics[0]
	}
	for i := range l.shards {
		l.shards[i] = &rlShard{entries: make(map[string]*rlEntry)}
	}
	return l
}

// shardFor selects the shard owning key (shardCount is a power of two).
func (l *RateLimiter) shardFor(key string) *rlShard {
	return l.shards[xxhash.Sum64String(key)&(shardCount-1)]
}

func (l *RateLimiter) enabled() bool { return l.rps > 0 }

// PeerIPMetaKey carries the real client IP across the in-process loopback. The
// HTTP gateway and MCP bridge forward every request through one bufconn
// connection, so their gRPC peer is always the synthetic address "bufconn";
// without this, all gateway+MCP traffic would collapse into a single shared
// rate-limit bucket. The gateway/MCP set it from their own observed client
// address (r.RemoteAddr), never from an untrusted inbound header.
//
// It is deliberately NOT "x-forwarded-for": grpc-gateway natively annotates the
// outgoing context with an x-forwarded-for entry derived from any inbound
// X-Forwarded-For header, so trusting that key would let an HTTP client spoof
// its rate-limit identity. This dedicated key is only ever set by our own
// trusted metadata hooks and is blocked from inbound header forwarding.
const PeerIPMetaKey = "x-gortexa-peer-ip"

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
		if ip := peerIP(ctx); ip != "" {
			return ip
		}
	}
	addr := p.Addr.String()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// peerIP returns the trusted peer IP carried on PeerIPMetaKey, if any. The value
// is set by our own gateway/MCP metadata hooks from r.RemoteAddr (a single IP),
// so no list parsing is needed.
func peerIP(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(PeerIPMetaKey)
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
}

func (l *RateLimiter) allow(ctx context.Context) bool {
	if !l.enabled() {
		return true
	}
	key := peerKey(ctx)
	now := time.Now()

	sh := l.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	// Amortized incremental eviction: run the bounded sweep only once every
	// evictBatch calls, so the average eviction cost is ~1 step/request and the
	// shard lock isn't held for O(evictBatch) on every request. The per-shard cap
	// below is the hard memory bound.
	sh.evictCount++
	if sh.evictCount%evictBatch == 0 {
		scanned := 0
		for k, e := range sh.entries {
			if scanned >= evictBatch {
				break
			}
			scanned++
			if now.Sub(e.seen) > l.ttl {
				delete(sh.entries, k)
			}
		}
	}

	if e, ok := sh.entries[key]; ok {
		e.seen = now
		return e.lim.Allow()
	}
	// New peer: enforce the per-shard cap to bound memory under a distributed
	// surge. Shedding the request (treating it as rate-limited) is preferable to OOM.
	if len(sh.entries) >= l.maxEntriesShard {
		return false
	}
	e := &rlEntry{lim: rate.NewLimiter(l.rps, l.burst), seen: now}
	sh.entries[key] = e
	return e.lim.Allow()
}

// Unary returns the unary interceptor.
func (l *RateLimiter) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !l.allow(ctx) {
			if l.metrics != nil {
				l.metrics.RateLimitTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("method", info.FullMethod)))
			}
			return nil, apperr.New(apperr.CatResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// Stream returns the stream interceptor.
func (l *RateLimiter) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !l.allow(ss.Context()) {
			if l.metrics != nil {
				l.metrics.RateLimitTotal.Add(ss.Context(), 1, metric.WithAttributes(attribute.String("method", info.FullMethod)))
			}
			return apperr.New(apperr.CatResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, ss)
	}
}
