package cache

import (
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
)

// TestNewRedisAppliesTunables pins that the client tunables flow from config
// into the go-redis client, so an operator can actually shorten timeouts or
// size the pool. A zero value is deliberately left for go-redis to default.
func TestNewRedisAppliesTunables(t *testing.T) {
	c, err := NewRedis(config.CacheConfig{
		Addr:         "127.0.0.1:6379",
		DialTimeout:  time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     42,
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := c.(*redisCache).client.Options()
	if opts.DialTimeout != time.Second {
		t.Errorf("DialTimeout = %v, want 1s", opts.DialTimeout)
	}
	if opts.ReadTimeout != 2*time.Second {
		t.Errorf("ReadTimeout = %v, want 2s", opts.ReadTimeout)
	}
	if opts.WriteTimeout != 3*time.Second {
		t.Errorf("WriteTimeout = %v, want 3s", opts.WriteTimeout)
	}
	if opts.PoolSize != 42 {
		t.Errorf("PoolSize = %d, want 42", opts.PoolSize)
	}
}
