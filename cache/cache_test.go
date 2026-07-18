package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/yshengliao/gortexa/cache"
	"github.com/yshengliao/gortexa/config"
)

func newCache(t *testing.T) (cache.Cache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := cache.NewRedis(config.CacheConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func TestCacheContract(t *testing.T) {
	ctx := context.Background()
	c, mr := newCache(t)

	// miss
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected miss, got %v", err)
	}

	// set + get
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("get = %q, %v", got, err)
	}

	// ttl expiry
	mr.FastForward(2 * time.Minute)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected expiry miss, got %v", err)
	}

	// del
	_ = c.Set(ctx, "d", []byte("x"), time.Minute)
	if err := c.Del(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "d"); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected miss after del, got %v", err)
	}
}
