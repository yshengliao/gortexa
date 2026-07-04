//go:build integration

package cache_test

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/cache"
	"github.com/yshengliao/gortexa/internal/config"
)

// redisAddr resolves the Redis address for the integration test. If REDIS_ADDR
// is set (CI's hard guarantee) it is used as-is and a connection failure fails
// the test; otherwise it falls back to the docker-compose default and skips
// when nothing is listening, so a local `make test-integration` stays green
// without Redis.
func redisAddr(t *testing.T) string {
	t.Helper()
	if a := os.Getenv("REDIS_ADDR"); a != "" {
		return a
	}
	const addr = "127.0.0.1:6379"
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("redis not reachable at %s: %v (start it with: docker compose -f deploy/docker-compose.yaml up -d redis)", addr, err)
	}
	_ = conn.Close()
	return addr
}

// TestRedisBackendAgainstRealServer exercises the Redis backend through the
// cache.New factory against a real server, proving the opt-in driver still
// works after the in-memory default was introduced.
func TestRedisBackendAgainstRealServer(t *testing.T) {
	addr := redisAddr(t)
	c, err := cache.New(config.CacheConfig{Driver: "redis", Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	key := "gortexa-it-" + time.Now().Format("150405.000000")

	if _, err := c.Get(ctx, key); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected miss, got %v", err)
	}
	if err := c.Set(ctx, key, []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, key)
	if err != nil || string(got) != "v" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := c.Set(ctx, key, []byte("v"), time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond) // let the real server expire the key
	if _, err := c.Get(ctx, key); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected expiry miss, got %v", err)
	}
	if err := c.Del(ctx, key); err != nil {
		t.Fatal(err)
	}
}
