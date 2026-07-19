package cache

import (
	"context"
	"errors"
	"time"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/internal/resp"
)

type redisCache struct {
	client *resp.Client
}

// NewRedis builds a Redis-backed cache from config, using the in-tree RESP
// client (no third-party Redis dependency). It does no I/O — connections dial
// lazily on first use.
func NewRedis(cfg config.CacheConfig) (Cache, error) {
	if cfg.PoolSize < 0 {
		return nil, apperr.New(apperr.CatInvalidArgument, "cache.pool_size must not be negative")
	}
	// A zero timeout/pool value is passed through as-is: resp.NewClient reads
	// zero as "use my default", so unset config preserves default behaviour while
	// a set value tunes fail-fast/pool sizing.
	client := resp.NewClient(resp.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password.Reveal(),
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})
	return &redisCache{client: client}, nil
}

func (r *redisCache) Get(ctx context.Context, key string) ([]byte, error) {
	s, err := r.client.Get(ctx, key)
	if errors.Is(err, resp.ErrNil) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.CatUnavailable, "cache get", err)
	}
	return []byte(s), nil
}

func (r *redisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, string(val), ttl); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "cache set", err)
	}
	return nil
}

func (r *redisCache) Del(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "cache del", err)
	}
	return nil
}

func (r *redisCache) Close() error { return r.client.Close() }
