package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// stubSet returns a KeySet whose fetch serves *current and counts calls.
func stubSet(current *map[string]crypto.PublicKey, fail *bool, calls *int) *KeySet {
	return &KeySet{fetch: func(context.Context) (map[string]crypto.PublicKey, error) {
		*calls++
		if *fail {
			return nil, errors.New("issuer down")
		}
		return *current, nil
	}}
}

func TestKeySetRefreshIsRateLimited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		k2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		current := map[string]crypto.PublicKey{"k1": &k1.PublicKey}
		fail, calls := false, 0
		ks := stubSet(&current, &fail, &calls)
		ctx := context.Background()
		if err := ks.refresh(ctx); err != nil {
			t.Fatal(err)
		}

		// A flood of unknown kids right after a fetch triggers no refetch.
		for range 100 {
			if _, ok := ks.key(ctx, "made-up"); ok {
				t.Fatal("unknown kid resolved")
			}
		}
		if calls != 1 {
			t.Fatalf("fetches after unknown-kid flood = %d, want 1", calls)
		}

		// The issuer rotates in k2; once the gap passes, its kid refetches.
		current = map[string]crypto.PublicKey{"k1": &k1.PublicKey, "k2": &k2.PublicKey}
		time.Sleep(jwksMinRefreshGap)
		if _, ok := ks.key(ctx, "k2"); !ok {
			t.Fatal("rotated key not picked up after the refresh gap")
		}
		if calls != 2 {
			t.Fatalf("fetches = %d, want 2", calls)
		}

		// A known kid does not refetch until the set goes stale.
		if _, ok := ks.key(ctx, "k1"); !ok || calls != 2 {
			t.Fatalf("known kid: ok=%v fetches=%d, want true/2", ok, calls)
		}

		// After the refresh interval the issuer revoked k1: a lookup refreshes
		// and k1 stops verifying.
		current = map[string]crypto.PublicKey{"k2": &k2.PublicKey}
		time.Sleep(jwksRefreshInterval)
		if _, ok := ks.key(ctx, "k1"); ok {
			t.Fatal("revoked key still resolves after the set went stale")
		}

		// An issuer outage keeps the last good keys.
		fail = true
		time.Sleep(jwksRefreshInterval)
		if _, ok := ks.key(ctx, "k2"); !ok {
			t.Fatal("known key lost during an issuer outage")
		}
	})
}
