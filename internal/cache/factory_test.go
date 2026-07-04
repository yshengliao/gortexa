package cache_test

import (
	"testing"

	"github.com/yshengliao/gortexa/internal/cache"
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// TestNewSelectsBackend pins the factory: empty/"memory" build the in-memory
// backend (no external service), and an unknown driver is InvalidArgument.
// "redis" is exercised by the integration test, since New(redis) dials lazily
// but the constructor path is the same as NewRedis (covered elsewhere).
func TestNewSelectsBackend(t *testing.T) {
	for _, driver := range []string{"", "memory"} {
		c, err := cache.New(config.CacheConfig{Driver: driver})
		if err != nil {
			t.Fatalf("driver %q: %v", driver, err)
		}
		_ = c.Close()
	}

	if _, err := cache.New(config.CacheConfig{Driver: "memcached"}); !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("unknown driver err = %v, want InvalidArgument", err)
	}
}
