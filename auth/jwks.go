package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// minRSABits rejects RSA keys too weak to trust for signatures.
	minRSABits = 2048
	// maxJWKSBytes caps the key-set response: a real set is a few KB, and the
	// cap stops a hostile or broken endpoint from making the verifier buffer an
	// unbounded body.
	maxJWKSBytes = 1 << 20
	// jwksFetchTimeout bounds one fetch. A refresh runs on the request path (an
	// unknown kid), so it must not hold a request for long.
	jwksFetchTimeout = 5 * time.Second
	// jwksRefreshInterval is how long a fetched set is trusted before the next
	// lookup refreshes it, so a key the issuer revoked stops verifying.
	jwksRefreshInterval = 15 * time.Minute
	// jwksMinRefreshGap rate-limits refreshes: a caller sending tokens with
	// made-up kids can trigger at most one fetch per gap, not one per request.
	jwksMinRefreshGap = 30 * time.Second
)

// KeySet holds the public keys an asymmetric Verifier checks signatures
// against, keyed by JWK "kid". A set built by NewJWKS refreshes itself from its
// URL on demand; there is no background goroutine.
type KeySet struct {
	fetch func(context.Context) (map[string]crypto.PublicKey, error) // nil: static set

	mu          sync.Mutex
	keys        map[string]crypto.PublicKey
	fetchedAt   time.Time // last successful fetch
	attemptedAt time.Time // last fetch attempt, successful or not
}

// NewStaticKeySet builds a KeySet from keys held in code, keyed by kid. Each
// key must be an *rsa.PublicKey of at least 2048 bits or a P-256
// *ecdsa.PublicKey.
func NewStaticKeySet(keys map[string]crypto.PublicKey) (*KeySet, error) {
	if len(keys) == 0 {
		return nil, errors.New("auth: key set is empty")
	}
	own := make(map[string]crypto.PublicKey, len(keys))
	for kid, k := range keys {
		if err := checkKey(k); err != nil {
			return nil, fmt.Errorf("auth: key %q: %w", kid, err)
		}
		own[kid] = k
	}
	return &KeySet{keys: own}, nil
}

// NewJWKS fetches the JSON Web Key Set at rawURL and returns a KeySet that
// refreshes from it when a token names an unknown kid or the set is older than
// 15 minutes. The URL must be https (plain http only for a loopback host), and
// the initial fetch must succeed, so a misconfigured issuer fails startup.
func NewJWKS(ctx context.Context, rawURL string) (*KeySet, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("auth: parse jwks url: %w", err)
	}
	secure := u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))
	if !secure {
		return nil, errors.New("auth: jwks url must be https (http only for a loopback host)")
	}
	// No redirects: following one could leave https (or loopback) after the
	// check above. A 3xx then fails the fetch as a non-200 status.
	client := &http.Client{
		Timeout:       jwksFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ks := &KeySet{fetch: func(ctx context.Context) (map[string]crypto.PublicKey, error) {
		return fetchJWKS(ctx, client, u.String())
	}}
	if err := ks.refresh(ctx); err != nil {
		return nil, err
	}
	return ks, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// refresh replaces the set with a fresh fetch. On failure the old keys stay in
// place: an issuer outage must not lock out tokens signed by known keys.
func (ks *KeySet) refresh(ctx context.Context) error {
	now := time.Now()
	ks.attemptedAt = now
	keys, err := ks.fetch(ctx)
	if err != nil {
		return err
	}
	ks.keys, ks.fetchedAt = keys, now
	return nil
}

// key returns the key for kid, refreshing first when the set is stale or kid
// is unknown, within the refresh rate limit. An empty kid is accepted only
// while the set holds exactly one key.
func (ks *KeySet) key(ctx context.Context, kid string) (crypto.PublicKey, bool) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k, ok := ks.lookup(kid)
	if ks.fetch == nil {
		return k, ok
	}
	now := time.Now()
	stale := now.Sub(ks.fetchedAt) >= jwksRefreshInterval
	if (ok && !stale) || now.Sub(ks.attemptedAt) < jwksMinRefreshGap {
		return k, ok
	}
	if err := ks.refresh(ctx); err != nil {
		return k, ok
	}
	return ks.lookup(kid)
}

func (ks *KeySet) lookup(kid string) (crypto.PublicKey, bool) {
	if kid == "" {
		if len(ks.keys) != 1 {
			return nil, false
		}
		for _, k := range ks.keys {
			return k, true
		}
	}
	k, ok := ks.keys[kid]
	return k, ok
}

func checkKey(k crypto.PublicKey) error {
	switch k := k.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < minRSABits {
			return fmt.Errorf("rsa key is %d bits, need at least %d", k.N.BitLen(), minRSABits)
		}
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return errors.New("ecdsa key must use P-256 (ES256)")
		}
	default:
		return fmt.Errorf("unsupported key type %T", k)
	}
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func fetchJWKS(ctx context.Context, client *http.Client, u string) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: jwks request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: fetch jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("auth: read jwks: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return nil, fmt.Errorf("auth: jwks exceeds %d bytes", maxJWKSBytes)
	}
	return parseJWKS(body)
}

// parseJWKS keeps the signing keys it can use and skips the rest (encryption
// keys, other key types, other curves), so an issuer publishing extra keys
// does not break verification. A set with no usable key is an error.
func parseJWKS(body []byte) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("auth: parse jwks: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, j := range set.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		k, err := j.publicKey()
		if err != nil || checkKey(k) != nil {
			continue
		}
		keys[j.Kid] = k
	}
	if len(keys) == 0 {
		return nil, errors.New("auth: jwks has no usable RS256/ES256 signing key")
	}
	return keys, nil
}

func (j jwk) publicKey() (crypto.PublicKey, error) {
	switch j.Kty {
	case "RSA":
		n, err := b64Int(j.N)
		if err != nil {
			return nil, err
		}
		e, err := b64Int(j.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("rsa exponent out of range")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		if j.Crv != "P-256" {
			return nil, errors.New("unsupported curve")
		}
		x, err := base64.RawURLEncoding.DecodeString(j.X)
		if err != nil {
			return nil, err
		}
		y, err := base64.RawURLEncoding.DecodeString(j.Y)
		if err != nil {
			return nil, err
		}
		if len(x) != 32 || len(y) != 32 {
			return nil, errors.New("P-256 coordinates must be 32 bytes")
		}
		// ParseUncompressedPublicKey rejects a point that is not on the curve.
		return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	default:
		return nil, errors.New("unsupported key type")
	}
}

func b64Int(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("empty integer")
	}
	return new(big.Int).SetBytes(b), nil
}
