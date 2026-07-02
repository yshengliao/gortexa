package interceptor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

type rlFakeAddr string

func (f rlFakeAddr) Network() string { return "tcp" }
func (f rlFakeAddr) String() string  { return string(f) }

// rlLoopbackAddr mimics grpc's bufconn peer (network "bufconn"), as seen by the
// rate limiter for every request the HTTP gateway / MCP bridge forward.
type rlLoopbackAddr struct{}

func (rlLoopbackAddr) Network() string { return loopbackNetwork }
func (rlLoopbackAddr) String() string  { return loopbackNetwork }

func loopbackCtx(peerIP string) context.Context {
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: rlLoopbackAddr{}})
	if peerIP != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(PeerIPMetaKey, peerIP))
	}
	return ctx
}

// Over the loopback, distinct forwarded client IPs must get distinct buckets
// (otherwise all gateway+MCP traffic collapses into the single "bufconn" key),
// while the trusted peer-IP key on a real network peer must be ignored
// (anti-spoof: only the loopback may carry it).
func TestRateLimiterForwardedForOnLoopback(t *testing.T) {
	l := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute})

	if k := peerKey(loopbackCtx("203.0.113.7")); k != "203.0.113.7" {
		t.Fatalf("loopback peer key = %q, want 203.0.113.7", k)
	}
	// Two distinct forwarded IPs over the loopback do not share a bucket.
	if !l.allow(loopbackCtx("198.51.100.1")) {
		t.Fatal("first IP over loopback should be allowed")
	}
	if l.allow(loopbackCtx("198.51.100.1")) {
		t.Fatal("same forwarded IP should be limited (burst=1)")
	}
	if !l.allow(loopbackCtx("198.51.100.2")) {
		t.Fatal("a different forwarded IP must get its own bucket")
	}

	// On a real (non-loopback) peer, the peer-IP metadata is NOT trusted: the key
	// falls back to the real peer IP so a client can't spoof another's bucket.
	spoof := metadata.NewIncomingContext(
		peer.NewContext(context.Background(), &peer.Peer{Addr: rlFakeAddr("192.0.2.50:5555")}),
		metadata.Pairs(PeerIPMetaKey, "203.0.113.7"),
	)
	if k := peerKey(spoof); k != "192.0.2.50" {
		t.Fatalf("non-loopback key = %q, want real peer 192.0.2.50 (forwarded metadata ignored)", k)
	}
}

// A flood of distinct client IPs must not grow the (now sharded) entries past
// the cap, so a distributed surge cannot OOM the process.
func TestRateLimiterBoundedGrowth(t *testing.T) {
	l := NewRateLimiter(RateLimitConfig{RPS: 1000, Burst: 1000, TTL: time.Hour, MaxEntries: 50})
	for i := range 5000 {
		ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: rlFakeAddr(fmt.Sprintf("10.%d.%d.%d:12345", i/65536, (i/256)%256, i%256))})
		l.allow(ctx)
	}
	total := 0
	for _, sh := range l.shards {
		sh.mu.Lock()
		if len(sh.entries) > l.maxEntriesShard {
			t.Errorf("shard has %d entries, want <= per-shard cap %d", len(sh.entries), l.maxEntriesShard)
		}
		total += len(sh.entries)
		sh.mu.Unlock()
	}
	if total > 50 {
		t.Fatalf("total entries = %d, want <= 50 (global cap)", total)
	}
}
