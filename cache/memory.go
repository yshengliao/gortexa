package cache

import (
	"bytes"
	"context"
	"sync"
	"time"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/config"
)

// cleanupInterval is how often the janitor sweeps expired entries. Reads expire
// lazily (Get treats an expired entry as a miss), so this only bounds the memory
// held by keys that are never read again after they expire.
const cleanupInterval = time.Minute

// errMemoryClosed is what every operation returns after Close, matching the
// Redis backend (whose closed client fails each call as Unavailable) so code
// tested against the default backend sees use-after-close the same way.
var errMemoryClosed = apperr.New(apperr.CatUnavailable, "cache: closed")

type memoryEntry struct {
	val       []byte
	expiresAt time.Time // zero means no expiry
}

// memoryCache is a process-local Cache backed by a map guarded by an RWMutex,
// with a background janitor for expired-entry eviction.
type memoryCache struct {
	mu    sync.RWMutex
	items map[string]memoryEntry
	// maxEntries bounds len(items); 0 means unbounded (config.MaxEntries < 0).
	maxEntries int
	closed     bool
	done       chan struct{}
	// wg tracks the janitor so Close returns only once it has exited — no
	// goroutine outlives the cache.
	wg sync.WaitGroup
}

// NewInMemory builds a process-local cache. It needs no external service, so it
// is the default backend and lets an app run with no Redis. It is per-instance:
// entries are not shared across replicas, so use the Redis backend when a
// distributed cache is required.
func NewInMemory(cfg config.CacheConfig) (Cache, error) {
	c := &memoryCache{
		items:      make(map[string]memoryEntry),
		maxEntries: maxEntries(cfg.MaxEntries),
		done:       make(chan struct{}),
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
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return nil, errMemoryClosed
	}
	if !ok || (!e.expiresAt.IsZero() && time.Now().After(e.expiresAt)) {
		return nil, ErrCacheMiss
	}
	// Return a copy so a caller mutating the result can't corrupt the stored
	// value, matching the fresh-bytes semantics of the Redis backend.
	return bytes.Clone(e.val), nil
}

func (c *memoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl < 0 {
		// A negative ttl used to persist the entry forever, the opposite of
		// what a caller computing "expires_at - now" on an already-expired value
		// meant; refuse it loudly instead.
		return apperr.New(apperr.CatInvalidArgument, "cache: negative ttl")
	}
	e := memoryEntry{val: bytes.Clone(val)} // copy so a later caller mutation can't reach the store
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errMemoryClosed
	}
	if _, exists := c.items[key]; !exists && c.maxEntries > 0 && len(c.items) >= c.maxEntries {
		c.evictOne()
	}
	c.items[key] = e
	c.mu.Unlock()
	return nil
}

// defaultMaxEntries applies when CacheConfig.MaxEntries is zero.
const defaultMaxEntries = 100_000

func maxEntries(configured int) int {
	switch {
	case configured == 0:
		return defaultMaxEntries
	case configured < 0:
		return 0
	default:
		return configured
	}
}

// evictOne frees a slot for a new key, with c.mu held: an expired entry if
// the first few visited include one, otherwise an arbitrary entry (Go's map
// iteration order is randomised, so this is random eviction, which keeps Set
// O(1) and needs no per-entry bookkeeping).
func (c *memoryCache) evictOne() {
	now := time.Now()
	victim, visited := "", 0
	for k, e := range c.items {
		if visited == 0 {
			victim = k
		}
		if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
			victim = k
			break
		}
		if visited++; visited == 8 {
			break
		}
	}
	delete(c.items, victim)
}

func (c *memoryCache) Del(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errMemoryClosed
	}
	delete(c.items, key)
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
