package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

type redisCache struct {
	client *redis.Client
}

// NewRedis builds a Redis-backed cache from config.
func NewRedis(cfg config.CacheConfig) (Cache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password.Reveal(),
		DB:       cfg.DB,
	})
	return &redisCache{client: client}, nil
}

// NewRedisFromClient wraps an existing client (used by tests with miniredis).
func NewRedisFromClient(client *redis.Client) Cache { return &redisCache{client: client} }

func (r *redisCache) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.CatUnavailable, "cache get", err)
	}
	return b, nil
}

func (r *redisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, val, ttl).Err(); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "cache set", err)
	}
	return nil
}

func (r *redisCache) Del(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "cache del", err)
	}
	return nil
}

func (r *redisCache) Close() error { return r.client.Close() }
