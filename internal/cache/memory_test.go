package cache_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	"github.com/yshengliao/gortexa/internal/cache"
	"github.com/yshengliao/gortexa/internal/config"
)

func TestMemoryCacheContract(t *testing.T) {
	ctx := context.Background()
	c, err := cache.NewInMemory(config.CacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected miss, got %v", err)
	}
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := c.Del(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("expected miss after del, got %v", err)
	}
}

// TestMemoryCacheTTL pins lazy expiry deterministically: synctest fake time
// advances past the TTL and the entry reads as a miss.
func TestMemoryCacheTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		c, err := cache.NewInMemory(config.CacheConfig{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }() // stop the janitor before the bubble ends

		if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute) // past the TTL (also fires the janitor)
		if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrCacheMiss) {
			t.Fatalf("expected expiry miss, got %v", err)
		}
	})
}

// TestMemoryCacheZeroTTLNeverExpires pins that ttl <= 0 stores an entry with no
// expiry, matching go-redis's 0-means-persist semantics.
func TestMemoryCacheZeroTTLNeverExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		c, _ := cache.NewInMemory(config.CacheConfig{})
		defer func() { _ = c.Close() }()

		_ = c.Set(ctx, "k", []byte("v"), 0)
		time.Sleep(time.Hour)
		if got, err := c.Get(ctx, "k"); err != nil || string(got) != "v" {
			t.Fatalf("get = %q, %v; want persisted value", got, err)
		}
	})
}

// TestMemoryCacheCopiesValues pins that stored bytes are isolated: mutating the
// input after Set or the result after Get must not corrupt the store.
func TestMemoryCacheCopiesValues(t *testing.T) {
	ctx := context.Background()
	c, _ := cache.NewInMemory(config.CacheConfig{})
	t.Cleanup(func() { _ = c.Close() })

	in := []byte("original")
	_ = c.Set(ctx, "k", in, time.Minute)
	in[0] = 'X' // mutate the caller's slice after Set

	got, _ := c.Get(ctx, "k")
	if string(got) != "original" {
		t.Fatalf("Set aliased the caller's slice: %q", got)
	}
	got[0] = 'Y' // mutate the returned slice
	again, _ := c.Get(ctx, "k")
	if string(again) != "original" {
		t.Fatalf("Get returned an aliased slice: %q", again)
	}
	if bytes.Equal(again, got) {
		t.Fatal("second Get should not observe the caller's mutation")
	}
}

// TestMemoryCacheNoGoroutineLeakAfterClose pins that Close stops the janitor.
func TestMemoryCacheNoGoroutineLeakAfterClose(t *testing.T) {
	opts := []goleak.Option{goleak.IgnoreCurrent()}
	c, _ := cache.NewInMemory(config.CacheConfig{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
	goleak.VerifyNone(t, opts...)
}
