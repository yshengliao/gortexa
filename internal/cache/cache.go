// Package cache is a pluggable key/value cache abstraction. The default backend
// is process-local in-memory (no external service), with an opt-in Redis
// backend for a distributed cache. Redis tests run against an in-process
// miniredis, so no server is required for the default suite.
package cache

import (
	"context"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// ErrCacheMiss is returned by Get when the key is absent.
var ErrCacheMiss = apperr.New(apperr.CatNotFound, "cache miss")

// Cache is a minimal byte-oriented cache.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	Close() error
}

// New selects a cache backend from config. The default (empty or "memory") is
// the process-local in-memory cache, so an app runs with no external Redis
// unless it explicitly opts in with driver "redis".
func New(cfg config.CacheConfig) (Cache, error) {
	switch cfg.Driver {
	case "memory", "":
		return NewInMemory(cfg)
	case "redis":
		return NewRedis(cfg)
	default:
		return nil, apperr.New(apperr.CatInvalidArgument, "cache: unsupported driver: "+cfg.Driver)
	}
}
