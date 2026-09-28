package auth_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
)

func mustRSA(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustEC(t *testing.T, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// mint signs a token the way an external issuer would.
func mint(t *testing.T, m jwt.SigningMethod, kid string, key any, iss string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(m, auth.Claims{
		Roles: []string{"admin"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			Issuer:    iss,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	})
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func keySetVerifier(t *testing.T, keys map[string]crypto.PublicKey) *auth.Verifier {
	t.Helper()
	ks, err := auth.NewStaticKeySet(keys)
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewKeySetVerifier(ks, "gortexa")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestKeySetVerifierAcceptsRS256AndES256(t *testing.T) {
	rk, ek := mustRSA(t, 2048), mustEC(t, elliptic.P256())
	v := keySetVerifier(t, map[string]crypto.PublicKey{"r": &rk.PublicKey, "e": &ek.PublicKey})
	for name, tok := range map[string]string{
		"RS256": mint(t, jwt.SigningMethodRS256, "r", rk, "gortexa"),
		"ES256": mint(t, jwt.SigningMethodES256, "e", ek, "gortexa"),
	} {
		c, err := v.Verify(tok)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Subject != "user-1" || len(c.Roles) != 1 {
			t.Fatalf("%s: claims = %+v", name, c)
		}
	}
}

func TestKeySetVerifierRejects(t *testing.T) {
	rk, ek := mustRSA(t, 2048), mustEC(t, elliptic.P256())
	other := mustRSA(t, 2048)
	v := keySetVerifier(t, map[string]crypto.PublicKey{"r": &rk.PublicKey, "e": &ek.PublicKey})
	pubDER := rk.N.Bytes()
	cases := map[string]string{
		// ES256 alg naming an RSA kid: key type must match the alg.
		"alg/key mismatch": mint(t, jwt.SigningMethodES256, "r", ek, "gortexa"),
		// Classic alg confusion: HS256 keyed with public key material.
		"HS256 in key-set mode": mint(t, jwt.SigningMethodHS256, "r", pubDER, "gortexa"),
		"unknown kid":           mint(t, jwt.SigningMethodRS256, "zzz", rk, "gortexa"),
		"wrong signer":          mint(t, jwt.SigningMethodRS256, "r", other, "gortexa"),
		"wrong issuer":          mint(t, jwt.SigningMethodRS256, "r", rk, "evil"),
		// With two keys, a token without kid is ambiguous.
		"missing kid": mint(t, jwt.SigningMethodRS256, "", rk, "gortexa"),
	}
	for name, tok := range cases {
		_, err := v.Verify(tok)
		if !apperr.Is(err, apperr.CatUnauthenticated) {
			t.Errorf("%s: err = %v, want Unauthenticated", name, err)
			continue
		}
		if msg := apperr.ToGRPCStatus(err).Message(); msg != "invalid or expired token" {
			t.Errorf("%s: client message = %q, want the generic one", name, msg)
		}
	}
}

func TestKeySetVerifierSingleKeyNeedsNoKid(t *testing.T) {
	rk := mustRSA(t, 2048)
	v := keySetVerifier(t, map[string]crypto.PublicKey{"only": &rk.PublicKey})
	if _, err := v.Verify(mint(t, jwt.SigningMethodRS256, "", rk, "gortexa")); err != nil {
		t.Fatalf("single-key set should accept a token without kid: %v", err)
	}
}

func TestKeySetVerifierCannotSign(t *testing.T) {
	rk := mustRSA(t, 2048)
	v := keySetVerifier(t, map[string]crypto.PublicKey{"r": &rk.PublicKey})
	if _, err := v.Sign("u", nil, time.Hour); !apperr.Is(err, apperr.CatInternal) {
		t.Fatalf("Sign on a key-set verifier: err = %v, want Internal", err)
	}
}

func TestNewStaticKeySetRejectsWeakKeys(t *testing.T) {
	for name, k := range map[string]crypto.PublicKey{
		"rsa 1024": &mustRSA(t, 1024).PublicKey,
		"P-384":    &mustEC(t, elliptic.P384()).PublicKey,
		"hmac":     []byte("0123456789abcdef0123456789abcdef"),
	} {
		if _, err := auth.NewStaticKeySet(map[string]crypto.PublicKey{"k": k}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := auth.NewStaticKeySet(nil); err == nil {
		t.Error("empty set accepted")
	}
	if _, err := auth.NewKeySetVerifier(nil, "gortexa"); err == nil {
		t.Error("nil key set accepted")
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(kid string, k *rsa.PublicKey) map[string]string {
	return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
}

func ecJWK(kid string, k *ecdsa.PublicKey) map[string]string {
	b, err := k.Bytes()
	if err != nil {
		panic(err)
	}
	return map[string]string{"kty": "EC", "kid": kid, "crv": "P-256", "x": b64(b[1:33]), "y": b64(b[33:])}
}

func serveJWKS(t *testing.T, keys ...map[string]string) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewJWKSVerifiesFetchedKeys(t *testing.T) {
	rk, ek := mustRSA(t, 2048), mustEC(t, elliptic.P256())
	enc := map[string]string{"kty": "RSA", "kid": "enc", "use": "enc", "n": "AQAB", "e": "AQAB"}
	srv := serveJWKS(t, rsaJWK("r", &rk.PublicKey), ecJWK("e", &ek.PublicKey), enc,
		map[string]string{"kty": "OKP", "kid": "ed", "crv": "Ed25519", "x": "AA"})
	ks, err := auth.NewJWKS(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewKeySetVerifier(ks, "gortexa")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(mint(t, jwt.SigningMethodRS256, "r", rk, "gortexa")); err != nil {
		t.Fatalf("RS256 from jwks: %v", err)
	}
	if _, err := v.Verify(mint(t, jwt.SigningMethodES256, "e", ek, "gortexa")); err != nil {
		t.Fatalf("ES256 from jwks: %v", err)
	}
}

func TestNewJWKSRejects(t *testing.T) {
	ctx := context.Background()
	if _, err := auth.NewJWKS(ctx, "http://issuer.example/jwks.json"); err == nil {
		t.Error("plain http to a non-loopback host accepted")
	}
	weak := serveJWKS(t, rsaJWK("w", &mustRSA(t, 1024).PublicKey))
	if _, err := auth.NewJWKS(ctx, weak.URL); err == nil {
		t.Error("set with only a weak key accepted")
	}
	offCurve := serveJWKS(t, map[string]string{"kty": "EC", "kid": "x", "crv": "P-256", "x": b64(make([]byte, 32)), "y": b64(make([]byte, 32))})
	if _, err := auth.NewJWKS(ctx, offCurve.URL); err == nil {
		t.Error("off-curve EC point accepted")
	}
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[` + strings.Repeat(" ", 2<<20) + `]}`))
	}))
	defer huge.Close()
	if _, err := auth.NewJWKS(ctx, huge.URL); err == nil {
		t.Error("oversized jwks accepted")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer down.Close()
	if _, err := auth.NewJWKS(ctx, down.URL); err == nil {
		t.Error("non-200 jwks accepted")
	}
	good := serveJWKS(t, rsaJWK("r", &mustRSA(t, 2048).PublicKey))
	redirect := httptest.NewServer(http.RedirectHandler(good.URL, http.StatusFound))
	defer redirect.Close()
	if _, err := auth.NewJWKS(ctx, redirect.URL); err == nil {
		t.Error("redirected jwks followed")
	}
}
