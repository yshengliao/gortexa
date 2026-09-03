package auth_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/auth"
)

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func hmacSHA512(key, data []byte) []byte {
	mac := hmac.New(sha512.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func FuzzVerify(f *testing.F) {
	key := bytes.Repeat([]byte("k"), 32)
	v, err := auth.NewVerifier(key, "iss")
	if err != nil {
		f.Fatal(err)
	}
	valid, err := v.Sign("subject", []string{"role"}, 0)
	if err != nil {
		f.Fatal(err)
	}

	b64 := base64.RawURLEncoding.EncodeToString

	// alg "none" token: header {"alg":"none","typ":"JWT"}, arbitrary claims, no signature.
	noneHeader := b64([]byte(`{"alg":"none","typ":"JWT"}`))
	nonePayload := b64([]byte(`{"iss":"iss","sub":"attacker","exp":99999999999}`))
	algNone := noneHeader + "." + nonePayload + "."

	// HS512 token signed with the same key (should be rejected: HS256 pinned).
	hs512Header := b64([]byte(`{"alg":"HS512","typ":"JWT"}`))
	hs512Payload := b64([]byte(`{"iss":"iss","sub":"attacker","exp":99999999999}`))
	hs512Signing := hs512Header + "." + hs512Payload
	hs512Sig := b64(hmacSHA512(key, []byte(hs512Signing)))
	algHS512 := hs512Signing + "." + hs512Sig

	// Token with no exp claim.
	noExpHeader := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	noExpPayload := b64([]byte(`{"iss":"iss","sub":"subject"}`))
	noExpSigning := noExpHeader + "." + noExpPayload
	noExpSig := b64(hmacSHA256(key, []byte(noExpSigning)))
	noExp := noExpSigning + "." + noExpSig

	seeds := []string{
		valid,
		algNone,
		algHS512,
		noExp,
		"a.b.c",
		"..",
		strings.Repeat("A", 100*1024),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data string) {
		claims, err := v.Verify(data)
		if err != nil {
			return
		}
		if claims.ExpiresAt == nil {
			t.Fatalf("claims accepted with nil ExpiresAt")
		}
		if claims.Issuer != "iss" {
			t.Fatalf("claims accepted with issuer %q, want %q", claims.Issuer, "iss")
		}
		// Decode the header ourselves rather than trusting the library's own
		// notion of which algorithm validated the token.
		parts := strings.Split(data, ".")
		if len(parts) != 3 {
			t.Fatalf("accepted token is not a 3-part JWS: %q", data)
		}
		hdrRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			t.Fatalf("accepted token header not valid base64url: %v", err)
		}
		var hdr struct {
			Alg string `json:"alg"`
		}
		if err := json.Unmarshal(hdrRaw, &hdr); err != nil {
			t.Fatalf("accepted token header not valid JSON: %v", err)
		}
		if hdr.Alg != "HS256" {
			t.Fatalf("accepted token with header alg %q, want HS256", hdr.Alg)
		}
	})
}

func FuzzBearerToken(f *testing.F) {
	seeds := []string{
		"Bearer abc",
		"bearer abc",
		"Bearer ",
		"",
		"Basic abc",
		"Bearer",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		tok, ok := auth.BearerToken(data)
		if !ok {
			return
		}
		// BearerToken trims surrounding whitespace off the extracted token, so it
		// is a suffix of the input only up to trailing whitespace; check against
		// the trimmed input instead of the raw input. A prefix-only match (e.g.
		// "Bearer  ", all-whitespace remainder) can legitimately trim down to "".
		if tok != "" && !strings.HasSuffix(strings.TrimSpace(data), tok) {
			t.Fatalf("returned token %q is not a (whitespace-trimmed) suffix of input %q", tok, data)
		}
	})
}
