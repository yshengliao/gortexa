package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestKeySetKnownKidDoesNotBlockOnRefresh pins that a refresh runs outside the
// key-set lock. A refresh is triggered on the request path by any token naming
// an unknown kid, which an unauthenticated caller can send once per
// jwksMinRefreshGap; if the fetch (up to jwksFetchTimeout) ran under the
// mutex, every other verification on the server would stall behind it
// whenever the issuer is slow, and the code's own promise that an outage never
// locks out tokens signed by known keys would not hold. Real time, not
// synctest: a goroutine blocked on a mutex is not durably blocked, so a fake
// clock could not advance past the stub's sleep.
func TestKeySetKnownKidDoesNotBlockOnRefresh(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]crypto.PublicKey{"k1": &k.PublicKey}
	const fetchDelay = 500 * time.Millisecond
	fetchStarted := make(chan struct{})
	var startOnce sync.Once
	ks := &KeySet{
		keys:        keys,
		fetchedAt:   time.Now(),
		attemptedAt: time.Now().Add(-time.Hour),
		fetch: func(context.Context) (map[string]crypto.PublicKey, error) {
			startOnce.Do(func() { close(fetchStarted) })
			time.Sleep(fetchDelay)
			return keys, nil
		},
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		ks.key(context.Background(), "made-up") // attacker-chosen kid triggers a refresh
	}()
	<-fetchStarted

	start := time.Now()
	if _, ok := ks.key(context.Background(), "k1"); !ok {
		t.Fatal("known kid lost while a refresh was in flight")
	}
	if d := time.Since(start); d > fetchDelay/4 {
		t.Fatalf("known-kid lookup blocked %v behind an in-flight refresh (fetch takes %v)", d, fetchDelay)
	}
	<-refreshDone
}

// TestKeySetConcurrentUnknownKidsFetchOnce pins the companion property: with
// the fetch outside the lock, a burst of unknown-kid lookups still produces
// one fetch, not one per caller — the refresh gap is claimed under the lock
// before the fetch starts.
func TestKeySetConcurrentUnknownKidsFetchOnce(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]crypto.PublicKey{"k1": &k.PublicKey}
	var fetches atomic.Int32
	ks := &KeySet{
		keys:        keys,
		fetchedAt:   time.Now(),
		attemptedAt: time.Now().Add(-time.Hour),
		fetch: func(context.Context) (map[string]crypto.PublicKey, error) {
			fetches.Add(1)
			time.Sleep(100 * time.Millisecond)
			return keys, nil
		},
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { ks.key(context.Background(), "made-up") })
	}
	wg.Wait()
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
	// The refreshed set is what later lookups see.
	if _, ok := ks.key(context.Background(), "k1"); !ok {
		t.Fatal("known kid missing after refresh")
	}
}
