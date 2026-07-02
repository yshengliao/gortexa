package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/yshengliao/gortexa/internal/cache"
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestNewRedis(t *testing.T) {
	// go-redis connects lazily, so the constructor succeeds without a server.
	mr := miniredis.RunT(t)
	c, err := cache.NewRedis(config.CacheConfig{Addr: mr.Addr(), Password: "", DB: 0})
	if err != nil {
		t.Fatalf("NewRedis() error = %v", err)
	}
	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set through NewRedis cache: %v", err)
	}
	got, err := c.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestCacheUnavailableErrors(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	// Disable retries and shorten dial/timeouts so, once the server is killed
	// below, each op fails fast instead of burning ~5s in go-redis backoff.
	c := cache.NewRedisFromClient(redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		MaxRetries:   -1,
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	}))
	t.Cleanup(func() { _ = c.Close() })

	// Kill the server so every operation fails with a transport error.
	mr.Close()

	cases := []struct {
		name string
		op   func() error
	}{
		{"get", func() error { _, err := c.Get(ctx, "k"); return err }},
		{"set", func() error { return c.Set(ctx, "k", []byte("v"), time.Minute) }},
		{"del", func() error { return c.Del(ctx, "k") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op()
			if err == nil {
				t.Fatal("expected error with server down")
			}
			if errors.Is(err, cache.ErrCacheMiss) {
				t.Fatal("transport error must not be a cache miss")
			}
			if !apperr.Is(err, apperr.CatUnavailable) {
				t.Fatalf("category = %v, want unavailable", err)
			}
		})
	}
}
