package cache

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
)

// cleanupInterval is how often the janitor sweeps expired entries. Reads expire
// lazily (Get treats an expired entry as a miss), so this only bounds the memory
// held by keys that are never read again after they expire.
const cleanupInterval = time.Minute

type memoryEntry struct {
	val       []byte
	expiresAt time.Time // zero means no expiry
}

// memoryCache is a process-local Cache backed by a map guarded by an RWMutex,
// with a background janitor for expired-entry eviction.
type memoryCache struct {
	mu     sync.RWMutex
	items  map[string]memoryEntry
	closed bool
	done   chan struct{}
	// wg tracks the janitor so Close returns only once it has exited — no
	// goroutine outlives the cache.
	wg sync.WaitGroup
}

// NewInMemory builds a process-local cache. It needs no external service, so it
// is the default backend and lets an app run with no Redis. It is per-instance:
// entries are not shared across replicas, so use the Redis backend when a
// distributed cache is required.
func NewInMemory(config.CacheConfig) (Cache, error) {
	c := &memoryCache{
		items: make(map[string]memoryEntry),
		done:  make(chan struct{}),
	}
	c.wg.Add(1)
	go c.janitor()
	return c, nil
}

func (c *memoryCache) janitor() {
	defer c.wg.Done()
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case now := <-ticker.C:
			c.mu.Lock()
			for k, e := range c.items {
				if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
					delete(c.items, k)
				}
			}
			c.mu.Unlock()
		}
	}
}

func (c *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || (!e.expiresAt.IsZero() && time.Now().After(e.expiresAt)) {
		return nil, ErrCacheMiss
	}
	// Return a copy so a caller mutating the result can't corrupt the stored
	// value, matching the fresh-bytes semantics of the Redis backend.
	return bytes.Clone(e.val), nil
}

func (c *memoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	e := memoryEntry{val: bytes.Clone(val)} // copy so a later caller mutation can't reach the store
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.items[key] = e
	c.mu.Unlock()
	return nil
}

func (c *memoryCache) Del(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
	return nil
}

func (c *memoryCache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()
	c.wg.Wait() // the janitor does not outlive Close
	return nil
}
