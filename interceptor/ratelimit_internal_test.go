package interceptor

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
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

// On the loopback with no forwarded peer-IP metadata, peerKey falls back to the
// synthetic bufconn address (peerIP returns "" for both the no-metadata and the
// metadata-without-the-key cases).
func TestPeerKeyLoopbackFallbacks(t *testing.T) {
	// No metadata at all on the loopback → peerIP returns "" → fall back to addr.
	if k := peerKey(loopbackCtx("")); k != loopbackNetwork {
		t.Fatalf("loopback without forwarded IP key = %q, want %q", k, loopbackNetwork)
	}
	// Metadata present but missing the peer-IP key → still "" → fall back to addr.
	ctx := metadata.NewIncomingContext(
		peer.NewContext(context.Background(), &peer.Peer{Addr: rlLoopbackAddr{}}),
		metadata.Pairs("x-other", "irrelevant"),
	)
	if k := peerKey(ctx); k != loopbackNetwork {
		t.Fatalf("loopback with unrelated metadata key = %q, want %q", k, loopbackNetwork)
	}
}

// peerKey degrades gracefully when there is no peer or no address on the context.
func TestPeerKeyNoPeer(t *testing.T) {
	if k := peerKey(context.Background()); k != "unknown" {
		t.Fatalf("no-peer key = %q, want unknown", k)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{})
	if k := peerKey(ctx); k != "unknown" {
		t.Fatalf("nil-addr key = %q, want unknown", k)
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

// A full shard must admit a new peer by evicting its least-recently-seen entry,
// not reject it: otherwise one host keeping MaxEntries addresses warm locks out
// every new client.
func TestRateLimiterFullShardEvictsOldest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewRateLimiter(RateLimitConfig{RPS: 1000, Burst: 1000, TTL: time.Hour, MaxEntries: 2 * shardCount})
		ctxFor := func(ip string) context.Context {
			return peer.NewContext(context.Background(), &peer.Peer{Addr: rlFakeAddr(ip + ":1")})
		}
		const newcomer = "192.0.2.1"
		target := l.shardFor(newcomer)
		// Fill the newcomer's shard to its cap of two, oldest first; both stay
		// well inside the TTL.
		var filled []string
		for i := 0; len(filled) < 2; i++ {
			ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
			if l.shardFor(ip) != target {
				continue
			}
			l.allow(ctxFor(ip))
			filled = append(filled, ip)
			time.Sleep(time.Second)
		}
		if !l.allow(ctxFor(newcomer)) {
			t.Fatal("new peer rejected by a full shard")
		}
		target.mu.Lock()
		defer target.mu.Unlock()
		_, oldKept := target.entries[filled[0]]
		_, recentKept := target.entries[filled[1]]
		_, added := target.entries[newcomer]
		if oldKept || !recentKept || !added || len(target.entries) != 2 {
			t.Fatalf("entries = %v, want %s evicted and %s, %s kept", target.entries, filled[0], filled[1], newcomer)
		}
	})
}

// IPv6 peers share one bucket per /64, so a host cannot mint a fresh bucket per
// address; IPv4 and IPv4-mapped peers key by address.
func TestPeerKeyAggregatesIPv6To64(t *testing.T) {
	ctxFor := func(addr string) context.Context {
		return peer.NewContext(context.Background(), &peer.Peer{Addr: rlFakeAddr(addr)})
	}
	cases := map[string]string{
		"[2001:db8:1:2:aaaa::1]:443": "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::9]:443": "2001:db8:1:2::/64",
		"[fe80::1%eth0]:443":         "fe80::/64",
		"[::ffff:192.0.2.7]:443":     "192.0.2.7",
		"192.0.2.7:443":              "192.0.2.7",
	}
	for addr, want := range cases {
		if got := peerKey(ctxFor(addr)); got != want {
			t.Errorf("peerKey(%s) = %q, want %q", addr, got, want)
		}
	}
	if got := peerKey(loopbackCtx("2001:db8:1:2::77")); got != "2001:db8:1:2::/64" {
		t.Errorf("loopback IPv6 key = %q, want 2001:db8:1:2::/64", got)
	}

	l := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute})
	if !l.allow(ctxFor("[2001:db8:1:2::1]:1")) {
		t.Fatal("first address in the /64 should be allowed")
	}
	if l.allow(ctxFor("[2001:db8:1:2::2]:1")) {
		t.Fatal("a second address in the same /64 must share the exhausted bucket")
	}
}
