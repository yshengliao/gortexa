package interceptor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/peer"
)

type rlFakeAddr string

func (f rlFakeAddr) Network() string { return "tcp" }
func (f rlFakeAddr) String() string  { return string(f) }

// A flood of distinct client IPs must not grow the entries map past the cap,
// so a distributed surge cannot OOM the process.
func TestRateLimiterBoundedGrowth(t *testing.T) {
	l := NewRateLimiter(RateLimitConfig{RPS: 1000, Burst: 1000, TTL: time.Hour, MaxEntries: 50})
	for i := 0; i < 500; i++ {
		ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: rlFakeAddr(fmt.Sprintf("10.0.0.%d:12345", i))})
		l.allow(ctx)
	}
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n > 50 {
		t.Fatalf("entries = %d, want <= 50 (capped)", n)
	}
}
