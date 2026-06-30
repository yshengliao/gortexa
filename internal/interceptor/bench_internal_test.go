package interceptor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	"github.com/yshengliao/gortexa/internal/auth"
)

type benchAddr string

func (b benchAddr) Network() string { return "tcp" }
func (b benchAddr) String() string  { return string(b) }

func benchPeer(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: benchAddr(ip + ":1")})
}

// Single-peer hot path: one shard, one bucket.
func BenchmarkRateLimiterAllow(b *testing.B) {
	l := NewRateLimiter(RateLimitConfig{RPS: 1e9, Burst: 1e9, TTL: time.Hour})
	ctx := benchPeer("10.0.0.1")
	b.ReportAllocs()
	for b.Loop() {
		l.allow(ctx)
	}
}

// Contended path: each goroutine uses a distinct peer key, so sharding should
// spread the lock across shards instead of one global mutex.
func BenchmarkRateLimiterAllowParallel(b *testing.B) {
	l := NewRateLimiter(RateLimitConfig{RPS: 1e9, Burst: 1e9, TTL: time.Hour})
	var ctr int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := atomic.AddInt64(&ctr, 1)
		ctx := benchPeer(fmt.Sprintf("10.%d.%d.%d", id/65536, (id/256)%256, id%256))
		for pb.Next() {
			l.allow(ctx)
		}
	})
}

// Full fixed-order interceptor chain overhead per unary RPC.
func BenchmarkChainUnary(b *testing.B) {
	set, err := NewSet(Config{
		Verifier: auth.MustNewVerifier(make([]byte, 32), "bench"),
		AuthSkip: func(string) bool { return true },
	})
	if err != nil {
		b.Fatal(err)
	}
	chain := set.ChainUnary()
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/M"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = chain(ctx, nil, info, handler)
	}
}
