package cache_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/cache"
	"github.com/yshengliao/gortexa/config"
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

// TestMemoryCacheUseAfterClose pins parity with the Redis backend: after Close
// every operation fails as Unavailable instead of Set silently reporting a
// write it dropped and Get/Del serving the stale map.
func TestMemoryCacheUseAfterClose(t *testing.T) {
	ctx := context.Background()
	c, _ := cache.NewInMemory(config.CacheConfig{})
	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "k2", []byte("v"), 0); !apperr.Is(err, apperr.CatUnavailable) {
		t.Fatalf("Set after Close = %v, want Unavailable", err)
	}
	if _, err := c.Get(ctx, "k"); !apperr.Is(err, apperr.CatUnavailable) {
		t.Fatalf("Get after Close = %v, want Unavailable", err)
	}
	if err := c.Del(ctx, "k"); !apperr.Is(err, apperr.CatUnavailable) {
		t.Fatalf("Del after Close = %v, want Unavailable", err)
	}
}

// TestMemoryCacheIsBounded pins the entry cap on the default backend. The map
// used to grow without limit, so a handler caching per-request-derived keys
// (per user, per query string) let an unauthenticated client grow the heap
// until OOM. A full cache evicts to admit a new key, an existing key is
// updated in place without eviction, and the bound is observable through the
// contract: at most MaxEntries of the written keys can still hit.
func TestMemoryCacheIsBounded(t *testing.T) {
	ctx := context.Background()
	const limit = 100
	c, err := cache.NewInMemory(config.CacheConfig{MaxEntries: limit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	for i := range 10 * limit {
		if err := c.Set(ctx, "k"+itoa(i), []byte("v"), 0); err != nil {
			t.Fatal(err)
		}
	}
	hits := 0
	for i := range 10 * limit {
		if _, err := c.Get(ctx, "k"+itoa(i)); err == nil {
			hits++
		}
	}
	if hits > limit {
		t.Fatalf("%d keys still cached, want <= %d: no eviction", hits, limit)
	}
	if hits == 0 {
		t.Fatal("eviction removed everything, want the cache still useful")
	}
	// Overwriting a live key must not evict another one: 2 keys, limit 2.
	small, _ := cache.NewInMemory(config.CacheConfig{MaxEntries: 2})
	t.Cleanup(func() { _ = small.Close() })
	_ = small.Set(ctx, "a", []byte("1"), 0)
	_ = small.Set(ctx, "b", []byte("1"), 0)
	_ = small.Set(ctx, "a", []byte("2"), 0)
	for _, k := range []string{"a", "b"} {
		if _, err := small.Get(ctx, k); err != nil {
			t.Fatalf("%q evicted by an in-place update: %v", k, err)
		}
	}
}

// TestMemoryCacheEvictsExpiredFirst pins the eviction preference: a full
// cache holding expired entries drops those before any live one.
func TestMemoryCacheEvictsExpiredFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		c, _ := cache.NewInMemory(config.CacheConfig{MaxEntries: 2})
		defer func() { _ = c.Close() }()
		_ = c.Set(ctx, "live", []byte("v"), 0)
		_ = c.Set(ctx, "stale", []byte("v"), time.Second)
		time.Sleep(2 * time.Second) // under the janitor's minute: still in the map
		if err := c.Set(ctx, "new", []byte("v"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Get(ctx, "live"); err != nil {
			t.Fatalf("live entry evicted while an expired one was available: %v", err)
		}
	})
}

// TestMemoryCacheRejectsNegativeTTL pins that a negative ttl is refused rather
// than stored forever: ttl <= 0 used to mean "persist", so a caller computing
// expires_at - now on an already-expired value cached it permanently.
func TestMemoryCacheRejectsNegativeTTL(t *testing.T) {
	ctx := context.Background()
	c, _ := cache.NewInMemory(config.CacheConfig{})
	t.Cleanup(func() { _ = c.Close() })
	err := c.Set(ctx, "k", []byte("v"), -time.Second)
	if !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("Set with negative ttl = %v, want InvalidArgument", err)
	}
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("negative-ttl entry was stored: %v", err)
	}
}

// TestMemoryCacheUnboundedOptIn pins the escape hatch: a negative MaxEntries
// removes the bound (zero is the default, not "unbounded").
func TestMemoryCacheUnboundedOptIn(t *testing.T) {
	ctx := context.Background()
	c, _ := cache.NewInMemory(config.CacheConfig{MaxEntries: -1})
	t.Cleanup(func() { _ = c.Close() })
	for i := range 1000 {
		_ = c.Set(ctx, "k"+itoa(i), []byte("v"), 0)
	}
	for i := range 1000 {
		if _, err := c.Get(ctx, "k"+itoa(i)); err != nil {
			t.Fatalf("key %d evicted from an unbounded cache", i)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
