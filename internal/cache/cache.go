// Package cache is a pluggable key/value cache abstraction with a Redis
// implementation. Tests run against an in-process miniredis, so no broker is
// required for the default suite.
package cache

import (
	"context"
	"time"

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
